package coordinator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

const DefaultPermissionMode = "auto"

func AllowedTools() []string { return []string{"Bash(coord:*)"} }

var gitWrites = []string{
	"commit", "add", "rm", "mv", "restore", "checkout", "switch", "reset", "clean", "stash", "apply", "am",
	"pull", "clone", "init", "merge", "rebase", "cherry-pick", "revert", "push", "tag", "worktree", "submodule",
	"update-ref", "config", "gc", "prune", "remote add", "remote remove", "remote rm", "remote rename", "remote set-url",
	"branch -d", "branch -D", "branch -m", "branch -M", "branch -c", "branch -C", "branch -f",
	"branch --delete", "branch --move", "branch --copy", "branch --force", "branch --set-upstream-to", "branch --unset-upstream",
	"-C", "-c",
}

var gitGlobalPaths = []string{"--git-dir", "--work-tree"}

var shellWrappers = []string{"sh -c", "bash -c", "zsh -c", "eval"}

func DisallowedTools() []string {
	out := []string{"NotebookEdit", "EnterWorktree"}
	for _, c := range gitWrites {
		out = append(out, "Bash(git "+c+":*)")
	}
	for _, c := range gitGlobalPaths {
		out = append(out, "Bash(git "+c+"*)")
	}
	for _, c := range shellWrappers {
		out = append(out, "Bash("+c+":*)")
	}
	return out
}

var identityVars = []string{
	"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_EFFORT",
	"CLAUDE_CODE_ENTRYPOINT", "CLAUDE_PID",
}

const identityPrefix = "CLAUDE_CODE_MESSAGING_"

type Launcher struct {
	Claude string
	Coord  string
}

var _ harness.CoordinatorLauncher = (*Launcher)(nil)

func New(coordBin string) *Launcher {
	return &Launcher{Claude: "claude", Coord: coordBin}
}

func (l *Launcher) Name() harness.Name { return harness.Claude }

func (l *Launcher) Args(spec harness.CoordinatorSpec) []string {
	mode := spec.PermissionMode
	if mode == "" {
		mode = DefaultPermissionMode
	}
	args := []string{
		"--append-system-prompt-file", spec.RolePromptFile,
		"--settings", spec.SettingsFile,
		"--permission-mode", mode,
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	if spec.Resume {
		args = append(args, "--resume", spec.SessionID)
	} else {
		args = append(args, "--session-id", spec.SessionID)
	}
	args = append(args, "--allowedTools")
	args = append(args, merge(AllowedTools(), spec.AllowedTools)...)
	args = append(args, "--disallowedTools")
	args = append(args, merge(DisallowedTools(), spec.DisallowedTools)...)
	return args
}

func merge(base, extra []string) []string {
	out := slices.Clone(base)
	for _, e := range extra {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

func Env(base []string) []string {
	return envlist.Filter(base, func(k string) bool {
		if len(k) >= len(identityPrefix) && envlist.Same(k[:len(identityPrefix)], identityPrefix) {
			return true
		}
		return slices.ContainsFunc(identityVars, func(v string) bool { return envlist.Same(k, v) })
	})
}

func check(spec harness.CoordinatorSpec) error {
	var missing []string
	for name, v := range map[string]string{
		"LaunchFolder": spec.LaunchFolder, "SessionID": spec.SessionID, "RolePromptFile": spec.RolePromptFile,
		"SettingsFile": spec.SettingsFile, "MemoryDir": spec.MemoryDir,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("coordinator launch spec is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

func (l *Launcher) Prepare(spec harness.CoordinatorSpec) error {
	if err := check(spec); err != nil {
		return err
	}
	if l.Coord == "" {
		return errors.New("coordinator launcher: the coord binary path is empty")
	}
	return WriteSettings(spec.SettingsFile, BuildSettings(l.Coord, spec.MemoryDir))
}

func (l *Launcher) Command(ctx context.Context, spec harness.CoordinatorSpec) (*exec.Cmd, error) {
	bin, err := exec.LookPath(l.Claude)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", l.Claude, err)
	}
	cmd := exec.CommandContext(ctx, bin, l.Args(spec)...)
	cmd.Dir = spec.LaunchFolder
	cmd.Env = Env(spec.Env)
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.Cancel = func() error { return terminate(cmd.Process) }
	return cmd, nil
}

func (l *Launcher) Launch(ctx context.Context, spec harness.CoordinatorSpec) (harness.CoordinatorExit, error) {
	out := harness.CoordinatorExit{SessionID: spec.SessionID}
	if err := l.Prepare(spec); err != nil {
		return out, err
	}
	cmd, err := l.Command(ctx, spec)
	if err != nil {
		return out, err
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, handledSignals...)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return out, fmt.Errorf("start %s: %w", l.Claude, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if forward(s) {
					cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	out.Code = cmd.ProcessState.ExitCode()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return out, fmt.Errorf("run %s: %w", l.Claude, err)
	}
	return out, nil
}
