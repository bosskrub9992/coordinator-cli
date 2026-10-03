package role

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

//go:embed coordinator.md
var coordinator string

func Coordinator() string {
	return strings.TrimRight(coordinator, "\n") + "\n\n" + States()
}

func States() string {
	var b strings.Builder
	b.WriteString("## Task states and their moves\n")
	b.WriteString("`coord show <task>` prints one Task's moves under `Next:`. ")
	fmt.Fprintf(&b, "Every state that is not final also allows `%s`: %s.\n", task.DropMove.Command, task.DropMove.When)
	for _, s := range task.States() {
		fmt.Fprintf(&b, "- `%s`: %s.", s.State, s.Meaning)
		if s.Terminal {
			b.WriteString(" Final.\n")
			continue
		}
		b.WriteString("\n")
		for _, m := range s.Moves {
			fmt.Fprintf(&b, "  - `%s`%s: %s.\n", m.Command, qualifier(m), m.When)
		}
	}
	return b.String()
}

func qualifier(m task.Move) string {
	var parts []string
	if m.Classes != nil {
		names := make([]string, len(m.Classes))
		for i, c := range m.Classes {
			names[i] = string(c)
		}
		parts = append(parts, strings.Join(names, ", "))
	}
	switch m.From {
	case task.QuestionFromWorker:
		parts = append(parts, "the Worker's question")
	case task.QuestionFromWatcher:
		parts = append(parts, "the watcher's MR facts")
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
