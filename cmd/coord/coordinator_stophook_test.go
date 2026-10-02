package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func hookInput(session string, active bool, bg ...backgroundTask) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": session, "hook_event_name": "Stop", "stop_hook_active": active,
		"background_tasks": append([]backgroundTask{}, bg...), "session_crons": []any{},
	})
	return string(b)
}

func runHook(t *testing.T, f fleet, token, stdin string) string {
	t.Helper()
	var out bytes.Buffer
	if err := stopHook(f.home, f.store, token, strings.NewReader(stdin), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func blocked(t *testing.T, out string) bool {
	t.Helper()
	if out == "" {
		return false
	}
	var d stopDecision
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Decision != "block" {
		t.Fatalf("bad hook output %q", out)
	}
	if !strings.Contains(d.Reason, "run_in_background=true") || !strings.Contains(d.Reason, "repeat your update to the Captain") {
		t.Fatalf("reason %q", d.Reason)
	}
	return true
}

func TestStopHookDecisions(t *testing.T) {
	armed := backgroundTask{Type: "shell", Status: "running", Command: "coord wait"}
	tests := []struct {
		name   string
		states [][]task.State
		input  string
		block  bool
	}{
		{"no Tasks", nil, hookInput("s", false), false},
		{"only finished Tasks", [][]task.State{{task.Running, task.Reported}, {task.Dropped}}, hookInput("s", false), false},
		{"queued Task, nothing armed", [][]task.State{{}}, hookInput("s", false), true},
		{"running Task, nothing armed", [][]task.State{{task.Running}}, hookInput("s", false), true},
		{"running Task, wait armed", [][]task.State{{task.Running}}, hookInput("s", false, armed), false},
		{"wait armed with Task id and pipe", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "shell", Status: "running", Command: "coord wait 001 2>&1 | tail -30"}), false},
		{"wait armed by absolute path", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "shell", Status: "running", Command: "'/Users/a b/bin/coord' wait"}), false},
		{"wait finished", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "shell", Status: "completed", Command: "coord wait"}), true},
		{"other shell running", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "shell", Status: "running", Command: "coord status"}), true},
		{"not a shell", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "agent", Status: "running", Command: "coord wait"}), true},
		{"lookalike binary", [][]task.State{{task.Running}}, hookInput("s", false, backgroundTask{Type: "shell", Status: "running", Command: "coordx wait"}), true},
		{"malformed input", [][]task.State{{task.Running}}, "not json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFleet(t)
			for i, s := range tt.states {
				f.task(t, "t"+string(rune('a'+i)), s...)
			}
			if got := blocked(t, runHook(t, f, "", tt.input)); got != tt.block {
				t.Fatalf("block = %v want %v", got, tt.block)
			}
		})
	}
}

