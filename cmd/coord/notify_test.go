package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestNoticesOnlyWhileNoCoordinatorIsOpen(t *testing.T) {
	h := home.New(setup(t))
	f := stubNotifier(t)
	s := newStore(h)
	tk, err := s.Create(task.NewTask{Title: "fix it", Class: config.Ship, Projects: []string{"api"}, Brief: "b", LaunchFolder: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(tk.ID, task.Running, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(tk.ID, task.NeedsDecision, "pick a field name"); err != nil {
		t.Fatal(err)
	}
	sent := f.all()
	if len(sent) != 1 || !strings.Contains(sent[0], string(tk.ID)+" is needs-decision: pick a field name") || !strings.Contains(sent[0], "Open coord") {
		t.Fatalf("sent %q", sent)
	}
	if _, err := h.AcquireLock(home.Owner{PID: os.Getpid(), LaunchFolder: t.TempDir(), SessionID: "s"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(tk.ID, task.Running, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(tk.ID, task.Failed, "gave up"); err != nil {
		t.Fatal(err)
	}
	if got := f.all(); len(got) != 1 {
		t.Fatalf("notified while the Coordinator is open: %q", got)
	}
}

func TestNotifyCommand(t *testing.T) {
	setup(t)
	f := stubNotifier(t)
	out, err := coord(t, "", "notify", "the", "proto MR is ready to merge")
	if err != nil || !strings.Contains(out, "Notified the Captain.") {
		t.Fatalf("out %q err %v", out, err)
	}
	if got := f.all(); len(got) != 1 || got[0] != "coord: the proto MR is ready to merge" {
		t.Fatalf("sent %q", got)
	}
	f.err = errors.New("osascript failed")
	if _, err := coord(t, "", "notify", "again"); err == nil || !strings.Contains(err.Error(), "osascript failed") {
		t.Fatalf("delivery error not returned: %v", err)
	}
}
