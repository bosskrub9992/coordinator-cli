package watcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

const (
	FetchTimeout = time.Minute
	excerptLimit = 400
)

type Record struct {
	PID   int       `json:"pid"`
	Start time.Time `json:"process_start,omitzero"`
	Since time.Time `json:"since"`
}

var currentPID = os.Getpid

func (r Record) Live() bool { return r.PID > 0 && home.ProcessLive(r.PID, r.Start) }

func RecordPath(h home.Home) string { return filepath.Join(h.Root, "watcher.json") }
func LogPath(h home.Home) string    { return filepath.Join(h.Root, "watcher.log") }
func lockPath(h home.Home) string   { return RecordPath(h) + ".lck" }

func ReadRecord(h home.Home) (Record, error) {
	var r Record
	_, err := home.ReadJSON(RecordPath(h), &r)
	return r, err
}

type Watched struct {
	Task task.Task
	MRs  []task.MR
}

func OpenMRs(s *task.Store) ([]Watched, error) {
	ts, err := s.List()
	if err != nil {
		return nil, err
	}
	var out []Watched
	for _, t := range ts {
		if t.State.Terminal() {
			continue
		}
		mrs, err := s.MRs(t.ID)
		if err != nil {
			return nil, err
		}
		var open []task.MR
		for _, m := range mrs {
			if m.Open() {
				open = append(open, m)
			}
		}
		if len(open) > 0 {
			out = append(out, Watched{Task: t, MRs: open})
		}
	}
	return out, nil
}

var ErrRunning = errors.New("a watcher is already running")

func Ensure(h home.Home, start func() error) (bool, error) {
	ws, err := OpenMRs(task.NewStore(h))
	if err != nil || len(ws) == 0 {
		return false, err
	}
	started := false
	err = home.WithFileLock(lockPath(h), func() error {
		r, err := ReadRecord(h)
		if err != nil {
			return err
		}
		if r.Live() {
			return nil
		}
		started = true
		return start()
	})
	return started, err
}

type Watcher struct {
	Home     home.Home
	Store    *task.Store
	Client   func(mrwatch.Kind) (mrwatch.Client, error)
	Interval func() time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
	Logf     func(string, ...any)
}

func (w *Watcher) now() time.Time { return w.Now().UTC() }

func (w *Watcher) claim() error {
	return home.WithFileLock(lockPath(w.Home), func() error {
		r, err := ReadRecord(w.Home)
		if err != nil {
			return err
		}
		if r.Live() {
			return fmt.Errorf("%w (pid %d)", ErrRunning, r.PID)
		}
		pid := currentPID()
		return home.WriteJSONAtomic(RecordPath(w.Home), Record{PID: pid, Start: home.ProcessStart(pid), Since: w.now()})
	})
}

func (w *Watcher) releaseIfIdle() (bool, error) {
	idle := false
	err := home.WithFileLock(lockPath(w.Home), func() error {
		ws, err := OpenMRs(w.Store)
		if err != nil || len(ws) > 0 {
			return err
		}
		idle = true
		return home.WriteJSONAtomic(RecordPath(w.Home), Record{})
	})
	return idle, err
}

func (w *Watcher) release() {
	home.WithFileLock(lockPath(w.Home), func() error {
		r, err := ReadRecord(w.Home)
		if err == nil && r.PID == currentPID() {
			return home.WriteJSONAtomic(RecordPath(w.Home), Record{})
		}
		return err
	})
}

func (w *Watcher) Run(ctx context.Context) error {
	if err := w.claim(); err != nil {
		return err
	}
	defer w.release()
	for {
		next, err := w.PollDue(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.Logf("poll: %v", err)
		}
		idle, err := w.releaseIfIdle()
		if err != nil {
			w.Logf("check idle: %v", err)
		}
		if idle {
			w.Logf("no open MRs; exiting")
			return nil
		}
		if next <= 0 {
			next = w.Interval()
		}
		if err := w.Sleep(ctx, next); err != nil {
			return nil
		}
	}
}

