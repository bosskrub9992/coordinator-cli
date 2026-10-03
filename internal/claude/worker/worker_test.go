package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/fakeclaude"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

func TestMain(m *testing.M) {
	if strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "claude" {
		os.Exit(fakeclaude.Main())
	}
	os.Exit(m.Run())
}

func recorded(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var rec struct {
			Dir string          `json:"dir"`
			Msg json.RawMessage `json:"msg"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Dir == "out" {
			out = append(out, append([]byte(nil), rec.Msg...))
		}
	}
	return out
}

func kinds(t *testing.T, name string) []harness.WorkerEvent {
	var evs []harness.WorkerEvent
	for _, line := range recorded(t, name) {
		evs = append(evs, Parse(line)...)
	}
	return evs
}

func TestParseRecordedMultiTurn(t *testing.T) {
	evs := kinds(t, "s1_multiturn.jsonl")
	count := map[harness.EventKind]int{}
	var turns []*harness.TurnResult
	var delivered []string
	for _, e := range evs {
		count[e.Kind]++
		if e.Turn != nil {
			turns = append(turns, e.Turn)
		}
		if e.Kind == harness.EventSteerDelivered {
			delivered = append(delivered, e.Text)
		}
		if len(e.Raw) == 0 {
			t.Fatalf("event without raw: %+v", e)
		}
	}
	if count[harness.EventSessionStarted] != 2 || len(turns) != 2 || count[harness.EventRateLimited] == 0 || count[harness.EventOther] == 0 {
		t.Fatalf("counts %v", count)
	}
	if turns[0].Subtype != "success" || turns[0].Text != "ONE" || turns[1].TotalCostUSD <= turns[0].TotalCostUSD {
		t.Fatalf("turns %+v %+v", turns[0], turns[1])
	}
	if len(delivered) != 2 || !strings.Contains(delivered[0], "ONE") || !strings.Contains(delivered[1], "TWO") {
		t.Fatalf("delivered %q", delivered)
	}
}

func TestParseRecordedInterrupt(t *testing.T) {
	evs := kinds(t, "s3_interrupt.jsonl")
	var tools []string
	var last *harness.TurnResult
	results := 0
	for _, e := range evs {
		switch e.Kind {
		case harness.EventToolUse:
			tools = append(tools, e.ToolName)
		case harness.EventToolResult:
			results++
		case harness.EventTurnEnded:
			if last == nil {
				last = e.Turn
			}
		}
	}
	if len(tools) == 0 || tools[0] != "Bash" || results == 0 {
		t.Fatalf("tools %v results %d", tools, results)
	}
	if last == nil || !last.IsError || last.TerminalReason != "aborted_tools" || last.Subtype != "error_during_execution" {
		t.Fatalf("interrupted turn %+v", last)
	}
}

func TestParseKeyOrderAndUnknown(t *testing.T) {
	e := Parse([]byte(`{"session_id":"s","total_cost_usd":0.5,"subtype":"success","type":"result","result":"done"}`))
	if len(e) != 1 || e[0].Kind != harness.EventTurnEnded || e[0].Turn.TotalCostUSD != 0.5 || e[0].SessionID != "s" {
		t.Fatalf("%+v", e)
	}
	e = Parse([]byte(`{"type":"brand_new_thing","x":1}`))
	if len(e) != 1 || e[0].Kind != harness.EventOther || string(e[0].Raw) != `{"type":"brand_new_thing","x":1}` {
		t.Fatalf("%+v", e)
	}
}

func TestArgs(t *testing.T) {
	spec := harness.WorkerSpec{SessionID: "sid", PermissionMode: "auto", SettingsFile: "/s.json", SystemPromptFile: "/p.md", Model: "m", Effort: "low", AddDirs: []string{"/a", "/b"}}
	got := strings.Join(Args(spec, false), " ")
	want := "-p --input-format stream-json --output-format stream-json --verbose --replay-user-messages --session-id sid --permission-mode auto --settings /s.json --append-system-prompt-file /p.md --model m --effort low --add-dir /a /b"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if d := strings.Join(Args(harness.WorkerSpec{SessionID: "sid"}, false), " "); !strings.Contains(d, "--permission-mode auto") {
		t.Fatalf("default mode args %s", d)
	}
	if r := strings.Join(Args(spec, true), " "); !strings.Contains(r, "--resume sid") || strings.Contains(r, "--session-id") {
		t.Fatalf("resume args %s", r)
	}
}

func fakeBin(t *testing.T) string {
	t.Helper()
	self, _ := filepath.Abs(os.Args[0])
	bin := filepath.Join(t.TempDir(), "claude")
	if runtime.GOOS == "windows" {
		bin += ".exe"
		raw, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, raw, 0o755); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

func next(t *testing.T, w harness.Worker, kind harness.EventKind) harness.WorkerEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-w.Events():
			if !ok {
				t.Fatalf("events closed waiting for %s", kind)
			}
			if e.Kind == kind {
				return e
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func TestRunnerLifecycle(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "worker.log")
	recording, err := filepath.Abs(filepath.Join("testdata", "s1_multiturn.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recording); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeclaude.EnvRecording, recording)
	r := Runner{Bin: fakeBin(t)}
	w, err := r.Start(context.Background(), harness.WorkerSpec{SessionID: "sid-1", Folder: dir, Brief: "SAY hello", LogPath: logPath, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	if e := next(t, w, harness.EventSteerDelivered); e.SteerID != BriefSteerID {
		t.Fatalf("brief delivery %+v", e)
	}
	if e := next(t, w, harness.EventSessionStarted); e.SessionID != "sid-1" {
		t.Fatalf("init %+v", e)
	}
	if e := next(t, w, harness.EventAssistantText); e.Text != "hello" {
		t.Fatalf("text %+v", e)
	}
	next(t, w, harness.EventTurnEnded)
	if err := w.Steer(context.Background(), harness.Steer{ID: "st-1", Text: "SLEEP 20"}); err != nil {
		t.Fatal(err)
	}
	if e := next(t, w, harness.EventSteerDelivered); e.SteerID != "st-1" {
		t.Fatalf("steer delivery %+v", e)
	}
	next(t, w, harness.EventToolUse)
	if err := w.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := next(t, w, harness.EventTurnEnded); e.Turn.TerminalReason != "aborted_tools" {
		t.Fatalf("interrupt turn %+v", e.Turn)
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Steer(context.Background(), harness.Steer{ID: "late", Text: "x"}); err == nil {
		t.Fatal("steer after stop accepted")
	}
	e := next(t, w, harness.EventExited)
	if e.Exit.Code != 0 {
		t.Fatalf("exit %+v", e.Exit)
	}
	if x, _ := w.Wait(); x.Code != 0 {
		t.Fatalf("wait %+v", x)
	}
	data, _ := os.ReadFile(logPath)
	var buf bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		RenderLog(&buf, line)
	}
	out := buf.String()
	for _, want := range []string{"Worker started", "-> sent: SAY hello", "assistant: hello", "tool Bash: sleep 20", "control request", "turn ended with error: error_during_execution aborted_tools", "stdin closed", "Worker exited (code 0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
}

func TestRunnerStartFailure(t *testing.T) {
	r := Runner{Bin: filepath.Join(t.TempDir(), "missing-claude")}
	if _, err := r.Start(context.Background(), harness.WorkerSpec{SessionID: "s", Folder: t.TempDir()}); err == nil {
		t.Fatal("started a missing binary")
	}
	if _, err := r.Start(context.Background(), harness.WorkerSpec{}); err == nil {
		t.Fatal("started without a session id")
	}
}

func TestResumeTaskNotificationResultKeepsSteer(t *testing.T) {
	log, err := openLog(filepath.Join(t.TempDir(), "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	w := &proc{log: log, grace: 30 * time.Millisecond, events: make(chan harness.WorkerEvent, 16), pending: []harness.Steer{{ID: "st-1", Text: "go on"}}}
	stub := []byte(`{"type":"result","subtype":"success","is_error":false,"duration_api_ms":0,"num_turns":0,"result":"","session_id":"s","origin":{"kind":"task-notification"}}`)
	w.handleLine(stub)
	if e := <-w.events; e.Kind != harness.EventOther {
		t.Fatalf("stub result became %s", e.Kind)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case e := <-w.events:
		t.Fatalf("unexpected event %s %s", e.Kind, e.SteerID)
	default:
	}
	w.handleLine([]byte(`{"type":"result","subtype":"success","num_turns":2,"session_id":"s","origin":{"kind":"task-notification"}}`))
	if e := <-w.events; e.Kind != harness.EventTurnEnded || e.Turn.Origin != "task-notification" {
		t.Fatalf("notification turn %+v", e)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case e := <-w.events:
		t.Fatalf("a task-notification turn dropped the steer: %s %s", e.Kind, e.SteerID)
	default:
	}
	w.handleLine([]byte(`{"type":"result","subtype":"success","num_turns":3,"session_id":"s"}`))
	if e := <-w.events; e.Kind != harness.EventTurnEnded {
		t.Fatalf("turn %+v", e)
	}
	select {
	case e := <-w.events:
		if e.Kind != harness.EventSteerDropped || e.SteerID != "st-1" {
			t.Fatalf("drop %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("a real turn end without the steer did not drop it")
	}
}
