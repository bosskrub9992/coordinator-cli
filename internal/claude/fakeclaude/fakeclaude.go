package fakeclaude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	EnvRecording = "FAKE_CLAUDE_RECORDING"
	EnvTrace     = "FAKE_CLAUDE_TRACE"
)

type args struct {
	sessionID    string
	resume       bool
	settings     string
	systemPrompt string
	addDirs      []string
	model        string
	effort       string
	permMode     string
	raw          []string
}

func parseArgs(argv []string) args {
	var a args
	a.raw = argv
	for i := 0; i < len(argv); i++ {
		next := func() string {
			if i+1 < len(argv) {
				i++
				return argv[i]
			}
			return ""
		}
		switch argv[i] {
		case "--session-id":
			a.sessionID = next()
		case "--resume":
			a.sessionID = next()
			a.resume = true
		case "--settings":
			a.settings = next()
		case "--append-system-prompt-file":
			a.systemPrompt = next()
		case "--model":
			a.model = next()
		case "--effort":
			a.effort = next()
		case "--permission-mode":
			a.permMode = next()
		case "--add-dir":
			for i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				i++
				a.addDirs = append(a.addDirs, argv[i])
			}
		}
	}
	return a
}

type hookSpec struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

type settings struct {
	Hooks       map[string][]hookSpec `json:"hooks"`
	Permissions struct {
		Allow []string `json:"allow"`
		Ask   []string `json:"ask"`
	} `json:"permissions"`
}

type fake struct {
	a         args
	cwd       string
	set       settings
	mu        sync.Mutex
	out       io.Writer
	turn      int
	cost      float64
	started   bool
	hooks     []json.RawMessage
	rateLimit []json.RawMessage
	init      map[string]any
	interrupt chan struct{}
	trace     *os.File
}

func Main() int {
	f := &fake{a: parseArgs(os.Args[1:]), out: os.Stdout, interrupt: make(chan struct{}, 4)}
	f.cwd, _ = os.Getwd()
	if p := os.Getenv(EnvTrace); p != "" {
		if tf, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			f.trace = tf
			defer tf.Close()
		}
	}
	f.tracef("start", map[string]any{"argv": f.a.raw, "cwd": f.cwd, "env": os.Environ()})
	if f.a.sessionID == "" {
		fmt.Fprintln(os.Stderr, "fake claude: no session id")
		return 2
	}
	if f.a.settings != "" {
		if b, err := os.ReadFile(f.a.settings); err == nil {
			json.Unmarshal(b, &f.set)
		}
	}
	f.loadRecording()
	users := make(chan json.RawMessage, 64)
	go f.readStdin(users)
	for msg := range users {
		if code, exit := f.runTurn(msg); exit {
			return code
		}
	}
	return 0
}

func (f *fake) tracef(kind string, v any) {
	if f.trace == nil {
		return
	}
	b, _ := json.Marshal(map[string]any{"t": time.Now().UTC(), "kind": kind, "v": v})
	f.mu.Lock()
	f.trace.Write(append(b, '\n'))
	f.mu.Unlock()
}

func (f *fake) emit(v any) {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out.Write(append(b, '\n'))
}

func (f *fake) loadRecording() {
	p := os.Getenv(EnvRecording)
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var rec struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Dir != "out" {
			continue
		}
		var m map[string]any
		if json.Unmarshal(rec.Msg, &m) != nil {
			continue
		}
		switch {
		case m["type"] == "system" && (m["subtype"] == "hook_started" || m["subtype"] == "hook_response"):
			if len(f.hooks) < 4 {
				f.hooks = append(f.hooks, rec.Msg)
			}
		case m["type"] == "system" && m["subtype"] == "init" && f.init == nil:
			f.init = m
		case m["type"] == "rate_limit_event" && len(f.rateLimit) == 0:
			f.rateLimit = append(f.rateLimit, rec.Msg)
		}
	}
}

func (f *fake) withSession(raw json.RawMessage) map[string]any {
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["session_id"] = f.a.sessionID
	return m
}

