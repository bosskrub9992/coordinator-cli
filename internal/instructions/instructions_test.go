package instructions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportsOf(t *testing.T) {
	text := strings.Join([]string{
		"@AGENTS.md",
		"see @docs/rules.md and email me@example.com",
		"inline `@not-this.md` but @this.md",
		"```",
		"@fenced.md",
		"```",
		"  ```go",
		"@also-fenced.md",
		"  ```",
		"path with space @my\\ file.md end",
		"`@x` @y.md `@z`",
		"@~/home.md",
	}, "\n")
	got := ImportsOf(text)
	want := []string{"AGENTS.md", "docs/rules.md", "this.md", "my file.md", "y.md", "~/home.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func layout(t *testing.T) (launch, wt, agentsOnly string) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	launch = filepath.Join(root, "launch")
	write(t, filepath.Join(launch, "CLAUDE.md"), "@AGENTS.md\nLAUNCH-CLAUDE-KIWI-4410\n")
	write(t, filepath.Join(launch, "AGENTS.md"), "LAUNCH-AGENTS-MANGO-7731\n")
	wt = filepath.Join(root, "worktrees", "fakesvc-wt1")
	write(t, filepath.Join(wt, "CLAUDE.md"), "@AGENTS.md\nWT-CLAUDE-FIG-6602\n")
	write(t, filepath.Join(wt, "AGENTS.md"), "WT-AGENTS-PAPAYA-3177\n`@nope.md`\n")
	write(t, filepath.Join(wt, "nope.md"), "SHOULD-NOT-LOAD\n")
	agentsOnly = filepath.Join(root, "worktrees", "agentsonly-wt1")
	write(t, filepath.Join(agentsOnly, "AGENTS.md"), "WT-AGONLY-DURIAN-8126\n@extra.md\n")
	write(t, filepath.Join(agentsOnly, "extra.md"), "AGONLY-EXTRA-1111\n")
	return launch, wt, agentsOnly
}

func TestSupplementSpikeLayout(t *testing.T) {
	launch, wt, agentsOnly := layout(t)
	srcs, text := Supplement([]Dir{{Path: wt}, {Path: agentsOnly}, {Path: launch}})
	for _, marker := range []string{"WT-AGENTS-PAPAYA-3177", "WT-AGONLY-DURIAN-8126", "AGONLY-EXTRA-1111", "LAUNCH-AGENTS-MANGO-7731"} {
		if !strings.Contains(text, marker) {
			t.Errorf("missing %s", marker)
		}
	}
	for _, marker := range []string{"WT-CLAUDE-FIG-6602", "LAUNCH-CLAUDE-KIWI-4410", "SHOULD-NOT-LOAD"} {
		if strings.Contains(text, marker) {
			t.Errorf("%s should not be in the supplement", marker)
		}
	}
	if len(srcs) != 4 {
		t.Fatalf("sources %+v", srcs)
	}
	if srcs[0].Parent != filepath.Join(wt, "CLAUDE.md") || srcs[1].Parent != "" {
		t.Fatalf("parents %q %q", srcs[0].Parent, srcs[1].Parent)
	}
	if !strings.Contains(text, "## "+filepath.Join(wt, "AGENTS.md")+" (imported by "+filepath.Join(wt, "CLAUDE.md")+")") {
		t.Fatalf("heading missing:\n%s", text)
	}
}

func TestSupplementIncludeFiles(t *testing.T) {
	launch, _, _ := layout(t)
	_, text := Supplement([]Dir{{Path: launch, IncludeFiles: true}})
	if !strings.Contains(text, "LAUNCH-CLAUDE-KIWI-4410") || !strings.Contains(text, "LAUNCH-AGENTS-MANGO-7731") {
		t.Fatalf("text:\n%s", text)
	}
}

func TestFourHopLimit(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(dir, "CLAUDE.md"), "@h1.md\n")
	for i := 1; i <= 5; i++ {
		write(t, filepath.Join(dir, "h"+string(rune('0'+i))+".md"), "HOP-"+string(rune('0'+i))+"\n@h"+string(rune('0'+i+1))+".md\n")
	}
	_, text := Supplement([]Dir{{Path: dir}})
	for i := 1; i <= 4; i++ {
		if !strings.Contains(text, "HOP-"+string(rune('0'+i))) {
			t.Errorf("hop %d missing", i)
		}
	}
	if strings.Contains(text, "HOP-5") {
		t.Error("hop 5 loaded past the limit")
	}
}

func TestCyclesRelativeHomeAndDedup(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", dir)
	write(t, filepath.Join(dir, "repo", "CLAUDE.md"), "@sub/a.md @~/global.md @missing.md\n")
	write(t, filepath.Join(dir, "repo", "sub", "a.md"), "A-TEXT\n@../CLAUDE.md @b.md\n")
	write(t, filepath.Join(dir, "repo", "sub", "b.md"), "B-TEXT\n@a.md\n")
	write(t, filepath.Join(dir, "global.md"), "GLOBAL-TEXT\n")
	write(t, filepath.Join(dir, "other", "CLAUDE.md"), "@../global.md\n")
	srcs, text := Supplement([]Dir{{Path: filepath.Join(dir, "repo")}, {Path: filepath.Join(dir, "other")}})
	var names []string
	for _, s := range srcs {
		names = append(names, filepath.Base(s.Path))
	}
	if !reflect.DeepEqual(names, []string{"a.md", "b.md", "global.md"}) {
		t.Fatalf("sources %v\n%s", names, text)
	}
}

func TestEmpty(t *testing.T) {
	srcs, text := Supplement([]Dir{{Path: t.TempDir()}})
	if srcs != nil || text != "" {
		t.Fatalf("%v %q", srcs, text)
	}
}
