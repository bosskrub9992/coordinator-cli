package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

const BriefSteerID = "brief"

type Runner struct {
	Bin        string
	SteerGrace time.Duration
}

const DefaultSteerGrace = 3 * time.Second

func (r Runner) steerGrace() time.Duration {
	if r.SteerGrace > 0 {
		return r.SteerGrace
	}
	return DefaultSteerGrace
}

func (r Runner) Name() harness.Name { return harness.Claude }

func (r Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "claude"
}

func Args(spec harness.WorkerSpec, resume bool) []string {
	args := []string{"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--replay-user-messages",
	}
	if resume {
		args = append(args, "--resume", spec.SessionID)
	} else {
		args = append(args, "--session-id", spec.SessionID)
	}
	mode := spec.PermissionMode
	if mode == "" {
		mode = "auto"
	}
	args = append(args, "--permission-mode", mode)
	if spec.SettingsFile != "" {
		args = append(args, "--settings", spec.SettingsFile)
	}
	if spec.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt-file", spec.SystemPromptFile)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	if len(spec.AddDirs) > 0 {
		args = append(args, "--add-dir")
		args = append(args, spec.AddDirs...)
	}
	return args
}

func (r Runner) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return r.start(ctx, spec, false)
}

func (r Runner) Resume(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	return r.start(ctx, spec, true)
}

func (r Runner) start(ctx context.Context, spec harness.WorkerSpec, resume bool) (harness.Worker, error) {
	if spec.SessionID == "" {
		return nil, errors.New("worker: a session id is required")
	}
	log, err := openLog(spec.LogPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(r.bin(), Args(spec, resume)...)
	cmd.Dir = spec.Folder
	cmd.Env = spec.Env
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	log.write("note", map[string]any{"argv": append([]string{r.bin()}, cmd.Args[1:]...), "cwd": spec.Folder, "resume": resume})
	if err := cmd.Start(); err != nil {
		log.write("note", map[string]any{"start_error": err.Error()})
		log.Close()
		return nil, fmt.Errorf("start %s: %w", r.bin(), err)
	}
	w := &proc{
		cmd:       cmd,
		stdin:     stdin,
		log:       log,
		sessionID: spec.SessionID,
		grace:     r.steerGrace(),
		events:    make(chan harness.WorkerEvent, 1024),
		exited:    make(chan struct{}),
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		w.readStdout(stdout)
	}()
	go func() {
		defer readers.Done()
		w.readStderr(stderr)
	}()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		w.exit = exitOf(err)
		termGroup(cmd.Process.Pid)
		log.write("note", map[string]any{"exit": w.exit})
		w.mu.Lock()
		if w.graceTimer != nil {
			w.graceTimer.Stop()
		}
		w.mu.Unlock()
		w.emit(harness.WorkerEvent{Kind: harness.EventExited, Exit: &w.exit})
		w.closeEvents()
		log.Close()
		close(w.exited)
	}()
	if spec.Brief != "" {
		if err := w.Steer(ctx, harness.Steer{ID: BriefSteerID, Text: spec.Brief}); err != nil {
			w.Kill()
			return nil, err
		}
	}
	return w, nil
}

func exitOf(err error) harness.WorkerExit {
	if err == nil {
		return harness.WorkerExit{}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		x := harness.WorkerExit{Code: ee.ExitCode()}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			x.Signal = ws.Signal().String()
		}
		return x
	}
	return harness.WorkerExit{Code: -1, Err: err.Error()}
}

type proc struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	log       *logFile
	sessionID string
	grace     time.Duration
	events    chan harness.WorkerEvent
	exited    chan struct{}
	exit      harness.WorkerExit

	emitMu       sync.Mutex
	eventsClosed bool

	mu         sync.Mutex
	closed     bool
	pending    []harness.Steer
	reqCount   int
	interrupts map[string]bool
	replays    int
	graceTimer *time.Timer
}

func (w *proc) SessionID() string                  { return w.sessionID }
func (w *proc) PID() int                           { return w.cmd.Process.Pid }
func (w *proc) Events() <-chan harness.WorkerEvent { return w.events }

func (w *proc) emit(e harness.WorkerEvent) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.SessionID == "" {
		e.SessionID = w.sessionID
	}
	w.emitMu.Lock()
	defer w.emitMu.Unlock()
	if w.eventsClosed {
		return
	}
	w.events <- e
}