func (f *fake) readStdin(users chan<- json.RawMessage) {
	defer close(users)
	r := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var m struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Request   struct {
					Subtype string `json:"subtype"`
				} `json:"request"`
			}
			if json.Unmarshal(line, &m) == nil {
				switch m.Type {
				case "user":
					users <- json.RawMessage(append([]byte(nil), line...))
				case "control_request":
					f.emit(map[string]any{"type": "control_response", "response": map[string]any{
						"subtype": "success", "request_id": m.RequestID, "response": map[string]any{"still_queued": []any{}},
					}})
					if m.Request.Subtype == "interrupt" {
						select {
						case f.interrupt <- struct{}{}:
						default:
						}
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func userText(raw json.RawMessage) (string, json.RawMessage) {
	var m struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	json.Unmarshal(raw, &m)
	var s string
	if json.Unmarshal(m.Message.Content, &s) == nil {
		return s, m.Message.Content
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(m.Message.Content, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n"), m.Message.Content
}

func (f *fake) drainInterrupts() {
	for {
		select {
		case <-f.interrupt:
		default:
			return
		}
	}
}

func (f *fake) runTurn(raw json.RawMessage) (int, bool) {
	text, content := userText(raw)
	f.drainInterrupts()
	f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content},
		"session_id": f.a.sessionID, "parent_tool_use_id": nil, "isReplay": true})
	if !f.started {
		f.started = true
		for _, h := range f.hooks {
			f.emit(f.withSession(h))
		}
		f.fireInstructions()
	}
	init := map[string]any{"type": "system", "subtype": "init", "tools": []string{"Bash", "Write", "Edit"}}
	for k, v := range f.init {
		init[k] = v
	}
	init["session_id"], init["cwd"], init["model"] = f.a.sessionID, f.cwd, f.a.model
	f.emit(init)
	if len(f.a.addDirs) > 0 {
		text = strings.ReplaceAll(text, "{{WT}}", f.a.addDirs[0])
	}
	for i, d := range f.a.addDirs {
		text = strings.ReplaceAll(text, fmt.Sprintf("{{WT%d}}", i+1), d)
	}
	directives := strings.Split(text, "\n")
	aborted := false
	for _, d := range directives {
		d = strings.TrimSpace(d)
		word, rest, _ := strings.Cut(d, " ")
		var ok bool
		switch word {
		case "BASH":
			ok = f.bash(rest)
		case "REPORT":
			status, body, _ := strings.Cut(rest, " ")
			flags := ""
			for strings.HasPrefix(body, "--mr ") {
				var url string
				url, body, _ = strings.Cut(strings.TrimPrefix(body, "--mr "), " ")
				flags += " --mr " + url
			}
			ok = f.bash("coord report --status " + status + flags + " --file - <<'EOF'\n" + body + "\nEOF")
		case "WRITE":
			path, body, _ := strings.Cut(rest, " ")
			ok = f.write(path, body)
		case "SLEEP":
			secs, _ := strconv.ParseFloat(rest, 64)
			ok = f.sleep(time.Duration(secs * float64(time.Second)))
		case "SAY":
			f.say(rest)
			ok = true
		case "RATELIMIT":
			f.emit(map[string]any{"type": "rate_limit_event", "session_id": f.a.sessionID, "rate_limit_info": map[string]any{
				"status": rest, "resetsAt": time.Now().Add(time.Hour).Unix(), "rateLimitType": "five_hour"}})
			ok = true
		case "EXIT":
			code, _ := strconv.Atoi(rest)
			return code, true
		default:
			ok = true
		}
		if !ok {
			aborted = true
			break
		}
	}
	if !aborted && !strings.Contains(text, "SAY ") {
		f.say("ok")
	}
	for _, r := range f.rateLimit {
		f.emit(f.withSession(r))
	}
	f.cost += 0.01
	res := map[string]any{"type": "result", "session_id": f.a.sessionID, "result_index": f.turn, "num_turns": 1,
		"total_cost_usd": f.cost, "permission_denials": []any{}}
	if aborted {
		f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"}}}, "session_id": f.a.sessionID})
		res["subtype"], res["is_error"], res["terminal_reason"], res["stop_reason"] = "error_during_execution", true, "aborted_tools", "tool_use"
	} else {
		res["subtype"], res["is_error"], res["terminal_reason"], res["result"], res["stop_reason"] = "success", false, "completed", "ok", "end_turn"
	}
	f.emit(res)
	f.turn++
	return 0, false
}

func (f *fake) say(text string) {
	f.emit(map[string]any{"type": "assistant", "session_id": f.a.sessionID, "parent_tool_use_id": nil,
		"message": map[string]any{"role": "assistant", "type": "message", "model": f.a.model, "content": []any{map[string]any{"type": "text", "text": text}}}})
}

var toolSeq atomic.Int64

func (f *fake) toolUse(name string, input map[string]any) string {
	id := fmt.Sprintf("toolu_fake_%d", toolSeq.Add(1))
	f.emit(map[string]any{"type": "assistant", "session_id": f.a.sessionID, "parent_tool_use_id": nil,
		"message": map[string]any{"role": "assistant", "type": "message", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}}}})
	return id
}

