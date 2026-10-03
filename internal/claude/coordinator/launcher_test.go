package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

func baseSpec(dir string) harness.CoordinatorSpec {
	return harness.CoordinatorSpec{
		LaunchFolder:   dir,
		SessionID:      "11111111-2222-4333-8444-555555555555",
		Model:          "claude-opus-5-5",
		Effort:         "high",
		RolePromptFile: filepath.Join(dir, "role.md"),
		MemoryDir:      filepath.Join(dir, "memory"),
		SettingsFile:   filepath.Join(dir, "settings.json"),
	}
}

func flagValues(args []string, flag string) []string {
	i := slices.Index(args, flag)
	if i < 0 {
		return nil
	}
	var out []string
	for _, a := range args[i+1:] {
		if strings.HasPrefix(a, "--") {
			break
		}
		out = append(out, a)
	}
	return out
}

func TestArgs(t *testing.T) {
	l := New("/opt/coord")
	tests := []struct {
		name   string
		edit   func(*harness.CoordinatorSpec)
		want   map[string][]string
		absent []string
	}{
		{"new session", func(*harness.CoordinatorSpec) {}, map[string][]string{
			"--session-id":      {"11111111-2222-4333-8444-555555555555"},
			"--permission-mode": {"auto"},
			"--model":           {"claude-opus-5-5"},
			"--effort":          {"high"},
			"--allowedTools":    {"Bash(coord:*)"},
		}, []string{"--resume"}},
		{"resume", func(s *harness.CoordinatorSpec) { s.Resume = true }, map[string][]string{
			"--resume": {"11111111-2222-4333-8444-555555555555"},
		}, []string{"--session-id"}},
		{"explicit mode, no model", func(s *harness.CoordinatorSpec) { s.PermissionMode = "plan"; s.Model = ""; s.Effort = "" }, map[string][]string{
			"--permission-mode": {"plan"},
		}, []string{"--model", "--effort"}},
		{"extra tools merged", func(s *harness.CoordinatorSpec) {
			s.AllowedTools = []string{"Bash(coord:*)", "Read"}
			s.DisallowedTools = []string{"Edit", "ExitWorktree"}
		}, map[string][]string{
			"--allowedTools": {"Bash(coord:*)", "Read"},
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := baseSpec("/work")
			tt.edit(&spec)
			args := l.Args(spec)
			for flag, want := range tt.want {
				if got := flagValues(args, flag); !slices.Equal(got, want) {
					t.Errorf("%s = %q want %q (args %q)", flag, got, want, args)
				}
			}
			for _, flag := range tt.absent {
				if slices.Contains(args, flag) {
					t.Errorf("%s present in %q", flag, args)
				}
			}
			if got := flagValues(args, "--append-system-prompt-file"); !slices.Equal(got, []string{filepath.Join("/work", "role.md")}) {
				t.Errorf("role file %q", got)
			}
			if got := flagValues(args, "--settings"); !slices.Equal(got, []string{filepath.Join("/work", "settings.json")}) {
				t.Errorf("settings %q", got)
			}
			deny := flagValues(args, "--disallowedTools")
			for _, d := range []string{"NotebookEdit", "EnterWorktree", "Bash(git commit:*)", "Bash(sh -c:*)"} {
				if !slices.Contains(deny, d) {
					t.Errorf("deny list misses %s: %q", d, deny)
				}
			}
			if tt.name != "extra tools merged" {
				for _, d := range []string{"Edit", "Write"} {
					if slices.Contains(deny, d) {
						t.Errorf("deny list has %s, which would block the Coordinator's memory; the guard hook limits it instead: %q", d, deny)
					}
				}
			}
			if len(deny) != len(slices.Compact(slices.Sorted(slices.Values(deny)))) {
				t.Errorf("deny list has duplicates: %q", deny)
			}
		})
	}
}

func bashDenied(deny []string, command string) bool {
	for _, d := range deny {
		p, ok := strings.CutPrefix(d, "Bash(")
		if !ok {
			continue
		}
		p = strings.TrimSuffix(p, ")")
		re := ""
		if prefix, ok := strings.CutSuffix(p, ":*"); ok {
			re = regexp.QuoteMeta(prefix) + "( .*)?"
		} else {
			re = strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*")
		}
		if regexp.MustCompile("^(?s:" + re + ")$").MatchString(command) {
			return true
		}
	}
	return false
}

func TestDisallowedToolsCoverGitWrites(t *testing.T) {
	deny := DisallowedTools()
	for _, c := range []string{
		"git commit -m x",
		"git add .",
		"git push origin HEAD",
		"git pull",
		"git clone git@x:y.git",
		"git checkout main",
		"git switch -c x",
		"git reset --hard",
		"git stash",
		"git rebase main",
		"git merge x",
		"git cherry-pick abc",
		"git tag v1",
		"git worktree add ../x",
		"git config user.name x",
		"git remote add up git@x:y.git",
		"git branch -D main",
		"git branch --delete x",
		"git -C /repo commit -m x",
		"git -c user.name=x commit -m y",
		"git --git-dir=/repo/.git push origin HEAD",
		"git --work-tree=/repo checkout -- .",
		"sh -c 'git commit -m x'",
		"bash -c 'git push'",
		"zsh -c 'git push'",
		"eval git commit",
	} {
		if !bashDenied(deny, c) {
			t.Errorf("%q is not denied", c)
		}
	}
}

