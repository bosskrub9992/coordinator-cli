package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
)

func setup(t *testing.T) string {
	t.Helper()
	h := filepath.Join(t.TempDir(), "home")
	t.Setenv(home.EnvHome, h)
	t.Setenv(home.EnvToken, "")
	t.Setenv(supervise.EnvRole, "")
	t.Setenv(supervise.EnvTask, "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude-config"))
	captainTTY(t, true)
	stubWatcher(t)
	stubNotifier(t)
	return h
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakeNotifier) Send(title, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, title+": "+text)
	return f.err
}

func (f *fakeNotifier) Fire(title, text string) { f.Send(title, text) }

func (f *fakeNotifier) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func stubNotifier(t *testing.T) *fakeNotifier {
	t.Helper()
	f := &fakeNotifier{}
	old := notifier
	notifier = f
	t.Cleanup(func() { notifier = old })
	return f
}

func stubWatcher(t *testing.T) *int {
	t.Helper()
	starts := new(int)
	old := watcherStarter
	watcherStarter = func(*app) error { *starts++; return nil }
	t.Cleanup(func() { watcherStarter = old })
	return starts
}

func captainTTY(t *testing.T, tty bool) {
	t.Helper()
	old, oldCaptain := isTerminal, captainTerminal
	isTerminal = func(io.Reader) bool { return tty }
	captainTerminal = func() bool { return tty }
	t.Cleanup(func() { isTerminal, captainTerminal = old, oldCaptain })
}

func coord(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	root := newRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func gitRepo(t *testing.T, origin string) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func TestStatusEmptyHome(t *testing.T) {
	setup(t)
	out, err := coord(t, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Coordinator: none") || !strings.Contains(out, "No Tasks in the Fleet.") {
		t.Fatalf("out = %q", out)
	}
	out, err = coord(t, "", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v statusView
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Tasks == nil || len(v.Tasks) != 0 {
		t.Fatalf("json %q %v", out, err)
	}
}

func TestProjectAndTaskFlow(t *testing.T) {
	setup(t)
	repo := gitRepo(t, "git@gitlab.example.com:acme/backend/api.git")
	out, err := coord(t, "", "project", "add", repo, "--name", "api")
	if err != nil || !strings.Contains(out, "Added Project api") || !strings.Contains(out, "gitlab gitlab.example.com/acme/backend/api") {
		t.Fatalf("add: %q %v", out, err)
	}
	if _, err := coord(t, "", "project", "add", repo); err == nil {
		t.Fatal("same checkout registered twice")
	}
	for _, args := range [][]string{{"projects"}, {"project", "list"}} {
		out, err := coord(t, "", args...)
		if err != nil || !strings.Contains(out, "api") {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}

	tests := []struct {
		name    string
		stdin   string
		args    []string
		wantOut string
		wantErr string
	}{
		{"creates", "Do the thing.", []string{"--project", "api", "--class", "ship", "--title", "Fix login timeout"}, "001-fix-login-timeout\n", ""},
		{"next id", "Look.", []string{"--project", "api", "--class", "scout", "--title", "Look around", "--model", "claude-sonnet-5-5"}, "002-look-around\n", ""},
		{"unknown project", "x", []string{"--project", "nope", "--class", "ship", "--title", "t"}, "", "coord project add"},
		{"bad class", "x", []string{"--project", "api", "--class", "build", "--title", "t"}, "", "unknown Task class"},
		{"empty brief", "  ", []string{"--project", "api", "--class", "ship", "--title", "t"}, "", "Brief is empty"},
		{"model outside may-choose", "x", []string{"--project", "api", "--class", "ship", "--title", "t", "--model", "claude-fable-5-1"}, "", "coordinator_may_choose"},
		{"missing flag", "x", []string{"--project", "api", "--class", "ship"}, "", "required flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := coord(t, tt.stdin, append([]string{"task", "new", "--brief", "-"}, tt.args...)...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || out != tt.wantOut {
				t.Fatalf("out %q err %v", out, err)
			}
		})
	}

	out, err = coord(t, "", "status")
	if err != nil || !strings.Contains(out, "001-fix-login-timeout  queued") || !strings.Contains(out, "002-look-around") {
		t.Fatalf("status: %q %v", out, err)
	}
}

func TestSupersededTokenRefused(t *testing.T) {
	h := setup(t)
	hm := home.New(h)
	if err := hm.Ensure(); err != nil {
		t.Fatal(err)
	}
	old, err := hm.AcquireLock(home.Owner{LaunchFolder: absPath("/a")}, false)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := hm.AcquireLock(home.Owner{LaunchFolder: absPath("/b")}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, old.Token)
	if _, err := coord(t, "", "status"); err == nil || !strings.Contains(err.Error(), "took over") {
		t.Fatalf("old token accepted: %v", err)
	}
	t.Setenv(home.EnvToken, cur.Token)
	out, err := coord(t, "", "status")
	if err != nil || !strings.Contains(out, "Coordinator: live") || !strings.Contains(out, absPath("/b")) {
		t.Fatalf("current token: %q %v", out, err)
	}
}

func TestAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{42 * time.Second, "42s"},
		{5 * time.Minute, "5m"},
		{3*time.Hour + 7*time.Minute, "3h07m"},
		{72 * time.Hour, "3d"},
	}
	for _, tt := range tests {
		if got := age(tt.d); got != tt.want {
			t.Errorf("age(%v) = %q want %q", tt.d, got, tt.want)
		}
	}
}

func absPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return abs
}

func linkSelf(t *testing.T, dir, name string) string {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
		raw, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, raw, 0o755); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

func addOrigin(t *testing.T, repo, origin string) {
	t.Helper()
	slashed := filepath.ToSlash(origin)
	if runtime.GOOS != "windows" {
		runGit(t, repo, "remote", "add", "origin", "file://localhost"+slashed)
		return
	}
	runGit(t, repo, "remote", "add", "origin", slashed)
}
