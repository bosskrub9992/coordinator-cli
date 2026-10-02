package task

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransitionFromIsCompareAndSwap(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "cas")
	if _, err := s.TransitionFrom(tk.ID, Queued, Running, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionFrom(tk.ID, Queued, Running, "second"); !errors.Is(err, ErrStateChanged) {
		t.Fatalf("second CAS: %v", err)
	}
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.TransitionFrom(tk.ID, Running, Blocked, "race")
			wins <- err == nil
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	got, _ := s.Get(tk.ID)
	if got.State != Blocked || n != 1 {
		t.Fatalf("state %s, %d winners", got.State, n)
	}
	evs, _ := s.Events(tk.ID, 0)
	changes := 0
	for _, e := range evs {
		if e.Type == EventStateChanged {
			changes++
		}
	}
	if changes != 2 {
		t.Fatalf("%d state events, want 2", changes)
	}
}

func TestTransitionWritesStateBeforeEvent(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "order")
	os.Remove(s.EventsPath(tk.ID))
	if err := os.Mkdir(s.EventsPath(tk.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(tk.ID, Running, "x"); err == nil {
		t.Fatal("append into a directory succeeded")
	}
	if got, _ := s.Get(tk.ID); got.State != Running {
		t.Fatalf("state %s: the event was attempted before state.json was written", got.State)
	}
}

func TestReportedCanEndOrEscalate(t *testing.T) {
	for _, to := range []State{Dropped, Failed, NeedsDecision, Running, WaitingReview, Merged, Landed} {
		if !CanTransition(Reported, to) {
			t.Errorf("reported -> %s refused", to)
		}
	}
	if CanTransition(Reported, Blocked) {
		t.Error("reported -> blocked allowed")
	}
}

func TestClaimSpawnIsExclusive(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "spawn")
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	var errs []error
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := s.ClaimSpawn(tk.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			releases = append(releases, rel)
		}()
	}
	wg.Wait()
	if len(releases) != 1 || len(errs) != 5 {
		t.Fatalf("%d claims, %d errors", len(releases), len(errs))
	}
	for _, err := range errs {
		if !errors.Is(err, ErrSpawning) {
			t.Fatalf("err %v", err)
		}
	}
	releases[0]()
	rel, err := s.ClaimSpawn(tk.ID)
	if err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	s.Transition(tk.ID, Running, "")
	rel()
	if _, err := s.ClaimSpawn(tk.ID); err == nil || !strings.Contains(err.Error(), "only a queued Task") {
		t.Fatalf("claim of a running Task: %v", err)
	}
}

func TestClaimSpawnTakesOverDeadClaim(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "stale")
	s.UpdateWorker(tk.ID, func(w *WorkerRecord) error {
		w.Spawning = &SpawnClaim{Token: "old", PID: 1 << 30, At: time.Now()}
		return nil
	})
	rel, err := s.ClaimSpawn(tk.ID)
	if err != nil {
		t.Fatalf("stale claim blocked spawn: %v", err)
	}
	rel()
	if w, _ := s.Worker(tk.ID); w.Spawning != nil {
		t.Fatalf("claim not released: %+v", w.Spawning)
	}
}

func TestReturnedWorktrees(t *testing.T) {
	w := WorkerRecord{Worktrees: []Worktree{{Path: "/a"}, {Path: "/b", ReturnedAt: time.Now()}}}
	if got := w.WorktreePaths(); len(got) != 1 || got[0] != "/a" {
		t.Fatalf("active paths %v", got)
	}
}
