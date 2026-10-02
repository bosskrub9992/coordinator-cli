package mrwatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeCLI(t *testing.T, dir, name, log string, fail bool) string {
	t.Helper()
	script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n"
	if fail {
		script += "echo 'merge request not found' >&2\nexit 1\n"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCloserCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake CLIs are shell scripts")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	c := Closer{GLab: fakeCLI(t, dir, "glab", log, false), GH: fakeCLI(t, dir, "gh", log, false)}
	for _, u := range []string{
		"https://gitlab.example.com/acme/backend/api/-/merge_requests/12",
		"http://gitlab.local/g/p/-/merge_requests/3",
		"https://github.com/cli/cli/pull/9",
	} {
		r, err := ParseURL(u)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Close(context.Background(), r); err != nil {
			t.Fatalf("close %s: %v", u, err)
		}
	}
	b, _ := os.ReadFile(log)
	want := "glab mr close 12 --repo https://gitlab.example.com/acme/backend/api\n" +
		"glab mr close 3 --repo http://gitlab.local/g/p\n" +
		"gh pr close 9 --repo github.com/cli/cli\n"
	if string(b) != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", b, want)
	}

	bad := Closer{GLab: fakeCLI(t, t.TempDir(), "glab", log, true)}
	r, _ := ParseURL("https://gitlab.example.com/g/p/-/merge_requests/4")
	if err := bad.Close(context.Background(), r); err == nil || !strings.Contains(err.Error(), "merge request not found") || !strings.Contains(err.Error(), "mr close 4") {
		t.Fatalf("failing close: %v", err)
	}
	missing := Closer{GH: filepath.Join(dir, "no-such-gh")}
	r, _ = ParseURL("https://github.com/a/b/pull/1")
	if err := missing.Close(context.Background(), r); err == nil {
		t.Fatal("missing gh did not fail")
	}
	if err := c.Close(context.Background(), Ref{URL: "x", Kind: "bitbucket"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}
