package task

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	h := home.New(filepath.Join(t.TempDir(), "home"))
	if err := h.Ensure(); err != nil {
		t.Fatal(err)
	}
	return NewStore(h), t.TempDir()
}

func create(t *testing.T, s *Store, launch, title string) Task {
	t.Helper()
	tk, err := s.Create(NewTask{Title: title, Class: config.Ship, Projects: []string{"api"}, Brief: "do it", LaunchFolder: launch})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Fix PROJ-1234: login timeout", "fix-proj-1234-login-timeout"},
		{"  --Hello,   World!--  ", "hello-world"},
		{"ทดสอบ", "task"},
		{"a very long title that keeps going past the forty character limit", "a-very-long-title-that-keeps-going-past"},
		{"x" + strings.Repeat("y", 50), "x" + strings.Repeat("y", 39)},
	}
	for _, tt := range tests {
		if got := Slug(tt.in); got != tt.want {
			t.Errorf("Slug(%q) = %q want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseID(t *testing.T) {
	tests := []struct {
		in   string
		num  int
		fail bool
	}{
		{"001-fix", 1, false},
		{"1234-a-b", 1234, false},
		{"01-x", 0, true},
		{"001-", 0, true},
		{"001-Fix", 0, true},
	}
	for _, tt := range tests {
		id, err := ParseID(tt.in)
		if (err != nil) != tt.fail || id.Number() != tt.num {
			t.Errorf("ParseID(%q) = %q %v", tt.in, id, err)
		}
	}
}

func TestCreateNumbersAndFiles(t *testing.T) {
	s, launch := newStore(t)
	a := create(t, s, launch, "First thing")
	os.MkdirAll(filepath.Join(s.home.TasksDir(), "041-manual"), 0o755)
	b := create(t, s, launch, "Second")
	if a.ID != "001-first-thing" || b.ID != "042-second" {
		t.Fatalf("ids %s %s", a.ID, b.ID)
	}
	if a.State != Queued || a.Folder != s.WorkDir(a.ID) {
		t.Fatalf("task %+v", a)
	}
	if st, err := os.Stat(a.Folder); err != nil || !st.IsDir() {
		t.Fatalf("work folder missing: %v", err)
	}
	brief, _ := s.Brief(a.ID)
	if brief != "do it\n" {
		t.Fatalf("brief %q", brief)
	}
	if _, found, _ := s.Report(a.ID); found {
		t.Fatal("report exists before it was written")
	}
	evs, _ := s.Events(a.ID, 0)
	if len(evs) != 1 || evs[0].Type != EventCreated || evs[0].Seq != 1 || evs[0].Task != a.ID {
		t.Fatalf("events %+v", evs)
	}
	got, err := s.Find("1")
	if err != nil || got.ID != a.ID {
		t.Fatalf("Find(1) = %v %v", got.ID, err)
	}
	if _, err := s.Find("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Find(nope) = %v", err)
	}
	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("list %d", len(list))
	}
}

func TestCreateValidation(t *testing.T) {
	s, launch := newStore(t)
	tests := []struct {
		name string
		n    NewTask
		want string
	}{
		{"no title", NewTask{Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: launch}, "title"},
		{"bad class", NewTask{Title: "t", Class: "build", Projects: []string{"p"}, Brief: "b", LaunchFolder: launch}, "class"},
		{"no project", NewTask{Title: "t", Class: config.Ship, Brief: "b", LaunchFolder: launch}, "Project"},
		{"no brief", NewTask{Title: "t", Class: config.Ship, Projects: []string{"p"}, Brief: " ", LaunchFolder: launch}, "Brief"},
		{"relative launch", NewTask{Title: "t", Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: "x"}, "not absolute"},
		{"bad ticket", NewTask{Title: "t", Class: config.Ship, Projects: []string{"p"}, Brief: "b", LaunchFolder: launch, Ticket: "at-1"}, "ticket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Create(tt.n); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("failed creates left Tasks: %+v", list)
	}
	if entries, _ := os.ReadDir(s.home.TasksDir()); len(entries) > 1 {
		t.Fatalf("failed creates left folders: %v", entries)
	}
}

func TestCreateConcurrentUniqueIDs(t *testing.T) {
	s, launch := newStore(t)
	var wg sync.WaitGroup
	ids := make(chan ID, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tk, err := s.Create(NewTask{Title: "same", Class: config.Scout, Projects: []string{"p"}, Brief: "b", LaunchFolder: launch})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- tk.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[ID]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 10 {
		t.Fatalf("got %d ids", len(seen))
	}
}

func TestTransitions(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "flow")
	steps := []struct {
		to      State
		wantErr bool
	}{
		{Landed, true},
		{Running, false},
		{Running, false},
		{NeedsDecision, false},
		{Running, false},
		{WaitingReview, false},
		{Landed, false},
		{Running, true},
		{"bogus", true},
	}
	for _, st := range steps {
		_, err := s.Transition(tk.ID, st.to, "why")
		if (err != nil) != st.wantErr {
			t.Fatalf("-> %s: err %v", st.to, err)
		}
	}
	got, _ := s.Get(tk.ID)
	if got.State != Landed || !got.State.Terminal() {
		t.Fatalf("state %s", got.State)
	}
	evs, _ := s.Events(tk.ID, 0)
	var path []string
	for _, e := range evs {
		if e.Type == EventStateChanged {
			path = append(path, string(e.From)+">"+string(e.To))
		}
	}
	want := "queued>running running>needs-decision needs-decision>running running>waiting-review waiting-review>landed"
	if strings.Join(path, " ") != want {
		t.Fatalf("path %v", path)
	}
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d: %+v", i, e)
		}
	}
}

