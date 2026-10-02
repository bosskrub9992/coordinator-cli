package supervise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func TestWorkerEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "coord")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	parent := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/x",
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=abc",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_MESSAGING_SOCKET=/s",
		"CLAUDE_CODE_MESSAGING_TOKEN=t",
		"CLAUDE_PID=1",
		"CLAUDE_CODE_SESSION_ATTENDED=1",
		"CLAUDE_EFFORT=high",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CODE_EXECPATH=/x",
		"COORD_TOKEN=secret",
		"COORD_ROLE=coordinator",
		"COORD_HOME=/wrong",
		"ORCA_PANE_KEY=p",
		"HERDR_X=1",
		"CLAUDE_CODE_FORCE_SESSION_PERSISTENCE=1",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY=0",
	}
	m := envMap(WorkerEnv(parent, "/h", "001-x", bin))
	for _, k := range strippedEnv {
		if _, ok := m[k]; ok {
			t.Errorf("%s kept", k)
		}
	}
	want := map[string]string{
		"COORD_ROLE": "worker", "COORD_TASK": "001-x", "COORD_HOME": "/h",
		"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
		"HOME": "/home/x", "ORCA_PANE_KEY": "p", "HERDR_X": "1", "CLAUDE_CODE_FORCE_SESSION_PERSISTENCE": "1",
		"PATH": filepath.Dir(bin) + string(os.PathListSeparator) + "/usr/bin:/bin",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q want %q", k, m[k], v)
		}
	}
	m = envMap(WorkerEnv([]string{"PATH=" + filepath.Dir(bin) + ":/usr/bin"}, "/h", "001-x", bin))
	if m["PATH"] != filepath.Dir(bin)+":/usr/bin" {
		t.Errorf("PATH prepended twice: %s", m["PATH"])
	}
	if _, ok := envMap(SupervisorEnv(parent))["COORD_TOKEN"]; ok {
		t.Error("supervisor env kept COORD_TOKEN")
	}
}

