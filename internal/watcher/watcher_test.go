package watcher

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

const (
	urlA = "https://git.example.com/team/api/-/merge_requests/7"
	urlB = "https://github.com/team/proto/pull/3"
)

type fakeClient struct {
	mu    sync.Mutex
	snaps map[string]mrwatch.Snapshot
	errs  map[string]error
	calls map[string]int
}

func (f *fakeClient) Fetch(_ context.Context, r mrwatch.Ref, after mrwatch.NoteCursor) (mrwatch.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[r.URL]++
	if err := f.errs[r.URL]; err != nil {
		return mrwatch.Snapshot{}, err
	}
	s := f.snaps[r.URL]
	var notes []mrwatch.Note
	for _, n := range s.Notes {
		if n.ID > after.Note {
			notes = append(notes, n)
		}
	}
	s.Notes = notes
	return s, nil
}

func (f *fakeClient) set(url string, s mrwatch.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps[url] = s
	delete(f.errs, url)
}

type rig struct {
	t     *testing.T
	h     home.Home
	s     *task.Store
	c     *fakeClient
	clock time.Time
	logs  []string
	w     *Watcher
}

func newRig(t *testing.T) *rig {
	t.Helper()
	h := home.New(t.TempDir())
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, h: h, s: task.NewStore(h), c: &fakeClient{snaps: map[string]mrwatch.Snapshot{}, errs: map[string]error{}, calls: map[string]int{}}, clock: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	r.w = &Watcher{
		Home:     h,
		Store:    r.s,
		Client:   func(mrwatch.Kind) (mrwatch.Client, error) { return r.c, nil },
		Interval: func() time.Duration { return time.Minute },
		Now:      func() time.Time { return r.clock },
		Sleep: func(ctx context.Context, d time.Duration) error {
			r.clock = r.clock.Add(d)
			return ctx.Err()
		},
		Logf: func(f string, a ...any) { r.logs = append(r.logs, f) },
	}
	return r
}

