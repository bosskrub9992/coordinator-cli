package worker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func logLine(t *testing.T, dir, msg string) []byte {
	t.Helper()
	b, err := json.Marshal(LogRecord{Time: time.Now(), Dir: dir, Msg: json.RawMessage(msg)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRenderFirstToolResultWithoutText(t *testing.T) {
	recorded := []string{
		`{"type":"assistant","message":{"model":"claude-sonnet-5-5","id":"msg_000000000000000000000001","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_000000000000000000000002","name":"ToolSearch","input":{"query":"select:mcp__playwright__browser_tabs","max_results":1},"caller":{"type":"direct"}}],"container":null,"stop_reason":null,"stop_sequence":null,"stop_details":null,"input_transformations":[],"diagnostics":null,"context_management":null},"parent_tool_use_id":null,"session_id":"00000000-0000-4000-8000-000000000001","uuid":"00000000-0000-4000-8000-000000000002","timestamp":"2026-10-01T10:16:18.222Z","request_id":"req_000000000000000000000003"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_000000000000000000000002","content":[{"type":"tool_reference","tool_name":"mcp__playwright__browser_tabs"}]}]},"parent_tool_use_id":null,"session_id":"00000000-0000-4000-8000-000000000001","uuid":"00000000-0000-4000-8000-000000000003","timestamp":"2026-10-01T10:16:18.302Z","tool_use_result":{"matches":["mcp__playwright__browser_tabs"],"query":"select:mcp__playwright__browser_tabs","total_deferred_tools":173}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_2","content":""}]},"parent_tool_use_id":null,"session_id":"s"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_3","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]},"parent_tool_use_id":null,"session_id":"s"}`,
	}
	var buf bytes.Buffer
	for _, m := range recorded {
		RenderLog(&buf, logLine(t, "out", m))
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{
		"tool ToolSearch: select:mcp__playwright__browser_tabs",
		"  result: loaded mcp__playwright__browser_tabs",
		"  result: (no output)",
		"  result: [image]",
	}
	if len(lines) != len(want) {
		t.Fatalf("rendered %d lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, w := range want {
		if !strings.HasSuffix(lines[i], w) {
			t.Errorf("line %d = %q, want suffix %q", i, lines[i], w)
		}
	}
}
