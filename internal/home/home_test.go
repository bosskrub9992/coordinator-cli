package home

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolve(t *testing.T) {
	tmp := t.TempDir()
	tests := []struct {
		name string
		env  string
		want string
	}{
		{"override", tmp, tmp},
		{"default", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvHome, tt.env)
			h, err := Resolve()
			if err != nil {
				t.Fatal(err)
			}
			want := tt.want
			if want == "" {
				dir, _ := os.UserHomeDir()
				want = filepath.Join(dir, DirName)
			}
			if h.Root != want {
				t.Fatalf("Root = %q, want %q", h.Root, want)
			}
		})
	}
}

func TestEnsureCreatesLayout(t *testing.T) {
	h := New(filepath.Join(t.TempDir(), "home"))
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{h.MemoryDir(), h.TasksDir()} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Fatalf("%s not a dir: %v", p, err)
		}
	}
	if err := os.WriteFile(h.CoordinatorMDPath(), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(h.CoordinatorMDPath())
	if string(got) != "mine" {
		t.Fatalf("Ensure overwrote COORDINATOR.md: %q", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.json")
	for _, content := range []string{"one", "two"} {
		if err := WriteFileAtomic(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(p)
		if string(got) != content {
			t.Fatalf("got %q want %q", got, content)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestReadJSONMissing(t *testing.T) {
	var v map[string]any
	found, err := ReadJSON(filepath.Join(t.TempDir(), "nope.json"), &v)
	if found || err != nil {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestLogAppendAndTornTail(t *testing.T) {
	type entry struct {
		N int `json:"n"`
	}
	tests := []struct {
		name    string
		seed    string
		want    []string
		wantLst string
	}{
		{"empty", "", []string{`{"n":1}`, `{"n":2}`}, `{"n":2}`},
		{"torn tail is skipped", `{"n":0}` + "\n" + `{"n":`, []string{`{"n":0}`, `{"n":1}`, `{"n":2}`}, `{"n":2}`},
		{"garbage line is skipped", "not json\n", []string{`{"n":1}`, `{"n":2}`}, `{"n":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Log{Path: filepath.Join(t.TempDir(), "events.jsonl")}
			if tt.seed != "" {
				os.WriteFile(l.Path, []byte(tt.seed), 0o644)
			}
			for i := 1; i <= 2; i++ {
				if err := l.Append(entry{N: i}); err != nil {
					t.Fatal(err)
				}
			}
			var got []string
			l.Each(func(line []byte) error { got = append(got, string(line)); return nil })
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("got %v want %v", got, tt.want)
			}
			last, _ := l.Last()
			if string(last) != tt.wantLst {
				t.Fatalf("last %q want %q", last, tt.wantLst)
			}
		})
	}
}

func TestLogConcurrentAppend(t *testing.T) {
	l := Log{Path: filepath.Join(t.TempDir(), "events.jsonl")}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Append(map[string]int{"n": i}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	n := 0
	l.Each(func([]byte) error { n++; return nil })
	if n != 20 {
		t.Fatalf("got %d lines", n)
	}
}

func fakeProbe(t *testing.T, procs map[int]time.Time) {
	t.Helper()
	old := probe
	probe = func(pid int) (bool, time.Time) {
		st, ok := procs[pid]
		return ok, st
	}
	t.Cleanup(func() { probe = old })
}

func TestLockLive(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	fakeProbe(t, map[int]time.Time{100: t0, 200: {}})
	tests := []struct {
		name string
		lock Lock
		want bool
	}{
		{"same process", Lock{PID: 100, StartTime: t0}, true},
		{"dead pid", Lock{PID: 300, StartTime: t0}, false},
		{"reused pid", Lock{PID: 100, StartTime: t0.Add(-time.Hour)}, false},
		{"start time unknown", Lock{PID: 200, StartTime: t0}, true},
		{"no recorded start", Lock{PID: 100}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.lock.Live(); got != tt.want {
				t.Fatalf("Live() = %v want %v", got, tt.want)
			}
		})
	}
}

func TestAcquireLock(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	procs := map[int]time.Time{100: t0, 200: t0.Add(time.Minute)}
	fakeProbe(t, procs)
	h := New(t.TempDir())

	first, err := h.AcquireLock(Owner{PID: 100, LaunchFolder: "/a", SessionID: "s1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == "" || !first.StartTime.Equal(t0) || first.LaunchFolder != "/a" || first.SessionID != "s1" {
		t.Fatalf("bad lock %+v", first)
	}
	if err := h.CheckToken(first.Token); err != nil {
		t.Fatal(err)
	}

	_, err = h.AcquireLock(Owner{PID: 200, LaunchFolder: "/b"}, false)
	var held *HeldError
	if !errors.As(err, &held) || held.Holder.PID != 100 {
		t.Fatalf("want HeldError from pid 100, got %v", err)
	}

	second, err := h.AcquireLock(Owner{PID: 200, LaunchFolder: "/b"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == first.Token {
		t.Fatal("takeover kept the old token")
	}
	if err := h.CheckToken(first.Token); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old token accepted: %v", err)
	}
	if err := h.CheckToken(second.Token); err != nil {
		t.Fatal(err)
	}
	if err := h.CheckToken(""); err != nil {
		t.Fatalf("calls without a token must pass: %v", err)
	}
	if err := h.ReleaseLock(first.Token); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old token released the lock: %v", err)
	}
	if _, err := h.UpdateLock(first.Token, func(l *Lock) {}); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("old token updated the lock: %v", err)
	}
	upd, err := h.UpdateLock(second.Token, func(l *Lock) { l.SessionID = "s2"; l.Token = "evil" })
	if err != nil || upd.SessionID != "s2" || upd.Token != second.Token {
		t.Fatalf("update: %+v %v", upd, err)
	}

	delete(procs, 200)
	third, err := h.AcquireLock(Owner{PID: 100, LaunchFolder: "/c"}, false)
	if err != nil {
		t.Fatalf("stale lock not replaced: %v", err)
	}
	if err := h.ReleaseLock(third.Token); err != nil {
		t.Fatal(err)
	}
	if l, _ := h.ReadLock(); l != nil {
		t.Fatalf("lock still present: %+v", l)
	}
	if err := h.CheckToken(third.Token); !errors.Is(err, ErrSuperseded) {
		t.Fatalf("token accepted after release: %v", err)
	}
}

func TestDefaultProbeSelf(t *testing.T) {
	alive, start := defaultProbe(os.Getpid())
	if !alive {
		t.Fatal("own process reported dead")
	}
	if start.IsZero() || time.Since(start) < 0 || time.Since(start) > time.Hour {
		t.Fatalf("implausible start time %v", start)
	}
	l := Lock{PID: os.Getpid(), StartTime: start}
	if !l.Live() {
		t.Fatal("own lock not live")
	}
}
