package task

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/project"
)

func ref(t *testing.T, url string) mrwatch.Ref {
	t.Helper()
	r, err := mrwatch.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRepoKey(t *testing.T) {
	want := "gitlab.example.com/acme/backend/api"
	for _, remote := range []string{
		"git@gitlab.example.com:acme/backend/api.git",
		"git@gitlab.example.com:acme/backend/api",
		"ssh://git@gitlab.example.com:2222/acme/backend/api.git",
		"ssh://git@gitlab.example.com/acme/backend/api",
		"https://gitlab.example.com/acme/backend/api.git",
		"https://oauth2:tok@gitlab.example.com/acme/backend/api",
		"https://GITLAB.EXAMPLE.COM/Acme/Backend/Api.git/",
	} {
		got, err := RepoKey(remote)
		if err != nil || got != want {
			t.Errorf("RepoKey(%q) = %q %v", remote, got, err)
		}
	}
	if got := ref(t, "https://gitlab.example.com/Acme/backend/api/-/merge_requests/9"); strings.ToLower(got.RepoKey()) != want {
		t.Errorf("ref key %q", got.RepoKey())
	}
	if _, err := RepoKey("not a remote"); err == nil {
		t.Error("garbage remote accepted")
	}
}

func TestMatchProject(t *testing.T) {
	ps := []project.Project{
		{Name: "api", Origin: "git@gitlab.example.com:acme/backend/api.git"},
		{Name: "api-2", Origin: "https://gitlab.example.com/acme/backend/api"},
		{Name: "cli", Origin: "git@github.com:cli/cli.git"},
	}
	tests := []struct {
		url    string
		prefer []string
		want   string
	}{
		{"https://gitlab.example.com/acme/backend/api/-/merge_requests/1", nil, "api"},
		{"https://gitlab.example.com/acme/backend/api/-/merge_requests/1", []string{"x", "api-2"}, "api-2"},
		{"https://github.com/CLI/cli/pull/7", nil, "cli"},
		{"https://gitlab.example.com/acme/platform/deploy-config/-/merge_requests/3", nil, ""},
	}
	for _, tt := range tests {
		if got := MatchProject(ref(t, tt.url), ps, tt.prefer); got != tt.want {
			t.Errorf("MatchProject(%s, %v) = %q want %q", tt.url, tt.prefer, got, tt.want)
		}
	}
}

func TestMRProgress(t *testing.T) {
	polled := func(st mrwatch.State) MR { return MR{Watch: mrwatch.Position{State: st}} }
	tests := []struct {
		name               string
		mrs                []MR
		anyOpen, allMerged bool
	}{
		{"none", nil, false, false},
		{"never polled is open", []MR{{}}, true, false},
		{"open", []MR{polled(mrwatch.Open)}, true, false},
		{"all merged", []MR{polled(mrwatch.Merged), polled(mrwatch.Merged)}, false, true},
		{"merged and unpolled", []MR{polled(mrwatch.Merged), {}}, true, false},
		{"merged and closed", []MR{polled(mrwatch.Merged), polled(mrwatch.Closed)}, false, false},
	}
	for _, tt := range tests {
		open, merged := MRProgress(tt.mrs)
		if open != tt.anyOpen || merged != tt.allMerged {
			t.Errorf("%s: open %v merged %v", tt.name, open, merged)
		}
	}
}

func registerProject(t *testing.T, s *Store, name, origin string) {
	t.Helper()
	if err := project.NewRegistry(s.home).Add(project.Project{Name: name, Path: "/repos/" + name, Origin: origin, CodeHost: project.CodeHost{Kind: project.GitLab}}); err != nil {
		t.Fatal(err)
	}
}

func TestAddMRDedupesAndKeepsWatch(t *testing.T) {
	s, launch := newStore(t)
	registerProject(t, s, "api", "git@gitlab.example.com:acme/backend/api.git")
	tk := create(t, s, launch, "mrs")
	a := ref(t, "https://gitlab.example.com/acme/backend/api/-/merge_requests/1")
	b := ref(t, "https://gitlab.example.com/acme/backend/api/-/merge_requests/2")
	c := ref(t, "https://gitlab.example.com/acme/platform/deploy-config/-/merge_requests/5")
	if mrs, err := s.MRs(tk.ID); err != nil || len(mrs) != 0 {
		t.Fatalf("fresh Task MRs %v %v", mrs, err)
	}
	if _, err := os.Stat(s.MRsPath(tk.ID)); !os.IsNotExist(err) {
		t.Fatalf("reading MRs wrote mrs.json: %v", err)
	}
	mrs, err := s.AddMR(tk.ID, MRFromWorker, a, b)
	if err != nil || len(mrs) != 2 || mrs[0].Project != "api" || mrs[1].Source != MRFromWorker || mrs[0].AddedAt.IsZero() {
		t.Fatalf("add %+v %v", mrs, err)
	}
	if _, err := s.UpdateMRs(tk.ID, func(m *[]MR) error {
		(*m)[0].Watch.State = mrwatch.Merged
		(*m)[0].CommentsSinceAck = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again := ref(t, "https://GITLAB.example.com/Acme/backend/api/-/merge_requests/1/")
	mrs, err = s.AddMR(tk.ID, MRLinked, again, c)
	if err != nil || len(mrs) != 3 {
		t.Fatalf("re-add %+v %v", mrs, err)
	}
	if mrs[0].Watch.State != mrwatch.Merged || mrs[0].Source != MRFromWorker || mrs[0].CommentsSinceAck != 3 {
		t.Fatalf("re-adding lost the watch position: %+v", mrs[0])
	}
	if mrs[2].Project != "" || mrs[2].Source != MRLinked {
		t.Fatalf("linked MR %+v", mrs[2])
	}
	got, err := s.MRs(tk.ID)
	if err != nil || len(got) != 3 || got[0].Watch.State != mrwatch.Merged {
		t.Fatalf("read back %+v %v", got, err)
	}
	if _, err := s.AddMR("999-none", MRLinked, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown Task: %v", err)
	}
}

func TestMRsMigrateFromReportEvents(t *testing.T) {
	s, launch := newStore(t)
	registerProject(t, s, "api", "https://gitlab.example.com/acme/backend/api.git")
	tk := create(t, s, launch, "legacy")
	urls := []string{"https://gitlab.example.com/acme/backend/api/-/merge_requests/1", "https://github.com/a/b/pull/2"}
	data, _ := json.Marshal(map[string]any{"status": "done", "mr_urls": urls})
	s.Append(tk.ID, Event{Type: EventReport, Text: "done: x", Data: data})
	data, _ = json.Marshal(map[string]any{"status": "done", "mr_urls": []string{urls[0], "not a url", "https://github.com/a/b/pull/3"}})
	s.Append(tk.ID, Event{Type: EventReport, Text: "done: y", Data: data})
	mrs, err := s.MRs(tk.ID)
	if err != nil || len(mrs) != 3 {
		t.Fatalf("migrated %+v %v", mrs, err)
	}
	if mrs[0].Project != "api" || mrs[0].Source != MRFromWorker || mrs[1].Ref.Kind != mrwatch.GitHub || mrs[2].Ref.Number != 3 {
		t.Fatalf("migrated %+v", mrs)
	}
	if _, err := os.Stat(s.MRsPath(tk.ID)); err != nil {
		t.Fatalf("migration not written: %v", err)
	}
	s.Append(tk.ID, Event{Type: EventReport, Data: []byte(`{"mr_urls":["https://github.com/a/b/pull/4"]}`)})
	if mrs, _ := s.MRs(tk.ID); len(mrs) != 3 {
		t.Fatalf("events read again after migration: %+v", mrs)
	}
}

func TestLegacyProjectField(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "old")
	raw, _ := os.ReadFile(s.StatePath(tk.ID))
	var m map[string]any
	json.Unmarshal(raw, &m)
	delete(m, "projects")
	m["project"] = "api"
	if err := home.WriteJSONAtomic(s.StatePath(tk.ID), m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(tk.ID)
	if err != nil || len(got.Projects) != 1 || got.Projects[0] != "api" || got.PrimaryProject() != "api" {
		t.Fatalf("legacy read %+v %v", got.Projects, err)
	}
	if _, err := s.Transition(tk.ID, Running, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.StatePath(tk.ID))
	if !strings.Contains(string(raw), `"projects": [`) || strings.Contains(string(raw), `"project":`) {
		t.Fatalf("not written in the new shape:\n%s", raw)
	}
}

func TestCreateProjects(t *testing.T) {
	s, launch := newStore(t)
	tk, err := s.Create(NewTask{Title: "two", Class: config.Ship, Projects: []string{"proto", "api"}, Brief: "b", LaunchFolder: launch})
	if err != nil || tk.PrimaryProject() != "proto" || len(tk.Projects) != 2 {
		t.Fatalf("create %+v %v", tk.Projects, err)
	}
	for _, ps := range [][]string{nil, {"a", "a"}, {"a", " "}} {
		if _, err := s.Create(NewTask{Title: "bad", Class: config.Ship, Projects: ps, Brief: "b", LaunchFolder: launch}); err == nil {
			t.Errorf("Projects %q accepted", ps)
		}
	}
}

func TestAddProject(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "grow")
	got, err := s.AddProject(tk.ID, "proto")
	if err != nil || strings.Join(got.Projects, ",") != "api,proto" {
		t.Fatalf("add %v %v", got.Projects, err)
	}
	if _, err := s.AddProject(tk.ID, "proto"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("queued duplicate: %v", err)
	}
	s.Transition(tk.ID, Running, "")
	s.Transition(tk.ID, WaitingReview, "")
	if _, err := s.AddProject(tk.ID, "proto"); err != nil {
		t.Fatalf("retry for a Project without a worktree: %v", err)
	}
	s.UpdateWorker(tk.ID, func(w *WorkerRecord) error {
		w.Worktrees = append(w.Worktrees, Worktree{Project: "proto", Path: "/pool/p"})
		return nil
	})
	if _, err := s.AddProject(tk.ID, "proto"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("duplicate with worktree: %v", err)
	}
	s.UpdateWorker(tk.ID, func(w *WorkerRecord) error {
		w.SupervisorPID = os.Getpid()
		w.SupervisorStart = home.ProcessStart(os.Getpid())
		return nil
	})
	if _, err := s.AddProject(tk.ID, "flags"); !errors.Is(err, ErrWorkerLive) || !strings.Contains(err.Error(), "coord stop") {
		t.Fatalf("live Worker: %v", err)
	}
	s.UpdateWorker(tk.ID, func(w *WorkerRecord) error {
		w.SupervisorPID = 0
		return nil
	})
	s.Transition(tk.ID, Dropped, "")
	if _, err := s.AddProject(tk.ID, "flags"); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("terminal: %v", err)
	}

	q := create(t, s, launch, "spawning")
	rel, err := s.ClaimSpawn(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProject(q.ID, "proto"); !errors.Is(err, ErrSpawning) {
		t.Fatalf("during spawn: %v", err)
	}
	rel()
	if _, err := s.AddProject(q.ID, "proto"); err != nil {
		t.Fatal(err)
	}
}

func waitingWithMR(t *testing.T, s *Store, launch, title string) Task {
	t.Helper()
	tk := create(t, s, launch, title)
	s.Transition(tk.ID, Running, "")
	s.Transition(tk.ID, WaitingReview, "")
	if _, err := s.AddMR(tk.ID, MRFromWorker, ref(t, "https://gitlab.example.com/g/p/-/merge_requests/1")); err != nil {
		t.Fatal(err)
	}
	return tk
}

func ask(t *testing.T, s *Store, id ID, text string) Task {
	t.Helper()
	if _, err := s.UpdateMRFile(id, func(f *MRFile) error {
		f.AddAsk(Ask{Kind: "mr-comments", Text: text})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settle(id, "")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func setMR(t *testing.T, s *Store, id ID, i int, st mrwatch.State) {
	t.Helper()
	if _, err := s.UpdateMRs(id, func(m *[]MR) error {
		(*m)[i].Watch.State = st
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSettle(t *testing.T) {
	s, launch := newStore(t)
	tk := waitingWithMR(t, s, launch, "raise")
	if got := ask(t, s, tk.ID, "CI failed on x\nmore"); got.State != NeedsDecision || got.QuestionFrom != QuestionFromWatcher {
		t.Fatalf("raise %+v", got)
	}
	ask(t, s, tk.ID, "2 new comments")
	q, _ := os.ReadFile(s.QuestionPath(tk.ID))
	if string(q) != "CI failed on x\nmore\n\n2 new comments\n" {
		t.Fatalf("question %q", q)
	}
	evs, _ := s.Events(tk.ID, 0)
	last := evs[len(evs)-1]
	if last.Type != EventStateChanged || last.To != NeedsDecision || last.Text != "CI failed on x" {
		t.Fatalf("events %+v", evs)
	}

	w := waitingWithMR(t, s, launch, "worker asks")
	s.Transition(w.ID, Running, "")
	s.SetQuestion(w.ID, QuestionFromWorker, "A or B?")
	s.Transition(w.ID, NeedsDecision, "")
	if got := ask(t, s, w.ID, "merged"); got.QuestionFrom != QuestionFromWorker {
		t.Fatalf("raised over a Worker question: %+v", got)
	}
	if q, _ := os.ReadFile(s.QuestionPath(w.ID)); string(q) != "A or B?\n" {
		t.Fatalf("Worker question overwritten: %q", q)
	}

	r := waitingWithMR(t, s, launch, "running")
	s.Transition(r.ID, Running, "")
	if got := ask(t, s, r.ID, "1 new comment on x from bob"); got.State != Running {
		t.Fatalf("raised on a running Task: %s", got.State)
	}
	s.Transition(r.ID, WaitingReview, "")
	if got, _ := s.Settle(r.ID, ""); got.State != NeedsDecision || !strings.Contains(string(must(os.ReadFile(s.QuestionPath(r.ID)))), "from bob") {
		t.Fatalf("an ask made while running was lost: %s", got.State)
	}

	m := waitingWithMR(t, s, launch, "merged")
	s.Transition(m.ID, Merged, "")
	if got := ask(t, s, m.ID, "closed"); got.State != NeedsDecision {
		t.Fatalf("merged raise %s", got.State)
	}
}

func must(b []byte, _ error) []byte { return b }

func TestSettleClosedAndMerged(t *testing.T) {
	s, launch := newStore(t)
	tk := waitingWithMR(t, s, launch, "two")
	if _, err := s.AddMR(tk.ID, MRFromWorker, ref(t, "https://gitlab.example.com/g/q/-/merge_requests/2")); err != nil {
		t.Fatal(err)
	}
	setMR(t, s, tk.ID, 0, mrwatch.Closed)
	ask(t, s, tk.ID, "closed without merging: 1")
	if got, err := s.Ack(tk.ID, ""); err != nil || got.State != WaitingReview {
		t.Fatalf("ack with one open: %s %v", got.State, err)
	}
	setMR(t, s, tk.ID, 1, mrwatch.Merged)
	if got, _ := s.Settle(tk.ID, ""); got.State != Merged {
		t.Fatalf("one closed, one merged: %s", got.State)
	}

	c := waitingWithMR(t, s, launch, "closed only")
	setMR(t, s, c.ID, 0, mrwatch.Closed)
	ask(t, s, c.ID, "closed without merging")
	if got, err := s.Ack(c.ID, ""); err != nil || got.State != Reported {
		t.Fatalf("all closed after ack: %s %v", got.State, err)
	}
	if _, err := s.AddMR(c.ID, MRLinked, ref(t, "https://gitlab.example.com/g/p/-/merge_requests/3")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settle(c.ID, "linked an MR"); got.State != WaitingReview {
		t.Fatalf("reported Task with a new open MR: %s", got.State)
	}
	setMR(t, s, c.ID, 1, mrwatch.Merged)
	s.Settle(c.ID, "")
	if _, err := s.Transition(c.ID, Landed, ""); err != nil {
		t.Fatalf("land after merge: %v", err)
	}
}

func TestAck(t *testing.T) {
	s, launch := newStore(t)
	tk := waitingWithMR(t, s, launch, "ack")
	s.UpdateMRs(tk.ID, func(m *[]MR) error {
		(*m)[0].CommentsSinceAck = 2
		return nil
	})
	ask(t, s, tk.ID, "2 new comments")
	got, err := s.Ack(tk.ID, "fixed by resuming the Worker")
	if err != nil || got.State != WaitingReview || got.QuestionFrom != "" {
		t.Fatalf("ack %+v %v", got, err)
	}
	if _, err := os.Stat(s.QuestionPath(tk.ID)); !os.IsNotExist(err) {
		t.Fatalf("question kept: %v", err)
	}
	if mrs, _ := s.MRs(tk.ID); mrs[0].CommentsSinceAck != 0 {
		t.Fatalf("comments not reset: %+v", mrs)
	}
	evs, _ := s.Events(tk.ID, 0)
	n := len(evs)
	if evs[n-2].Type != EventAck || evs[n-2].Text != "fixed by resuming the Worker" || evs[n-1].To != WaitingReview {
		t.Fatalf("events %+v", evs[n-2:])
	}
	s.UpdateMRs(tk.ID, func(m *[]MR) error {
		(*m)[0].Watch.State = mrwatch.Merged
		return nil
	})
	ask(t, s, tk.ID, "approved, ready")
	if got, err := s.Ack(tk.ID, ""); err != nil || got.State != Merged {
		t.Fatalf("ack with all merged: %s %v", got.State, err)
	}
	if got, err := s.Ack(tk.ID, ""); err != nil || got.State != Merged {
		t.Fatalf("ack of a merged Task: %s %v", got.State, err)
	}
	if evs, _ := s.Events(tk.ID, 0); evs[len(evs)-1].Text != "MR facts handled" {
		t.Fatalf("default note %+v", evs[len(evs)-1])
	}

	none := create(t, s, launch, "no mrs")
	s.Transition(none.ID, Running, "")
	s.Transition(none.ID, WaitingReview, "")
	if _, err := s.Ack(none.ID, ""); err == nil || !strings.Contains(err.Error(), "no MRs") {
		t.Fatalf("no MRs: %v", err)
	}
	wq := waitingWithMR(t, s, launch, "worker question")
	s.Transition(wq.ID, Running, "")
	s.SetQuestion(wq.ID, QuestionFromWorker, "which?")
	s.Transition(wq.ID, NeedsDecision, "")
	if _, err := s.Ack(wq.ID, ""); err == nil || !strings.Contains(err.Error(), "Worker's own") {
		t.Fatalf("Worker question: %v", err)
	}
	s.Transition(wq.ID, Running, "")
	if _, err := s.Ack(wq.ID, ""); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("running: %v", err)
	}
	live := waitingWithMR(t, s, launch, "live")
	s.UpdateWorker(live.ID, func(w *WorkerRecord) error {
		w.SupervisorPID = os.Getpid()
		w.SupervisorStart = home.ProcessStart(os.Getpid())
		return nil
	})
	if _, err := s.Ack(live.ID, ""); !errors.Is(err, ErrWorkerLive) {
		t.Fatalf("live Worker: %v", err)
	}
}

func TestMRStateTransitions(t *testing.T) {
	allowed := [][2]State{
		{Running, Merged}, {WaitingReview, Merged}, {NeedsDecision, Merged}, {NeedsDecision, WaitingReview}, {NeedsDecision, Landed},
		{Merged, Running}, {Merged, NeedsDecision}, {Merged, WaitingReview}, {Merged, Landed}, {Merged, Dropped}, {Merged, Failed},
		{Reported, WaitingReview}, {Reported, Merged}, {Reported, Landed}, {WaitingReview, Reported},
	}
	for _, p := range allowed {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s refused", p[0], p[1])
		}
	}
	for _, p := range [][2]State{{Merged, Blocked}, {Merged, Reported}, {Queued, Merged}, {Blocked, Merged}, {Queued, Landed}} {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s allowed", p[0], p[1])
		}
	}
	if Merged.Terminal() || !Merged.Supervised() || Merged.holdsSupervisor() {
		t.Error("merged is a live, unsupervised-by-a-Worker state")
	}
	s, launch := newStore(t)
	tk := create(t, s, launch, "flow")
	for _, to := range []State{Running, WaitingReview, Merged, Running, Merged, NeedsDecision, Merged, Landed} {
		if _, err := s.Transition(tk.ID, to, ""); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
}
