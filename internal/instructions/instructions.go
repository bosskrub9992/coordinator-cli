package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const MaxHops = 4

type Source struct {
	Path   string
	Parent string
	Text   string
}

type Dir struct {
	Path         string
	IncludeFiles bool
}

func ImportsOf(text string) []string {
	var out []string
	fenced := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		out = append(out, lineImports(stripCodeSpans(line))...)
	}
	return out
}

func stripCodeSpans(line string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(line, '`')
		if i < 0 {
			b.WriteString(line)
			return b.String()
		}
		j := strings.IndexByte(line[i+1:], '`')
		if j < 0 {
			b.WriteString(line)
			return b.String()
		}
		b.WriteString(line[:i])
		line = line[i+1+j+1:]
	}
}

func lineImports(line string) []string {
	var out []string
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '@' {
			continue
		}
		if i > 0 && (isWord(rs[i-1]) || rs[i-1] == '`') {
			continue
		}
		var b strings.Builder
		j := i + 1
		for j < len(rs) {
			if rs[j] == '\\' && j+1 < len(rs) && rs[j+1] == ' ' {
				b.WriteRune(' ')
				j += 2
				continue
			}
			if unicode.IsSpace(rs[j]) || rs[j] == '`' {
				break
			}
			b.WriteRune(rs[j])
			j++
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
		i = j - 1
	}
	return out
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func resolve(path, base string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(h, strings.TrimPrefix(path, "~"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return realpath(path)
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func expand(file string, seen map[string]bool, depth int, out *[]Source) {
	if depth > MaxHops {
		return
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	for _, imp := range ImportsOf(string(data)) {
		target := resolve(imp, filepath.Dir(file))
		if seen[target] || !isFile(target) {
			continue
		}
		text, err := os.ReadFile(target)
		if err != nil {
			continue
		}
		seen[target] = true
		*out = append(*out, Source{Path: target, Parent: file, Text: string(text)})
		expand(target, seen, depth+1, out)
	}
}

func claudeFiles(dir string) []string {
	var out []string
	for _, f := range []string{
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, ".claude", "CLAUDE.md"),
		filepath.Join(dir, "CLAUDE.local.md"),
	} {
		if isFile(f) {
			out = append(out, realpath(f))
		}
	}
	return out
}

func Resolve(d Dir, seen map[string]bool) []Source {
	var out []Source
	files := claudeFiles(d.Path)
	if len(files) == 0 {
		agents := filepath.Join(d.Path, "AGENTS.md")
		if !isFile(agents) {
			return nil
		}
		files = []string{realpath(agents)}
		d.IncludeFiles = true
	}
	var top []string
	for _, f := range files {
		if seen[f] {
			continue
		}
		seen[f] = true
		top = append(top, f)
	}
	for _, f := range top {
		if d.IncludeFiles {
			if text, err := os.ReadFile(f); err == nil {
				out = append(out, Source{Path: f, Text: string(text)})
			}
		}
		expand(f, seen, 1, &out)
	}
	return out
}

func Supplement(dirs []Dir) ([]Source, string) {
	seen := map[string]bool{}
	var all []Source
	for _, d := range dirs {
		all = append(all, Resolve(d, seen)...)
	}
	if len(all) == 0 {
		return nil, ""
	}
	parts := []string{
		"# Project instructions for the folders attached to this Task",
		"Claude Code does not load these files for this session on its own; they are supplied here and carry the same weight as the CLAUDE.md files they come from.",
	}
	for _, s := range all {
		via := ""
		if s.Parent != "" {
			via = " (imported by " + s.Parent + ")"
		}
		parts = append(parts, "\n## "+s.Path+via+"\n\n"+strings.TrimSpace(s.Text))
	}
	return all, strings.Join(parts, "\n") + "\n"
}
