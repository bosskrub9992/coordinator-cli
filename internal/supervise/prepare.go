package supervise

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/instructions"
	"github.com/bosskrub9992/coordinator-cli/internal/role"
	"github.com/bosskrub9992/coordinator-cli/internal/shellquote"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

const (
	PermissionMode = "auto"
	guardMatcher   = "Edit|Write|MultiEdit|NotebookEdit"
)

func hookCommand(coordBin string, args ...string) string {
	return shellquote.Join(append([]string{coordBin}, args...)...)
}

func Settings(coordBin string, id task.ID) map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": guardMatcher,
				"hooks":   []any{map[string]any{"type": "command", "command": hookCommand(coordBin, "_guard", string(id))}},
			}},
			"InstructionsLoaded": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": hookCommand(coordBin, "_hook", "instructions", string(id))}},
			}},
		},
		"permissions": map[string]any{
			"allow": WorkerAllowRules(),
		},
	}
}

func WorkerAllowRules() []string {
	return []string{"Bash(coord report *)"}
}

func WhereToWork(t task.Task, w task.WorkerRecord) string {
	var b strings.Builder
	b.WriteString("## Where to work\n\n")
	fmt.Fprintf(&b, "Task: %s (%s, class %s, %s)\n\n", t.ID, t.Title, t.Class, projectsLabel(t.Projects))
	if t.Class == config.Ship {
		if t.SkipPlan && w.PlanApproval != string(config.PlanAll) {
			b.WriteString("Plan: pre-approved by the Coordinator; skip the Plan step.\n\n")
		} else {
			b.WriteString("Plan: required. Before changing anything, write PLAN.md in the Task folder and submit it with `coord report --status plan`, even when the Brief lists exact steps; build only once the answer approves it.\n\n")
		}
	}
	active := w.ActiveWorktrees()
	if len(active) == 0 && len(w.Worktrees) > 0 {
		b.WriteString("Worktrees: none. The worktree lease was returned after your Report, so this follow-up is read-only: answer from what you already know or can read, and report blocked if it needs a code change.\n")
	} else {
		b.WriteString("Worktrees (yours; make code changes only here):\n")
	}
	for _, wt := range active {
		fmt.Fprintf(&b, "- %s: %s on branch %s\n", wt.Project, wt.Path, wt.Branch)
	}
	fmt.Fprintf(&b, "\nTask folder (your current folder; plans, notes and evidence): %s\n\n", t.Folder)
	b.WriteString("Main checkouts are off-limits. Never edit, commit or run git commands that change these:\n")
	for _, wt := range w.Worktrees {
		fmt.Fprintf(&b, "- %s\n", wt.MainCheckout)
	}
	return b.String()
}

func projectsLabel(projects []string) string {
	if len(projects) == 1 {
		return "Project " + projects[0]
	}
	return "Projects " + strings.Join(projects, ", ")
}

func InstructionDirs(t task.Task, w task.WorkerRecord) []instructions.Dir {
	var dirs []instructions.Dir
	for _, wt := range w.ActiveWorktrees() {
		dirs = append(dirs, instructions.Dir{Path: wt.Path})
	}
	if t.LaunchFolder != "" {
		inside := within(resolveExisting(t.LaunchFolder), resolveExisting(t.Folder))
		dirs = append(dirs, instructions.Dir{Path: t.LaunchFolder, IncludeFiles: !inside})
	}
	return dirs
}

func SystemPrompt(t task.Task, w task.WorkerRecord) string {
	parts := []string{strings.TrimSpace(role.Worker()), strings.TrimSpace(WhereToWork(t, w))}
	if _, sup := instructions.Supplement(InstructionDirs(t, w)); sup != "" {
		parts = append(parts, strings.TrimSpace(sup))
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func WriteFiles(s *task.Store, t task.Task, w task.WorkerRecord, coordBin string) error {
	if err := home.WriteFileAtomic(s.SystemPromptPath(t.ID), []byte(SystemPrompt(t, w)), 0o644); err != nil {
		return err
	}
	return home.WriteJSONAtomic(s.SettingsPath(t.ID), Settings(coordBin, t.ID))
}

func LoadedInstructions(s *task.Store, id task.ID) []string {
	var files []string
	seen := map[string]bool{}
	home.Log{Path: s.InstructionsPath(id)}.Each(func(line []byte) error {
		var r struct {
			FilePath string `json:"file_path"`
		}
		if json.Unmarshal(line, &r) == nil && r.FilePath != "" && !seen[r.FilePath] {
			seen[r.FilePath] = true
			files = append(files, r.FilePath)
		}
		return nil
	})
	return files
}
