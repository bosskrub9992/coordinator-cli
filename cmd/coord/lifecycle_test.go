package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func (r *workerRig) addRepo(name string) string {
	r.t.Helper()
	root := filepath.Dir(filepath.Dir(r.repo))
	origin := filepath.Join(root, name+".git")
	repo := filepath.Join(root, "launch", name)
	runGit(r.t, root, "init", "-q", "--bare", origin)
	runGit(r.t, root, "init", "-q", "-b", "main", repo)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte(name+"\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 4\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(strings.ToUpper(name)+"-AGENTS-MARKER\n"), 0o644)
	runGit(r.t, repo, "add", ".")
	runGit(r.t, repo, "commit", "-q", "-m", "init")
	addOrigin(r.t, repo, origin)
	runGit(r.t, repo, "push", "-q", "origin", "main")
	if out, err := coord(r.t, "", "project", "add", repo, "--name", name); err != nil {
		r.t.Fatalf("project add %s: %q %v", name, out, err)
	}
	return origin
}

func mrFor(origin string, n int) string {
	host := "localhost"
	repo := strings.TrimSuffix(origin, ".git")
	if vol := filepath.VolumeName(repo); vol != "" {
		host = strings.TrimSuffix(vol, ":")
		repo = strings.TrimPrefix(repo, vol)
	}
	return fmt.Sprintf("https://%s/%s/-/merge_requests/%d", host, strings.TrimPrefix(filepath.ToSlash(repo), "/"), n)
}

func (r *workerRig) setMRs(id task.ID, st mrwatch.State) {
	r.t.Helper()
	if _, err := r.store().UpdateMRs(id, func(mrs *[]task.MR) error {
		for i := range *mrs {
			(*mrs)[i].Watch = mrwatch.Position{State: st, CI: mrwatch.CIPassed, Approved: true}
		}
		return nil
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *workerRig) mrs(id task.ID) []task.MR {
	r.t.Helper()
	mrs, err := r.store().MRs(id)
	if err != nil {
		r.t.Fatal(err)
	}
	return mrs
}

func fakeCodeHosts(t *testing.T, failOn string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake glab and gh are shell scripts")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	for _, name := range []string{"glab", "gh"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n"
		if failOn != "" {
			script += "case \"$*\" in *\" " + failOn + " \"*) echo 'cannot close' >&2; exit 1;; esac\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestMultiProjectShipLandFlow(t *testing.T) {
	r := newWorkerRig(t, true)
	second := r.addRepo("second")
	mr1, mr2, mr3 := mrFor(r.origin, 1), mrFor(second, 2), mrFor(second, 3)
	brief := strings.Join([]string{
		"WRITE {{WT1}}/a.txt a",
		"WRITE {{WT2}}/b.txt b",
		"BASH git -C {{WT1}} add a.txt && git -C {{WT1}} commit -qm a && git -C {{WT1}} push -q origin HEAD",
		"BASH git -C {{WT2}} add b.txt && git -C {{WT2}} commit -qm b && git -C {{WT2}} push -q origin HEAD",
		"REPORT done --mr " + mr1 + " --mr " + mr2 + " --mr " + mr3 + " Pushed both.",
	}, "\n")
	if _, err := coord(t, "x", "task", "new", "--project", "repo", "--project", "repo", "--class", "ship", "--title", "dup", "--brief", "-"); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate --project: %v", err)
	}
	id := r.newTask("ship", "Two repos", brief, "--project", "second")
	out := r.run("spawn", string(id))
	if !strings.Contains(out, "repo: ") || !strings.Contains(out, "second: ") {
		t.Fatalf("spawn output %q", out)
	}
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)

	s := r.store()
	tk, _ := s.Get(id)
	rec, _ := s.Worker(id)
	if strings.Join(tk.Projects, ",") != "repo,second" || len(rec.Worktrees) != 2 || rec.Worktrees[0].Project != "repo" || rec.Worktrees[1].Project != "second" {
		t.Fatalf("Projects %v worktrees %+v", tk.Projects, rec.Worktrees)
	}
	wt1, wt2 := rec.Worktrees[0].Path, rec.Worktrees[1].Path
	for _, f := range []string{filepath.Join(wt1, "a.txt"), filepath.Join(wt2, "b.txt")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("the guard blocked a write into a worktree: %v", err)
		}
	}
	var st struct {
		Argv []string `json:"argv"`
	}
	json.Unmarshal(r.traces("start")[0], &st)
	if argv := strings.Join(st.Argv, " "); !strings.Contains(argv, "--add-dir "+wt1+" "+wt2) {
		t.Errorf("argv lacks both worktrees: %s", argv)
	}
	prompt, _ := os.ReadFile(s.SystemPromptPath(id))
	for _, want := range []string{"Projects repo, second", wt1, wt2, r.repo, filepath.Join(filepath.Dir(r.repo), "second"), "REPO-AGENTS-MARKER", "SECOND-AGENTS-MARKER"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	mrs := r.mrs(id)
	if len(mrs) != 3 || mrs[0].Project != "repo" || mrs[1].Project != "second" || mrs[2].Project != "second" || mrs[0].Source != task.MRFromWorker {
		t.Fatalf("MR records %+v", mrs)
	}
	if ev := r.events(id, task.EventReport); len(ev) != 1 || !strings.Contains(string(ev[0].Data), `"mr_urls":["`+mr1) {
		t.Fatalf("report event %+v", ev)
	}

	show := r.run("show", string(id))
	for _, want := range []string{"Projects:    repo, second", "Worktree:    second: " + wt2, "MR:          " + mr1 + " (repo): not polled yet, 0 comments since ack", "MR:          " + mr3 + " (second)"} {
		if !strings.Contains(show, want) {
			t.Errorf("show lacks %q:\n%s", want, show)
		}
	}
	if status := r.run("status"); !strings.Contains(status, "repo,second") || !strings.Contains(status, "3 open") {
		t.Errorf("status:\n%s", status)
	}
	var v statusView
	json.Unmarshal([]byte(r.run("status", "--json")), &v)
	if len(v.Tasks) != 1 || len(v.Tasks[0].Projects) != 2 || len(v.Tasks[0].MRs) != 3 {
		t.Errorf("status --json %+v", v.Tasks)
	}

	if _, err := coord(t, "", "land", string(id)); err == nil || !strings.Contains(err.Error(), "open MRs") {
		t.Fatalf("land with open MRs: %v", err)
	}
	r.setMRs(id, mrwatch.Merged)
	if _, err := s.Transition(id, task.Merged, "all merged"); err != nil {
		t.Fatal(err)
	}
	if line, _ := statusLine(home.New(r.home), s, "", absPath("/launch")); !strings.Contains(line, "1 merged") || strings.Contains(line, "need") {
		t.Errorf("status line %q", line)
	}
	deploy := "https://gitlab.example.com/acme/platform/deploy-config/-/merge_requests/9"
	out = r.run("task", "add-mr", string(id), deploy)
	if !strings.Contains(out, "(waiting-review) has 4 MRs") || !strings.Contains(out, deploy+": not polled yet") {
		t.Fatalf("add-mr output %q", out)
	}
	if mrs := r.mrs(id); mrs[3].Source != task.MRLinked || mrs[3].Project != "" {
		t.Fatalf("linked MR %+v", mrs[3])
	}
	if _, err := coord(t, "", "land", string(id)); err == nil || !strings.Contains(err.Error(), deploy) {
		t.Fatalf("land with the linked MR open: %v", err)
	}
	r.setMRs(id, mrwatch.Merged)
	if out := r.run("ack", string(id), "--note", "deploy-config merged too"); !strings.Contains(out, "is merged") {
		t.Fatalf("ack %q", out)
	}
	out = r.run("land", string(id))
	if !strings.Contains(out, "is landed") || strings.Contains(out, "kept") {
		t.Fatalf("land output %q", out)
	}
	if ret := r.events(id, task.EventWorktreeReturned); len(ret) != 2 {
		t.Fatalf("returned %+v", ret)
	}
	if r.state(id) != task.Landed {
		t.Fatalf("state %s", r.state(id))
	}
}

func TestAddProjectAndResume(t *testing.T) {
	r := newWorkerRig(t, true)
	r.addRepo("second")
	mr1 := mrFor(r.origin, 1)

	q := r.newTask("ship", "Queued one", "x")
	if out := r.run("task", "add-project", string(q), "second"); !strings.Contains(out, "coord spawn leases") {
		t.Fatalf("queued add-project %q", out)
	}
	if _, err := coord(t, "", "task", "add-project", string(q), "nope"); err == nil || !strings.Contains(err.Error(), "coord project add") {
		t.Fatalf("unknown Project: %v", err)
	}

	id := r.newTask("ship", "Grows", "REPORT done --mr "+mr1+" up")
	r.run("spawn", string(id))
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)
	out := r.run("task", "add-project", string(id), "second")
	if !strings.Contains(out, "Added Project second") || !strings.Contains(out, "coord steer resumes") {
		t.Fatalf("add-project %q", out)
	}
	if _, err := coord(t, "", "task", "add-project", string(id), "second"); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("second add-project: %v", err)
	}
	rec, _ := r.store().Worker(id)
	if len(rec.ActiveWorktrees()) != 2 || rec.Worktrees[1].Project != "second" || rec.Worktrees[1].Branch != "coord/"+string(id) {
		t.Fatalf("worktrees %+v", rec.Worktrees)
	}
	wt2 := rec.Worktrees[1].Path
	r.run("steer", string(id), "WRITE {{WT2}}/c.txt c\nREPORT done --mr "+mr1+" again")
	r.waitEvent(id, task.EventWorkerStarted, 2)
	r.waitEvent(id, task.EventReport, 2)
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)
	var st struct {
		Argv []string `json:"argv"`
	}
	json.Unmarshal(r.traces("start")[1], &st)
	if argv := strings.Join(st.Argv, " "); !strings.Contains(argv, "--resume") || !strings.Contains(argv, wt2) {
		t.Fatalf("resume argv %s", argv)
	}
	if _, err := os.Stat(filepath.Join(wt2, "c.txt")); err != nil {
		t.Fatalf("write into the added worktree: %v", err)
	}
	if mrs := r.mrs(id); len(mrs) != 1 {
		t.Fatalf("re-reported MR duplicated: %+v", mrs)
	}
	r.setMRs(id, mrwatch.Merged)
	if _, err := r.store().Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	out = r.run("land", string(id))
	if !strings.Contains(out, "is landed") || !strings.Contains(out, "kept "+wt2+" (second): it has uncommitted changes") {
		t.Fatalf("land with a dirty worktree: %q", out)
	}
	rec, _ = r.store().Worker(id)
	if !rec.Worktrees[0].Returned() || rec.Worktrees[1].Returned() {
		t.Fatalf("land returned %+v", rec.Worktrees)
	}
	if notes := r.events(id, task.EventNote); len(notes) == 0 || !strings.Contains(notes[len(notes)-1].Text, wt2+" kept") {
		t.Fatalf("no note for the kept worktree: %+v", notes)
	}

	live := r.newTask("scout", "Busy", "SLEEP 30")
	r.run("spawn", string(live))
	r.waitFor("session started", func() bool {
		w, _ := r.store().Worker(live)
		return w.SessionStarted
	})
	if _, err := coord(t, "", "task", "add-project", string(live), "second"); err == nil || !strings.Contains(err.Error(), "coord stop") {
		t.Fatalf("add-project to a live Worker: %v", err)
	}
	r.run("drop", string(q))
	if _, err := coord(t, "", "task", "add-project", string(q), "repo"); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("add-project to a dropped Task: %v", err)
	}
}

