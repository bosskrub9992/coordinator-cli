package supervise

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/worker"
	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type fakeWorker struct {
	events chan harness.WorkerEvent
	mu     sync.Mutex
	steers []harness.Steer
	ints   int
	stops  int
	once   sync.Once
}

func (w *fakeWorker) SessionID() string                  { return "sid" }
func (w *fakeWorker) PID() int                           { return os.Getpid() }
func (w *fakeWorker) Events() <-chan harness.WorkerEvent { return w.events }
func (w *fakeWorker) Stop(context.Context) error {
	w.mu.Lock()
	w.stops++
	w.mu.Unlock()
	return nil
}
func (w *fakeWorker) Kill() error                       { w.exit(); return nil }
func (w *fakeWorker) Wait() (harness.WorkerExit, error) { return harness.WorkerExit{}, nil }
func (w *fakeWorker) Interrupt(context.Context) error {
	w.mu.Lock()
	w.ints++
	w.mu.Unlock()
	return nil
}
func (w *fakeWorker) Steer(_ context.Context, s harness.Steer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.steers = append(w.steers, s)
	return nil
}

func (w *fakeWorker) sent() []harness.Steer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]harness.Steer(nil), w.steers...)
}

func (w *fakeWorker) stopped() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stops
}

func (w *fakeWorker) exit() {
	w.once.Do(func() {
		w.events <- harness.WorkerEvent{Kind: harness.EventExited, Exit: &harness.WorkerExit{}}
		close(w.events)
	})
}

type fakeRunner struct {
	mu      sync.Mutex
	workers []*fakeWorker
	specs   []harness.WorkerSpec
}

func (r *fakeRunner) Name() harness.Name { return harness.Claude }
func (r *fakeRunner) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return r.start(spec)
}
func (r *fakeRunner) Resume(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return r.start(spec)
}
func (r *fakeRunner) start(spec harness.WorkerSpec) (harness.Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := &fakeWorker{events: make(chan harness.WorkerEvent, 64)}
	r.workers = append(r.workers, w)
	r.specs = append(r.specs, spec)
	return w, nil
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.workers)
}