func (w *Watcher) PollDue(ctx context.Context) (time.Duration, error) {
	ws, err := OpenMRs(w.Store)
	if err != nil {
		return w.Interval(), err
	}
	interval := w.Interval()
	var next time.Duration
	var errs []error
	for _, wt := range ws {
		for _, m := range wt.MRs {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			wait := interval - w.now().Sub(m.Watch.PolledAt)
			if !m.Watch.Polled || m.Watch.PolledAt.IsZero() || wait <= 0 {
				if err := w.poll(ctx, wt.Task.ID, m); err != nil {
					errs = append(errs, err)
				}
				wait = interval
			}
			if next == 0 || wait < next {
				next = wait
			}
		}
	}
	return next, errors.Join(errs...)
}

func (w *Watcher) poll(ctx context.Context, id task.ID, m task.MR) error {
	var facts []mrwatch.Fact
	var next mrwatch.Position
	c, err := w.Client(m.Ref.Kind)
	if err == nil {
		fctx, cancel := context.WithTimeout(ctx, FetchTimeout)
		var snap mrwatch.Snapshot
		snap, err = c.Fetch(fctx, m.Ref, m.Watch.Cursor())
		cancel()
		if err == nil {
			facts, next = mrwatch.Diff(m.Watch, snap, m.Ref, m.AddedAt, w.now())
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w.Logf("%s %s: %v", id, m.Ref.URL, err)
		facts, next = mrwatch.Failed(m.Watch, m.Ref, err, w.now())
	}
	saved, err := w.save(id, m, next, facts)
	if err != nil || !saved {
		return err
	}
	for _, f := range facts {
		data, err := json.Marshal(eventData(f))
		if err != nil {
			return err
		}
		if _, err := w.Store.Append(id, task.Event{Type: task.EventType(f.Kind), Text: f.Text, Data: data}); err != nil {
			return fmt.Errorf("%s %s: %w", id, m.Ref.URL, err)
		}
	}
	if _, err := w.Store.Settle(id, ""); err != nil {
		return fmt.Errorf("%s %s: %w", id, m.Ref.URL, err)
	}
	return nil
}

func (w *Watcher) save(id task.ID, m task.MR, next mrwatch.Position, facts []mrwatch.Fact) (bool, error) {
	saved := false
	_, err := w.Store.UpdateMRFile(id, func(f *task.MRFile) error {
		saved = false
		for i := range f.MRs {
			cur := &f.MRs[i]
			if cur.Ref.URL != m.Ref.URL || !reflect.DeepEqual(cur.Watch, m.Watch) {
				continue
			}
			cur.Watch = next
			saved = true
			for _, fact := range facts {
				if fact.Kind == mrwatch.FactComments {
					cur.CommentsSinceAck += len(fact.Notes)
				}
				if fact.Kind.NeedsCaptain() {
					f.AddAsk(task.Ask{URL: fact.Ref.URL, Kind: string(fact.Kind), Text: questionText(fact), At: w.now()})
				}
			}
			return nil
		}
		return nil
	})
	return saved, err
}

type factData struct {
	URL   string         `json:"url"`
	Kind  mrwatch.Kind   `json:"kind"`
	Notes []mrwatch.Note `json:"notes,omitempty"`
}

func eventData(f mrwatch.Fact) factData {
	d := factData{URL: f.Ref.URL, Kind: f.Ref.Kind}
	for _, n := range f.Notes {
		n.Body = excerpt(n.Body)
		d.Notes = append(d.Notes, n)
	}
	return d
}

func questionText(f mrwatch.Fact) string {
	if len(f.Notes) == 0 {
		return f.Text
	}
	var b strings.Builder
	b.WriteString(f.Text)
	for _, n := range f.Notes {
		fmt.Fprintf(&b, "\n- %s: %s", n.Author, excerpt(n.Body))
		if n.URL != "" {
			fmt.Fprintf(&b, " (%s)", n.URL)
		}
	}
	return b.String()
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > excerptLimit {
		return string(r[:excerptLimit-1]) + "…"
	}
	return s
}

func SleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
