package supervise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func TestGuardHardening(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", root)
	wt := filepath.Join(root, "pool", "repo")
	main := filepath.Join(root, "main")
	for _, d := range []string{wt, main, filepath.Join(wt, ".claude"), filepath.Join(wt, "sub", ".Claude")} {
		os.MkdirAll(d, 0o755)
	}
	os.Symlink(filepath.Join(main, "new.txt"), filepath.Join(wt, "dangling"))
	os.Symlink("../../main/rel.txt", filepath.Join(wt, "dangling-rel"))
	os.Symlink(filepath.Join(wt, "loop-b"), filepath.Join(wt, "loop-a"))
	os.Symlink(filepath.Join(wt, "loop-a"), filepath.Join(wt, "loop-b"))
	os.Symlink(filepath.Join(wt, "real.txt"), filepath.Join(wt, "inside-link"))
	sc := GuardScope{Allow: []string{wt}, Protected: []string{main}}
	tests := []struct {
		path  string
		allow bool
	}{
		{"~/pool/repo/a.go", true},
		{"~", false},
		{"~/main/a.go", false},
		{filepath.Join(wt, "dangling"), false},
		{filepath.Join(wt, "dangling-rel"), false},
		{filepath.Join(wt, "loop-a"), false},
		{filepath.Join(wt, "inside-link"), true},
		{filepath.Join(wt, ".claude", "settings.json"), false},
		{filepath.Join(wt, ".claude"), false},
		{filepath.Join(wt, "sub", ".Claude", "commands", "x.md"), false},
		{filepath.Join(wt, ".claudex", "ok.md"), true},
		{filepath.Join(wt, "CLAUDE.md"), true},
	}
	for _, tt := range tests {
		in, _ := json.Marshal(map[string]string{"file_path": tt.path})
		d, reason := Guard(GuardInput{ToolName: "Write", ToolInput: in}, sc)
		if ok := d == GuardAllow; ok != tt.allow || (!ok && d != GuardDeny) {
			t.Errorf("%s: decision=%q want allow=%v (%s)", tt.path, d, tt.allow, reason)
		}
	}
}

func TestGuardProtectsMainCheckoutsBeforeTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	launch := filepath.Join(tmp, "launch")
	main := filepath.Join(launch, "repo")
	other := filepath.Join(tmp, "other-project")
	folder := filepath.Join(launch, "AT-1-x")
	wt := filepath.Join(tmp, "pool", "1", "repo")
	scratch := filepath.Join(tmp, "scratch")
	for _, d := range []string{main, other, folder, wt, scratch} {
		os.MkdirAll(d, 0o755)
	}
	tk := task.Task{Folder: folder, LaunchFolder: launch}
	w := task.WorkerRecord{Worktrees: []task.Worktree{{MainCheckout: main, Path: wt}}}
	sc := Scope(tk, w, []string{other})
	sc.Temp = []string{tmp}
	for path, want := range map[string]GuardDecision{
		filepath.Join(wt, "a.go"):             GuardAllow,
		filepath.Join(folder, "PLAN.md"):      GuardAllow,
		filepath.Join(main, "a.go"):           GuardDeny,
		filepath.Join(main, ".git", "config"): GuardDeny,
		filepath.Join(other, "a.go"):          GuardDeny,
		filepath.Join(launch, "notes.md"):     GuardDeny,
		filepath.Join(scratch, "x.txt"):       GuardNone,
		"/elsewhere/x.txt":                    GuardDeny,
	} {
		in, _ := json.Marshal(map[string]string{"file_path": path})
		if d, reason := Guard(GuardInput{ToolName: "Write", ToolInput: in}, sc); d != want {
			t.Errorf("%s: %q want %q (%s)", path, d, want, reason)
		}
	}
	in, _ := json.Marshal(map[string]any{"tool_name": "Write", "tool_input": map[string]string{"file_path": filepath.Join(scratch, "x.txt")}})
	if out := GuardResponse(in, sc); string(out) != "{}" {
		t.Fatalf("temp write got a decision: %s", out)
	}
	for _, tool := range []string{"Read", "Bash", "Grep"} {
		in, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": map[string]string{"file_path": filepath.Join(main, ".env"), "command": "cat " + filepath.Join(main, ".env")}})
		if out := GuardResponse(in, sc); string(out) != "{}" {
			t.Errorf("%s got a decision from the guard: %s", tool, out)
		}
	}
}

func TestDriveOrRootRelative(t *testing.T) {
	for p, want := range map[string]bool{
		`C:foo\bar`:      true,
		`C:`:             true,
		`\Users\x`:       true,
		`/Users/x`:       true,
		`C:\Users\x`:     false,
		`C:/Users/x`:     false,
		`\\server\share`: false,
		`//server/share`: false,
		`relative\path`:  false,
		`notes.md`:       false,
		`1:odd`:          false,
	} {
		if got := driveOrRootRelative(p); got != want {
			t.Errorf("driveOrRootRelative(%q) = %v want %v", p, got, want)
		}
	}
}

func TestWorkerEnvWithoutPathKeepsSystemPath(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "coord")
	m := envMap(WorkerEnv([]string{"HOME=/h"}, "/home", "001-x", bin))
	if p := m["PATH"]; !strings.HasPrefix(p, filepath.Dir(bin)+string(os.PathListSeparator)) || len(p) == len(filepath.Dir(bin))+1 {
		t.Fatalf("PATH %q has no system path after coord's folder", p)
	}
}

func TestCoordinatorGuard(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	mem := filepath.Join(root, "home", "memory")
	launch := filepath.Join(root, "workspace")
	for _, d := range []string{mem, launch} {
		os.MkdirAll(d, 0o755)
	}
	tests := []struct {
		tool, path, want string
	}{
		{"Write", filepath.Join(mem, "reviews-by-opus.md"), "allow"},
		{"Edit", filepath.Join(mem, "MEMORY.md"), "allow"},
		{"MultiEdit", filepath.Join(mem, "MEMORY.md"), "allow"},
		{"Write", filepath.Join(launch, "PLAN.md"), "deny"},
		{"Edit", filepath.Join(root, "home", "tasks", "x", "state.json"), "deny"},
		{"Write", filepath.Join(os.TempDir(), "scratch.txt"), "deny"},
		{"Write", filepath.Join(mem, "..", "config.json"), "deny"},
		{"Write", "", "deny"},
		{"Read", filepath.Join(launch, "PLAN.md"), ""},
	}
	for _, tt := range tests {
		in, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "cwd": launch, "tool_name": tt.tool, "tool_input": map[string]string{"file_path": tt.path}})
		var out guardOutput
		json.Unmarshal(CoordinatorGuardResponse(in, mem), &out)
		if got := out.HookSpecificOutput.PermissionDecision; got != tt.want {
			t.Errorf("%s %s: decision %q want %q (%s)", tt.tool, tt.path, got, tt.want, out.HookSpecificOutput.PermissionDecisionReason)
		}
		if tt.want == "deny" && tt.path != "" && !strings.Contains(out.HookSpecificOutput.PermissionDecisionReason, "Delegate the change to a Worker") {
			t.Errorf("%s: reason %q", tt.path, out.HookSpecificOutput.PermissionDecisionReason)
		}
	}
	var out guardOutput
	json.Unmarshal(CoordinatorGuardResponse([]byte("not json"), mem), &out)
	if out.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("unreadable input decision %q", out.HookSpecificOutput.PermissionDecision)
	}
}
