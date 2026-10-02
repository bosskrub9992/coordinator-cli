package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/worker"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

var FinalStates = []task.State{task.Reported, task.WaitingReview, task.Merged, task.Failed, task.Landed, task.Dropped}

func IsFinal(s task.State) bool { return slices.Contains(FinalStates, s) }

type Supervisor struct {
	Store     *task.Store
	Runner    harness.WorkerRunner
	Task      task.ID
	HomeRoot  string
	CoordBin  string
	Env       []string
	Poll      time.Duration
	StopGrace time.Duration
	Treehouse Returner
	Logf      func(format string, args ...any)
}

type Returner interface {
	Return(path, leaseID string) error
}

var ErrSupervised = errors.New("another supervisor is already running this Task")

func (s *Supervisor) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Supervisor) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 250 * time.Millisecond
}

func (s *Supervisor) grace() time.Duration {
	if s.StopGrace > 0 {
		return s.StopGrace
	}
	return 30 * time.Second
}

func (s *Supervisor) claim() (task.WorkerRecord, error) {
	pid := os.Getpid()
	return s.Store.UpdateWorker(s.Task, func(w *task.WorkerRecord) error {
		if w.SupervisorPID != 0 && w.SupervisorPID != pid && w.SupervisorLive() {
			return fmt.Errorf("%w (pid %d)", ErrSupervised, w.SupervisorPID)
		}
		w.SupervisorPID = pid
		w.SupervisorStart = home.ProcessStart(pid)
		return nil
	})
}

func (s *Supervisor) release(exit harness.WorkerExit) {
	s.Store.UpdateWorker(s.Task, func(w *task.WorkerRecord) error {
		if w.SupervisorPID == os.Getpid() {
			w.SupervisorPID = 0
			w.SupervisorStart = time.Time{}
		}
		w.WorkerPID = 0
		w.ExitedAt = time.Now().UTC()
		code := exit.Code
		w.ExitCode = &code
		return nil
	})
}

func (s *Supervisor) note(text string) {
	s.Store.Append(s.Task, task.Event{Type: task.EventNote, Text: text})
}

func (s *Supervisor) state() task.State {
	t, err := s.Store.Get(s.Task)
	if err != nil {
		return ""
	}
	return t.State
}

func (s *Supervisor) transition(to task.State, note string) {
	cur := s.state()
	if cur == to || !task.CanTransition(cur, to) {
		return
	}
	if _, err := s.Store.TransitionFrom(s.Task, cur, to, note); err != nil && !errors.Is(err, task.ErrStateChanged) {
		s.logf("transition to %s: %v", to, err)
	}
}

func (s *Supervisor) cancel(msgs []InboxMsg, why string) {
	for _, m := range msgs {
		if err := Archive(m, InboxCancelled); err != nil {
			s.logf("archive steer %s: %v", m.ID, err)
		}
		data, _ := json.Marshal(map[string]any{"steer_id": m.ID, "text": m.Text})
		s.Store.Append(s.Task, task.Event{Type: task.EventSteerCancelled, Text: "steer " + m.ID + " not delivered (" + why + "); steer again if it still applies", Data: data})
	}
}

func (s *Supervisor) archive(m InboxMsg) {
	if err := Archive(m, InboxSent); err != nil {
		s.logf("archive %s %s: %v", m.Kind, m.ID, err)
	}
}

func (s *Supervisor) startQueue() (steers []InboxMsg, stopped bool) {
	msgs, err := Queued(s.Store, s.Task)
	if err != nil {
		s.logf("inbox: %v", err)
	}
	for _, m := range msgs {
		switch m.Kind {
		case InboxSteer:
			steers = append(steers, m)
			stopped = false
		case InboxInterrupt:
			s.cancel(steers, "an interrupt was queued after it")
			steers = nil
			s.archive(m)
		case InboxStop:
			s.cancel(steers, "a stop was queued after it")
			steers = nil
			stopped = true
			s.archive(m)
		default:
			s.archive(m)
		}
	}
	return steers, stopped
}