func TestStopHookLoopGuard(t *testing.T) {
	f := newFleet(t)
	f.task(t, "live", task.Running)
	steps := []struct {
		session string
		active  bool
		block   bool
	}{
		{"s1", false, true},
		{"s1", true, false},
		{"s1", false, true},
		{"s2", true, true},
		{"s2", true, false},
	}
	for i, st := range steps {
		if got := blocked(t, runHook(t, f, "", hookInput(st.session, st.active))); got != st.block {
			t.Fatalf("step %d (%s active=%v): block = %v want %v", i, st.session, st.active, got, st.block)
		}
	}
	evs, err := f.store.FleetEvents(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Type != task.EventUnsupervised || !strings.Contains(evs[0].Text, "session s1") || !strings.Contains(evs[1].Text, "session s2") || evs[1].Seq != 2 {
		t.Fatalf("Fleet events %+v", evs)
	}

	if blocked(t, runHook(t, f, "", hookInput("s3", false, backgroundTask{Type: "shell", Status: "running", Command: "coord wait"}))) {
		t.Fatal("armed wait blocked")
	}
	if !blocked(t, runHook(t, f, "", hookInput("s3", true))) {
		t.Fatal("counter not reset after an allowed stop")
	}
}

func TestStopHookPointsAtUnreadEvents(t *testing.T) {
	f := newFleet(t)
	a := f.task(t, "live", task.Running)
	drain(t, f)
	var d stopDecision
	json.Unmarshal([]byte(runHook(t, f, "", hookInput("s", false))), &d)
	if !strings.Contains(d.Reason, "1 Task in flight") {
		t.Fatalf("reason without unread events %q", d.Reason)
	}
	f.event(t, a, task.Event{Type: task.EventQuestion, Text: "which queue?"})
	f.event(t, a, task.Event{Type: task.EventInstructionsLoaded})
	out := runHook(t, f, "", hookInput("s2", false))
	if !blocked(t, out) {
		t.Fatal("not blocked")
	}
	json.Unmarshal([]byte(out), &d)
	if !strings.Contains(d.Reason, "1 unread event waiting") || !strings.Contains(d.Reason, "returns them at once") {
		t.Fatalf("reason %q", d.Reason)
	}
}

func TestStopHookSupersededAllows(t *testing.T) {
	f := newFleet(t)
	f.task(t, "live", task.Running)
	old, _ := f.home.AcquireLock(home.Owner{LaunchFolder: "/a"}, false)
	cur, _ := f.home.AcquireLock(home.Owner{LaunchFolder: "/b"}, true)
	if blocked(t, runHook(t, f, old.Token, hookInput("s", false))) {
		t.Fatal("superseded session blocked")
	}
	if !blocked(t, runHook(t, f, cur.Token, hookInput("s", false))) {
		t.Fatal("current session not blocked")
	}
}

func TestStopHookCommandIgnoresStaleToken(t *testing.T) {
	f := newFleet(t)
	f.task(t, "live", task.Running)
	old, _ := f.home.AcquireLock(home.Owner{LaunchFolder: "/a"}, false)
	f.home.AcquireLock(home.Owner{LaunchFolder: "/b"}, true)
	t.Setenv(home.EnvToken, old.Token)
	out, err := coord(t, hookInput("s", false), "_stop-hook")
	if err != nil || out != "" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestStatusLine(t *testing.T) {
	f := newFleet(t)
	lock, err := f.home.AcquireLock(home.Owner{LaunchFolder: "/w/workspace"}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, lock.Token)
	out, err := coord(t, "{}", "_statusline")
	if err != nil || out != "coord · workspace · no Tasks in flight\n" {
		t.Fatalf("%q %v", out, err)
	}
	f.task(t, "a", task.Running)
	f.task(t, "b", task.Running)
	f.task(t, "c", task.Running, task.NeedsDecision)
	f.task(t, "d", task.Running, task.Reported)
	f.task(t, "e", task.Dropped)
	out, _ = coord(t, "{}", "_statusline")
	if out != "coord · workspace · 2 running · 1 needs you\n" {
		t.Fatalf("%q", out)
	}
	f.home.AcquireLock(home.Owner{LaunchFolder: "/w/other"}, true)
	out, err = coord(t, "{}", "_statusline")
	if err != nil || !strings.Contains(out, "superseded") {
		t.Fatalf("%q %v", out, err)
	}
}

func writeStatusSetting(t *testing.T, dir, command string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"statusLine": map[string]string{"type": "command", "command": command}})
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusLineComposesCaptainLine(t *testing.T) {
	f := newFleet(t)
	launch := filepath.Join(t.TempDir(), "workspace")
	os.MkdirAll(launch, 0o755)
	lock, err := f.home.AcquireLock(home.Owner{LaunchFolder: launch, SessionID: "old"}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, lock.Token)
	in := `{"session_id":"s-1","workspace":{"current_dir":"/elsewhere"}}`

	out, err := coord(t, in, "_statusline")
	if err != nil || out != "coord · workspace · no Tasks in flight\n" {
		t.Fatalf("no Captain status line: %q %v", out, err)
	}

	writeStatusSetting(t, os.Getenv("CLAUDE_CONFIG_DIR"), "printf 'BASE '; cat")
	out, _ = coord(t, in, "_statusline")
	if out != "BASE "+in+"\ncoord · workspace · no Tasks in flight\n" {
		t.Fatalf("composed %q", out)
	}

	writeStatusSetting(t, filepath.Join(launch, ".claude"), "echo PROJECT")
	if out, _ = coord(t, in, "_statusline"); !strings.HasPrefix(out, "PROJECT\ncoord · ") {
		t.Fatalf("project setting not preferred: %q", out)
	}

	writeStatusSetting(t, filepath.Join(launch, ".claude"), "/bin/coord _statusline")
	if out, _ = coord(t, in, "_statusline"); out != "coord · workspace · no Tasks in flight\n" {
		t.Fatalf("coord's own status line ran recursively: %q", out)
	}

	writeStatusSetting(t, filepath.Join(launch, ".claude"), "exit 3")
	if out, _ = coord(t, in, "_statusline"); out != "coord · workspace · no Tasks in flight\n" {
		t.Fatalf("failing Captain status line: %q", out)
	}
}

func TestHooksTrackTheCoordinatorSession(t *testing.T) {
	f := newFleet(t)
	lock, err := f.home.AcquireLock(home.Owner{LaunchFolder: "/w", SessionID: "first"}, false)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(f.home.CoordinatorRunDir(), 0o755)
	if err := home.WriteJSONAtomic(sessionPath(f.home), sessionRecord{SessionID: "first", LaunchFolder: "/w"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(home.EnvToken, lock.Token)
	coord(t, `{"session_id":"second"}`, "_statusline")
	l, _ := f.home.ReadLock()
	var rec sessionRecord
	home.ReadJSON(sessionPath(f.home), &rec)
	if l.SessionID != "second" || rec.SessionID != "second" {
		t.Fatalf("lock %q session record %q", l.SessionID, rec.SessionID)
	}
	runHook(t, f, lock.Token, hookInput("third", false))
	if l, _ = f.home.ReadLock(); l.SessionID != "third" {
		t.Fatalf("stop hook left %q", l.SessionID)
	}
	if out, _ := coord(t, "", "status"); !strings.Contains(out, "session third") {
		t.Fatalf("status %q", out)
	}
	runHook(t, f, "stale-token", hookInput("fourth", false))
	if l, _ = f.home.ReadLock(); l.SessionID != "third" {
		t.Fatalf("a stale token moved the session to %q", l.SessionID)
	}
}
