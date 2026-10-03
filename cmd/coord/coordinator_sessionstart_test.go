package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

func runSessionStart(t *testing.T, f fleet, token, source string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"session_id": "s", "hook_event_name": "SessionStart", "source": source})
	var out bytes.Buffer
	if err := sessionStart(f.home, f.store, token, bytes.NewReader(in), &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return ""
	}
	var o sessionStartOutput
	if err := json.Unmarshal(out.Bytes(), &o); err != nil || o.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("bad hook output %q", out.String())
	}
	return o.HookSpecificOutput.AdditionalContext
}

func TestSessionStartGivesTheFleetAndAsksForARecap(t *testing.T) {
	f := newFleet(t)
	if ctx := runSessionStart(t, f, "", "startup"); !strings.Contains(ctx, "No Tasks in the Fleet.") || strings.Contains(ctx, "recap") {
		t.Fatalf("empty Fleet context %q", ctx)
	}
	id := f.task(t, "live", task.Running, task.Blocked)
	f.event(t, id, task.Event{Type: task.EventQuestion, Text: "which queue?"})
	ctx := runSessionStart(t, f, "", "startup")
	for _, want := range []string{string(id), "blocked", "steer, drop", "not read yet", "Open your first reply to the Captain", "arm `coord wait`"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("context lacks %q:\n%s", want, ctx)
		}
	}
	if ctx := runSessionStart(t, f, "", "compact"); !strings.Contains(ctx, string(id)) || strings.Contains(ctx, "first reply") {
		t.Fatalf("compact context %q", ctx)
	}
}

func TestSessionStartIgnoresSupersededSession(t *testing.T) {
	f := newFleet(t)
	f.task(t, "live", task.Running)
	old, _ := f.home.AcquireLock(home.Owner{LaunchFolder: absPath("/a")}, false)
	cur, _ := f.home.AcquireLock(home.Owner{LaunchFolder: absPath("/b")}, true)
	if ctx := runSessionStart(t, f, old.Token, "startup"); ctx != "" {
		t.Fatalf("superseded session got %q", ctx)
	}
	if ctx := runSessionStart(t, f, cur.Token, "startup"); ctx == "" {
		t.Fatal("current session got nothing")
	}
}