func (s *Supervisor) Run(ctx context.Context) error {
	t, err := s.Store.Get(s.Task)
	if err != nil {
		return err
	}
	rec, err := s.claim()
	if err != nil {
		return err
	}
	if rec.SessionID == "" {
		return errors.New("the Task has no Worker session id; start it with coord spawn")
	}
	resume := rec.SessionStarted
	steers, stopped := s.startQueue()
	if stopped || (resume && len(steers) == 0) {
		s.release(harness.WorkerExit{})
		if stopped && !IsFinal(s.state()) {
			s.transition(task.Failed, "stopped by the Coordinator before the Worker started")
		}
		return nil
	}
	if s.CoordBin != "" {
		if err := WriteFiles(s.Store, t, rec, s.CoordBin); err != nil {
			s.logf("rewrite Worker files: %v", err)
		}
	}
	spec := harness.WorkerSpec{
		TaskID:           string(t.ID),
		SessionID:        rec.SessionID,
		Folder:           t.Folder,
		AddDirs:          rec.WorktreePaths(),
		Model:            rec.Model,
		Effort:           rec.Effort,
		SystemPromptFile: s.Store.SystemPromptPath(t.ID),
		PermissionMode:   PermissionMode,
		SettingsFile:     s.Store.SettingsPath(t.ID),
		Env:              WorkerEnvWithFile(WorkerEnv(s.Env, s.HomeRoot, string(t.ID), s.CoordBin), s.Store, t.ID),
		LogPath:          s.Store.WorkerLogPath(t.ID),
	}
	var w harness.Worker
	if resume {
		w, err = s.Runner.Resume(ctx, spec)
	} else {
		brief, berr := s.Store.Brief(t.ID)
		if berr != nil {
			s.release(harness.WorkerExit{Code: -1, Err: berr.Error()})
			return berr
		}
		spec.Brief = brief
		w, err = s.Runner.Start(ctx, spec)
	}
	if err != nil {
		s.release(harness.WorkerExit{Code: -1, Err: err.Error()})
		s.transition(task.Failed, "the Worker could not start: "+err.Error())
		return err
	}
	s.Store.UpdateWorker(s.Task, func(r *task.WorkerRecord) error {
		r.WorkerPID = w.PID()
		r.StartedAt = time.Now().UTC()
		r.ExitedAt = time.Time{}
		r.ExitCode = nil
		return nil
	})
	data, _ := json.Marshal(map[string]any{
		"session_id": rec.SessionID, "resume": resume, "pid": w.PID(),
		"harness": s.Runner.Name(), "model": rec.Model, "effort": rec.Effort,
		"worktrees": rec.WorktreePaths(), "task_folder": t.Folder,
	})
	verb := "started"
	if resume {
		verb = "resumed"
	}
	s.Store.Append(s.Task, task.Event{Type: task.EventWorkerStarted, Text: fmt.Sprintf("Worker %s (%s %s/%s)", verb, s.Runner.Name(), rec.Model, rec.Effort), Data: data})
	s.transition(task.Running, "Worker "+verb)
	ls := &loopState{inflight: map[string]InboxMsg{}, noted: map[string]bool{}}
	if !resume {
		ls.inflight[worker.BriefSteerID] = InboxMsg{ID: worker.BriefSteerID}
	}
	for _, m := range steers {
		s.sendSteer(ls, w, m)
	}
	if err := s.loop(ctx, w, ls); err != nil {
		return err
	}
	if ctx.Err() != nil || !s.steerWaiting() || s.state().Terminal() {
		return nil
	}
	if ls.delivered == 0 {
		s.note("steers stay queued: the Worker exited without reading any; coord steer resumes it")
		return nil
	}
	return s.Run(ctx)
}

func (s *Supervisor) steerWaiting() bool {
	msgs, err := Queued(s.Store, s.Task)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if m.Kind == InboxSteer {
			return true
		}
	}
	return false
}

type loopState struct {
	inflight      map[string]InboxMsg
	noted         map[string]bool
	delivered     int
	idle          bool
	idleWhy       string
	stopRequested bool
	stdinClosed   bool
	sawInit       bool
	rateStatus    string
}

func lastStop(msgs []InboxMsg) int {
	last := -1
	for i, m := range msgs {
		if m.Kind == InboxStop {
			last = i
		}
	}
	return last
}