func (w *proc) closeEvents() {
	w.emitMu.Lock()
	defer w.emitMu.Unlock()
	w.eventsClosed = true
	close(w.events)
}

func (w *proc) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("worker: stdin is closed")
	}
	w.log.writeRaw("in", b)
	if _, err := w.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("worker: write stdin: %w", err)
	}
	return nil
}

func UserMessage(text string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
}

func (w *proc) Steer(ctx context.Context, s harness.Steer) error {
	w.mu.Lock()
	w.pending = append(w.pending, s)
	w.mu.Unlock()
	if err := w.writeLine(UserMessage(s.Text)); err != nil {
		w.mu.Lock()
		w.pending = w.pending[:len(w.pending)-1]
		w.mu.Unlock()
		return err
	}
	return nil
}

func (w *proc) Interrupt(ctx context.Context) error {
	w.mu.Lock()
	w.reqCount++
	id := "coord_int_" + strconv.Itoa(w.reqCount)
	if w.interrupts == nil {
		w.interrupts = map[string]bool{}
	}
	w.interrupts[id] = true
	w.mu.Unlock()
	return w.writeLine(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}})
}

func (w *proc) Stop(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	w.log.write("note", "stdin closed")
	return w.stdin.Close()
}

func (w *proc) Kill() error {
	select {
	case <-w.exited:
		return nil
	default:
	}
	return killGroup(w.cmd.Process)
}

func (w *proc) Wait() (harness.WorkerExit, error) {
	<-w.exited
	return w.exit, nil
}

func (w *proc) delivered(text string) (harness.Steer, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.replays++
	if len(w.pending) == 0 {
		return harness.Steer{}, false
	}
	i := slices.IndexFunc(w.pending, func(s harness.Steer) bool { return s.Text == text })
	if i < 0 {
		i = 0
	}
	s := w.pending[i]
	w.pending = slices.Delete(w.pending, i, i+1)
	return s, true
}

func (w *proc) merged(text string) []harness.Steer {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []harness.Steer
	for len(w.pending) > 0 && w.pending[0].Text != "" && strings.Contains(text, w.pending[0].Text) {
		out = append(out, w.pending[0])
		w.pending = w.pending[1:]
	}
	return out
}

func (w *proc) armGrace() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return
	}
	if w.graceTimer != nil {
		w.graceTimer.Stop()
	}
	ids := make([]string, len(w.pending))
	for i, s := range w.pending {
		ids[i] = s.ID
	}
	seen := w.replays
	w.graceTimer = time.AfterFunc(w.grace, func() { w.dropUnread(ids, seen) })
}

func (w *proc) dropUnread(ids []string, seen int) {
	w.mu.Lock()
	if w.replays != seen {
		w.mu.Unlock()
		return
	}
	var dropped []harness.Steer
	w.pending = slices.DeleteFunc(w.pending, func(s harness.Steer) bool {
		if slices.Contains(ids, s.ID) {
			dropped = append(dropped, s)
			return true
		}
		return false
	})
	w.mu.Unlock()
	for _, s := range dropped {
		w.log.write("note", "steer "+s.ID+" was not read before the turn ended")
		w.emit(harness.WorkerEvent{Kind: harness.EventSteerDropped, SteerID: s.ID, Text: s.Text})
	}
}

func (w *proc) interruptAcked(line []byte) bool {
	var m struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "control_response" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.interrupts[m.Response.RequestID] {
		return false
	}
	delete(w.interrupts, m.Response.RequestID)
	return true
}

func (w *proc) readStdout(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			w.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

func (w *proc) readStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		text := sc.Text()
		w.log.write("stderr", text)
		w.emit(harness.WorkerEvent{Kind: harness.EventStderr, Text: text})
	}
}

func (w *proc) handleLine(line []byte) {
	if !json.Valid(line) {
		w.log.write("out", string(line))
		w.emit(harness.WorkerEvent{Kind: harness.EventOther, Text: string(line)})
		return
	}
	w.log.writeRaw("out", line)
	if w.interruptAcked(line) {
		w.armGrace()
	}
	for _, e := range Parse(line) {
		switch e.Kind {
		case harness.EventSteerDelivered:
			s, ok := w.delivered(e.Text)
			if ok {
				e.SteerID = s.ID
			}
			w.emit(e)
			for _, m := range w.merged(e.Text) {
				w.emit(harness.WorkerEvent{Kind: harness.EventSteerDelivered, SteerID: m.ID, Text: m.Text, SessionID: e.SessionID, Raw: e.Raw})
			}
			continue
		case harness.EventTurnEnded:
			if e.Turn == nil || e.Turn.Origin != originTaskNotification {
				w.armGrace()
			}
		}
		w.emit(e)
	}
}