func (f *fake) toolResult(id, text string, isError bool) {
	f.emit(map[string]any{"type": "user", "session_id": f.a.sessionID, "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": isError}}}})
}

func (f *fake) rejected(id string) {
	f.toolResult(id, "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). STOP what you are doing and wait for the user to tell you how to proceed.", true)
}

func notGranted(tool string) string {
	return "Claude requested permissions to use " + tool + ", but you haven't granted it yet."
}

func matchesAny(rules []string, command string) bool {
	for _, r := range rules {
		if !strings.HasPrefix(r, "Bash(") || !strings.HasSuffix(r, ")") {
			continue
		}
		p := strings.TrimSuffix(strings.TrimPrefix(r, "Bash("), ")")
		if strings.HasSuffix(p, ":*") {
			p = strings.TrimSuffix(p, ":*") + " *"
		}
		re := regexp.QuoteMeta(p)
		re = strings.ReplaceAll(re, `\*`, ".*")
		if strings.HasSuffix(re, " .*") {
			re = strings.TrimSuffix(re, " .*") + "( .*)?"
		}
		if regexp.MustCompile("^(?s:" + re + ")$").MatchString(command) {
			return true
		}
	}
	return false
}

func (f *fake) bash(command string) bool {
	input := map[string]any{"command": command, "description": "fake"}
	id := f.toolUse("Bash", input)
	if matchesAny(f.set.Permissions.Ask, command) {
		f.toolResult(id, notGranted("Bash"), true)
		return true
	}
	c := exec.Command("sh", "-c", command)
	c.Dir = f.cwd
	out, err := c.CombinedOutput()
	f.toolResult(id, string(out), err != nil)
	return true
}

func (f *fake) runHooks(event, tool string, payload map[string]any) []byte {
	var last []byte
	for _, h := range f.set.Hooks[event] {
		if h.Matcher != "" && tool != "" && !matches(h.Matcher, tool) {
			continue
		}
		for _, x := range h.Hooks {
			b, _ := json.Marshal(payload)
			c := exec.Command("sh", "-c", x.Command)
			c.Dir = f.cwd
			c.Stdin = bytes.NewReader(b)
			out, err := c.Output()
			f.tracef("hook", map[string]any{"event": event, "command": x.Command, "out": string(out), "err": fmt.Sprint(err)})
			last = out
		}
	}
	return last
}

func matches(matcher, tool string) bool {
	for _, m := range strings.Split(matcher, "|") {
		if m == tool {
			return true
		}
	}
	return false
}

func (f *fake) fireInstructions() {
	dirs := append([]string{f.cwd}, f.a.addDirs...)
	for _, d := range dirs {
		p := filepath.Join(d, "CLAUDE.md")
		if _, err := os.Stat(p); err != nil {
			continue
		}
		f.runHooks("InstructionsLoaded", "", map[string]any{"session_id": f.a.sessionID, "cwd": f.cwd,
			"hook_event_name": "InstructionsLoaded", "file_path": p, "memory_type": "Project", "load_reason": "session_start"})
	}
}

func (f *fake) write(path, body string) bool {
	input := map[string]any{"file_path": path, "content": body}
	id := f.toolUse("Write", input)
	out := f.runHooks("PreToolUse", "Write", map[string]any{"session_id": f.a.sessionID, "cwd": f.cwd,
		"hook_event_name": "PreToolUse", "tool_name": "Write", "tool_input": input})
	var d struct {
		H struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	json.Unmarshal(out, &d)
	switch d.H.Decision {
	case "deny", "ask":
		f.toolResult(id, d.H.Reason, true)
		return true
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(f.cwd, path)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.toolResult(id, err.Error(), true)
		return true
	}
	f.toolResult(id, "File created successfully at: "+path, false)
	return true
}

func (f *fake) sleep(d time.Duration) bool {
	id := f.toolUse("Bash", map[string]any{"command": fmt.Sprintf("sleep %v", d.Seconds()), "description": "wait"})
	select {
	case <-time.After(d):
		f.toolResult(id, "", false)
		return true
	case <-f.interrupt:
		f.rejected(id)
		return false
	}
}
