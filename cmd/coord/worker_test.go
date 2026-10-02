package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/fakeclaude"
	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

const envTestMain = "COORD_TEST_MAIN"

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "claude" {
		os.Exit(fakeclaude.Main())
	}
	if os.Getenv(envTestMain) == "1" {
		watcherStarter = func(*app) error { return nil }
		notifier = &fakeNotifier{}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type workerRig struct {
	t      *testing.T
	home   string
	repo   string
	origin string
	trace  string
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newWorkerRig(t *testing.T, pool bool) *workerRig {
	t.Helper()
	if _, err := exec.LookPath("treehouse"); err != nil {
		t.Skip("treehouse not installed")
	}
	h := setup(t)
	root, _ := filepath.EvalSymlinks(t.TempDir())
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	linkSelf(t, bin, "claude")
	coordBin := linkSelf(t, bin, "coord")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(envTestMain, "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@example.com"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@example.com"}} {
		t.Setenv(kv[0], kv[1])
	}
	t.Setenv("TREEHOUSE_ROOT", filepath.Join(root, "pool"))
	tmp := filepath.Join(root, "tmp")
	os.MkdirAll(tmp, 0o755)
	t.Setenv("TMPDIR", tmp)
	t.Setenv(supervise.EnvRole, "")
	t.Setenv("CLAUDECODE", "1")
	trace := filepath.Join(root, "trace.jsonl")
	t.Setenv(fakeclaude.EnvTrace, trace)
	recording, err := filepath.Abs(filepath.Join("..", "..", "internal", "claude", "worker", "testdata", "s1_multiturn.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recording); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeclaude.EnvRecording, recording)
	old := coordExecutable
	coordExecutable = func() (string, error) { return coordBin, nil }
	t.Cleanup(func() { coordExecutable = old })

	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "launch", "repo")
	os.MkdirAll(filepath.Dir(repo), 0o755)
	runGit(t, root, "init", "-q", "--bare", origin)
	runGit(t, root, "init", "-q", "-b", "main", repo)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("main\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("REPO-AGENTS-MARKER\n"), 0o644)
	if pool {
		os.WriteFile(filepath.Join(repo, "treehouse.toml"), []byte("max_trees = 4\n"), 0o644)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-q", "-m", "init")
	addOrigin(t, repo, origin)
	runGit(t, repo, "push", "-q", "origin", "main")
	r := &workerRig{t: t, home: h, repo: repo, origin: origin, trace: trace}
	if out, err := coord(t, "", "project", "add", repo, "--name", "repo"); err != nil {
		t.Fatalf("project add: %q %v", out, err)
	}
	t.Chdir(filepath.Dir(repo))
	t.Cleanup(r.stopAll)
	return r
}

func (r *workerRig) store() *task.Store { return task.NewStore(home.New(r.home)) }

func (r *workerRig) newTask(class, title, brief string, extra ...string) task.ID {
	r.t.Helper()
	args := append([]string{"task", "new", "--project", "repo", "--class", class, "--title", title, "--brief", "-"}, extra...)
	out, err := coord(r.t, brief, args...)
	if err != nil {
		r.t.Fatalf("task new: %q %v", out, err)
	}
	return task.ID(strings.TrimSpace(out))
}

func (r *workerRig) run(args ...string) string {
	r.t.Helper()
	out, err := coord(r.t, "", args...)
	if err != nil {
		r.t.Fatalf("coord %v: %q %v", args, out, err)
	}
	return out
}

func (r *workerRig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.dump()
	r.t.Fatalf("timed out waiting for %s", what)
}

func (r *workerRig) dump() {
	s := r.store()
	list, _ := s.List()
	for _, tk := range list {
		evs, _ := s.Events(tk.ID, 0)
		for _, e := range evs {
			r.t.Logf("%s %s %s>%s %s %s", tk.ID, e.Type, e.From, e.To, e.Text, e.Data)
		}
		b, _ := os.ReadFile(s.SupervisorLogPath(tk.ID))
		r.t.Logf("supervisor.log: %s", b)
	}
}

func (r *workerRig) state(id task.ID) task.State {
	tk, err := r.store().Get(id)
	if err != nil {
		return ""
	}
	return tk.State
}

func (r *workerRig) waitState(id task.ID, want task.State) {
	r.t.Helper()
	r.waitFor(string(id)+" "+string(want), func() bool { return r.state(id) == want })
}

func (r *workerRig) events(id task.ID, typ task.EventType) []task.Event {
	evs, _ := r.store().Events(id, 0)
	var out []task.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (r *workerRig) waitEvent(id task.ID, typ task.EventType, n int) task.Event {
	r.t.Helper()
	r.waitFor(string(id)+" "+string(typ), func() bool { return len(r.events(id, typ)) >= n })
	return r.events(id, typ)[n-1]
}

func (r *workerRig) idle(id task.ID) bool {
	w, _ := r.store().Worker(id)
	return !w.SupervisorLive()
}

func (r *workerRig) waitIdle(id task.ID) {
	r.t.Helper()
	r.waitFor(string(id)+" supervisor exit", func() bool { return r.idle(id) })
}

func (r *workerRig) stopAll() {
	s := r.store()
	list, _ := s.List()
	for _, tk := range list {
		w, _ := s.Worker(tk.ID)
		if w.SupervisorLive() {
			supervise.Post(s, tk.ID, supervise.InboxStop, "")
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for _, tk := range list {
		for time.Now().Before(deadline) {
			w, _ := s.Worker(tk.ID)
			if !w.SupervisorLive() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

type traceRec struct {
	Kind string          `json:"kind"`
	V    json.RawMessage `json:"v"`
}

func (r *workerRig) traces(kind string) []json.RawMessage {
	b, _ := os.ReadFile(r.trace)
	var out []json.RawMessage
	for _, line := range strings.Split(string(b), "\n") {
		var tr traceRec
		if json.Unmarshal([]byte(line), &tr) == nil && tr.Kind == kind {
			out = append(out, tr.V)
		}
	}
	return out
}

func TestSpawnShipEndToEnd(t *testing.T) {
	r := newWorkerRig(t, true)
	evil := filepath.Join(r.repo, "evil.txt")
	brief := strings.Join([]string{
		"WRITE {{WT}}/hello.txt hi",
		"WRITE " + evil + " nope",
		"BASH git -C {{WT}} add hello.txt && git -C {{WT}} commit -qm hello && git -C {{WT}} push -q origin HEAD",
		"REPORT done --mr https://git.example.com/g/p/-/merge_requests/7 Pushed hello. MR https://git.example.com/g/p/-/merge_requests/7",
	}, "\n")
	id := r.newTask("ship", "Say hello", brief, "--ticket", "AT-77")
	out := r.run("spawn", string(id))
	if !strings.Contains(out, "branch coord/"+string(id)) {
		t.Fatalf("spawn output %q", out)
	}
	if _, err := coord(t, "", "spawn", string(id)); err == nil || !strings.Contains(err.Error(), "only a queued Task") {
		t.Fatalf("second spawn: %v", err)
	}
	r.waitState(id, task.WaitingReview)
	r.waitIdle(id)

	s := r.store()
	tk, _ := s.Get(id)
	rec, _ := s.Worker(id)
	if len(rec.Worktrees) != 1 || rec.Worktrees[0].Branch != "coord/"+string(id) {
		t.Fatalf("worktrees %+v", rec.Worktrees)
	}
	wt := rec.Worktrees[0].Path
	if tk.Folder != filepath.Join(filepath.Dir(r.repo), "AT-77-say-hello") {
		t.Fatalf("Task folder %s", tk.Folder)
	}
	if st, err := os.Stat(tk.Folder); err != nil || !st.IsDir() {
		t.Fatalf("Task folder missing: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(wt, "hello.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("hello.txt %q %v", b, err)
	}
	if _, err := os.Stat(evil); !os.IsNotExist(err) {
		t.Fatal("the guard let a Write into the main checkout")
	}
	if got := runGit(t, r.origin, "log", "--format=%s", "-1", "coord/"+string(id)); got != "hello" {
		t.Fatalf("remote branch log %q", got)
	}
	if got := runGit(t, r.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("main checkout changed: %q", got)
	}
	if got := runGit(t, r.repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("main checkout on %s", got)
	}
	report, found, _ := s.Report(id)
	if !found || !strings.Contains(report, "merge_requests/7") {
		t.Fatalf("report %q", report)
	}
	ev := r.events(id, task.EventReport)
	if len(ev) != 1 || !strings.Contains(string(ev[0].Data), `"model"`) {
		t.Fatalf("report events %+v", ev)
	}
	if rec.PlanApproval != "product-decisions" || rec.PlanFrom != "global" {
		t.Fatalf("plan approval %q from %q", rec.PlanApproval, rec.PlanFrom)
	}
	if ex := r.events(id, task.EventWorkerExited); len(ex) != 1 || !strings.Contains(string(ex[0].Data), `"code":0`) {
		t.Fatalf("exit events %+v", ex)
	}
	il := r.events(id, task.EventInstructionsLoaded)
	wantFile, _ := json.Marshal(filepath.Join(wt, "CLAUDE.md"))
	if len(il) != 1 || !strings.Contains(string(il[0].Data), strings.Trim(string(wantFile), `"`)) {
		t.Fatalf("instructions events %+v", il)
	}
	prompt, _ := os.ReadFile(s.SystemPromptPath(id))
	for _, want := range []string{"Worker role", wt, tk.Folder, r.repo, "REPO-AGENTS-MARKER", "off-limits"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	starts := r.traces("start")
	if len(starts) != 1 {
		t.Fatalf("claude started %d times", len(starts))
	}
	var st struct {
		Argv []string `json:"argv"`
		CWD  string   `json:"cwd"`
		Env  []string `json:"env"`
	}
	json.Unmarshal(starts[0], &st)
	argv := strings.Join(st.Argv, " ")
	for _, want := range []string{"-p", "--input-format stream-json", "--replay-user-messages", "--session-id " + rec.SessionID, "--permission-mode auto", "--add-dir " + wt, "--model claude-opus-5-5", "--effort high"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	for _, gone := range []string{"--mcp-config", "--permission-prompt-tool"} {
		if strings.Contains(argv, gone) {
			t.Errorf("argv has %s: %s", gone, argv)
		}
	}
	if st.CWD != tk.Folder {
		t.Errorf("cwd %s", st.CWD)
	}
	env := strings.Join(st.Env, "\n")
	for _, want := range []string{"COORD_ROLE=worker", "COORD_TASK=" + string(id), "COORD_HOME=" + r.home, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1", "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("worker env lacks %s", want)
		}
	}
	if strings.Contains(env, "\nCLAUDECODE=") {
		t.Error("worker env kept CLAUDECODE")
	}
	watch := r.run("watch", string(id))
	for _, want := range []string{"Worker started", "tool Write:", "tool Bash: git -C", "error: coordinator-cli guard:", "turn ended", "Worker exited (code 0)"} {
		if !strings.Contains(watch, want) {
			t.Errorf("watch lacks %q:\n%s", want, watch)
		}
	}
}

func TestSteerInterruptQuestionResumeStop(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("scout", "Look around", "SLEEP 30")
	r.run("spawn", string(id))
	r.waitFor("session started", func() bool {
		w, _ := r.store().Worker(id)
		return w.SessionStarted
	})
	r.run("interrupt", string(id))
	r.waitState(id, task.Blocked)
	r.run("steer", string(id), "REPORT needs-decision Which color?")
	q := r.waitEvent(id, task.EventQuestion, 1)
	if !strings.Contains(q.Text, "Which color?") {
		t.Fatalf("question %+v", q)
	}
	r.waitState(id, task.NeedsDecision)
	if r.events(id, task.EventSteer)[0].Text != "REPORT needs-decision Which color?" {
		t.Fatalf("steer events %+v", r.events(id, task.EventSteer))
	}
	r.run("steer", string(id), "REPORT done Blue it is.")
	r.waitState(id, task.Reported)
	r.waitIdle(id)
	if _, err := coord(t, "", "interrupt", string(id)); err == nil || !strings.Contains(err.Error(), "no live Worker") {
		t.Fatalf("interrupt of an exited Worker: %v", err)
	}
	out := r.run("steer", string(id), "SAY again")
	if !strings.Contains(out, "Resuming") {
		t.Fatalf("steer output %q", out)
	}
	r.waitEvent(id, task.EventWorkerStarted, 2)
	r.waitState(id, task.Blocked)
	starts := r.traces("start")
	if len(starts) != 2 || !strings.Contains(string(starts[1]), `"--resume"`) {
		t.Fatalf("second start not a resume: %s", starts)
	}
	ret := r.events(id, task.EventWorktreeReturned)
	if len(ret) != 1 {
		t.Fatalf("scout lease not returned after its Report: %+v", ret)
	}
	var resumed struct {
		Argv []string `json:"argv"`
	}
	json.Unmarshal(starts[1], &resumed)
	if slices.Contains(resumed.Argv, "--add-dir") {
		t.Fatalf("resumed scout still gets the returned worktree: %v", resumed.Argv)
	}
	rec, _ := r.store().Worker(id)
	if len(rec.Worktrees) != 1 || !rec.Worktrees[0].Returned() {
		t.Fatalf("worktrees %+v", rec.Worktrees)
	}
	r.run("stop", string(id))
	r.waitState(id, task.Failed)
	r.waitIdle(id)
}

func TestShipPlanWaitsForApproval(t *testing.T) {
	r := newWorkerRig(t, true)
	brief := "REPORT plan Impact: the Captain gets X. Product decision: name the field a or b; recommend a."
	id := r.newTask("ship", "Plan first", brief, "--plan-approval", "all")
	r.run("spawn", string(id))
	p := r.waitEvent(id, task.EventPlan, 1)
	if !strings.HasPrefix(p.Text, "Impact: the Captain gets X.") {
		t.Fatalf("plan event %+v", p)
	}
	r.waitState(id, task.NeedsDecision)
	out := r.run("show", string(id))
	for _, want := range []string{"Plan:        all (from the task setting)", "Question (needs-decision):", "Product decision: name the field a or b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	if _, err := coord(t, "", "ack", string(id)); err == nil {
		t.Fatal("ack accepted a Worker's Plan")
	}
	r.run("steer", string(id), "REPORT done Plan approved: built a.")
	r.waitState(id, task.Reported)
	r.waitIdle(id)
}

func TestWorkerExitWithoutReportFails(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("scout", "Crash", "EXIT 3")
	r.run("spawn", string(id))
	r.waitState(id, task.Failed)
	r.waitIdle(id)
	ex := r.events(id, task.EventWorkerExited)
	if len(ex) != 1 || !strings.Contains(string(ex[0].Data), `"code":3`) {
		t.Fatalf("exit %+v", ex)
	}
}

func TestSpawnRefusesWithoutPool(t *testing.T) {
	r := newWorkerRig(t, false)
	id := r.newTask("ship", "No pool", "x")
	_, err := coord(t, "", "spawn", string(id))
	if err == nil || !strings.Contains(err.Error(), "treehouse init") {
		t.Fatalf("err = %v", err)
	}
	if r.state(id) != task.Queued {
		t.Fatalf("state %s", r.state(id))
	}
}

func TestWorkerRoleRestrictsCommands(t *testing.T) {
	setup(t)
	if _, err := coord(t, "", "report", "--status", "done", "--file", "-"); err == nil || !strings.Contains(err.Error(), "Workers only") {
		t.Fatalf("report outside a Worker: %v", err)
	}
	t.Setenv(supervise.EnvRole, supervise.RoleWorker)
	for _, args := range [][]string{{"status"}, {"spawn", "1"}, {"task", "new"}, {"steer", "1", "x"}, {}} {
		if _, err := coord(t, "", args...); err == nil || !strings.Contains(err.Error(), "not available to a Worker") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := coord(t, "", "status"); err == nil || strings.Contains(err.Error(), supervise.EnvRole) {
		t.Errorf("refusal names the role variable: %v", err)
	}
	if _, err := coord(t, "", "report", "--status", "done", "--file", "-"); err == nil || !strings.Contains(err.Error(), "COORD_TASK") {
		t.Fatalf("report without COORD_TASK: %v", err)
	}
	t.Setenv(supervise.EnvRole, "")
	t.Setenv(supervise.EnvTask, "001-x")
	captainTTY(t, false)
	for _, args := range [][]string{{"status"}, {"spawn", "1"}, {"steer", "1", "x"}} {
		if _, err := coord(t, "", args...); err == nil || !strings.Contains(err.Error(), "not available to a Worker") {
			t.Errorf("COORD_ROLE cleared, %v: %v", args, err)
		}
	}
	captainTTY(t, true)
	if _, err := coord(t, "", "status"); err != nil {
		t.Errorf("a terminal with a stray COORD_TASK is not a Worker: %v", err)
	}
	if out, err := coord(t, "", "_guard", "nope"); err != nil || !strings.Contains(out, `"deny"`) {
		t.Fatalf("guard for unknown Task: %q %v", out, err)
	}
}

func TestReportTarget(t *testing.T) {
	mr := []string{"https://gitlab.example.com/acme/backend/api/-/merge_requests/123"}
	open := []task.MR{{}}
	merged := []task.MR{{Watch: mrwatch.Position{State: mrwatch.Merged}}}
	tests := []struct {
		class, status string
		mrs           []task.MR
		want          task.State
	}{
		{"ship", "done", open, task.WaitingReview},
		{"ship", "done", append(slices.Clone(merged), open...), task.WaitingReview},
		{"ship", "done", merged, task.Merged},
		{"ship", "done", nil, task.Reported},
		{"review-code", "done", nil, task.Reported},
		{"ship", "failed", nil, task.Failed},
		{"scout", "blocked", nil, task.Blocked},
		{"scout", "needs-decision", nil, task.NeedsDecision},
		{"ship", "plan", nil, task.NeedsDecision},
	}
	for _, tt := range tests {
		got, err := reportTarget(config.Class(tt.class), tt.status, tt.mrs)
		if err != nil || got != tt.want {
			t.Errorf("%s %s %v = %s %v", tt.class, tt.status, tt.mrs, got, err)
		}
	}
	if _, err := reportTarget("ship", "maybe", nil); err == nil {
		t.Error("bad status accepted")
	}
	if _, err := reportTarget("scout", "plan", nil); err == nil || !strings.Contains(err.Error(), "for ship Tasks") {
		t.Errorf("scout plan: %v", err)
	}
	for _, bad := range []struct {
		class, status string
		mrs           []string
	}{
		{"scout", "done", mr},
		{"ship", "blocked", mr},
		{"ship", "done", []string{"https://gitlab.example.com/a/b/-/issues/3"}},
		{"ship", "done", []string{"see https://github.com/a/b/pull/9"}},
		{"ship", "done", []string{"https://github.com/a/b/pull/x"}},
	} {
		if err := checkMRs(config.Class(bad.class), bad.status, bad.mrs); err == nil {
			t.Errorf("checkMRs accepted %+v", bad)
		}
	}
	if err := checkMRs(config.Ship, "done", append(mr, "https://github.com/a/b/pull/9")); err != nil {
		t.Error(err)
	}
}

func TestShipReportWithoutMRFlagIsNotWaitingReview(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("ship", "Text only", "REPORT done MR https://git.example.com/g/p/-/merge_requests/8 mentioned only in text")
	r.run("spawn", string(id))
	r.waitState(id, task.Reported)
	r.waitIdle(id)
	if ret := r.events(id, task.EventWorktreeReturned); len(ret) != 0 {
		t.Fatalf("ship lease returned: %+v", ret)
	}
}

func TestWorkerCommandsRunWithoutCoordinator(t *testing.T) {
	r := newWorkerRig(t, true)
	brief := strings.Join([]string{
		"BASH git -C {{WT}} log --oneline -1",
		"BASH d=$(pwd); cd {{WT}} && B=HEAD; git log --oneline -1 $B; touch $d/made.txt",
		"REPORT done looked",
	}, "\n")
	id := r.newTask("scout", "No asking", brief)
	r.run("spawn", string(id))
	r.waitState(id, task.Reported)
	r.waitIdle(id)
	if !strings.Contains(r.run("watch", string(id)), "init") {
		t.Errorf("git log output missing from the Worker's stream")
	}
	tk, _ := r.store().Get(id)
	if _, err := os.Stat(filepath.Join(tk.Folder, "made.txt")); err != nil {
		t.Errorf("the Worker's command did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.store().Dir(id), "permissions")); !os.IsNotExist(err) {
		t.Errorf("a permissions folder exists: %v", err)
	}
}

func TestConcurrentSpawnStartsOneWorker(t *testing.T) {
	r := newWorkerRig(t, true)
	id := r.newTask("scout", "Race", "REPORT done raced")
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := coord(t, "", "spawn", string(id))
			errs <- err
		}()
	}
	ok := 0
	for range 4 {
		err := <-errs
		if err == nil {
			ok++
			continue
		}
		if !strings.Contains(err.Error(), "already starting") && !strings.Contains(err.Error(), "only a queued Task") {
			t.Errorf("unexpected spawn error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d spawns succeeded", ok)
	}
	r.waitState(id, task.Reported)
	r.waitIdle(id)
	if n := len(r.traces("start")); n != 1 {
		t.Fatalf("claude started %d times", n)
	}
}

func TestKeepLeaseRefusesToOverwrite(t *testing.T) {
	rec := []task.Worktree{{Project: "p", Path: "/pool/1", LeaseID: "L1"}}
	if err := keepLease(rec, task.Worktree{Project: "p", Path: "/pool/1", LeaseID: "L1"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range []task.Worktree{{Project: "p", Path: "/pool/2", LeaseID: "L2"}, {Project: "p", Path: "/pool/1", LeaseID: "L9"}} {
		if err := keepLease(rec, got); err == nil || !strings.Contains(err.Error(), "will not overwrite") {
			t.Errorf("%+v: %v", got, err)
		}
	}
	if err := keepLease(nil, task.Worktree{Project: "p", Path: "/pool/2"}); err != nil {
		t.Fatal(err)
	}
}