func (s *Supervisor) loop(ctx context.Context, w harness.Worker, ls *loopState) error {
	tick := time.NewTicker(s.poll())
	defer tick.Stop()
	var kill <-chan time.Time
	closeStdin := func(why string) {
		if ls.stdinClosed {
			return
		}
		ls.stdinClosed = true
		s.logf("closing Worker stdin: %s", why)
		w.Stop(context.Background())
	}
	stop := func(why string) {
		ls.stopRequested = true
		s.cancelInflight(ls, why)
		w.Interrupt(context.Background())
		closeStdin(why)
		if kill == nil {
			kill = time.After(s.grace())
		}
	}
	events := w.Events()
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			stop("supervisor cancelled")
		case <-kill:
			s.logf("Worker did not exit within %s; killing it", s.grace())
			w.Kill()
			kill = nil
		case <-tick.C:
			s.checkIdle(ls)
			msgs, err := Queued(s.Store, s.Task)
			if err != nil {
				s.logf("inbox: %v", err)
			}
			cut := lastStop(msgs)
			for i, m := range msgs {
				switch m.Kind {
				case InboxSteer:
					if i < cut {
						delete(ls.inflight, m.ID)
						s.cancel([]InboxMsg{m}, "the Coordinator stopped the Worker")
						continue
					}
					if _, ok := ls.inflight[m.ID]; ok {
						continue
					}
					if ls.stdinClosed {
						s.noteOnce(ls, m.ID, "steer "+m.ID+" stays queued: the Worker is ending, so its session is resumed with it afterwards")
						continue
					}
					s.sendSteer(ls, w, m)
				case InboxInterrupt:
					if err := w.Interrupt(context.Background()); err != nil {
						s.note("interrupt not delivered: " + err.Error())
					}
					s.archive(m)
				case InboxStop:
					s.archive(m)
					stop("the Coordinator stopped the Worker")
				case InboxReported:
					s.archive(m)
					if IsFinal(s.state()) {
						closeStdin("final Report submitted")
					}
				default:
					s.archive(m)
				}
			}
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.Kind {
			case harness.EventSessionStarted:
				ls.idle = false
				s.onInit(ls)
			case harness.EventSteerDelivered:
				s.onSteerDelivered(ls, ev)
			case harness.EventSteerDropped:
				if m, ok := ls.inflight[ev.SteerID]; ok {
					delete(ls.inflight, ev.SteerID)
					if ev.SteerID != worker.BriefSteerID {
						s.cancel([]InboxMsg{m}, "the Worker's turn ended without reading it, most likely an interrupt")
					}
				}
				s.checkIdle(ls)
			case harness.EventTurnEnded:
				s.onTurnEnded(ls, ev, closeStdin)
			case harness.EventRateLimited:
				s.onRateLimit(ls, ev)
			case harness.EventExited:
				s.onExit(ls, ev)
			}
		}
	}
}

func (s *Supervisor) cancelInflight(ls *loopState, why string) {
	var msgs []InboxMsg
	for id, m := range ls.inflight {
		if id != worker.BriefSteerID {
			msgs = append(msgs, m)
		}
		delete(ls.inflight, id)
	}
	slices.SortFunc(msgs, func(a, b InboxMsg) int { return a.At.Compare(b.At) })
	s.cancel(msgs, why)
}

func (s *Supervisor) noteOnce(ls *loopState, key, text string) {
	if ls.noted[key] {
		return
	}
	ls.noted[key] = true
	s.note(text)
}

func (s *Supervisor) onInit(ls *loopState) {
	if ls.sawInit {
		return
	}
	ls.sawInit = true
	s.Store.UpdateWorker(s.Task, func(r *task.WorkerRecord) error {
		r.SessionStarted = true
		return nil
	})
	files := LoadedInstructions(s.Store, s.Task)
	if len(files) == 0 {
		return
	}
	data, _ := json.Marshal(map[string]any{"files": files})
	s.Store.Append(s.Task, task.Event{Type: task.EventInstructionsLoaded, Text: fmt.Sprintf("%d instruction files loaded", len(files)), Data: data})
}

func (s *Supervisor) sendSteer(ls *loopState, w harness.Worker, m InboxMsg) {
	if err := w.Steer(context.Background(), harness.Steer{ID: m.ID, Text: m.Text}); err != nil {
		s.noteOnce(ls, m.ID, "steer "+m.ID+" not delivered yet ("+err.Error()+"); it stays queued")
		return
	}
	ls.inflight[m.ID] = m
	ls.idle = false
}

func (s *Supervisor) onSteerDelivered(ls *loopState, ev harness.WorkerEvent) {
	m, ok := ls.inflight[ev.SteerID]
	if !ok {
		return
	}
	delete(ls.inflight, ev.SteerID)
	ls.delivered++
	ls.idle = false
	if ev.SteerID == worker.BriefSteerID {
		return
	}
	s.archive(m)
	data, _ := json.Marshal(map[string]any{"steer_id": ev.SteerID})
	s.Store.Append(s.Task, task.Event{Type: task.EventSteer, Text: m.Text, Data: data})
	if st, err := os.Stat(s.Store.QuestionPath(s.Task)); err == nil && st.ModTime().Before(m.At) {
		os.Remove(s.Store.QuestionPath(s.Task))
	}
	t, err := s.Store.Get(s.Task)
	if err != nil || t.StateSince.After(m.At) {
		return
	}
	if t.State == task.Blocked || t.State == task.NeedsDecision {
		s.transition(task.Running, "steer "+ev.SteerID+" delivered")
	}
}

