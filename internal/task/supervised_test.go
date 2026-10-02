package task

import (
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Skip("no true binary:", err)
	}
	return c.Process.Pid
}

func withSupervisor(t *testing.T, s *Store, id ID, pid int) {
	t.Helper()
	if _, err := s.UpdateWorker(id, func(w *WorkerRecord) error {
		w.SessionID = "s"
		w.SupervisorPID = pid
		w.SupervisorStart = home.ProcessStart(pid)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func count(evs []Event, ty EventType) int {
	n := 0
	for _, e := range evs {
		if e.Type == ty {
			n++
		}
	}
	return n
}

func TestReapLostSupervisorOnce(t *testing.T) {
	s, launch := newStore(t)
	lostTask := create(t, s, launch, "lost")
	liveTask := create(t, s, launch, "live")
	queued := create(t, s, launch, "queued")
	review := create(t, s, launch, "review")
	for _, id := range []ID{lostTask.ID, liveTask.ID, review.ID} {
		if _, err := s.Transition(id, Running, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Transition(review.ID, WaitingReview, ""); err != nil {
		t.Fatal(err)
	}
	dead := deadPID(t)
	withSupervisor(t, s, lostTask.ID, dead)
	withSupervisor(t, s, liveTask.ID, os.Getpid())
	withSupervisor(t, s, queued.ID, dead)
	withSupervisor(t, s, review.ID, dead)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []ID
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ReapLostSupervisors()
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			all = append(all, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(all) != 1 || all[0] != lostTask.ID {
		t.Fatalf("reaped %v, want only %s once", all, lostTask.ID)
	}
	got, _ := s.Get(lostTask.ID)
	if got.State != Failed {
		t.Fatalf("state %s", got.State)
	}
	evs, _ := s.Events(lostTask.ID, 0)
	if count(evs, EventWorkerExited) != 1 {
		t.Fatalf("worker-exited written %d times", count(evs, EventWorkerExited))
	}
	var exited Event
	for _, e := range evs {
		if e.Type == EventWorkerExited {
			exited = e
		}
	}
	var data struct{ Reason string }
	if json.Unmarshal(exited.Data, &data); data.Reason != ReasonSupervisorLost {
		t.Fatalf("exit data %s", exited.Data)
	}
	if last := evs[len(evs)-1]; last.Type != EventStateChanged || last.From != Running || last.To != Failed || last.Text != ReasonSupervisorLost {
		t.Fatalf("last event %+v", last)
	}
	w, _ := s.Worker(lostTask.ID)
	if w.SupervisorPID != 0 || w.ExitedAt.IsZero() {
		t.Fatalf("worker record %+v", w)
	}
	for _, id := range []ID{liveTask.ID, queued.ID, review.ID} {
		before, _ := s.Get(id)
		if again, _ := s.ReapLostSupervisors(); len(again) != 0 {
			t.Fatalf("second reap %v", again)
		}
		if after, _ := s.Get(id); after.State != before.State {
			t.Fatalf("%s moved %s -> %s", id, before.State, after.State)
		}
	}
}

func TestReapCleanExitIsNotLost(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "clean")
	s.Transition(tk.ID, Running, "")
	s.Transition(tk.ID, NeedsDecision, "")
	withSupervisor(t, s, tk.ID, 0)
	if got, err := s.ReapLostSupervisors(); err != nil || len(got) != 0 {
		t.Fatalf("reaped %v %v", got, err)
	}
}
