package project

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

func TestParseRemote(t *testing.T) {
	tests := []struct {
		in      string
		want    CodeHost
		wantErr bool
	}{
		{"git@gitlab.example.com:acme/backend/api.git", CodeHost{GitLab, "gitlab.example.com", "acme/backend/api"}, false},
		{"ssh://git@gitlab.example.com:2222/acme/proto.git", CodeHost{GitLab, "gitlab.example.com", "acme/proto"}, false},
		{"https://github.com/bosskrub9992/coordinator-cli.git", CodeHost{GitHub, "github.com", "bosskrub9992/coordinator-cli"}, false},
		{"https://user:pw@gitlab.com/group/sub/repo", CodeHost{GitLab, "gitlab.com", "group/sub/repo"}, false},
		{"git@github.com:owner/repo", CodeHost{GitHub, "github.com", "owner/repo"}, false},
		{"git@GitHub.example.com:owner/repo.git/", CodeHost{GitHub, "github.example.com", "owner/repo"}, false},
		{"file:///C:/tmp/origin.git", CodeHost{GitLab, "localhost", "C:/tmp/origin"}, false},
		{"/local/path/repo", CodeHost{}, true},
		{"https://host-only", CodeHost{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRemote(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newRepo(t *testing.T, origin string) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	run(t, repo, "init", "-q")
	if origin != "" {
		run(t, repo, "remote", "add", "origin", origin)
	}
	return repo
}

func TestInspect(t *testing.T) {
	repo := newRepo(t, "git@gitlab.example.com:acme/backend/api.git")
	run(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(filepath.Dir(repo), "wt")
	run(t, repo, "worktree", "add", "-q", wt)
	noOrigin := newRepo(t, "")
	tests := []struct {
		name    string
		dir     string
		want    string
		wantErr string
	}{
		{"top", repo, repo, ""},
		{"subfolder", filepath.Join(repo, "sub"), repo, ""},
		{"linked worktree resolves to main checkout", wt, repo, ""},
		{"no origin", noOrigin, "", "no origin remote"},
		{"not git", t.TempDir(), "", "not inside a git checkout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Inspect(tt.dir)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != tt.want || got.CodeHost.Kind != GitLab || got.CodeHost.Path != "acme/backend/api" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	h := home.New(t.TempDir())
	r := NewRegistry(h)
	if ps, err := r.List(); err != nil || len(ps) != 0 {
		t.Fatalf("empty registry: %v %v", ps, err)
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	gl := CodeHost{GitLab, "gitlab.example.com", "x/y"}
	outer := filepath.Join(base, "works")
	inner := filepath.Join(base, "works", "api")
	os.MkdirAll(filepath.Join(inner, "internal"), 0o755)
	os.MkdirAll(filepath.Join(base, "elsewhere"), 0o755)

	adds := []struct {
		name    string
		p       Project
		wantErr string
	}{
		{"outer", Project{Name: "works", Path: outer, CodeHost: gl}, ""},
		{"inner", Project{Name: "api", Path: inner, CodeHost: gl}, ""},
		{"duplicate name", Project{Name: "api", Path: filepath.Join(base, "elsewhere"), CodeHost: gl}, "already registered"},
		{"duplicate path", Project{Name: "other", Path: inner, CodeHost: gl}, "already registered as Project"},
		{"bad name", Project{Name: "-x", Path: inner, CodeHost: gl}, "must start"},
		{"relative path", Project{Name: "rel", Path: "rel", CodeHost: gl}, "not absolute"},
		{"bad code host", Project{Name: "bad", Path: filepath.Join(base, "elsewhere"), CodeHost: CodeHost{Kind: "bitbucket"}}, "not gitlab or github"},
	}
	for _, tt := range adds {
		t.Run(tt.name, func(t *testing.T) {
			err := r.Add(tt.p)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}

	ps, _ := r.List()
	if len(ps) != 2 || ps[0].Name != "api" || ps[1].Name != "works" {
		t.Fatalf("list = %+v", ps)
	}
	if _, err := r.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get unknown: %v", err)
	}

	folders := []struct {
		dir   string
		want  string
		found bool
	}{
		{filepath.Join(inner, "internal"), "api", true},
		{inner, "api", true},
		{outer, "works", true},
		{filepath.Join(base, "elsewhere"), "", false},
	}
	for _, tt := range folders {
		p, found, err := r.ForFolder(tt.dir)
		if err != nil {
			t.Fatal(err)
		}
		if found != tt.found || p.Name != tt.want {
			t.Fatalf("ForFolder(%s) = %q %v", tt.dir, p.Name, found)
		}
	}
}

func TestWithin(t *testing.T) {
	tests := []struct {
		parent, child string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/c", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/a/b/..x", true},
	}
	for _, tt := range tests {
		if got := within(filepath.FromSlash(tt.parent), filepath.FromSlash(tt.child)); got != tt.want {
			t.Errorf("within(%s, %s) = %v", tt.parent, tt.child, got)
		}
	}
}
