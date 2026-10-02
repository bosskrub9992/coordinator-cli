package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type fleet struct {
	home  home.Home
	store *task.Store
}

func newFleet(t *testing.T) fleet {
	t.Helper()
	h := home.New(setup(t))
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	oldPoll, oldBatch, oldSettle := waitPoll, waitBatch, waitSettle
	waitPoll, waitBatch, waitSettle = 10*time.Millisecond, 20*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { waitPoll, waitBatch, waitSettle = oldPoll, oldBatch, oldSettle })
	return fleet{home: h, store: task.NewStore(h)}
}

func (f fleet) task(t *testing.T, title string, states ...task.State) task.ID {
	t.Helper()
	tk, err := f.store.Create(task.NewTask{Title: title, Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: "/launch"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if _, err := f.store.Transition(tk.ID, s, ""); err != nil {
			t.Fatal(err)
		}
	}
	return tk.ID
}

func (f fleet) event(t *testing.T, id task.ID, e task.Event) {
	t.Helper()
	if _, err := f.store.Append(id, e); err != nil {
		t.Fatal(err)
	}
}

func lines(out string) []string {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestWaitCursor(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running)
	b := f.task(t, "beta", task.Running)

	out, err := coord(t, "", "wait", "--timeout", "50ms")
	if err != nil || out != "no-event: timeout after 50ms\n" {
		t.Fatalf("quiet events woke the wait: %q %v", out, err)
	}

	f.event(t, a, task.Event{Type: task.EventNote, Text: "Worker denied: Bash(rm -rf build)"})
	f.event(t, b, task.Event{Type: task.EventQuestion, Text: "which\nqueue?"})
	if _, err := f.store.Transition(a, task.NeedsDecision, "needs the Captain"); err != nil {
		t.Fatal(err)
	}
	out, err = coord(t, "", "wait", "--timeout", "1s")
	if err != nil {
		t.Fatal(err)
	}
	got := lines(out)
	want := []string{
		string(a) + " note Worker denied: Bash(rm -rf build)",
		string(b) + " question which queue?",
		string(a) + " state running -> needs-decision: needs the Captain",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	out, _ = coord(t, "", "wait", "--timeout", "30ms")
	if !strings.HasPrefix(out, "no-event: timeout") {
		t.Fatalf("events delivered twice: %q", out)
	}

	f.event(t, a, task.Event{Type: task.EventNote, Text: "for a"})
	f.event(t, b, task.Event{Type: task.EventNote, Text: "for b"})
	out, _ = coord(t, "", "wait", string(b), "--timeout", "1s")
	if lines(out)[0] != string(b)+" note for b" || len(lines(out)) != 1 {
		t.Fatalf("filtered wait: %q", out)
	}
	out, _ = coord(t, "", "wait", "1", "--timeout", "1s")
	if out != string(a)+" note for a\n" {
		t.Fatalf("filter by number: %q", out)
	}

	if _, err := coord(t, "", "wait", "nope"); err == nil {
		t.Fatal("unknown Task accepted")
	}
}

func TestWaitWakesOnNewEvent(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running)
	go func() {
		time.Sleep(80 * time.Millisecond)
		f.store.WriteReport(a, "# Report\n")
	}()
	start := time.Now()
	var out bytes.Buffer
	err := waitEvents(context.Background(), f.home, f.store, waitOptions{timeout: 5 * time.Second, poll: 10 * time.Millisecond}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(a) + " report Report at " + f.store.ReportPath(a) + "\n"; out.String() != want {
		t.Fatalf("out %q want %q", out.String(), want)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("wait did not wake promptly")
	}
}

func TestWaitShutdown(t *testing.T) {
	f := newFleet(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := waitEvents(ctx, f.home, f.store, waitOptions{poll: 10 * time.Millisecond}, &out); err != nil || out.String() != "no-event: shutdown\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
}

func TestWaitSupersededLeavesEventsUnread(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "alpha", task.Running)
	old, err := f.home.AcquireLock(home.Owner{LaunchFolder: "/a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.home.AcquireLock(home.Owner{LaunchFolder: "/b"}, true); err != nil {
		t.Fatal(err)
	}
	f.event(t, a, task.Event{Type: task.EventNote, Text: "x"})
	var out bytes.Buffer
	if err := waitEvents(context.Background(), f.home, f.store, waitOptions{token: old.Token, timeout: time.Second, poll: 10 * time.Millisecond}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "no-event: superseded") {
		t.Fatalf("out %q", out.String())
	}
	out.Reset()
	if err := waitEvents(context.Background(), f.home, f.store, waitOptions{timeout: time.Second, poll: 10 * time.Millisecond}, &out); err != nil || out.String() != string(a)+" note x\n" {
		t.Fatalf("event lost after superseded wait: %q %v", out.String(), err)
	}
}

func TestEventLineTruncates(t *testing.T) {
	s := task.NewStore(home.New(t.TempDir()))
	line := eventLine(s, task.Event{Task: "001-a", Type: task.EventNote, Text: strings.Repeat("x", 500)})
	if r := []rune(line); len(r) != len("001-a note ")+summaryLimit || !strings.HasSuffix(line, "…") {
		t.Fatalf("line %q", line)
	}
	for _, ty := range []task.EventType{task.EventInstructionsLoaded, task.EventWorktreeReturned} {
		if !quiet(task.Event{Type: ty}) {
			t.Errorf("%s wakes the Coordinator", ty)
		}
	}
	if quiet(task.Event{Type: task.EventQuestion}) || quiet(task.Event{Type: task.EventSteerCancelled}) || quiet(task.Event{Type: task.EventRateLimited}) {
		t.Error("Worker-raised events must wake the Coordinator")
	}
	if got := eventLine(s, task.Event{Task: "001-a", Type: task.EventNote, Data: []byte(`{ "request_id": "r-9" }`)}); got != `001-a note {"request_id":"r-9"}` {
		t.Fatalf("data line %q", got)
	}
}