func (s *Supervisor) onTurnEnded(ls *loopState, ev harness.WorkerEvent, closeStdin func(string)) {
	if ev.Turn != nil {
		cost := ev.Turn.TotalCostUSD
		s.Store.UpdateWorker(s.Task, func(r *task.WorkerRecord) error {
			r.CostUSD = cost
			return nil
		})
	}
	if IsFinal(s.state()) {
		closeStdin("final Report submitted")
		return
	}
	why := "the Worker ended its turn without a Report"
	if ev.Turn != nil {
		if ev.Turn.TerminalReason == "aborted_tools" || ev.Turn.Subtype == "error_during_execution" {
			why = "the Worker's turn was interrupted"
		} else if ev.Turn.IsError {
			why += " (" + ev.Turn.Subtype + ")"
		}
		if t := strings.TrimSpace(ev.Turn.Text); t != "" {
			why += ": " + oneLine(t, 300)
		}
	}
	ls.idle = true
	ls.idleWhy = why
	s.checkIdle(ls)
}

func (s *Supervisor) checkIdle(ls *loopState) {
	if !ls.idle || ls.stopRequested || len(ls.inflight) > 0 || s.state() != task.Running {
		return
	}
	ls.idle = false
	s.transition(task.Blocked, ls.idleWhy)
}

func (s *Supervisor) onRateLimit(ls *loopState, ev harness.WorkerEvent) {
	var r struct {
		Info struct {
			Status        string `json:"status"`
			ResetsAt      int64  `json:"resetsAt"`
			RateLimitType string `json:"rateLimitType"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(ev.Raw, &r) != nil || r.Info.Status == "" {
		return
	}
	prev := ls.rateStatus
	ls.rateStatus = r.Info.Status
	if r.Info.Status == "allowed" || r.Info.Status == prev {
		return
	}
	if r.Info.Status == "rejected" && s.state() == task.Running {
		s.transition(task.Blocked, UsageLimitText(r.Info.ResetsAt, time.Now()))
		return
	}
	text := "usage limit " + r.Info.Status
	if r.Info.Status == "rejected" {
		text = UsageLimitText(r.Info.ResetsAt, time.Now())
	} else if r.Info.ResetsAt > 0 {
		text += ", resets at " + time.Unix(r.Info.ResetsAt, 0).UTC().Format(time.RFC3339)
	}
	data, _ := json.Marshal(r.Info)
	s.Store.Append(s.Task, task.Event{Type: task.EventRateLimited, Text: text, Data: data})
}

func UsageLimitText(resetsAt int64, now time.Time) string {
	if resetsAt <= 0 {
		return "usage limit"
	}
	at := time.Unix(resetsAt, 0).In(time.Local)
	n := now.In(time.Local)
	layout := "15:04"
	if at.Year() != n.Year() || at.YearDay() != n.YearDay() {
		layout = "2006-01-02 15:04"
	}
	return "usage limit, resets at " + at.Format(layout)
}

func (s *Supervisor) onExit(ls *loopState, ev harness.WorkerEvent) {
	var x harness.WorkerExit
	if ev.Exit != nil {
		x = *ev.Exit
	}
	rec, _ := s.Store.Worker(s.Task)
	data, _ := json.Marshal(map[string]any{
		"code": x.Code, "signal": x.Signal, "error": x.Err, "session_id": rec.SessionID, "total_cost_usd": rec.CostUSD,
	})
	text := fmt.Sprintf("Worker exited (code %d)", x.Code)
	if x.Signal != "" {
		text += " on " + x.Signal
	}
	s.Store.Append(s.Task, task.Event{Type: task.EventWorkerExited, Text: text, Data: data})
	cur := s.state()
	switch {
	case IsFinal(cur):
	case ls.stopRequested:
		s.transition(task.Failed, "stopped by the Coordinator before a final Report")
	case cur == task.Running:
		s.transition(task.Failed, fmt.Sprintf("the Worker exited (code %d) without a final Report", x.Code))
	}
	s.returnLease()
	s.release(x)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
