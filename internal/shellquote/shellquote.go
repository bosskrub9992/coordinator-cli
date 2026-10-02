package shellquote

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var safe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func Quote(s string) string { return quote(s, runtime.GOOS == "windows") }

func Join(args ...string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = Quote(a)
	}
	return strings.Join(parts, " ")
}

func quote(s string, windows bool) string {
	if windows {
		s = strings.ReplaceAll(filepath.ToSlash(s), `\`, "/")
	}
	if safe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