func TestDisallowedToolsLeaveGitReads(t *testing.T) {
	deny := DisallowedTools()
	for _, c := range []string{
		"git log --oneline -5",
		"git shortlog -sn",
		"git status --short",
		"git diff master...HEAD",
		"git show HEAD",
		"git blame main.go",
		"git fetch origin",
		"git branch",
		"git branch -r",
		"git branch --list",
		"git rev-parse HEAD",
		"git ls-files",
		"git remote -v",
		"git reflog",
		"git describe --tags",
		"coord status",
		"gitleaks detect",
		"shellcheck x.sh",
	} {
		if bashDenied(deny, c) {
			t.Errorf("%q is denied", c)
		}
	}
}

func TestEnvStripsClaudeIdentity(t *testing.T) {
	in := []string{
		"PATH=/bin", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=x", "CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_CODE_MESSAGING_SOCKET=/s", "CLAUDE_EFFORT=high", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_PID=9",
		"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1", "ORCA_X=1", "COORD_TOKEN=t",
	}
	want := []string{"PATH=/bin", "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1", "ORCA_X=1", "COORD_TOKEN=t"}
	if got := Env(in); !slices.Equal(got, want) {
		t.Fatalf("Env = %q want %q", got, want)
	}
}

func TestSettings(t *testing.T) {
	tests := []struct {
		bin, wantCmd string
	}{
		{"/usr/local/bin/coord", "/usr/local/bin/coord"},
		{"/Users/a b/bin/coord", "'/Users/a b/bin/coord'"},
		{"/tmp/it's/coord", `'/tmp/it'\''s/coord'`},
	}
	for _, tt := range tests {
		s := BuildSettings(tt.bin, "/h/memory")
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			AutoMemoryDirectory string `json:"autoMemoryDirectory"`
			StatusLine          struct{ Type, Command string }
			Hooks               map[string][]struct {
				Matcher string
				Hooks   []struct {
					Type    string
					Command string
					Timeout int
				}
			}
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.AutoMemoryDirectory != "/h/memory" {
			t.Errorf("memory %q", got.AutoMemoryDirectory)
		}
		if got.StatusLine.Type != "command" || got.StatusLine.Command != tt.wantCmd+" _statusline" {
			t.Errorf("statusLine %+v", got.StatusLine)
		}
		stop := got.Hooks["Stop"]
		if len(stop) != 1 || len(stop[0].Hooks) != 1 || stop[0].Hooks[0].Command != tt.wantCmd+" _stop-hook" || stop[0].Hooks[0].Timeout != 10 || stop[0].Hooks[0].Type != "command" {
			t.Errorf("Stop hook %s", data)
		}
		guard := got.Hooks["PreToolUse"]
		if len(guard) != 1 || guard[0].Matcher != "Edit|Write|MultiEdit" || len(guard[0].Hooks) != 1 || guard[0].Hooks[0].Command != tt.wantCmd+" _coordinator-guard" || guard[0].Hooks[0].Type != "command" {
			t.Errorf("PreToolUse guard %s", data)
		}
	}
}

func TestPrepareRejectsIncompleteSpec(t *testing.T) {
	err := New("/opt/coord").Prepare(harness.CoordinatorSpec{LaunchFolder: "/w"})
	if err == nil || !strings.Contains(err.Error(), "MemoryDir, RolePromptFile, SessionID, SettingsFile") {
		t.Fatalf("err = %v", err)
	}
}

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestLaunchRunsClaude(t *testing.T) {
	out := t.TempDir()
	fakeClaude(t, `pwd -P > "$OUT/cwd"; for a in "$@"; do printf '%s\n' "$a"; done > "$OUT/argv"; env > "$OUT/env"; cat > "$OUT/stdin"; echo hello; exit 3`+"\n")
	work, _ := filepath.EvalSymlinks(t.TempDir())
	spec := baseSpec(work)
	spec.Env = []string{"OUT=" + out, "PATH=" + os.Getenv("PATH"), "CLAUDECODE=1", "KEEP=yes"}
	spec.Stdin = strings.NewReader("typed")
	var stdout bytes.Buffer
	spec.Stdout = &stdout
	spec.Stderr = &stdout
	exit, err := New("/opt/coord").Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if exit.Code != 3 || exit.SessionID != spec.SessionID {
		t.Fatalf("exit %+v", exit)
	}
	if stdout.String() != "hello\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := strings.TrimSpace(read("cwd")); got != work {
		t.Errorf("cwd %q want %q", got, work)
	}
	if got := strings.Split(strings.TrimSpace(read("argv")), "\n"); !slices.Equal(got, New("/opt/coord").Args(spec)) {
		t.Errorf("argv %q", got)
	}
	env := read("env")
	if strings.Contains(env, "CLAUDECODE=") || !strings.Contains(env, "KEEP=yes") {
		t.Errorf("env %q", env)
	}
	if read("stdin") != "typed" {
		t.Errorf("stdin not passed through")
	}
	var s Settings
	data, err := os.ReadFile(spec.SettingsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &s); err != nil || s.AutoMemoryDirectory != spec.MemoryDir || s.StatusLine.Command != "/opt/coord _statusline" {
		t.Fatalf("settings %s %v", data, err)
	}
}

func TestLaunchMissingClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	spec := baseSpec(t.TempDir())
	if _, err := New("/opt/coord").Launch(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "find claude") {
		t.Fatalf("err = %v", err)
	}
}
