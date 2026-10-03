package mrwatch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergerCommands(t *testing.T) {
	m := Merger{}
	gl, _ := ParseURL("https://gitlab.example.com/acme/backend/api/-/merge_requests/12")
	glHTTP, _ := ParseURL("http://gitlab.local/g/p/-/merge_requests/3")
	gh, _ := ParseURL("https://github.com/cli/cli/pull/9")
	cases := []struct {
		name   string
		ref    Ref
		method Method
		bin    string
		want   string
		fail   bool
	}{
		{"gitlab default", gl, MethodAuto, "glab", "mr merge 12 --repo https://gitlab.example.com/acme/backend/api --yes --auto-merge=false", false},
		{"gitlab merge", gl, MethodMerge, "glab", "mr merge 12 --repo https://gitlab.example.com/acme/backend/api --yes --auto-merge=false", false},
		{"gitlab squash", gl, MethodSquash, "glab", "mr merge 12 --repo https://gitlab.example.com/acme/backend/api --yes --auto-merge=false --squash", false},
		{"gitlab rebase http", glHTTP, MethodRebase, "glab", "mr merge 3 --repo http://gitlab.local/g/p --yes --auto-merge=false --rebase", false},
		{"github merge", gh, MethodMerge, "gh", "pr merge 9 --repo github.com/cli/cli --merge", false},
		{"github squash", gh, MethodSquash, "gh", "pr merge 9 --repo github.com/cli/cli --squash", false},
		{"github rebase", gh, MethodRebase, "gh", "pr merge 9 --repo github.com/cli/cli --rebase", false},
		{"github needs a method", gh, MethodAuto, "", "", true},
		{"unknown kind", Ref{URL: "x", Kind: "bitbucket"}, MethodMerge, "", "", true},
	}
	for _, c := range cases {
		bin, args, err := m.command(c.ref, c.method)
		if c.fail {
			if err == nil {
				t.Errorf("%s: no error", c.name)
			}
			continue
		}
		if err != nil || bin != c.bin || strings.Join(args, " ") != c.want {
			t.Errorf("%s: %s %v %v, want %s %s", c.name, bin, args, err, c.bin, c.want)
		}
		for _, bad := range []string{"--delete-branch", "--admin", "--auto", "--remove-source-branch"} {
			for _, a := range args {
				if a == bad {
					t.Errorf("%s: forbidden flag %s", c.name, a)
				}
			}
		}
	}
}

func TestParseMethod(t *testing.T) {
	for in, want := range map[string]Method{"": MethodAuto, "merge": MethodMerge, " Squash ": MethodSquash, "rebase": MethodRebase} {
		if got, err := ParseMethod(in); err != nil || got != want {
			t.Errorf("ParseMethod(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseMethod("fast-forward"); err == nil {
		t.Error("unknown method accepted")
	}
}

func fakeGHRepo(t *testing.T, dir, log, repoJSON string, failMerge bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		script := "@echo off\r\n>>\"" + log + "\" echo gh %*\r\n" +
			"if \"%1\"==\"repo\" (\r\n  echo " + repoJSON + "\r\n  exit /b 0\r\n)\r\n"
		if failMerge {
			script += "echo Pull request is not mergeable 1>&2\r\nexit /b 1\r\n"
		}
		p := filepath.Join(dir, "gh.cmd")
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	script := "#!/bin/sh\necho \"gh $*\" >> " + log + "\n" +
		"case \"$1\" in repo) echo '" + repoJSON + "'; exit 0;; esac\n"
	if failMerge {
		script += "echo 'Pull request is not mergeable' >&2\nexit 1\n"
	}
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMergerResolveAndMerge(t *testing.T) {
	a, _ := ParseURL("https://github.com/o/a/pull/1")
	b, _ := ParseURL("https://github.com/o/a/pull/2")
	g, _ := ParseURL("https://gitlab.example.com/g/p/-/merge_requests/3")
	cases := []struct {
		name    string
		json    string
		want    Method
		refs    []Ref
		methods []Method
		err     string
	}{
		{"only squash", `{"mergeCommitAllowed":false,"squashMergeAllowed":true,"rebaseMergeAllowed":false}`, MethodAuto, []Ref{a, b, g}, []Method{MethodSquash, MethodSquash, MethodAuto}, ""},
		{"only merge", `{"mergeCommitAllowed":true,"squashMergeAllowed":false,"rebaseMergeAllowed":false}`, MethodAuto, []Ref{a}, []Method{MethodMerge}, ""},
		{"several", `{"mergeCommitAllowed":true,"squashMergeAllowed":true,"rebaseMergeAllowed":false}`, MethodAuto, []Ref{a}, nil, "merge, squash"},
		{"none", `{}`, MethodAuto, []Ref{a}, nil, "could not be read"},
		{"explicit skips the lookup", `{}`, MethodRebase, []Ref{a, g}, []Method{MethodRebase, MethodRebase}, ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		log := filepath.Join(dir, "calls.log")
		m := Merger{GH: fakeGHRepo(t, dir, log, c.json, false)}
		got, err := m.Resolve(context.Background(), c.refs, c.want)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s: %v", c.name, err)
			} else if c.name == "several" && !strings.Contains(err.Error(), "--method") {
				t.Errorf("%s: error does not mention --method: %v", c.name, err)
			}
			continue
		}
		if err != nil || len(got) != len(c.methods) {
			t.Errorf("%s: %v %v", c.name, got, err)
			continue
		}
		for i := range got {
			if got[i] != c.methods[i] {
				t.Errorf("%s: methods %v want %v", c.name, got, c.methods)
			}
		}
		out, _ := os.ReadFile(log)
		lookups := strings.Count(string(out), "repo view github.com/o/a --json mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed")
		if c.want == MethodAuto && lookups != 1 || c.want != MethodAuto && lookups != 0 {
			t.Errorf("%s: %d repo lookups:\n%s", c.name, lookups, out)
		}
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	if err := (Merger{GH: fakeGHRepo(t, dir, log, `{}`, false)}).Merge(context.Background(), a, MethodSquash); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(log); !strings.Contains(string(out), "gh pr merge 1 --repo github.com/o/a --squash") {
		t.Fatalf("calls:\n%s", out)
	}
	bad := Merger{GH: fakeGHRepo(t, t.TempDir(), log, `{}`, true)}
	if err := bad.Merge(context.Background(), a, MethodMerge); err == nil || !strings.Contains(err.Error(), "not mergeable") {
		t.Fatalf("failing merge: %v", err)
	}
	if err := (Merger{GH: filepath.Join(dir, "no-such-gh")}).Merge(context.Background(), a, MethodMerge); err == nil {
		t.Fatal("missing gh did not fail")
	}
}