func TestDropStopsRecordsAndCloses(t *testing.T) {
	r := newWorkerRig(t, true)
	log := fakeCodeHosts(t, "13")

	busy := r.newTask("ship", "Busy", "WRITE {{WT}}/scratch.txt wip\nBASH git -C {{WT}} commit --allow-empty -qm local-only\nSLEEP 60")
	r.run("spawn", string(busy))
	r.waitFor("the Worker to reach its sleep", func() bool {
		b, _ := os.ReadFile(r.store().WorkerLogPath(busy))
		return strings.Contains(string(b), `"command":"sleep 60"`)
	})
	out := r.run("drop", string(busy))
	if !strings.Contains(out, "is dropped") || strings.Contains(out, "kept") {
		t.Fatalf("drop output %q", out)
	}
	if rec, _ := r.store().Worker(busy); len(rec.ActiveWorktrees()) != 0 || len(r.events(busy, task.EventWorktreeReturned)) != 1 {
		t.Fatalf("dirty worktree not returned on drop: %+v", rec.Worktrees)
	}
	if !r.idle(busy) {
		t.Fatal("the Worker still runs after drop")
	}
	dw := r.events(busy, task.EventDroppedWork)
	if len(dw) != 1 || !strings.Contains(string(dw[0].Data), "?? scratch.txt") || !strings.Contains(string(dw[0].Data), "local-only") || !strings.Contains(dw[0].Text, "1 uncommitted change, 1 commit on no remote") {
		t.Fatalf("dropped-work %+v", dw)
	}
	if r.state(busy) != task.Dropped {
		t.Fatalf("state %s", r.state(busy))
	}
	if _, err := coord(t, "", "drop", string(busy)); err == nil || !strings.Contains(err.Error(), "already dropped") {
		t.Fatalf("second drop: %v", err)
	}

	gl, gh := mrFor(r.origin, 5), "https://github.com/o/r/pull/6"
	shipped := r.newTask("ship", "Shipped", "REPORT done --mr "+gl+" --mr "+gh+" up")
	r.run("spawn", string(shipped))
	r.waitState(shipped, task.WaitingReview)
	r.waitIdle(shipped)
	out = r.run("drop", string(shipped), "--close-mr")
	if !strings.Contains(out, "Closed "+gl) || !strings.Contains(out, "Closed "+gh) || strings.Contains(out, "kept") {
		t.Fatalf("drop --close-mr output %q", out)
	}
	calls, _ := os.ReadFile(log)
	repo := strings.TrimSuffix(strings.TrimPrefix(gl, "https://"), "/-/merge_requests/5")
	if !strings.Contains(string(calls), "glab mr close 5 --repo https://"+repo+"\n") || !strings.Contains(string(calls), "gh pr close 6 --repo github.com/o/r\n") {
		t.Fatalf("code host calls:\n%s", calls)
	}
	for _, m := range r.mrs(shipped) {
		if m.Open() {
			t.Errorf("MR still open after --close-mr: %+v", m)
		}
	}
	if ret := r.events(shipped, task.EventWorktreeReturned); len(ret) != 1 || r.state(shipped) != task.Dropped {
		t.Fatalf("returned %+v state %s", ret, r.state(shipped))
	}

	stuck := r.newTask("ship", "Stuck", "REPORT done --mr "+mrFor(r.origin, 13)+" up")
	r.run("spawn", string(stuck))
	r.waitState(stuck, task.WaitingReview)
	r.waitIdle(stuck)
	if _, err := coord(t, "", "drop", string(stuck), "--close-mr"); err == nil || !strings.Contains(err.Error(), "cannot close") {
		t.Fatalf("drop with a failing close: %v", err)
	}
	rec, _ := r.store().Worker(stuck)
	if r.state(stuck) != task.WaitingReview || len(rec.ActiveWorktrees()) != 1 || len(r.events(stuck, task.EventDroppedWork)) != 0 {
		t.Fatalf("a failed close still dropped: %s %+v", r.state(stuck), rec.Worktrees)
	}
}