func (r *rig) shipTask(state task.State, urls ...string) task.ID {
	r.t.Helper()
	tk, err := r.s.Create(task.NewTask{Title: "change api", Class: config.Ship, Projects: []string{"api"}, Brief: "do it", LaunchFolder: r.t.TempDir()})
	if err != nil {
		r.t.Fatal(err)
	}
	var refs []mrwatch.Ref
	for _, u := range urls {
		ref, err := mrwatch.ParseURL(u)
		if err != nil {
			r.t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	if _, err := r.s.AddMR(tk.ID, task.MRFromWorker, refs...); err != nil {
		r.t.Fatal(err)
	}
	for _, to := range []task.State{task.Running, state} {
		if _, err := r.s.Transition(tk.ID, to, ""); err != nil {
			r.t.Fatal(err)
		}
	}
	return tk.ID
}

func (r *rig) poll() {
	r.t.Helper()
	if _, err := r.w.PollDue(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) tick() {
	r.clock = r.clock.Add(time.Minute)
	r.poll()
}

func (r *rig) state(id task.ID) task.State {
	r.t.Helper()
	tk, err := r.s.Get(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tk.State
}

func (r *rig) events(id task.ID, typ mrwatch.FactKind) []task.Event {
	r.t.Helper()
	evs, err := r.s.Events(id, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []task.Event
	for _, e := range evs {
		if e.Type == task.EventType(typ) {
			out = append(out, e)
		}
	}
	return out
}

func (r *rig) question(id task.ID) string {
	b, _ := os.ReadFile(r.s.QuestionPath(id))
	return string(b)
}

func open(ci mrwatch.CI, approved bool, notes ...mrwatch.Note) mrwatch.Snapshot {
	return mrwatch.Snapshot{State: mrwatch.Open, HeadSHA: "abc", CI: ci, Approved: approved, Notes: notes}
}

func TestBaselineThenComments(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	r.c.set(urlA, open(mrwatch.CIPassed, false, mrwatch.Note{ID: 5, Author: "old", Body: "before the watch"}))
	r.poll()
	if got := r.state(id); got != task.WaitingReview {
		t.Fatalf("after baseline state %s", got)
	}
	if evs, _ := r.s.Events(id, 0); len(r.events(id, mrwatch.FactComments)) != 0 {
		t.Fatalf("baseline raised comments: %+v", evs)
	}
	r.tick()
	if r.c.calls[urlA] != 2 {
		t.Fatalf("calls %d", r.c.calls[urlA])
	}
	long := strings.Repeat("word ", 200)
	r.c.set(urlA, open(mrwatch.CIPassed, false,
		mrwatch.Note{ID: 5, Author: "old", Body: "before the watch"},
		mrwatch.Note{ID: 9, Author: "alice", Body: "please rename this", URL: urlA + "#note_9"},
		mrwatch.Note{ID: 10, Author: "bob", Body: long}))
	r.tick()
	if got := r.state(id); got != task.NeedsDecision {
		t.Fatalf("state %s", got)
	}
	evs := r.events(id, mrwatch.FactComments)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "2 new comments") {
		t.Fatalf("comment events %+v", evs)
	}
	q := r.question(id)
	if !strings.Contains(q, "alice: please rename this ("+urlA+"#note_9)") || !strings.Contains(q, "…") || strings.Contains(q, long) {
		t.Fatalf("question %q", q)
	}
	mrs, _ := r.s.MRs(id)
	if mrs[0].CommentsSinceAck != 2 || mrs[0].Watch.LastNoteID != 10 {
		t.Fatalf("mr %+v", mrs[0])
	}
	r.tick()
	if n := len(r.events(id, mrwatch.FactComments)); n != 1 {
		t.Fatalf("comments repeated: %d", n)
	}
	if _, err := r.s.Ack(id, "told the Captain"); err != nil {
		t.Fatal(err)
	}
	if got := r.state(id); got != task.WaitingReview {
		t.Fatalf("after ack %s", got)
	}
}

func TestCIRedReadyAndMerged(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA, urlB)
	r.c.set(urlA, open(mrwatch.CIRunning, false))
	r.c.set(urlB, open(mrwatch.CIPassed, false))
	r.poll()
	r.c.set(urlA, open(mrwatch.CIFailed, false))
	r.tick()
	if got := r.state(id); got != task.NeedsDecision || !strings.Contains(r.question(id), "CI failed on "+urlA) {
		t.Fatalf("state %s question %q", got, r.question(id))
	}
	if _, err := r.s.Ack(id, ""); err != nil {
		t.Fatal(err)
	}
	r.c.set(urlA, open(mrwatch.CIPassed, true))
	r.tick()
	if len(r.events(id, mrwatch.FactCIGreen)) != 1 || len(r.events(id, mrwatch.FactReady)) != 1 || r.state(id) != task.NeedsDecision {
		t.Fatalf("green/ready not raised; state %s", r.state(id))
	}
	if _, err := r.s.Ack(id, ""); err != nil {
		t.Fatal(err)
	}
	r.c.set(urlA, mrwatch.Snapshot{State: mrwatch.Merged, HeadSHA: "abc", CI: mrwatch.CIPassed, Approved: true})
	r.tick()
	if got := r.state(id); got != task.WaitingReview || len(r.events(id, mrwatch.FactMerged)) != 1 {
		t.Fatalf("one of two merged: state %s", got)
	}
	r.c.set(urlB, mrwatch.Snapshot{State: mrwatch.Merged, HeadSHA: "def", CI: mrwatch.CIPassed})
	r.tick()
	if got := r.state(id); got != task.Merged {
		t.Fatalf("all merged: state %s", got)
	}
	ws, err := OpenMRs(r.s)
	if err != nil || len(ws) != 0 {
		t.Fatalf("open MRs after merge %+v %v", ws, err)
	}
}

func TestClosedNeedsCaptainAndRunningTaskOnlyGetsEvents(t *testing.T) {
	r := newRig(t)
	closed := r.shipTask(task.WaitingReview, urlA)
	running := r.shipTask(task.Running, urlB)
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	r.c.set(urlB, open(mrwatch.CIPassed, false))
	r.poll()
	r.c.set(urlA, mrwatch.Snapshot{State: mrwatch.Closed, HeadSHA: "abc"})
	r.c.set(urlB, open(mrwatch.CIFailed, false))
	r.tick()
	if got := r.state(closed); got != task.NeedsDecision || !strings.Contains(r.question(closed), "closed without merging") {
		t.Fatalf("closed: state %s", got)
	}
	if got := r.state(running); got != task.Running || len(r.events(running, mrwatch.FactCIRed)) != 1 {
		t.Fatalf("running Task: state %s", got)
	}
}

func TestFailingReportedOnceAfterAnHour(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	r.c.errs[urlA] = errors.New("context deadline exceeded")
	for range 61 {
		r.tick()
	}
	if n := len(r.events(id, mrwatch.FactFailing)); n != 1 {
		t.Fatalf("failing events %d", n)
	}
	if got := r.state(id); got != task.WaitingReview {
		t.Fatalf("failing changed state to %s", got)
	}
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	r.tick()
	mrs, _ := r.s.MRs(id)
	if !mrs[0].Watch.Polled || mrs[0].Watch.LastError != "" || !mrs[0].Watch.FailingSince.IsZero() {
		t.Fatalf("recovery %+v", mrs[0].Watch)
	}
}

func TestSaveKeepsAConcurrentChange(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	mrs, _ := r.s.MRs(id)
	stale := mrs[0]
	if _, err := r.s.UpdateMRs(id, func(m *[]task.MR) error {
		(*m)[0].Watch.State = mrwatch.Closed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	if err := r.w.poll(context.Background(), id, stale); err != nil {
		t.Fatal(err)
	}
	mrs, _ = r.s.MRs(id)
	if mrs[0].Watch.State != mrwatch.Closed {
		t.Fatalf("poll overwrote the concurrent change: %+v", mrs[0].Watch)
	}
}

func TestRunExitsWhenIdleAndHoldsTheClaim(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	polls := 0
	r.w.Sleep = func(ctx context.Context, d time.Duration) error {
		polls++
		rec, err := ReadRecord(r.h)
		if err != nil || !rec.Live() || rec.PID != os.Getpid() {
			t.Errorf("claim while running %+v %v", rec, err)
		}
		if err := r.w.claim(); !errors.Is(err, ErrRunning) {
			t.Errorf("second claim: %v", err)
		}
		r.clock = r.clock.Add(d)
		if polls == 2 {
			r.c.set(urlA, mrwatch.Snapshot{State: mrwatch.Merged, HeadSHA: "abc"})
		}
		return nil
	}
	if err := r.w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.state(id); got != task.Merged {
		t.Fatalf("state %s", got)
	}
	rec, err := ReadRecord(r.h)
	if err != nil || rec.Live() {
		t.Fatalf("record after exit %+v %v", rec, err)
	}
}

func TestEnsure(t *testing.T) {
	r := newRig(t)
	starts := 0
	start := func() error { starts++; return nil }
	if ok, err := Ensure(r.h, start); err != nil || ok || starts != 0 {
		t.Fatalf("no MRs: started=%v %v", ok, err)
	}
	r.shipTask(task.WaitingReview, urlA)
	if ok, err := Ensure(r.h, start); err != nil || !ok || starts != 1 {
		t.Fatalf("open MR: started=%v %v", ok, err)
	}
	if err := r.w.claim(); err != nil {
		t.Fatal(err)
	}
	if ok, err := Ensure(r.h, start); err != nil || ok || starts != 1 {
		t.Fatalf("live watcher: started=%v %v", ok, err)
	}
}

func TestOneClosedThenTheOtherMerged(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA, urlB)
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	r.c.set(urlB, open(mrwatch.CIPassed, false))
	r.poll()
	r.c.set(urlA, mrwatch.Snapshot{State: mrwatch.Closed, HeadSHA: "abc"})
	r.tick()
	if got := r.state(id); got != task.NeedsDecision {
		t.Fatalf("closed: %s", got)
	}
	if _, err := r.s.Ack(id, ""); err != nil {
		t.Fatal(err)
	}
	r.c.set(urlB, mrwatch.Snapshot{State: mrwatch.Merged, HeadSHA: "def"})
	r.tick()
	if got := r.state(id); got != task.Merged {
		t.Fatalf("one closed, one merged: %s", got)
	}
	if ws, _ := OpenMRs(r.s); len(ws) != 0 {
		t.Fatalf("still watching %+v", ws)
	}
}

func TestFactWhileWorkerRunsIsRaisedLater(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	r.poll()
	if _, err := r.s.Transition(id, task.Running, "fixing"); err != nil {
		t.Fatal(err)
	}
	r.c.set(urlA, open(mrwatch.CIPassed, false, mrwatch.Note{ID: 3, Author: "carol", Body: "one more thing"}))
	r.tick()
	if got := r.state(id); got != task.Running {
		t.Fatalf("running Task moved to %s", got)
	}
	if _, err := r.s.Transition(id, task.WaitingReview, "pushed"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.state(id); got != task.NeedsDecision || !strings.Contains(r.question(id), "carol: one more thing") {
		t.Fatalf("state %s question %q", got, r.question(id))
	}
}

func TestRestartRepeatsNothing(t *testing.T) {
	r := newRig(t)
	id := r.shipTask(task.WaitingReview, urlA)
	r.c.set(urlA, open(mrwatch.CIPassed, false))
	r.poll()
	r.c.set(urlA, open(mrwatch.CIFailed, false, mrwatch.Note{ID: 4, Author: "dave", Body: "red"}))
	r.tick()
	again := *r.w
	r.w = &again
	r.tick()
	r.tick()
	if n, m := len(r.events(id, mrwatch.FactCIRed)), len(r.events(id, mrwatch.FactComments)); n != 1 || m != 1 {
		t.Fatalf("ci-red %d comments %d", n, m)
	}
	f, _ := r.s.ReadMRFile(id)
	if len(f.Asks) != 2 || f.MRs[0].CommentsSinceAck != 1 {
		t.Fatalf("asks %+v comments %d", f.Asks, f.MRs[0].CommentsSinceAck)
	}
}