func TestStateTableConsistent(t *testing.T) {
	for _, i := range States() {
		if i.Terminal && len(i.Next) > 0 {
			t.Errorf("%s is terminal but has next states", i.State)
		}
		if i.Meaning == "" {
			t.Errorf("%s has no meaning", i.State)
		}
		for _, n := range i.Next {
			if _, ok := Info(n); !ok {
				t.Errorf("%s -> unknown %s", i.State, n)
			}
			if n == Queued {
				t.Errorf("%s -> queued", i.State)
			}
		}
	}
}

func TestReportWrite(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "scout it")
	if err := s.WriteReport(tk.ID, "found it"); err != nil {
		t.Fatal(err)
	}
	r, found, err := s.Report(tk.ID)
	if err != nil || !found || r != "found it\n" {
		t.Fatalf("report %q %v %v", r, found, err)
	}
	if err := s.WriteReport("999-none", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("report for missing Task: %v", err)
	}
}

func TestReadPosition(t *testing.T) {
	s, launch := newStore(t)
	a := create(t, s, launch, "a")
	b := create(t, s, launch, "b")
	ev, err := s.Unread()
	if err != nil || len(ev) != 2 {
		t.Fatalf("unread %d %v", len(ev), err)
	}
	if err := s.MarkRead(ev); err != nil {
		t.Fatal(err)
	}
	if ev, _ := s.Unread(); len(ev) != 0 {
		t.Fatalf("still unread: %+v", ev)
	}
	s.Transition(a.ID, Running, "")
	first, _ := s.Unread()
	s.Transition(b.ID, Running, "")
	if err := s.MarkRead(first); err != nil {
		t.Fatal(err)
	}
	rest, _ := s.Unread()
	if len(first) != 1 || first[0].Task != a.ID || len(rest) != 1 || rest[0].Task != b.ID {
		t.Fatalf("an event was lost or repeated: first=%+v rest=%+v", first, rest)
	}
	if err := s.MarkRead(first); err != nil {
		t.Fatal(err)
	}
	rp, _ := s.ReadPosition()
	if rp.Seen[a.ID] != 2 || rp.Seen[b.ID] != 1 {
		t.Fatalf("position went backwards: %+v", rp.Seen)
	}
}

func TestResolveFolder(t *testing.T) {
	launch := t.TempDir()
	os.MkdirAll(filepath.Join(launch, "PROJ-100-login-timeout"), 0o755)
	os.MkdirAll(filepath.Join(launch, "PROJ-200-a"), 0o755)
	os.MkdirAll(filepath.Join(launch, "PROJ-200-b"), 0o755)
	os.MkdirAll(filepath.Join(launch, "PROJ-1000-other"), 0o755)
	work := "/home/tasks/001-x/work"
	tests := []struct {
		name, ticket, folder string
		want, wantErr        string
	}{
		{"no ticket", "", "", work, ""},
		{"existing ticket folder", "PROJ-100", "", filepath.Join(launch, "PROJ-100-login-timeout"), ""},
		{"prefix must end at dash", "AT-10", "", work, ""},
		{"no folder yet", "PROJ-300", "", work, ""},
		{"ambiguous", "PROJ-200", "", "", "several ticket folders"},
		{"explicit", "PROJ-300", "PROJ-300-new", filepath.Join(launch, "PROJ-300-new"), ""},
		{"explicit escapes", "", "../x", "", "inside the Launch folder"},
		{"explicit absolute", "", launch, "", "folder name inside"},
		{"bad ticket", "at-1", "", "", "not like"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFolder(launch, tt.ticket, tt.folder, work)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q %v want %q", got, err, tt.want)
			}
		})
	}
}
