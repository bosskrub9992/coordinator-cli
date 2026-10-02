package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestShowIsOpenAndRendersTheReport(t *testing.T) {
	f := newFleet(t)
	captainTTY(t, false)
	id := f.task(t, "fix login timeout", task.Running)
	if _, err := f.store.UpdateWorker(id, func(w *task.WorkerRecord) error {
		w.Worktrees = []task.Worktree{{Project: "p", Path: "/pool/1/p", Branch: "coord/" + string(id)}}
		w.Model, w.Effort, w.Harness = "claude-opus-5-5", "high", "claude"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out, err := coord(t, "", "show", string(id)); err != nil || !strings.Contains(out, "No Report yet.") || !strings.Contains(out, "running") {
		t.Fatalf("before the Report: %q %v", out, err)
	}
	if _, err := coord(t, "", "show", string(id), "--report"); err == nil || !strings.Contains(err.Error(), "no Report yet") {
		t.Fatalf("--report before the Report: %v", err)
	}
	mr := "https://gitlab.example.com/acme/backend/api/-/merge_requests/9"
	if err := f.store.WriteReport(id, "# Fixed\nlogin timeout gone\n"); err != nil {
		t.Fatal(err)
	}
	f.event(t, id, task.Event{Type: task.EventReport, Text: "done: Fixed", Data: []byte(`{"status":"done","mr_urls":["` + mr + `"]}`)})
	if _, err := f.store.Transition(id, task.WaitingReview, ""); err != nil {
		t.Fatal(err)
	}
	out, err := coord(t, "", "show", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{string(id), "waiting-review", "Class:       ship", "Project:     p", "/pool/1/p (branch coord/" + string(id) + ")", "MR:          " + mr, "claude-opus-5-5 high", "Report:\n# Fixed\nlogin timeout gone\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if out, err := coord(t, "", "show", string(id), "--report"); err != nil || out != "# Fixed\nlogin timeout gone\n" {
		t.Fatalf("--report: %q %v", out, err)
	}
	if out, err := coord(t, "", "show", string(id), "--brief"); err != nil || out != "b\n" {
		t.Fatalf("--brief: %q %v", out, err)
	}
	if _, err := coord(t, "", "show", string(id), "--brief", "--report"); err == nil {
		t.Fatal("two parts accepted")
	}
}

func TestShowPendingQuestion(t *testing.T) {
	f := newFleet(t)
	captainTTY(t, false)
	id := f.task(t, "pick one", task.Running, task.NeedsDecision)
	if err := os.WriteFile(f.store.QuestionPath(id), []byte("A or B?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := coord(t, "", "show", string(id))
	if err != nil || !strings.Contains(out, "Question (needs-decision):\nA or B?\n") {
		t.Fatalf("show: %q %v", out, err)
	}
	if out, err := coord(t, "", "show", string(id), "--question"); err != nil || out != "A or B?\n" {
		t.Fatalf("--question: %q %v", out, err)
	}
}

func TestShowRefusedToWorkers(t *testing.T) {
	f := newFleet(t)
	id := f.task(t, "a", task.Running)
	t.Setenv(supervise.EnvRole, supervise.RoleWorker)
	if _, err := coord(t, "", "show", string(id)); err == nil || !strings.Contains(err.Error(), "not available to a Worker") {
		t.Fatalf("Worker ran show: %v", err)
	}
}
