package treehouse

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitEnv() []string {
	return append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repo(t *testing.T) (main string, env []string) {
	t.Helper()
	if _, err := exec.LookPath("treehouse"); err != nil {
		t.Skip("treehouse not installed")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	origin := filepath.Join(root, "origin.git")
	main = filepath.Join(root, "repo")
	git(t, root, "init", "-q", "--bare", origin)
	git(t, root, "init", "-q", "-b", "main", main)
	os.WriteFile(filepath.Join(main, "README.md"), []byte("hi\n"), 0o644)
	git(t, main, "add", ".")
	git(t, main, "commit", "-q", "-m", "init")
	git(t, main, "remote", "add", "origin", origin)
	git(t, main, "push", "-q", "origin", "main")
	env = append(gitEnv(), "TREEHOUSE_ROOT="+filepath.Join(root, "pool"))
	return main, env
}

func TestNoPoolRefused(t *testing.T) {
	main, env := repo(t)
	c := Client{Env: env}
	if _, err := c.Acquire(main, "coord/001-x", "001-x"); !errors.Is(err, ErrNoPool) || !strings.Contains(err.Error(), "treehouse init") {
		t.Fatalf("err = %v", err)
	}
}

func TestLease(t *testing.T) {
	main, env := repo(t)
	os.WriteFile(filepath.Join(main, ConfigFile), []byte("max_trees = 4\n"), 0o644)
	c := Client{Env: env}
	l, err := c.Acquire(main, "coord/001-x", "001-x")
	if err != nil {
		t.Fatal(err)
	}
	if l.Path == "" || l.LeaseHolder != "001-x" || l.LeaseID == "" || l.Branch != "coord/001-x" {
		t.Fatalf("lease %+v", l)
	}
	if b := git(t, l.Path, "rev-parse", "--abbrev-ref", "HEAD"); b != "coord/001-x" {
		t.Fatalf("branch %s", b)
	}
	again, err := c.Acquire(main, "coord/001-x", "001-x")
	if err != nil || again.Path != l.Path || again.LeaseID != l.LeaseID {
		t.Fatalf("re-acquire %+v %v", again, err)
	}
	other, err := c.Acquire(main, "coord/002-y", "002-y")
	if err != nil || other.Path == l.Path {
		t.Fatalf("second lease %+v %v", other, err)
	}
	slots, err := c.Status(main)
	if err != nil || len(slots) != 2 {
		t.Fatalf("status %+v %v", slots, err)
	}
	if b := git(t, main, "rev-parse", "--abbrev-ref", "HEAD"); b != "main" {
		t.Fatalf("main checkout moved to %s", b)
	}
}

func TestReturnDirtyNeedsForce(t *testing.T) {
	main, env := repo(t)
	os.WriteFile(filepath.Join(main, ConfigFile), []byte("max_trees = 4\n"), 0o644)
	c := Client{Env: env}
	l, err := c.Acquire(main, "coord/001-x", "001-x")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(l.Path, "scratch.txt"), []byte("wip\n"), 0o644)
	if err := c.Return(l.Path, l.LeaseID); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("dirty return without force: %v", err)
	}
	if err := (Client{Env: env, Force: true}).Return(l.Path, l.LeaseID); err != nil {
		t.Fatalf("forced return: %v", err)
	}
	slots, err := c.Status(main)
	if err != nil || len(slots) != 1 || slots[0].Status == "leased" {
		t.Fatalf("status after forced return %+v %v", slots, err)
	}
}
