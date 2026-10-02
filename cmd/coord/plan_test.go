package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestSkipPlan(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("ship", "Tiny fix", "REPORT done fixed", "--skip-plan")
	tk, _ := r.store().Get(id)
	if !tk.SkipPlan {
		t.Fatalf("SkipPlan not recorded: %+v", tk)
	}
	r.run("spawn", string(id))
	r.waitState(id, task.Reported)
	r.waitIdle(id)
	prompt, err := os.ReadFile(r.store().SystemPromptPath(id))
	if err != nil || !strings.Contains(string(prompt), "Plan: pre-approved by the Coordinator") {
		t.Fatalf("prompt lacks pre-approval: %v", err)
	}
	planned := r.newTask("ship", "Real change", "SAY hi")
	r.run("spawn", string(planned))
	r.waitState(planned, task.Blocked)
	prompt, _ = os.ReadFile(r.store().SystemPromptPath(planned))
	if !strings.Contains(string(prompt), "Plan: required.") || !strings.Contains(string(prompt), "even when the Brief lists exact steps") {
		t.Fatalf("prompt lacks the Plan rule:\n%s", prompt)
	}
	r.run("stop", string(planned))
	r.waitIdle(planned)
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"--class", "scout", "--skip-plan"}, "for ship Tasks"},
		{[]string{"--class", "ship", "--skip-plan", "--plan-approval", "all"}, "plan_approval is all (from the task setting)"},
	} {
		args := append([]string{"task", "new", "--project", "repo", "--title", "x", "--brief", "-"}, tt.args...)
		if _, err := coord(t, "b", args...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: %v", tt.args, err)
		}
	}
}
