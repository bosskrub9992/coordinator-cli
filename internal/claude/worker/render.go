package worker

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

const shortLen = 160

func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

func shortInput(raw json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return short(string(raw), shortLen)
	}
	for _, k := range []string{"command", "file_path", "notebook_path", "pattern", "url", "query", "prompt", "description"} {
		if v, ok := m[k].(string); ok && v != "" {
			return short(v, shortLen)
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		b, _ := json.Marshal(m[k])
		parts = append(parts, k+"="+string(b))
	}
	return short(strings.Join(parts, " "), shortLen)
}

func resultText(raw json.RawMessage) string {
	if t := strings.TrimSpace(ContentText(raw)); t != "" {
		return t
	}
	var bs []block
	json.Unmarshal(raw, &bs)
	var loaded, other []string
	for _, b := range bs {
		switch {
		case b.Type == "tool_reference" && b.ToolName != "":
			loaded = append(loaded, b.ToolName)
		case b.Type != "text":
			other = append(other, "["+b.Type+"]")
		}
	}
	if len(loaded) > 0 {
		other = append([]string{"loaded " + strings.Join(loaded, ", ")}, other...)
	}
	if len(other) == 0 {
		return "(no output)"
	}
	return strings.Join(other, " ")
}

func RenderLog(w io.Writer, line []byte) {
	var rec LogRecord
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	ts := rec.Time.Local().Format("15:04:05")
	p := func(format string, args ...any) {
		fmt.Fprintf(w, "%s "+format+"\n", append([]any{ts}, args...)...)
	}
	switch rec.Dir {
	case "stderr":
		var s string
		json.Unmarshal(rec.Msg, &s)
		p("stderr: %s", short(s, 300))
		return
	case "note":
		var v map[string]any
		if json.Unmarshal(rec.Msg, &v) == nil {
			if _, ok := v["argv"]; ok {
				p("== Worker started (resume=%v) in %v", v["resume"], v["cwd"])
				return
			}
			if _, ok := v["exit"]; ok {
				var x struct {
					Exit harness.WorkerExit `json:"exit"`
				}
				json.Unmarshal(rec.Msg, &x)
				msg := fmt.Sprintf("== Worker exited (code %d)", x.Exit.Code)
				if x.Exit.Signal != "" {
					msg += " on " + x.Exit.Signal
				}
				if x.Exit.Err != "" {
					msg += ": " + x.Exit.Err
				}
				p("%s", msg)
				return
			}
			if e, ok := v["start_error"]; ok {
				p("== Worker failed to start: %v", e)
				return
			}
		}
		var s string
		if json.Unmarshal(rec.Msg, &s) == nil {
			p("== %s", s)
		}
		return
	}
	var m streamMsg
	if json.Unmarshal(rec.Msg, &m) != nil {
		var s string
		json.Unmarshal(rec.Msg, &s)
		p("%s", short(s, 300))
		return
	}
	if rec.Dir == "in" {
		switch m.Type {
		case "user":
			p("-> sent: %s", short(messageText(m.Message), 300))
		case "control_request":
			p("-> control request (interrupt)")
		}
		return
	}
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			var init struct {
				Model string `json:"model"`
				CWD   string `json:"cwd"`
			}
			json.Unmarshal(rec.Msg, &init)
			p("-- turn started (%s, cwd %s)", init.Model, init.CWD)
		}
	case "assistant":
		_, bs := Blocks(m.Message)
		for _, b := range bs {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					p("assistant: %s", strings.ReplaceAll(t, "\n", "\n          "))
				}
			case "tool_use":
				p("tool %s: %s", b.Name, shortInput(b.Input))
			}
		}
	case "user":
		if m.IsReplay {
			p("<- delivered: %s", short(messageText(m.Message), 120))
			return
		}
		_, bs := Blocks(m.Message)
		for _, b := range bs {
			if b.Type != "tool_result" {
				continue
			}
			tag := "result"
			if b.IsError {
				tag = "error"
			}
			p("  %s: %s", tag, short(resultText(b.Content), shortLen))
		}
	case "result":
		status := "turn ended"
		if m.IsError {
			status = "turn ended with error"
		}
		p("== %s: %s %s (turns %d, total $%.4f)", status, m.Subtype, m.TerminalReason, m.NumTurns, m.TotalCostUSD)
	case "rate_limit_event":
		var r struct {
			Info struct {
				Status   string `json:"status"`
				ResetsAt int64  `json:"resetsAt"`
			} `json:"rate_limit_info"`
		}
		json.Unmarshal(rec.Msg, &r)
		if r.Info.Status != "allowed" {
			p("== rate limit: %s", r.Info.Status)
		}
	case "control_response":
		p("<- control response")
	}
}
