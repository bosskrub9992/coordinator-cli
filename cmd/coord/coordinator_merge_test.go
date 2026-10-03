package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type stateClient map[string]mrwatch.State

func (c stateClient) Fetch(_ context.Context, r mrwatch.Ref, _ mrwatch.NoteCursor) (mrwatch.Snapshot, error) {
	return mrwatch.Snapshot{State: c[r.URL]}, nil
}

func fakeGHScript(t *testing.T, dir, log, repoJSON, failOn string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		script := "@echo off\r\n>>\"" + log + "\" echo gh %*\r\n" +
			"if \"%1\"==\"repo\" (\r\n  echo " + repoJSON + "\r\n  exit /b 0\r\n)\r\n"
		if failOn != "" {
			script += "echo %*| findstr /C:\" " + failOn + " \" >nul && (\r\n  echo Pull request is not mergeable 1>&2\r\n  exit /b 1\r\n)\r\n"
		}
		script += "exit /b 0\r\n"
		p := filepath.Join(dir, "gh.cmd")
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	script := "#!/bin/sh\necho \"gh $*\" >> " + log + "\n" +
		"case \"$1\" in repo) echo '" + repoJSON + "'; exit 0;; esac\n"
	if failOn != "" {
		script += "case \"$*\" in *\" " + failOn + " \"*) echo 'Pull request is not mergeable' >&2; exit 1;; esac\n"
	}
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readCalls(log string) string {
	b, _ := os.ReadFile(log)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func fakeMergeHosts(t *testing.T, repoJSON, failOn string, clients stateClient) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	gh := fakeGHScript(t, dir, log, repoJSON, failOn)
	oldMerger, oldClients := mrMerger, mrClients
	mrMerger = mrwatch.Merger{GH: gh}
	mrClients = func(mrwatch.Kind) (mrwatch.Client, error) { return clients, nil }
	t.Cleanup(func() { mrMerger, mrClients = oldMerger, oldClients })
	return log
}

const squashOnly = `{"mergeCommitAllowed":false,"squashMergeAllowed":true,"rebaseMergeAllowed":false}`

func shipWithMRs(t *testing.T, f fleet, title string, urls ...string) task.ID {
	t.Helper()
	id := f.task(t, title, task.Running, task.WaitingReview)
	for _, u := range urls {
		if _, err := f.store.AddMR(id, task.MRFromWorker, mustRef(t, u)); err != nil {
			t.Fatal(err)
		}
	}
	f.store.UpdateMRs(id, func(m *[]task.MR) error {
		for i := range *m {
			(*m)[i].Watch = mrwatch.Position{Polled: true, State: mrwatch.Open}
		}
		return nil
	})
	return id
}

func TestMergeOrdersSettlesAndClearsTheWatcherQuestion(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u1, u2 := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2"
	log := fakeMergeHosts(t, squashOnly, "", stateClient{u1: mrwatch.Merged, u2: mrwatch.Merged})
	id := shipWithMRs(t, f, "ship", u1, u2)
	if _, err := f.store.UpdateMRFile(id, func(mf *task.MRFile) error {
		mf.AddAsk(task.Ask{URL: u1, Kind: "mr-ready", Text: u1 + " is approved with green CI"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	if tk, _ := f.store.Get(id); tk.State != task.NeedsDecision || tk.QuestionFrom != task.QuestionFromWatcher {
		t.Fatalf("setup: %s from %q", tk.State, tk.QuestionFrom)
	}

	out, err := coord(t, "", "merge", string(id))
	if err != nil {
		t.Fatalf("merge: %q %v", out, err)
	}
	if !strings.Contains(out, "Merged "+u1+"\nMerged "+u2+"\n") || !strings.Contains(out, "is merged.") {
		t.Fatalf("output %q", out)
	}
	calls := readCalls(log)
	want := "gh repo view github.com/a/b --json mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed\n" +
		"gh pr merge 1 --repo github.com/a/b --squash\n" +
		"gh pr merge 2 --repo github.com/a/b --squash\n"
	if calls != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", calls, want)
	}
	tk, _ := f.store.Get(id)
	if tk.State != task.Merged || tk.QuestionFrom != "" {
		t.Fatalf("Task is %s with question from %q", tk.State, tk.QuestionFrom)
	}
	mf, _ := f.store.ReadMRFile(id)
	if len(mf.Asks) != 0 || !mf.MRs[0].Merged() || !mf.MRs[1].Merged() {
		t.Fatalf("MR file %+v", mf)
	}
	if _, err := os.Stat(f.store.QuestionPath(id)); !os.IsNotExist(err) {
		t.Fatalf("question file remains: %v", err)
	}
	var notes []string
	for _, e := range eventsOfType(t, f.store, id, task.EventMerged) {
		notes = append(notes, e.Text)
	}
	if strings.Join(notes, "|") != "merged "+u1+" (coord merge, squash)|merged "+u2+" (coord merge, squash)" {
		t.Fatalf("notes %q", notes)
	}
	if _, err := coord(t, "", "merge", string(id)); err == nil || !strings.Contains(err.Error(), "waiting on its MRs") {
		t.Fatalf("merge with nothing open: %v", err)
	}
}

func TestMergeStopsAtTheFirstFailure(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u1, u2, u3 := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2", "https://github.com/a/b/pull/3"
	log := fakeMergeHosts(t, squashOnly, "2", stateClient{u1: mrwatch.Merged, u2: mrwatch.Merged, u3: mrwatch.Merged})
	id := shipWithMRs(t, f, "ship", u1, u2, u3)
	_, err := coord(t, "", "merge", string(id))
	if err == nil || !strings.Contains(err.Error(), "not mergeable") || !strings.Contains(err.Error(), "done before it: "+u1) || !strings.Contains(err.Error(), "left untouched: "+u3) {
		t.Fatalf("merge: %v", err)
	}
	if calls := readCalls(log); strings.Contains(calls, "pr merge 3") {
		t.Fatalf("a later MR was attempted:\n%s", calls)
	}
	mrs, _ := f.store.MRs(id)
	if !mrs[0].Merged() || !mrs[1].Open() || !mrs[2].Open() {
		t.Fatalf("MR states %v %v %v", mrs[0].Watch.State, mrs[1].Watch.State, mrs[2].Watch.State)
	}
	if tk, _ := f.store.Get(id); tk.State != task.WaitingReview {
		t.Fatalf("Task is %s", tk.State)
	}
}

func TestMergeSelectsWithMRAndHonoursMethod(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u1, u2, u3 := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2", "https://github.com/a/b/pull/3"
	both := `{"mergeCommitAllowed":true,"squashMergeAllowed":true,"rebaseMergeAllowed":false}`
	log := fakeMergeHosts(t, both, "", stateClient{u1: mrwatch.Merged, u2: mrwatch.Open, u3: mrwatch.Merged})
	id := shipWithMRs(t, f, "ship", u1, u2, u3)
	if _, err := coord(t, "", "merge", string(id)); err == nil || !strings.Contains(err.Error(), "--method") || !strings.Contains(err.Error(), "merge, squash") {
		t.Fatalf("ambiguous method: %v", err)
	}
	if calls := readCalls(log); strings.Contains(calls, "pr merge") {
		t.Fatalf("merged before the method was settled:\n%s", calls)
	}
	if _, err := coord(t, "", "merge", string(id), "--mr", "https://github.com/a/b/pull/9"); err == nil || !strings.Contains(err.Error(), "coord task add-mr") {
		t.Fatalf("unlinked --mr: %v", err)
	}
	out, err := coord(t, "", "merge", string(id), "--mr", u3, "--mr", u1, "--method", "rebase")
	if err != nil {
		t.Fatalf("merge: %q %v", out, err)
	}
	calls := readCalls(log)
	if !strings.HasSuffix(calls, "gh pr merge 3 --repo github.com/a/b --rebase\ngh pr merge 1 --repo github.com/a/b --rebase\n") {
		t.Fatalf("calls:\n%s", calls)
	}
	if !strings.Contains(out, "Merged "+u3) || !strings.Contains(out, "is waiting-review.") {
		t.Fatalf("output %q", out)
	}
	mrs, _ := f.store.MRs(id)
	if !mrs[2].Merged() || !mrs[0].Merged() || !mrs[1].Open() {
		t.Fatalf("MR states %v %v %v", mrs[0].Watch.State, mrs[1].Watch.State, mrs[2].Watch.State)
	}
}

func TestMergeStopsWhenAMergeIsNotConfirmed(t *testing.T) {
	for _, unreadable := range []bool{false, true} {
		name := "still open"
		if unreadable {
			name = "unreadable"
		}
		t.Run(name, func(t *testing.T) {
			stubWatcher(t)
			f := newFleet(t)
			u1, u2, u3 := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2", "https://github.com/a/b/pull/3"
			log := fakeMergeHosts(t, squashOnly, "", stateClient{u1: mrwatch.Merged, u2: mrwatch.Open, u3: mrwatch.Merged})
			if unreadable {
				mrClients = func(mrwatch.Kind) (mrwatch.Client, error) { return nil, errors.New("no token") }
			}
			id := shipWithMRs(t, f, "ship", u2, u1, u3)
			out, err := coord(t, "", "merge", string(id))
			if err == nil || !strings.Contains(err.Error(), "coord merge "+string(id)) || !strings.Contains(err.Error(), "left untouched: "+u1+", "+u3) {
				t.Fatalf("merge: %q %v", out, err)
			}
			if !strings.Contains(out, "Merge of "+u2+" was requested and is pending") {
				t.Fatalf("output %q", out)
			}
			if calls := readCalls(log); strings.Contains(calls, "pr merge 1") || strings.Contains(calls, "pr merge 3") || !strings.Contains(calls, "pr merge 2") {
				t.Fatalf("calls:\n%s", calls)
			}
			mrs, _ := f.store.MRs(id)
			if !mrs[0].Open() || !mrs[1].Open() || !mrs[2].Open() {
				t.Fatalf("MR states %v %v %v", mrs[0].Watch.State, mrs[1].Watch.State, mrs[2].Watch.State)
			}
			if tk, _ := f.store.Get(id); tk.State != task.WaitingReview {
				t.Fatalf("Task is %s", tk.State)
			}
		})
	}
}

func TestMergeLeavesLinkedMRsUnlessAsked(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	w, l := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2"
	log := fakeMergeHosts(t, squashOnly, "", stateClient{w: mrwatch.Merged, l: mrwatch.Merged})
	id := shipWithMRs(t, f, "ship", w)
	if _, err := f.store.AddMR(id, task.MRLinked, mustRef(t, l)); err != nil {
		t.Fatal(err)
	}
	f.store.UpdateMRs(id, func(m *[]task.MR) error {
		(*m)[1].Watch = mrwatch.Position{Polled: true, State: mrwatch.Open}
		return nil
	})
	out, err := coord(t, "", "merge", string(id))
	if err != nil {
		t.Fatalf("merge: %q %v", out, err)
	}
	if !strings.Contains(out, "Merged "+w) || !strings.Contains(out, "left open: "+l+" (linked; pass --mr to merge it)") || !strings.Contains(out, "is waiting-review.") {
		t.Fatalf("output %q", out)
	}
	if calls := readCalls(log); strings.Contains(calls, "pr merge 2") {
		t.Fatalf("linked MR merged without --mr:\n%s", calls)
	}
	out, err = coord(t, "", "merge", string(id))
	if err == nil || !strings.Contains(err.Error(), l) || !strings.Contains(err.Error(), "--mr") {
		t.Fatalf("only a linked MR left: %q %v", out, err)
	}
	if out, err = coord(t, "", "merge", string(id), "--mr", l); err != nil {
		t.Fatalf("merge --mr: %q %v", out, err)
	}
	if tk, _ := f.store.Get(id); tk.State != task.Merged {
		t.Fatalf("Task is %s", tk.State)
	}
}

func TestMergeClearsOnlyTheMergedMRsFacts(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u1, u2 := "https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2"
	fakeMergeHosts(t, squashOnly, "", stateClient{u1: mrwatch.Merged, u2: mrwatch.Open})
	id := shipWithMRs(t, f, "ship", u1, u2)
	if _, err := f.store.UpdateMRFile(id, func(mf *task.MRFile) error {
		mf.AddAsk(task.Ask{URL: u1, Kind: "mr-ready", Text: u1 + " is approved with green CI"})
		mf.AddAsk(task.Ask{URL: u2, Kind: "mr-comments", Text: u2 + " has 2 new comments"})
		mf.MRs[0].CommentsSinceAck = 1
		mf.MRs[1].CommentsSinceAck = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Settle(id, ""); err != nil {
		t.Fatal(err)
	}
	if out, err := coord(t, "", "merge", string(id), "--mr", u1); err != nil {
		t.Fatalf("merge: %q %v", out, err)
	}
	tk, _ := f.store.Get(id)
	if tk.State != task.NeedsDecision || tk.QuestionFrom != task.QuestionFromWatcher {
		t.Fatalf("Task is %s from %q", tk.State, tk.QuestionFrom)
	}
	mf, _ := f.store.ReadMRFile(id)
	if len(mf.Asks) != 1 || mf.Asks[0].URL != u2 || mf.MRs[0].CommentsSinceAck != 0 || mf.MRs[1].CommentsSinceAck != 2 {
		t.Fatalf("MR file %+v", mf)
	}
	q, _ := os.ReadFile(f.store.QuestionPath(id))
	if strings.Contains(string(q), u1) || !strings.Contains(string(q), u2+" has 2 new comments") {
		t.Fatalf("question %q", q)
	}
}

func TestMergeEventsDoNotWakeTheCoordinator(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u := "https://github.com/a/b/pull/1"
	fakeMergeHosts(t, squashOnly, "", stateClient{u: mrwatch.Merged})
	id := shipWithMRs(t, f, "ship", u)
	if out, err := coord(t, "", "merge", string(id)); err != nil {
		t.Fatalf("merge: %q %v", out, err)
	}
	evs, err := f.store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawMerged, sawSettle bool
	for _, e := range evs {
		switch {
		case e.Type == task.EventMerged:
			sawMerged = true
			if !quiet(e) {
				t.Errorf("event %+v wakes the Coordinator", e)
			}
		case e.Type == task.EventStateChanged && e.To == task.Merged:
			sawSettle = true
			if !quiet(e) {
				t.Errorf("event %+v wakes the Coordinator", e)
			}
		}
	}
	if !sawMerged || !sawSettle {
		t.Fatalf("events %+v", evs)
	}
	if quiet(task.Event{Type: task.EventNote, Text: "closed " + u + " (coord drop --close-mr)"}) {
		t.Error("coord drop --close-mr notes became quiet")
	}
}

func TestMergeRefusals(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u := "https://github.com/a/b/pull/1"
	log := fakeMergeHosts(t, squashOnly, "", stateClient{u: mrwatch.Merged})

	scout, err := f.store.Create(task.NewTask{Title: "look", Class: "scout", Projects: []string{"p"}, Brief: "b", LaunchFolder: absPath("/launch")})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := f.store.Create(task.NewTask{Title: "q", Class: "ship", Projects: []string{"p"}, Brief: "b", LaunchFolder: absPath("/launch")})
	if err != nil {
		t.Fatal(err)
	}
	dropped := shipWithMRs(t, f, "dropped", u)
	f.store.Transition(dropped, task.Dropped, "")
	noMRs := f.task(t, "no mrs", task.Running, task.Reported)
	closed := shipWithMRs(t, f, "closed", u)
	f.store.UpdateMRs(closed, func(m *[]task.MR) error { (*m)[0].Watch.State = mrwatch.Closed; return nil })
	live := shipWithMRs(t, f, "live", u)
	if _, err := f.store.UpdateWorker(live, func(w *task.WorkerRecord) error {
		w.SupervisorPID = os.Getpid()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		id   task.ID
		want string
	}{
		{scout.ID, "only ship Tasks"},
		{queued.ID, "is queued"},
		{dropped, "already dropped"},
		{noMRs, "waiting on its MRs"},
		{closed, "no open MRs"},
		{live, "still running"},
	} {
		if out, err := coord(t, "", "merge", string(c.id)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Task %s: %q %v, want %q", c.id, out, err, c.want)
		}
	}
	if calls := readCalls(log); calls != "" {
		t.Fatalf("a refused merge reached the host CLI:\n%s", calls)
	}
}

func TestMergeIsGatedOnTheStateTable(t *testing.T) {
	stubWatcher(t)
	f := newFleet(t)
	u := "https://github.com/a/b/pull/1"
	log := fakeMergeHosts(t, squashOnly, "", stateClient{u: mrwatch.Merged})
	withMR := func(id task.ID) task.ID {
		if _, err := f.store.AddMR(id, task.MRFromWorker, mustRef(t, u)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failed := withMR(f.task(t, "failed", task.Running, task.Failed))
	blocked := withMR(f.task(t, "blocked", task.Running, task.Blocked))
	running := withMR(f.task(t, "running", task.Running))
	asked := withMR(f.task(t, "asked", task.Running, task.NeedsDecision))
	if _, err := f.store.SetQuestion(asked, task.QuestionFromWorker, "which way?"); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]task.ID{"failed": failed, "blocked": blocked, "running": running, "needs-decision with the Worker's question": asked} {
		out, err := coord(t, "", "merge", string(id))
		if err == nil || !strings.Contains(err.Error(), "coord merge is for a ship Task waiting on its MRs") || !strings.Contains(err.Error(), "from here") {
			t.Errorf("%s: %q %v", name, out, err)
		}
	}
	if calls := readCalls(log); calls != "" {
		t.Fatalf("a refused merge reached the host CLI:\n%s", calls)
	}
}
