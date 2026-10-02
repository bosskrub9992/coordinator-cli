package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func runWait(t *testing.T, f fleet, timeout time.Duration) string {
	t.Helper()
	var out bytes.Buffer
	if err := waitEvents(context.Background(), f.home, f.store, waitOptions{timeout: timeout, poll: waitPoll}, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func drain(t *testing.T, f fleet) {
	t.Helper()
	evs, err := f.store.Unread()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkRead(evs); err != nil {
		t.Fatal(err)
	}
}

func TestWaitBatchesEventsWithinWindow(t *testing.T) {
	f := newFleet(t)
	waitBatch = 150 * time.Millisecond
	a := f.task(t, "alpha", task.Running)
	b := f.task(t, "beta", task.Running)
	drain(t, f)
	go func() {
		f.store.WriteReport(a, "# Report\n")
		time.Sleep(40 * time.Millisecond)
		f.event(t, b, task.Event{Type: task.EventQuestion, Text: "which queue?"})
	}()
	got := lines(runWait(t, f, 2*time.Second))
	if len(got) != 2 || !strings.HasPrefix(got[0], string(a)+" report") || got[1] != string(b)+" question which queue?" {
		t.Fatalf("batch %q", got)
	}
	if out := runWait(t, f, 30*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("batch not marked read: %q", out)
	}
}

func TestWaitStateAccompanyingQuestionDoesNotWakeAgain(t *testing.T) {
	f := newFleet(t)
	waitSettle = time.Second
	a := f.task(t, "alpha", task.Running)
	drain(t, f)
	f.event(t, a, task.Event{Type: task.EventQuestion, Text: "which queue?"})
	out := runWait(t, f, time.Second)
	if out != string(a)+" question which queue?\n" {
		t.Fatalf("question batch %q", out)
	}
	if _, err := f.store.Transition(a, task.NeedsDecision, "which queue?"); err != nil {
		t.Fatal(err)
	}
	if out := runWait(t, f, 100*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("accompanying state change woke the Coordinator: %q", out)
	}
}

func TestWaitStateWrittenBeforeItsReportArrivesInOneBatch(t *testing.T) {
	f := newFleet(t)
	waitSettle = time.Second
	a := f.task(t, "alpha", task.Running)
	drain(t, f)
	go func() {
		f.store.Transition(a, task.Reported, "done")
		time.Sleep(200 * time.Millisecond)
		f.store.WriteReport(a, "# Report\n")
	}()
	got := lines(runWait(t, f, 3*time.Second))
	if len(got) != 2 || got[0] != string(a)+" state running -> reported: done" || !strings.HasPrefix(got[1], string(a)+" report") {
		t.Fatalf("got %q", got)
	}
}

func TestWaitLoneStateChangeWakesAfterSettling(t *testing.T) {
	f := newFleet(t)
	waitSettle = 120 * time.Millisecond
	a := f.task(t, "alpha", task.Running)
	drain(t, f)
	f.store.Transition(a, task.Blocked, "usage limit")
	start := time.Now()
	out := runWait(t, f, 2*time.Second)
	if out != string(a)+" state running -> blocked: usage limit\n" {
		t.Fatalf("out %q", out)
	}
	if d := time.Since(start); d < waitSettle {
		t.Fatalf("woke after %s, before the state settled", d)
	}
}

func TestWaitWorkerExitAfterReportDoesNotWake(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running)
	f.event(t, a, task.Event{Type: task.EventWorkerStarted})
	f.store.WriteReport(a, "# Report\n")
	f.store.Transition(a, task.Reported, "")
	drain(t, f)
	f.event(t, a, task.Event{Type: task.EventWorkerExited, Text: "Worker exited (code 0)"})
	if out := runWait(t, f, 100*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("exit after Report woke the Coordinator: %q", out)
	}

	f.store.Transition(a, task.Running, "")
	f.event(t, a, task.Event{Type: task.EventWorkerStarted})
	drain(t, f)
	f.event(t, a, task.Event{Type: task.EventWorkerExited, Text: "Worker exited (code 1)"})
	if out := runWait(t, f, time.Second); out != string(a)+" worker-exited Worker exited (code 1)\n" {
		t.Fatalf("crash without Report: %q", out)
	}
}

func TestWaitLeftoverQuietEventsDoNotWakeForNewTask(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running)
	f.event(t, a, task.Event{Type: task.EventWorkerStarted})
	f.store.WriteReport(a, "# Report\n")
	drain(t, f)
	f.event(t, a, task.Event{Type: task.EventWorkerExited, Text: "Worker exited (code 0)"})
	b := f.task(t, "beta")
	f.store.Transition(b, task.Running, "Worker spawning")
	f.event(t, b, task.Event{Type: task.EventWorkerStarted})
	if out := runWait(t, f, 200*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("leftover events woke the Coordinator: %q", out)
	}
}

