package role

import (
	"strings"
	"testing"
)

func TestCoordinatorRole(t *testing.T) {
	r := Coordinator()
	for _, want := range []string{
		"never make a change", "Treat this folder's SOPs", "`run_in_background: true`", "re-arm `coord wait`",
		"Never substitute subagents", "coordinator_may_choose", "plan_approval", "--brief -", "`scout`",
		"stay available", "auto permission mode", "never read or grep the Home", "supervisor-lost", "fleet unsupervised", "--mr", "coord show <task>",
		"coord task add-mr", "coord task add-project", "coord ack", "coord land", "coord drop", "--close-mr", "only when the SOP says so", "coord notify",
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
	if n := len(strings.Fields(r)); n > 1100 {
		t.Errorf("role is %d words; it is always loaded, keep it tight", n)
	}
}
