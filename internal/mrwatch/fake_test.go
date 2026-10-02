package mrwatch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	envFakeFixture = "COORD_FAKE_API_FIXTURE"
	envFakeLog     = "COORD_FAKE_API_LOG"
)

func TestMain(m *testing.M) {
	switch name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); name {
	case "glab", "gh":
		os.Exit(fakeAPI(name, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeEntry struct {
	Pages  []json.RawMessage `json:"pages"`
	Joined bool              `json:"joined"`
	Stderr string            `json:"stderr"`
	Exit   int               `json:"exit"`
}

func fakeAPI(name string, args []string) int {
	if log := os.Getenv(envFakeLog); log != "" {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			line, _ := json.Marshal(append([]string{name}, args...))
			f.Write(append(line, '\n'))
			f.Close()
		}
	}
	if len(args) < 6 || args[0] != "api" || args[1] != "--hostname" || args[3] != "--method" || args[4] != "GET" {
		fmt.Fprintf(os.Stderr, "fake %s: unexpected call %q\n", name, args)
		return 3
	}
	path := args[len(args)-1]
	paginate := slices.Contains(args[5:len(args)-1], "--paginate")
	raw, err := os.ReadFile(os.Getenv(envFakeFixture))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	var fixture map[string]fakeEntry
	if err := json.Unmarshal(raw, &fixture); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	e, ok := fixture[path]
	if !ok {
		fmt.Print(`{"message":"404 Not found"}`)
		fmt.Fprintf(os.Stderr, "%s: 404 Not found (HTTP 404)\n", name)
		return 1
	}
	if e.Exit != 0 {
		fmt.Fprintln(os.Stderr, e.Stderr)
		return e.Exit
	}
	pages := e.Pages
	if !paginate && len(pages) > 1 {
		pages = pages[:1]
	}
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	if e.Joined && paginate {
		var all []json.RawMessage
		for _, p := range pages {
			var items []json.RawMessage
			json.Unmarshal(p, &items)
			all = append(all, items...)
		}
		out, _ := json.Marshal(all)
		w.Write(out)
		return 0
	}
	for _, p := range pages {
		w.Write(p)
	}
	return 0
}

type fakeRig struct {
	t       *testing.T
	bin     string
	env     []string
	fixture string
	log     string
}

func newFake(t *testing.T, name, fixture string) *fakeRig {
	t.Helper()
	dir := t.TempDir()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if runtime.GOOS == "windows" {
		raw, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	r := &fakeRig{t: t, bin: bin, fixture: filepath.Join(dir, "fixture.json"), log: filepath.Join(dir, "calls.jsonl")}
	raw, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	r.write(raw)
	r.edit(func(f map[string]fakeEntry) {
		if _, ok := f["user"]; !ok {
			f["user"] = fakeEntry{Pages: raws(`{"username":"coord-self","login":"coord-self"}`)}
		}
	})
	r.env = append(os.Environ(), envFakeFixture+"="+r.fixture, envFakeLog+"="+r.log)
	return r
}

func (r *fakeRig) write(raw []byte) {
	r.t.Helper()
	if err := os.WriteFile(r.fixture, raw, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fakeRig) edit(fn func(map[string]fakeEntry)) {
	r.t.Helper()
	raw, err := os.ReadFile(r.fixture)
	if err != nil {
		r.t.Fatal(err)
	}
	var fixture map[string]fakeEntry
	if err := json.Unmarshal(raw, &fixture); err != nil {
		r.t.Fatal(err)
	}
	fn(fixture)
	out, _ := json.Marshal(fixture)
	r.write(out)
}

func (r *fakeRig) calls() [][]string {
	r.t.Helper()
	raw, err := os.ReadFile(r.log)
	if err != nil {
		r.t.Fatal(err)
	}
	var calls [][]string
	for line := range strings.Lines(string(raw)) {
		var c []string
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			r.t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

func (r *fakeRig) callsTo(path string) int {
	r.t.Helper()
	n := 0
	for _, c := range r.calls() {
		if c[len(c)-1] == path {
			n++
		}
	}
	return n
}

func (r *fakeRig) setUser(json string) {
	r.t.Helper()
	r.edit(func(f map[string]fakeEntry) { f["user"] = fakeEntry{Pages: raws(json)} })
}

func (r *fakeRig) checkGETs(host string, paginated ...string) {
	r.t.Helper()
	for _, c := range r.calls() {
		path := c[len(c)-1]
		want := []string{c[0], "api", "--hostname", host, "--method", "GET"}
		if slices.Contains(paginated, path) {
			want = append(want, "--paginate")
		}
		want = append(want, path)
		if !slices.Equal(c, want) {
			r.t.Errorf("call %q, want %q", c, want)
		}
	}
}

func raws(pages ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(pages))
	for i, p := range pages {
		out[i] = json.RawMessage(p)
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