func TestUsageLimitBlocksTheTask(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("scout", "Limited", "RATELIMIT rejected")
	r.run("spawn", string(id))
	r.waitState(id, task.Blocked)
	r.waitFor("turn end", func() bool {
		b, _ := os.ReadFile(r.store().WorkerLogPath(id))
		return strings.Contains(string(b), `"type":"result"`)
	})
	var blocked []task.Event
	for _, e := range r.events(id, task.EventStateChanged) {
		if e.To == task.Blocked {
			blocked = append(blocked, e)
		}
	}
	if len(blocked) != 1 || !strings.HasPrefix(blocked[0].Text, "usage limit, resets at ") || len(r.events(id, task.EventRateLimited)) != 0 {
		t.Fatalf("blocked %+v rate-limited %+v", blocked, r.events(id, task.EventRateLimited))
	}
	if r.idle(id) || len(r.traces("start")) != 1 {
		t.Fatal("the Worker exited or restarted on its own")
	}
	r.run("steer", string(id), "REPORT done back")
	r.waitState(id, task.Reported)
	r.waitIdle(id)
}

func TestAckAndAddMRCommands(t *testing.T) {
	f := newFleet(t)
	id := f.task(t, "ship it", task.Running, task.WaitingReview)
	scout, err := f.store.Create(task.NewTask{Title: "look", Class: "scout", Projects: []string{"p"}, Brief: "b", LaunchFolder: absPath("/launch")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coord(t, "", "task", "add-mr", string(scout.ID), "https://github.com/a/b/pull/1"); err == nil || !strings.Contains(err.Error(), "ship Tasks") {
		t.Fatalf("add-mr on a scout: %v", err)
	}
	if _, err := coord(t, "", "task", "add-mr", string(id), "https://github.com/a/b/issues/1"); err == nil {
		t.Fatal("issue URL linked")
	}
	if _, err := coord(t, "", "ack", string(id)); err == nil || !strings.Contains(err.Error(), "no MRs") {
		t.Fatalf("ack without MRs: %v", err)
	}
	out, err := coord(t, "", "task", "add-mr", string(id), "https://github.com/a/b/pull/1", "https://gitlab.example.com/g/p/-/merge_requests/2")
	if err != nil || !strings.Contains(out, "has 2 MRs") {
		t.Fatalf("add-mr %q %v", out, err)
	}
	if len(eventsOfType(t, f.store, id, task.EventMRLinked)) != 1 {
		t.Fatal("no mr-linked event")
	}
	f.store.UpdateMRs(id, func(m *[]task.MR) error {
		for i := range *m {
			(*m)[i].Watch = mrwatch.Position{State: mrwatch.Open}
			(*m)[i].CommentsSinceAck = 4
		}
		return nil
	})
	if _, err := f.store.UpdateMRFile(id, func(mf *task.MRFile) error {
		mf.AddAsk(task.Ask{Kind: "mr-comments", Text: "4 new comments on https://github.com/a/b/pull/1 from alice"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	if out, err := coord(t, "", "show", string(id), "--question"); err != nil || !strings.Contains(out, "4 new comments") {
		t.Fatalf("watcher question %q %v", out, err)
	}
	out, err = coord(t, "", "ack", string(id), "--note", "listed for the Captain")
	if err != nil || !strings.Contains(out, "is waiting-review") {
		t.Fatalf("ack %q %v", out, err)
	}
	if acks := eventsOfType(t, f.store, id, task.EventAck); len(acks) != 1 || acks[0].Text != "listed for the Captain" {
		t.Fatalf("ack events %+v", acks)
	}
	show, _ := coord(t, "", "show", string(id))
	if !strings.Contains(show, "https://github.com/a/b/pull/1: open, CI none, not approved, 0 comments since ack") {
		t.Fatalf("show after ack:\n%s", show)
	}
	f.store.UpdateMRs(id, func(m *[]task.MR) error {
		for i := range *m {
			(*m)[i].Watch.State = mrwatch.Merged
		}
		return nil
	})
	f.store.Transition(id, task.Merged, "")
	out, err = coord(t, "", "task", "add-mr", string(id), "https://github.com/a/b/pull/1")
	if err != nil || !strings.Contains(out, "(merged) has 2 MRs") {
		t.Fatalf("re-linking a merged MR: %q %v", out, err)
	}
	out, err = coord(t, "", "task", "add-mr", string(id), "https://github.com/a/b/pull/7")
	if err != nil || !strings.Contains(out, "(waiting-review) has 3 MRs") {
		t.Fatalf("linking an open MR to a merged Task: %q %v", out, err)
	}

	w := f.task(t, "asks", task.Running)
	f.store.AddMR(w, task.MRFromWorker, mustRef(t, "https://github.com/a/b/pull/9"))
	f.store.SetQuestion(w, task.QuestionFromWorker, "A or B?")
	f.store.Transition(w, task.NeedsDecision, "")
	if _, err := coord(t, "", "ack", string(w)); err == nil || !strings.Contains(err.Error(), "coord steer") {
		t.Fatalf("ack of a Worker question: %v", err)
	}
	f.store.Transition(w, task.Dropped, "")
	if _, err := coord(t, "", "task", "add-mr", string(w), "https://github.com/a/b/pull/10"); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("add-mr on a dropped Task: %v", err)
	}
}

func mustRef(t *testing.T, url string) mrwatch.Ref {
	t.Helper()
	r, err := mrwatch.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func eventsOfType(t *testing.T, s *task.Store, id task.ID, typ task.EventType) []task.Event {
	t.Helper()
	evs, err := s.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []task.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestSteerOnWatcherFactsAcknowledgesThem(t *testing.T) {
	r := newWorkerRig(t, true)
	mr1 := mrFor(r.origin, 1)
	id := r.newTask("ship", "Review fixes", "REPORT done --mr "+mr1+" up")
	r.run("spawn", string(id))
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)
	s := r.store()
	if _, err := s.UpdateMRFile(id, func(f *task.MRFile) error {
		f.AddAsk(task.Ask{URL: mr1, Kind: "mr-comments", Text: "2 new comments on " + mr1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	if r.state(id) != task.NeedsDecision {
		t.Fatalf("state %s", r.state(id))
	}
	r.run("steer", string(id), "REPORT done --mr "+mr1+" fixed")
	r.waitEvent(id, task.EventReport, 2)
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)
	if f, err := s.ReadMRFile(id); err != nil || len(f.Asks) != 0 {
		t.Fatalf("asks after the steer %+v %v", f.Asks, err)
	}
}

func TestLandFollowsTheStateTable(t *testing.T) {
	f := newFleet(t)
	captainTTY(t, true)
	scout, err := f.store.Create(task.NewTask{Title: "look", Class: config.Scout, Projects: []string{"p"}, Brief: "b", LaunchFolder: absPath("/launch")})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []task.State{task.Running, task.Failed} {
		if _, err := f.store.Transition(scout.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := coord(t, "", "land", string(scout.ID)); err == nil || !strings.Contains(err.Error(), "cannot land from here") || !strings.Contains(err.Error(), "`coord steer "+string(scout.ID)+" <message>`") {
		t.Fatalf("land a failed scout: %v", err)
	}
	for _, st := range []task.State{task.Running, task.Reported} {
		if _, err := f.store.Transition(scout.ID, st, ""); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := coord(t, "", "status"); err != nil || !strings.Contains(out, "NEXT") || !strings.Contains(out, "land, steer, drop") {
		t.Fatalf("status: %q %v", out, err)
	}
	if out, err := coord(t, "", "land", string(scout.ID)); err != nil || !strings.Contains(out, "is landed") {
		t.Fatalf("land a reported scout: %q %v", out, err)
	}
	ship := f.task(t, "ship", task.Running, task.Failed)
	if _, err := coord(t, "", "land", string(ship)); err == nil || !strings.Contains(err.Error(), "cannot land from here") {
		t.Fatalf("land a failed ship Task: %v", err)
	}
}