func (r *fakeRunner) worker(t *testing.T, i int) *fakeWorker {
	t.Helper()
	eventually(t, "worker started", func() bool { return r.count() > i })
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.workers[i]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 400 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func spawned(t *testing.T, started bool) (*task.Store, task.Task, *fakeRunner, *Supervisor) {
	t.Helper()
	s, tk := newStore(t)
	s.Transition(tk.ID, task.Running, "spawned")
	s.UpdateWorker(tk.ID, func(w *task.WorkerRecord) error {
		w.SessionID = "sid"
		w.SessionStarted = started
		return nil
	})
	r := &fakeRunner{}
	return s, tk, r, &Supervisor{Store: s, Runner: r, Task: tk.ID, Poll: 10 * time.Millisecond, StopGrace: time.Second}
}

func eventsOf(s *task.Store, id task.ID, typ task.EventType) []task.Event {
	evs, _ := s.Events(id, 0)
	var out []task.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestSupervisorActsOnStopQueuedBeforeStart(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	st, _ := Post(s, tk.ID, InboxSteer, "do more")
	Post(s, tk.ID, InboxStop, "")
	if err := sv.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Fatal("a Worker started although a stop was queued after the steer")
	}
	if got, _ := s.Get(tk.ID); got.State != task.Failed {
		t.Fatalf("state %s", got.State)
	}
	c := eventsOf(s, tk.ID, task.EventSteerCancelled)
	if len(c) != 1 || !strings.Contains(string(c[0].Data), st.ID) {
		t.Fatalf("cancelled %+v", c)
	}
	if q, _ := Queued(s, tk.ID); len(q) != 0 {
		t.Fatalf("inbox left %+v", q)
	}
}

func TestSupervisorStartQueueKeepsSteersAfterStopAndInterrupt(t *testing.T) {
	s, tk, _, sv := spawned(t, true)
	Post(s, tk.ID, InboxSteer, "old")
	Post(s, tk.ID, InboxInterrupt, "")
	Post(s, tk.ID, InboxSteer, "new")
	steers, stopped := sv.startQueue()
	if stopped || len(steers) != 1 || steers[0].Text != "new" {
		t.Fatalf("steers %+v stopped %v", steers, stopped)
	}
	if c := eventsOf(s, tk.ID, task.EventSteerCancelled); len(c) != 1 || !strings.Contains(c[0].Text, "interrupt") {
		t.Fatalf("cancelled %+v", c)
	}
}

func TestSupervisorSteerReceiptsDropsAndIdle(t *testing.T) {
	s, tk, r, sv := spawned(t, false)
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	w.events <- harness.WorkerEvent{Kind: harness.EventSteerDelivered, SteerID: worker.BriefSteerID}
	a, _ := Post(s, tk.ID, InboxSteer, "first")
	b, _ := Post(s, tk.ID, InboxSteer, "second")
	eventually(t, "both steers sent", func() bool { return len(w.sent()) == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := len(w.sent()); n != 2 {
		t.Fatalf("steers re-sent: %d", n)
	}
	w.events <- harness.WorkerEvent{Kind: harness.EventSteerDelivered, SteerID: a.ID, Text: "first, as Claude Code echoed it"}
	w.events <- harness.WorkerEvent{Kind: harness.EventTurnEnded, Turn: &harness.TurnResult{Subtype: "success"}}
	w.events <- harness.WorkerEvent{Kind: harness.EventSteerDropped, SteerID: b.ID}
	eventually(t, "Task blocked once nothing is in flight", func() bool {
		got, _ := s.Get(tk.ID)
		return got.State == task.Blocked
	})
	if st := eventsOf(s, tk.ID, task.EventSteer); len(st) != 1 || st[0].Text != "first" {
		t.Fatalf("steer events %+v", st)
	}
	if c := eventsOf(s, tk.ID, task.EventSteerCancelled); len(c) != 1 || !strings.Contains(string(c[0].Data), b.ID) {
		t.Fatalf("cancelled %+v", c)
	}
	if q, _ := Queued(s, tk.ID); len(q) != 0 {
		t.Fatalf("inbox left %+v", q)
	}
	sent, _ := os.ReadDir(filepath.Join(s.InboxDir(tk.ID), InboxSent))
	cancelled, _ := os.ReadDir(filepath.Join(s.InboxDir(tk.ID), InboxCancelled))
	if len(sent) != 1 || len(cancelled) != 1 {
		t.Fatalf("sent %d cancelled %d", len(sent), len(cancelled))
	}
	w.exit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorKeepsUndeliveredSteerQueued(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	m, _ := Post(s, tk.ID, InboxSteer, "please")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	w.exit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	q, _ := Queued(s, tk.ID)
	if len(q) != 1 || q[0].ID != m.ID {
		t.Fatalf("undelivered steer lost: %+v", q)
	}
	if r.count() != 1 {
		t.Fatalf("resumed %d times with a steer the Worker never read", r.count())
	}
}

func TestSupervisorStopCancelsInflightSteers(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	m, _ := Post(s, tk.ID, InboxSteer, "go on")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	Post(s, tk.ID, InboxStop, "")
	eventually(t, "steer cancelled", func() bool { return len(eventsOf(s, tk.ID, task.EventSteerCancelled)) == 1 })
	w.exit()
	<-done
	if q, _ := Queued(s, tk.ID); len(q) != 0 {
		t.Fatalf("inbox %+v", q)
	}
	if c := eventsOf(s, tk.ID, task.EventSteerCancelled); !strings.Contains(string(c[0].Data), m.ID) {
		t.Fatalf("cancelled %+v", c)
	}
	if got, _ := s.Get(tk.ID); got.State != task.Failed {
		t.Fatalf("state %s", got.State)
	}
}

type fakeReturner struct{ paths []string }

func (f *fakeReturner) Return(path, leaseID string) error {
	f.paths = append(f.paths, path+"#"+leaseID)
	return nil
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestReturnLeaseAfterScoutReport(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h := home.New(filepath.Join(root, "home"))
	h.Ensure()
	s := task.NewStore(h)
	origin, wt := filepath.Join(root, "origin.git"), filepath.Join(root, "wt")
	gitT(t, root, "init", "-q", "--bare", origin)
	gitT(t, root, "init", "-q", "-b", "main", wt)
	os.WriteFile(filepath.Join(wt, "a"), []byte("a"), 0o644)
	gitT(t, wt, "add", ".")
	gitT(t, wt, "commit", "-qm", "a")
	gitT(t, wt, "remote", "add", "origin", origin)
	gitT(t, wt, "push", "-q", "origin", "main")
	for _, class := range []config.Class{config.Scout, config.Ship} {
		tk, err := s.Create(task.NewTask{Title: string(class), Class: class, Projects: []string{"p"}, Brief: "b", LaunchFolder: root})
		if err != nil {
			t.Fatal(err)
		}
		s.Transition(tk.ID, task.Running, "")
		s.Transition(tk.ID, task.Reported, "")
		s.UpdateWorker(tk.ID, func(w *task.WorkerRecord) error {
			w.Worktrees = []task.Worktree{{Project: "p", Path: wt, LeaseID: "L1"}}
			return nil
		})
		f := &fakeReturner{}
		(&Supervisor{Store: s, Task: tk.ID, Treehouse: f}).returnLease()
		rec, _ := s.Worker(tk.ID)
		if class == config.Ship {
			if len(f.paths) != 0 || len(rec.WorktreePaths()) != 1 {
				t.Fatalf("ship lease returned: %v", f.paths)
			}
			continue
		}
		if len(f.paths) != 1 || f.paths[0] != wt+"#L1" || len(rec.WorktreePaths()) != 0 || rec.Worktrees[0].ReturnedAt.IsZero() {
			t.Fatalf("scout lease not returned: %v %+v", f.paths, rec.Worktrees)
		}
		if ev := eventsOf(s, tk.ID, task.EventWorktreeReturned); len(ev) != 1 {
			t.Fatalf("events %+v", ev)
		}
	}
	if ok, _ := WorktreeSettled(wt); !ok {
		t.Fatal("clean pushed worktree not settled")
	}
	os.WriteFile(filepath.Join(wt, "b"), []byte("b"), 0o644)
	if ok, why := WorktreeSettled(wt); ok || !strings.Contains(why, "uncommitted") {
		t.Fatalf("dirty worktree: %v %s", ok, why)
	}
	gitT(t, wt, "add", ".")
	gitT(t, wt, "commit", "-qm", "b")
	if ok, why := WorktreeSettled(wt); ok || !strings.Contains(why, "1 commits") {
		t.Fatalf("unpushed commit: %v %s", ok, why)
	}
}

func TestSupervisorSteerReadAfterTurnEndIsNotIdle(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	m, _ := Post(s, tk.ID, InboxSteer, "next")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	w.events <- harness.WorkerEvent{Kind: harness.EventTurnEnded, Turn: &harness.TurnResult{Subtype: "success"}}
	w.events <- harness.WorkerEvent{Kind: harness.EventSteerDelivered, SteerID: m.ID}
	eventually(t, "steer event", func() bool { return len(eventsOf(s, tk.ID, task.EventSteer)) == 1 })
	time.Sleep(100 * time.Millisecond)
	if got, _ := s.Get(tk.ID); got.State != task.Running {
		t.Fatalf("a Worker reading its next steer was marked %s", got.State)
	}
	w.exit()
	<-done
}

func TestUsageLimitText(t *testing.T) {
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.Local)
	if got := UsageLimitText(time.Date(2026, 10, 2, 14, 30, 0, 0, time.Local).Unix(), now); got != "usage limit, resets at 14:30" {
		t.Errorf("today: %q", got)
	}
	if got := UsageLimitText(time.Date(2026, 10, 3, 1, 5, 0, 0, time.Local).Unix(), now); got != "usage limit, resets at 2026-10-03 01:05" {
		t.Errorf("tomorrow: %q", got)
	}
	if got := UsageLimitText(0, now); got != "usage limit" {
		t.Errorf("no reset time: %q", got)
	}
}

func rateLimit(status string, resets int64) harness.WorkerEvent {
	return harness.WorkerEvent{Kind: harness.EventRateLimited, Raw: []byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"` + status + `","resetsAt":` + strconv.FormatInt(resets, 10) + `,"rateLimitType":"five_hour"}}`)}
}

func TestUsageLimitBlocksOnce(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	Post(s, tk.ID, InboxSteer, "go on")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	resets := time.Now().Add(2 * time.Hour).Unix()
	w.events <- harness.WorkerEvent{Kind: harness.EventSteerDelivered, SteerID: w.sent()[0].ID}
	w.events <- rateLimit("allowed", resets)
	w.events <- rateLimit("rejected", resets)
	w.events <- rateLimit("rejected", resets)
	w.events <- harness.WorkerEvent{Kind: harness.EventTurnEnded, Turn: &harness.TurnResult{Subtype: "success", IsError: true, Text: "limit reached"}}
	eventually(t, "blocked", func() bool { got, _ := s.Get(tk.ID); return got.State == task.Blocked })
	time.Sleep(100 * time.Millisecond)
	var blocked []task.Event
	for _, e := range eventsOf(s, tk.ID, task.EventStateChanged) {
		if e.To == task.Blocked {
			blocked = append(blocked, e)
		}
	}
	want := UsageLimitText(resets, time.Now())
	if len(blocked) != 1 || blocked[0].Text != want || len(eventsOf(s, tk.ID, task.EventRateLimited)) != 0 {
		t.Fatalf("blocked events %+v, rate-limited %+v; want one %q", blocked, eventsOf(s, tk.ID, task.EventRateLimited), want)
	}
	if !strings.HasPrefix(want, "usage limit, resets at ") {
		t.Fatalf("text %q", want)
	}
	if r.count() != 1 {
		t.Fatal("the Worker was restarted on its own")
	}
	w.exit()
	<-done
	if got, _ := s.Get(tk.ID); got.State != task.Blocked {
		t.Fatalf("state after exit %s", got.State)
	}
}

func TestSupervisorClosesStdinWhenReportSettlesToWatcherAsk(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	Post(s, tk.ID, InboxSteer, "follow up")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	s.SetQuestion(tk.ID, task.QuestionFromWatcher, "MR checks failed")
	s.Transition(tk.ID, task.NeedsDecision, "MR checks failed")
	Post(s, tk.ID, InboxReported, "done")
	eventually(t, "stdin closed", func() bool { return w.stopped() == 1 })
	w.exit()
	<-done
	got, _ := s.Get(tk.ID)
	if got.State != task.NeedsDecision || got.QuestionFrom != task.QuestionFromWatcher {
		t.Fatalf("got %s from %q", got.State, got.QuestionFrom)
	}
}

func TestSupervisorKeepsStdinOpenOnWorkerNeedsDecision(t *testing.T) {
	s, tk, r, sv := spawned(t, true)
	Post(s, tk.ID, InboxSteer, "follow up")
	done := make(chan error, 1)
	go func() { done <- sv.Run(context.Background()) }()
	w := r.worker(t, 0)
	eventually(t, "steer sent", func() bool { return len(w.sent()) == 1 })
	s.SetQuestion(tk.ID, task.QuestionFromWorker, "which approach")
	s.Transition(tk.ID, task.NeedsDecision, "which approach")
	Post(s, tk.ID, InboxReported, "asked")
	time.Sleep(200 * time.Millisecond)
	if n := w.stopped(); n != 0 {
		t.Fatalf("stdin closed %d times for a Worker-raised needs-decision", n)
	}
	w.exit()
	<-done
}