func TestWaitDeliversFleetEvents(t *testing.T) {
	f := newFleet(t)
	if _, err := f.store.AppendFleet(task.Event{Type: task.EventUnsupervised, Text: "2 Tasks in flight are unsupervised"}); err != nil {
		t.Fatal(err)
	}
	if out := runWait(t, f, time.Second); out != "fleet unsupervised 2 Tasks in flight are unsupervised\n" {
		t.Fatalf("out %q", out)
	}
	if out := runWait(t, f, 30*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("fleet event delivered twice: %q", out)
	}
	a := f.task(t, "alpha", task.Running)
	f.store.AppendFleet(task.Event{Type: task.EventUnsupervised, Text: "x"})
	var out bytes.Buffer
	waitEvents(context.Background(), f.home, f.store, waitOptions{task: a, timeout: 50 * time.Millisecond, poll: waitPoll}, &out)
	if !strings.HasPrefix(out.String(), "no-event: timeout") {
		t.Fatalf("a Task-filtered wait printed a fleet event: %q", out.String())
	}
}

func lostSupervisor(t *testing.T, f fleet, title string) task.ID {
	t.Helper()
	id := f.task(t, title, task.Running)
	if _, err := f.store.UpdateWorker(id, func(w *task.WorkerRecord) error {
		w.SessionID = "s"
		w.SupervisorPID = deadPID(t)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWaitWakesWhenSupervisorIsLost(t *testing.T) {
	f := newFleet(t)
	a := lostSupervisor(t, f, "alpha")
	got := lines(runWait(t, f, 2*time.Second))
	if len(got) != 2 || !strings.HasPrefix(got[0], string(a)+" worker-exited") || got[1] != string(a)+" state running -> failed: "+task.ReasonSupervisorLost {
		t.Fatalf("got %q", got)
	}
	if out := runWait(t, f, 50*time.Millisecond); !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("lost supervisor reported twice: %q", out)
	}
}

func TestStatusAndStopHookReapLostSupervisor(t *testing.T) {
	f := newFleet(t)
	a := lostSupervisor(t, f, "alpha")
	out, err := coord(t, "", "status")
	if err != nil || !strings.Contains(out, string(a)+"  failed") {
		t.Fatalf("status %q %v", out, err)
	}
	b := lostSupervisor(t, f, "beta")
	if blocked(t, runHook(t, f, "", hookInput("s", false))) {
		t.Fatal("stop hook blocked on a Task whose supervisor is gone")
	}
	if tk, _ := f.store.Get(b); tk.State != task.Failed {
		t.Fatalf("stop hook left %s %s", b, tk.State)
	}
	c := lostSupervisor(t, f, "gamma")
	line, err := statusLine(f.home, f.store, "", absPath("/launch"))
	if err != nil || !strings.Contains(line, "3 failed") || strings.Contains(line, "need") || strings.Contains(line, "running") {
		t.Fatalf("status line %q %v", line, err)
	}
	if tk, _ := f.store.Get(c); tk.State != task.Failed {
		t.Fatalf("status line left %s %s", c, tk.State)
	}
}

func TestStatusLineCountsAndUnsupervised(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running, task.NeedsDecision)
	f.task(t, "beta", task.Running, task.Blocked)
	f.task(t, "gamma", task.Running)
	line, err := statusLine(f.home, f.store, "", absPath("/launch"))
	if err != nil || !strings.Contains(line, "1 running · 2 need you") || strings.Contains(line, "blocked") {
		t.Fatalf("status line %q %v", line, err)
	}
	out, err := coord(t, "", "status")
	if err != nil || !strings.Contains(out, string(a)+"  needs-decision") {
		t.Fatalf("status %q %v", out, err)
	}
	out, _ = coord(t, "", "status", "--json")
	var v statusView
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Tasks) != 3 || v.Tasks[0].ID != a || strings.Contains(out, "pending_permissions") {
		t.Fatalf("json %q %v", out, err)
	}
	f.store.AppendFleet(task.Event{Type: task.EventUnsupervised})
	if line, _ := statusLine(f.home, f.store, "", absPath("/launch")); !strings.Contains(line, "unsupervised") {
		t.Fatalf("status line misses unsupervised: %q", line)
	}
}
