package task

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type ID string

var idPattern = regexp.MustCompile(`^(\d{3,})-([a-z0-9]+(?:-[a-z0-9]+)*)$`)

func ParseID(s string) (ID, error) {
	if !idPattern.MatchString(s) {
		return "", fmt.Errorf("Task id %q is not NNN-slug", s)
	}
	return ID(s), nil
}

func (id ID) Number() int {
	m := idPattern.FindStringSubmatch(string(id))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func FormatID(n int, title string) ID {
	return ID(fmt.Sprintf("%03d-%s", n, Slug(title)))
}

const maxSlugLen = 40

func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
		if i := strings.LastIndexByte(s, '-'); i > maxSlugLen/2 {
			s = s[:i]
		}
		s = strings.Trim(s, "-")
	}
	if s == "" {
		return "task"
	}
	return s
}
