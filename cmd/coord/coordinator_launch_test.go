package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/role"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type launchRig struct {
	home   home.Home
	folder string
	coord  string
	out    string
}

func newLaunchRig(t *testing.T) launchRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a shell script")
	}
	h := home.New(setup(t))
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	r := launchRig{home: h, out: t.TempDir()}
	r.folder, _ = filepath.EvalSymlinks(t.TempDir())
	t.Chdir(r.folder)

	binDir, _ := filepath.EvalSymlinks(t.TempDir())
	r.coord = filepath.Join(binDir, "coord")
	if err := os.WriteFile(r.coord, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := coordExecutable
	coordExecutable = func() (string, error) { return r.coord, nil }
	t.Cleanup(func() { coordExecutable = old })

	claudeDir := t.TempDir()
	script := `#!/bin/sh
pwd -P > "$FAKE_OUT/cwd"
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_OUT/argv"
env > "$FAKE_OUT/env"
cp "$COORD_HOME/lock" "$FAKE_OUT/lock"
exit 0
`
	if err := os.WriteFile(filepath.Join(claudeDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", claudeDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("FAKE_OUT", r.out)
	t.Setenv("CLAUDECODE", "1")
	return r
}

func (r launchRig) ran() bool {
	_, err := os.Stat(filepath.Join(r.out, "argv"))
	return err == nil
}

func (r launchRig) argv(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, "argv"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (r launchRig) env(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.out, "env"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

func (r launchRig) heldLock(t *testing.T) home.Lock {
	t.Helper()
	var l home.Lock
	if found, err := home.ReadJSON(filepath.Join(r.out, "lock"), &l); err != nil || !found {
		t.Fatalf("lock during run: %v %v", found, err)
	}
	return l
}

func argAfter(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestLaunchBuildsCoordinatorSession(t *testing.T) {
	r := newLaunchRig(t)
	c := config.Default()
	c.Coordinator.Model = "claude-sonnet-5-5"
	c.Coordinator.Effort = "low"
	if err := c.Save(r.home.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(r.home.CoordinatorMDPath(), []byte("# Mine\n\nAlways answer in haiku.\n"), 0o644)

	if out, err := coord(t, ""); err != nil {
		t.Fatalf("launch: %q %v", out, err)
	}
	args := r.argv(t)
	run := r.home.CoordinatorRunDir()
	checks := map[string]string{
		"--append-system-prompt-file": filepath.Join(run, "role.md"),
		"--settings":                  filepath.Join(run, "settings.json"),
		"--permission-mode":           "auto",
		"--model":                     "claude-sonnet-5-5",
		"--effort":                    "low",
		"--allowedTools":              "Bash(coord:*)",
	}
	for flag, want := range checks {
		if got := argAfter(args, flag); got != want {
			t.Errorf("%s = %q want %q", flag, got, want)
		}
	}
	sid := argAfter(args, "--session-id")
	if !uuidPattern.MatchString(sid) {
		t.Errorf("session id %q", sid)
	}
	if !slices.Contains(args, "Bash(git commit:*)") || !slices.Contains(args, "EnterWorktree") || slices.Contains(args, "Bash(git:*)") {
		t.Errorf("deny rules missing: %q", args)
	}
	cwdOut, _ := os.ReadFile(filepath.Join(r.out, "cwd"))
	if strings.TrimSpace(string(cwdOut)) != r.folder {
		t.Errorf("cwd %q want %q", cwdOut, r.folder)
	}

	held := r.heldLock(t)
	env := r.env(t)
	if env[home.EnvHome] != r.home.Root || env[home.EnvToken] != held.Token || held.Token == "" {
		t.Errorf("env home %q token %q lock %+v", env[home.EnvHome], env[home.EnvToken], held)
	}
	if held.LaunchFolder != r.folder || held.SessionID != sid || held.PID != os.Getpid() {
		t.Errorf("lock %+v", held)
	}
	if !strings.HasPrefix(env["PATH"], filepath.Dir(r.coord)+string(os.PathListSeparator)) {
		t.Errorf("PATH %q does not start with the coord dir", env["PATH"])
	}
	if _, ok := env["CLAUDECODE"]; ok {
		t.Errorf("CLAUDECODE leaked into the Coordinator env")
	}

	roleText, _ := os.ReadFile(filepath.Join(run, "role.md"))
	if !strings.HasPrefix(string(roleText), role.Coordinator()[:200]) || !strings.HasSuffix(string(roleText), "Always answer in haiku.\n") {
		t.Errorf("role file %q", roleText)
	}
	var s struct {
		AutoMemoryDirectory string
		StatusLine          struct{ Command string }
		Hooks               map[string][]struct{ Hooks []struct{ Command string } }
	}
	data, _ := os.ReadFile(filepath.Join(run, "settings.json"))
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.AutoMemoryDirectory != r.home.MemoryDir() || s.StatusLine.Command != r.coord+" _statusline" || s.Hooks["Stop"][0].Hooks[0].Command != r.coord+" _stop-hook" {
		t.Errorf("settings %s", data)
	}

	if l, _ := r.home.ReadLock(); l != nil {
		t.Errorf("lock not released: %+v", l)
	}

	os.Remove(filepath.Join(r.out, "argv"))
	if out, err := coord(t, "", "--continue"); err != nil {
		t.Fatalf("continue: %q %v", out, err)
	}
	args = r.argv(t)
	if argAfter(args, "--resume") != sid || slices.Contains(args, "--session-id") {
		t.Errorf("continue argv %q", args)
	}

	other, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(other)
	if _, err := coord(t, "", "--continue"); err == nil || !strings.Contains(err.Error(), r.folder) {
		t.Errorf("continue from another folder: %v", err)
	}
}

func TestLaunchContinueWithoutHistory(t *testing.T) {
	r := newLaunchRig(t)
	if _, err := coord(t, "", "--continue"); err == nil || !strings.Contains(err.Error(), "no earlier Coordinator conversation") {
		t.Fatalf("err = %v", err)
	}
	if r.ran() {
		t.Fatal("claude ran")
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestLaunchTakeover(t *testing.T) {
	tests := []struct {
		name       string
		stale      bool
		tty        bool
		answer     string
		args       []string
		wantRun    bool
		wantErr    string
		wantPrompt bool
	}{
		{name: "non-tty refuses", wantErr: "interactive terminal"},
		{name: "non-tty --takeover refuses", args: []string{"--takeover"}, wantErr: "interactive terminal"},
		{name: "tty --takeover", tty: true, args: []string{"--takeover"}, wantRun: true},
		{name: "tty yes", tty: true, answer: "y\n", wantRun: true, wantPrompt: true},
		{name: "tty YES", tty: true, answer: "YES\n", wantRun: true, wantPrompt: true},
		{name: "tty no", tty: true, answer: "n\n", wantErr: "not taking over", wantPrompt: true},
		{name: "tty empty answer", tty: true, answer: "\n", wantErr: "not taking over", wantPrompt: true},
		{name: "tty eof", tty: true, answer: "", wantErr: "not taking over", wantPrompt: true},
		{name: "stale lock taken silently", tty: true, stale: true, wantRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newLaunchRig(t)
			old := isTerminal
			isTerminal = func(io.Reader) bool { return tt.tty }
			t.Cleanup(func() { isTerminal = old })

			s := task.NewStore(r.home)
			tk, err := s.Create(task.NewTask{Title: "busy", Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: r.folder})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Transition(tk.ID, task.Running, ""); err != nil {
				t.Fatal(err)
			}
			owner := home.Owner{LaunchFolder: "/elsewhere"}
			if tt.stale {
				owner.PID = deadPID(t)
			}
			prev, err := r.home.AcquireLock(owner, false)
			if err != nil {
				t.Fatal(err)
			}

			out, err := coord(t, tt.answer, tt.args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("err = %v (%q)", err, out)
			}
			prompted := strings.Contains(out, "Take over here? [y/N]")
			if prompted != tt.wantPrompt {
				t.Fatalf("prompted = %v: %q", prompted, out)
			}
			if tt.wantPrompt && !strings.Contains(out, "in /elsewhere") || tt.wantPrompt && !strings.Contains(out, "1 Task in flight") {
				t.Fatalf("prompt lacks holder or count: %q", out)
			}
			if r.ran() != tt.wantRun {
				t.Fatalf("claude ran = %v", r.ran())
			}
			if !tt.wantRun {
				if l, _ := r.home.ReadLock(); l == nil || l.Token != prev.Token {
					t.Fatalf("holder changed after refusal: %+v", l)
				}
				return
			}
			if held := r.heldLock(t); held.Token == prev.Token || held.LaunchFolder != r.folder {
				t.Fatalf("lock during run %+v", held)
			}
			if err := r.home.CheckToken(prev.Token); err == nil {
				t.Fatal("old Coordinator token still accepted")
			}
		})
	}
}

func TestLaunchPrintsCoordResumeHint(t *testing.T) {
	newLaunchRig(t)
	out, err := coord(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Resume this Coordinator with: coord --continue") || strings.Contains(out, "claude --resume") {
		t.Fatalf("out %q", out)
	}
}

func TestLaunchEnvNeverLeavesOnlyCoordOnPath(t *testing.T) {
	h := home.New(t.TempDir())
	bin := filepath.Join(t.TempDir(), "coord")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	for name, base := range map[string][]string{
		"no PATH":    {"HOME=/h"},
		"empty PATH": {"PATH=", "HOME=/h"},
	} {
		t.Run(name, func(t *testing.T) {
			env := launchEnv(base, h, "tok", bin)
			var paths []string
			for _, kv := range env {
				if k, v, _ := strings.Cut(kv, "="); k == "PATH" {
					paths = append(paths, v)
				}
			}
			if len(paths) != 1 || paths[0] == filepath.Dir(bin) || !strings.HasPrefix(paths[0], filepath.Dir(bin)+string(os.PathListSeparator)) {
				t.Fatalf("PATH entries %q", paths)
			}
		})
	}
}