func TestGuard(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix paths")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	wt := filepath.Join(root, "pool", "1", "repo")
	folder := filepath.Join(root, "launch", "AT-1-x")
	main := filepath.Join(root, "launch", "repo")
	for _, d := range []string{wt, folder, main} {
		os.MkdirAll(d, 0o755)
	}
	os.Symlink(main, filepath.Join(wt, "escape"))
	sc := GuardScope{Allow: []string{wt, folder}, Protected: []string{main, filepath.Join(root, "launch")}, Temp: []string{"/tmp"}}
	tests := []struct {
		tool  string
		input string
		cwd   string
		allow bool
	}{
		{"Write", `{"file_path":"` + wt + `/a.go"}`, "", true},
		{"Edit", `{"file_path":"` + wt + `/new/dir/b.go"}`, "", true},
		{"Write", `{"file_path":"` + folder + `/PLAN.md"}`, "", true},
		{"NotebookEdit", `{"notebook_path":"` + wt + `/n.ipynb"}`, "", true},
		{"Write", `{"file_path":"notes.md"}`, folder, true},
		{"Write", `{"file_path":"` + main + `/a.go"}`, "", false},
		{"Edit", `{"file_path":"` + wt + `/../../../launch/repo/a.go"}`, "", false},
		{"Write", `{"file_path":"` + wt + `/escape/a.go"}`, "", false},
		{"Write", `{"file_path":"` + filepath.Join(root, "launch") + `/x.md"}`, "", false},
		{"Write", `{"file_path":"` + wt + `x/a.go"}`, "", false},
		{"Write", `{"file_path":"../repo/a.go"}`, folder, false},
		{"Write", `{}`, "", false},
	}
	for _, tt := range tests {
		d, reason := Guard(GuardInput{ToolName: tt.tool, CWD: tt.cwd, ToolInput: json.RawMessage(tt.input)}, sc)
		if ok := d == GuardAllow; ok != tt.allow || (!ok && d != GuardDeny) {
			t.Errorf("%s %s: allow=%v want %v (%s)", tt.tool, tt.input, ok, tt.allow, reason)
		}
		if d == GuardDeny && !strings.Contains(reason, "coordinator-cli guard") {
			t.Errorf("reason %q", reason)
		}
	}
	raw := GuardResponse([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"`+main+`/x"}}`), sc)
	var m map[string]map[string]string
	json.Unmarshal(raw, &m)
	h := m["hookSpecificOutput"]
	if h["hookEventName"] != "PreToolUse" || h["permissionDecision"] != "deny" || !strings.Contains(h["permissionDecisionReason"], "off-limits") {
		t.Fatalf("response %s", raw)
	}
	raw = GuardResponse([]byte(`not json`), sc)
	json.Unmarshal(raw, &m)
	if m["hookSpecificOutput"]["permissionDecision"] != "deny" {
		t.Fatalf("bad input allowed: %s", raw)
	}
}

func newStore(t *testing.T) (*task.Store, task.Task) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	h := home.New(filepath.Join(root, "home"))
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	s := task.NewStore(h)
	launch := filepath.Join(root, "launch")
	os.MkdirAll(launch, 0o755)
	tk, err := s.Create(task.NewTask{Title: "x", Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: launch})
	if err != nil {
		t.Fatal(err)
	}
	return s, tk
}

func TestInbox(t *testing.T) {
	s, tk := newStore(t)
	a, _ := Post(s, tk.ID, InboxSteer, "one")
	Post(s, tk.ID, InboxInterrupt, "")
	Post(s, tk.ID, InboxSteer, "two")
	got, err := Queued(s, tk.ID)
	if err != nil || len(got) != 3 || got[0].ID != a.ID || got[0].Text != "one" || got[1].Kind != InboxInterrupt || got[2].Text != "two" {
		t.Fatalf("queued %+v %v", got, err)
	}
	if again, _ := Queued(s, tk.ID); len(again) != 3 {
		t.Fatalf("reading the inbox consumed it: %+v", again)
	}
	if err := Archive(got[0], InboxSent); err != nil {
		t.Fatal(err)
	}
	if err := Archive(got[2], InboxCancelled); err != nil {
		t.Fatal(err)
	}
	if err := Archive(got[0], InboxSent); err != nil {
		t.Fatalf("archiving twice: %v", err)
	}
	left, _ := Queued(s, tk.ID)
	if len(left) != 1 || left[0].Kind != InboxInterrupt {
		t.Fatalf("left %+v", left)
	}
	for _, p := range []string{filepath.Join(s.InboxDir(tk.ID), InboxSent, filepath.Base(got[0].Path)), filepath.Join(s.InboxDir(tk.ID), InboxCancelled, filepath.Base(got[2].Path))} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("archived message missing: %v", err)
		}
	}
}

func TestPrepareFiles(t *testing.T) {
	s, tk := newStore(t)
	root := filepath.Dir(tk.LaunchFolder)
	wt := filepath.Join(root, "pool", "repo")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, "CLAUDE.md"), []byte("@AGENTS.md\nWT-CLAUDE\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "AGENTS.md"), []byte("WT-AGENTS\n"), 0o644)
	os.WriteFile(filepath.Join(tk.LaunchFolder, "CLAUDE.md"), []byte("LAUNCH-CLAUDE\n@AGENTS.md\n"), 0o644)
	os.WriteFile(filepath.Join(tk.LaunchFolder, "AGENTS.md"), []byte("LAUNCH-AGENTS\n"), 0o644)
	w := task.WorkerRecord{Worktrees: []task.Worktree{{Project: "p", MainCheckout: filepath.Join(tk.LaunchFolder, "repo"), Path: wt, Branch: "coord/001-x"}}}
	bin := "/opt/my tools/coord"
	if err := WriteFiles(s, tk, w, bin); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(s.SystemPromptPath(tk.ID))
	for _, want := range []string{"# Worker role", "## Where to work", wt, "coord/001-x", tk.Folder, filepath.Join(tk.LaunchFolder, "repo"), "WT-AGENTS", "LAUNCH-CLAUDE", "LAUNCH-AGENTS"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(string(prompt), "WT-CLAUDE") {
		t.Error("worktree CLAUDE.md duplicated in the prompt")
	}
	var set map[string]any
	b, _ := os.ReadFile(s.SettingsPath(tk.ID))
	json.Unmarshal(b, &set)
	sb, _ := json.Marshal(set)
	for _, want := range []string{`"matcher":"Edit|Write|MultiEdit|NotebookEdit"`, `'/opt/my tools/coord' _guard 001-x`, `"InstructionsLoaded"`, `_hook instructions 001-x`, `"allow":["Bash(coord report *)"]`} {
		if !strings.Contains(string(sb), want) {
			t.Errorf("settings lack %s: %s", want, sb)
		}
	}
	if strings.Contains(string(sb), `"ask"`) {
		t.Errorf("settings still carry ask rules: %s", sb)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(tk.ID), "mcp.json")); !os.IsNotExist(err) {
		t.Errorf("an MCP config was written: %v", err)
	}
}

func TestPrepareTicketFolderInsideLaunch(t *testing.T) {
	_, tk := newStore(t)
	os.WriteFile(filepath.Join(tk.LaunchFolder, "CLAUDE.md"), []byte("LAUNCH-CLAUDE\n@AGENTS.md\n"), 0o644)
	os.WriteFile(filepath.Join(tk.LaunchFolder, "AGENTS.md"), []byte("LAUNCH-AGENTS\n"), 0o644)
	tk.Folder = filepath.Join(tk.LaunchFolder, "AT-1-x")
	os.MkdirAll(tk.Folder, 0o755)
	p := SystemPrompt(tk, task.WorkerRecord{})
	if !strings.Contains(p, "LAUNCH-AGENTS") || strings.Contains(p, "LAUNCH-CLAUDE") {
		t.Fatalf("prompt:\n%s", p)
	}
}
