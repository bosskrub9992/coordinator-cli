package role

import (
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestCoordinatorRole(t *testing.T) {
	r := Coordinator()
	for _, want := range []string{
		"never make a change", "Treat this folder's SOPs", "`run_in_background: true`", "re-arm `coord wait`",
		"Never substitute subagents", "coordinator_may_choose", "plan_approval", "--brief -", "`scout`",
		"stay available", "auto permission mode", "never read or grep the Home", "supervisor-lost", "fleet unsupervised", "--mr", "coord show <task>",
		"coord task add-mr", "coord task add-project", "coord ack", "coord land", "coord drop", "--close-mr", "coord notify",
		"coord merge <task>", "never ask a Worker to merge", "`Next:`", "--ticket-folder", "never spend a Worker", "first reply", "coord <command> --help",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("role lacks %q", want)
		}
	}
	for _, gone := range []string{"coord answer", "coord permission", "needs-permission", "secret-read", "standing grants"} {
		if strings.Contains(r, gone) {
			t.Errorf("role still mentions %q", gone)
		}
	}
	if n := len(strings.Fields(r)); n > 1650 {
		t.Errorf("role is %d words; it is always loaded, keep it tight", n)
	}
}

func TestStatesSectionCoversEveryState(t *testing.T) {
	s := States()
	for _, i := range task.States() {
		if !strings.Contains(s, "- `"+string(i.State)+"`: ") {
			t.Errorf("states section lacks %s", i.State)
		}
		for _, m := range i.Moves {
			if !strings.Contains(s, "`"+m.Command+"`") {
				t.Errorf("states section lacks %s's move %s", i.State, m.Command)
			}
		}
	}
	if !strings.Contains(s, "`coord drop <task>`") {
		t.Error("states section lacks the drop move")
	}
}
