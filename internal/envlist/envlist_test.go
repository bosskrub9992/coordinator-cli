package envlist

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func withFold(t *testing.T, v bool) {
	t.Helper()
	old := foldCase
	foldCase = v
	t.Cleanup(func() { foldCase = old })
}

func TestWindowsKeysIgnoreCase(t *testing.T) {
	withFold(t, true)
	env := []string{"Path=C:\\bin", "SystemRoot=C:\\Win", "coord_token=x"}
	if got := Get(env, "PATH"); got != `C:\bin` {
		t.Fatalf("Get PATH = %q", got)
	}
	got := Set(env, "COORD_TOKEN", "y")
	if want := []string{"Path=C:\\bin", "SystemRoot=C:\\Win", "coord_token=y"}; !slices.Equal(got, want) {
		t.Fatalf("Set = %q want %q", got, want)
	}
	got = Filter(env, func(k string) bool { return Same(k, "COORD_TOKEN") })
	if len(got) != 2 {
		t.Fatalf("Filter = %q", got)
	}
}

func TestUnixKeysKeepCase(t *testing.T) {
	withFold(t, false)
	env := []string{"Path=/odd", "PATH=/bin"}
	if got := Get(env, "PATH"); got != "/bin" {
		t.Fatalf("Get = %q", got)
	}
	if got := Set(env, "PATH", "/x"); !slices.Equal(got, []string{"Path=/odd", "PATH=/x"}) {
		t.Fatalf("Set = %q", got)
	}
}

func TestSetCollapsesDuplicates(t *testing.T) {
	withFold(t, true)
	got := Set([]string{"PATH=a", "X=1", "Path=b"}, "PATH", "c")
	if !slices.Equal(got, []string{"Path=c", "X=1"}) {
		t.Fatalf("Set = %q", got)
	}
}

func TestPrependPathKeepsExistingEntries(t *testing.T) {
	withFold(t, true)
	sep := string(os.PathListSeparator)
	got := PrependPath([]string{`Path=C:\Windows\System32`}, `C:\coord`)
	if !slices.Equal(got, []string{`Path=C:\coord` + sep + `C:\Windows\System32`}) {
		t.Fatalf("PrependPath = %q", got)
	}
	got = PrependPath([]string{`SYSTEMROOT=D:\Win`}, `C:\coord`)
	v := Get(got, "PATH")
	if !strings.HasPrefix(v, `C:\coord`+sep) || !strings.Contains(v, `D:\Win\System32`) {
		t.Fatalf("PATH from nothing = %q", v)
	}
	withFold(t, false)
	if v := Get(PrependPath(nil, "/c"), "PATH"); v == "/c" || !strings.HasPrefix(v, "/c"+sep) || !strings.Contains(v, "/usr/bin") {
		t.Fatalf("unix PATH from nothing = %q", v)
	}
}
