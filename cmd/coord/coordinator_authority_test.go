package main

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

var mutating = [][]string{
	{"task", "new", "--project", "p", "--class", "ship", "--title", "t", "--brief", "-"},
	{"spawn", "001-a"},
	{"steer", "001-a", "go on"},
	{"interrupt", "001-a"},
	{"stop", "001-a"},
	{"project", "add", "/tmp"},
}

func authorityFleet(t *testing.T) fleet {
	t.Helper()
	f := newFleet(t)
	f.task(t, "a", task.Running)
	return f
}

func refusedAsUnauthorized(t *testing.T, args []string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "changes the Fleet") {
		t.Fatalf("%v: err = %v, want an authority refusal", args, err)
	}
	if strings.Contains(err.Error(), "COORD_") {
		t.Fatalf("refusal names an env var: %v", err)
	}
}

func TestMutatingCommandsNeedCoordinatorOrCaptain(t *testing.T) {
	authorityFleet(t)
	captainTTY(t, false)
	for _, args := range mutating {
		_, err := coord(t, "brief", args...)
		refusedAsUnauthorized(t, args, err)
	}
}

func TestCaptainTerminalInsideCoordManagedEnvIsNotEnough(t *testing.T) {
	for _, k := range []string{supervise.EnvTask, supervise.EnvRole} {
		t.Run(k, func(t *testing.T) {
			authorityFleet(t)
			t.Setenv(k, "001-a")
			_, err := coord(t, "", "interrupt", "001-a")
			refusedAsUnauthorized(t, []string{"interrupt"}, err)
		})
	}
}

func TestOpenCommandsNeedNoAuthority(t *testing.T) {
	authorityFleet(t)
	captainTTY(t, false)
	for _, args := range [][]string{{"status"}, {"projects"}, {"project", "list"}, {"config", "show"}, {"wait", "--timeout", "20ms"}} {
		if out, err := coord(t, "", args...); err != nil {
			t.Fatalf("%v: %q %v", args, out, err)
		}
	}
}

func TestLiveTokenAuthorizes(t *testing.T) {
	f := authorityFleet(t)
	captainTTY(t, false)
	repo := gitRepo(t, "git@gitlab.example.com:acme/backend/api.git")
	old, err := f.home.AcquireLock(home.Owner{LaunchFolder: repo}, false)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := f.home.AcquireLock(home.Owner{LaunchFolder: repo}, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, cur.Token)
	if out, err := coord(t, "", "project", "add", repo, "--name", "api"); err != nil {
		t.Fatalf("live Coordinator refused: %q %v", out, err)
	}
	t.Setenv(home.EnvToken, old.Token)
	if _, err := coord(t, "x", "task", "new", "--project", "api", "--class", "ship", "--title", "t", "--brief", "-"); err == nil || !strings.Contains(err.Error(), "took over") {
		t.Fatalf("superseded token accepted: %v", err)
	}

	l, _ := f.home.ReadLock()
	l.PID = deadPID(t)
	if err := home.WriteJSONAtomic(f.home.LockPath(), l); err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, cur.Token)
	if _, err := coord(t, "x", "task", "new", "--project", "api", "--class", "ship", "--title", "t", "--brief", "-"); err == nil {
		t.Fatal("token of a dead Coordinator accepted")
	}
}

func TestCaptainAtTerminalAuthorizes(t *testing.T) {
	authorityFleet(t)
	repo := gitRepo(t, "git@gitlab.example.com:acme/backend/api.git")
	if out, err := coord(t, "", "project", "add", repo, "--name", "api"); err != nil {
		t.Fatalf("Captain refused: %q %v", out, err)
	}
	if out, err := coord(t, "", "projects"); err != nil || !strings.Contains(out, "api") {
		t.Fatalf("projects: %q %v", out, err)
	}
}

func TestLaunchRefusedInsideCoordManagedEnv(t *testing.T) {
	for _, k := range []string{home.EnvToken, supervise.EnvTask} {
		t.Run(k, func(t *testing.T) {
			r := newLaunchRig(t)
			t.Setenv(k, "x")
			_, err := coord(t, "")
			if err == nil || !strings.Contains(err.Error(), "inside a Coordinator or a Worker") || strings.Contains(err.Error(), "COORD_") {
				t.Fatalf("err = %v", err)
			}
			if r.ran() {
				t.Fatal("claude ran")
			}
		})
	}
}

func TestMsysPTYName(t *testing.T) {
	for name, want := range map[string]bool{
		`\msys-1888ae32e00d56aa-pty0-from-master`: true,
		`\cygwin-e022582115c10879-pty4-to-master`: true,
		`\msys-1888ae32e00d56aa-pipe-from-master`: false,
		`\some-other-pipe`:                        false,
		`\msys-1888ae32e00d56aa-pty0-from-slave`:  false,
	} {
		if got := msysPTYName(name); got != want {
			t.Errorf("msysPTYName(%q) = %v want %v", name, got, want)
		}
	}
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ttyFile(f) {
		t.Fatal("the null device counts as the Captain's terminal")
	}
}

const terminalProbeEnv = "COORD_TEST_TERMINAL_PROBE"

func TestTerminalProbeChild(t *testing.T) {
	if os.Getenv(terminalProbeEnv) != "1" {
		t.Skip("run only as the child of TestWorkerSupervisorHasNoCaptainTerminal")
	}
	if processTerminal() {
		os.Stdout.WriteString("terminal=yes\n")
	} else {
		os.Stdout.WriteString("terminal=no\n")
	}
	os.Exit(0)
}

func probeTerminal(t *testing.T, detached bool) string {
	t.Helper()
	c := exec.Command(os.Args[0], "-test.run=^TestTerminalProbeChild$")
	c.Env = append(os.Environ(), terminalProbeEnv+"=1")
	if detached {
		detach(c)
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var out bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader("a piped Brief\n"), &out, null
	if detached {
		c.Stdin = null
	}
	if err := c.Run(); err != nil {
		t.Fatalf("probe: %v %s", err, out.String())
	}
	return strings.TrimSpace(out.String())
}

const underPTYEnv = "COORD_TEST_UNDER_PTY"

func hasControllingTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func rerunUnderPTY(t *testing.T) {
	t.Helper()
	m, sl, err := openPTY()
	if err != nil {
		t.Skipf("no controlling terminal and no pty: %v", err)
	}
	defer m.Close()
	defer sl.Close()
	c := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	c.Env = append(os.Environ(), underPTYEnv+"=1")
	var out bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = sl, &out, &out
	withControllingTerminal(c)
	if err := c.Run(); err != nil || !strings.Contains(out.String(), "--- PASS: "+t.Name()) {
		t.Fatalf("under a pty: %v\n%s", err, out.String())
	}
}

func TestWorkerSupervisorHasNoCaptainTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got := probeTerminal(t, true); got != "terminal=no" {
			t.Fatalf("a detached process counts as the Captain's terminal: %q", got)
		}
		return
	}
	if !hasControllingTerminal() {
		if os.Getenv(underPTYEnv) == "1" {
			t.Fatal("the pty did not become the controlling terminal")
		}
		rerunUnderPTY(t)
		return
	}
	if got := probeTerminal(t, false); got != "terminal=yes" {
		t.Fatalf("the Captain piping into coord at a terminal is not recognised: %q", got)
	}
	if got := probeTerminal(t, true); got != "terminal=no" {
		t.Fatalf("a supervisor-detached process with redirected std streams counts as the Captain's terminal: %q", got)
	}
}