type streamMsg struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	IsReplay       bool            `json:"isReplay"`
	ParentToolUse  *string         `json:"parent_tool_use_id"`
	Message        json.RawMessage `json:"message"`
	IsError        bool            `json:"is_error"`
	TerminalReason string          `json:"terminal_reason"`
	Result         string          `json:"result"`
	NumTurns       int             `json:"num_turns"`
	TotalCostUSD   float64         `json:"total_cost_usd"`
	Origin         struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

const originTaskNotification = "task-notification"

type block struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
	ToolName string          `json:"tool_name"`
}

func Blocks(message json.RawMessage) (string, []block) {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &m) != nil {
		return "", nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s, nil
	}
	var bs []block
	json.Unmarshal(m.Content, &bs)
	return "", bs
}

func ContentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return string(raw)
	}
	var b bytes.Buffer
	for _, x := range bs {
		if x.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(x.Text)
		}
	}
	return b.String()
}

func messageText(message json.RawMessage) string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &m) != nil {
		return ""
	}
	return ContentText(m.Content)
}

func Parse(line []byte) []harness.WorkerEvent {
	raw := json.RawMessage(append([]byte(nil), line...))
	var m streamMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return []harness.WorkerEvent{{Kind: harness.EventOther, Raw: raw}}
	}
	base := harness.WorkerEvent{SessionID: m.SessionID, Raw: raw}
	one := func(k harness.EventKind) []harness.WorkerEvent {
		e := base
		e.Kind = k
		return []harness.WorkerEvent{e}
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			return one(harness.EventSessionStarted)
		}
	case "rate_limit_event":
		return one(harness.EventRateLimited)
	case "result":
		if m.Origin.Kind == originTaskNotification && m.NumTurns == 0 {
			return one(harness.EventOther)
		}
		e := base
		e.Kind = harness.EventTurnEnded
		e.Text = m.Result
		e.Turn = &harness.TurnResult{
			IsError: m.IsError, Subtype: m.Subtype, TerminalReason: m.TerminalReason,
			Text: m.Result, NumTurns: m.NumTurns, TotalCostUSD: m.TotalCostUSD, Origin: m.Origin.Kind,
		}
		return []harness.WorkerEvent{e}
	case "user":
		if m.IsReplay {
			e := base
			e.Kind = harness.EventSteerDelivered
			e.Text = messageText(m.Message)
			return []harness.WorkerEvent{e}
		}
		_, bs := Blocks(m.Message)
		var out []harness.WorkerEvent
		for _, b := range bs {
			if b.Type == "tool_result" {
				e := base
				e.Kind = harness.EventToolResult
				e.Text = ContentText(b.Content)
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
	case "assistant":
		_, bs := Blocks(m.Message)
		var out []harness.WorkerEvent
		for _, b := range bs {
			switch b.Type {
			case "text":
				e := base
				e.Kind = harness.EventAssistantText
				e.Text = b.Text
				out = append(out, e)
			case "tool_use":
				e := base
				e.Kind = harness.EventToolUse
				e.ToolName = b.Name
				e.Text = string(b.Input)
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return one(harness.EventOther)
}

type logFile struct {
	mu sync.Mutex
	f  *os.File
}

func openLog(path string) (*logFile, error) {
	if path == "" {
		return &logFile{}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open worker log: %w", err)
	}
	return &logFile{f: f}, nil
}

type LogRecord struct {
	Time time.Time       `json:"t"`
	Dir  string          `json:"dir"`
	Msg  json.RawMessage `json:"msg"`
}

func (l *logFile) write(dir string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	l.writeRaw(dir, b)
}

func (l *logFile) writeRaw(dir string, msg []byte) {
	if l == nil || l.f == nil {
		return
	}
	rec, err := json.Marshal(LogRecord{Time: time.Now().UTC(), Dir: dir, Msg: json.RawMessage(msg)})
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Write(append(rec, '\n'))
}

func (l *logFile) Close() {
	if l == nil || l.f == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.f.Close()
	l.f = nil
}
