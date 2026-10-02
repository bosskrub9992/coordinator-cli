package home

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func eachLines(t *testing.T, l Log) []string {
	t.Helper()
	var out []string
	if err := l.Each(func(line []byte) error { out = append(out, string(line)); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func reverseLines(t *testing.T, l Log) []string {
	t.Helper()
	var out []string
	if err := l.Reverse(func(line []byte) bool { out = append(out, string(line)); return true }); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(out)
	return out
}

func TestReverseMatchesEach(t *testing.T) {
	big := strings.Repeat("x", reverseChunk+123)
	cases := map[string]string{
		"empty":                "",
		"one line":             `{"seq":1}` + "\n",
		"unterminated only":    `{"seq":1}`,
		"torn tail":            `{"seq":1}` + "\n" + `{"seq":2,"te`,
		"valid unterminated":   `{"seq":1}` + "\n" + `{"seq":2}`,
		"blank and junk lines": "\n\n" + `{"seq":1}` + "\nnot json\n\n" + `{"seq":2}` + "\n",
		"crosses chunks":       fmt.Sprintf(`{"seq":1,"t":"%s"}`+"\n"+`{"seq":2}`+"\n"+`{"seq":3,"t":"%s"}`+"\n", big, big),
		"crlf":                 `{"seq":1}` + "\r\n" + `{"seq":2}` + "\r\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "log.jsonl")
			os.WriteFile(p, []byte(content), 0o644)
			l := Log{Path: p}
			want, got := eachLines(t, l), reverseLines(t, l)
			if !slices.Equal(got, want) {
				t.Fatalf("Reverse %q\nEach    %q", got, want)
			}
			last, err := l.Last()
			if err != nil {
				t.Fatal(err)
			}
			wantLast := ""
			if len(want) > 0 {
				wantLast = want[len(want)-1]
			}
			if string(last) != wantLast {
				t.Fatalf("Last %q want %q", last, wantLast)
			}
		})
	}
}

func TestReverseStopsEarly(t *testing.T) {
	l := Log{Path: filepath.Join(t.TempDir(), "log.jsonl")}
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, `{"seq":%d}`+"\n", i)
	}
	os.WriteFile(l.Path, []byte(b.String()), 0o644)
	n := 0
	if err := l.Reverse(func([]byte) bool { n++; return n < 3 }); err != nil || n != 3 {
		t.Fatalf("visited %d lines, err %v", n, err)
	}
	last, _ := l.Last()
	if string(last) != `{"seq":5000}` {
		t.Fatalf("Last %q", last)
	}
}

func TestReverseMissingFile(t *testing.T) {
	called := false
	if err := (Log{Path: filepath.Join(t.TempDir(), "none")}).Reverse(func([]byte) bool { called = true; return true }); err != nil || called {
		t.Fatalf("err %v called %v", err, called)
	}
}
