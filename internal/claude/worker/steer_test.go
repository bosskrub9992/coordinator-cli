package worker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

func testProc(grace time.Duration, pending ...harness.Steer) *proc {
	return &proc{grace: grace, events: make(chan harness.WorkerEvent, 64), log: &logFile{}, pending: pending}
}

func replay(text string) []byte {
	b, _ := json.Marshal(map[string]any{"type": "user", "isReplay": true, "message": map[string]any{"role": "user", "content": text}})
	return b
}

var resultLine = []byte(`{"type":"result","subtype":"success","is_error":false,"result":"ok"}`)

func drain(w *proc, wait time.Duration) []harness.WorkerEvent {
	var out []harness.WorkerEvent
	deadline := time.After(wait)
	for {
		select {
		case e := <-w.events:
			out = append(out, e)
		case <-deadline:
			return out
		}
	}
}

func ofKind(evs []harness.WorkerEvent, k harness.EventKind) []harness.WorkerEvent {
	var out []harness.WorkerEvent
	for _, e := range evs {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func TestReplayReceiptsAreFIFO(t *testing.T) {
	w := testProc(time.Hour, harness.Steer{ID: "a", Text: "first"}, harness.Steer{ID: "b", Text: "second"}, harness.Steer{ID: "c", Text: "third"})
	w.handleLine(replay("first, reworded by the harness"))
	w.handleLine(replay("third"))
	got := ofKind(drain(w, 50*time.Millisecond), harness.EventSteerDelivered)
	if len(got) != 2 || got[0].SteerID != "a" || got[1].SteerID != "c" {
		t.Fatalf("receipts %+v", got)
	}
	if len(w.pending) != 1 || w.pending[0].ID != "b" {
		t.Fatalf("pending %+v", w.pending)
	}
}

func TestMergedReplayDeliversEveryPart(t *testing.T) {
	w := testProc(time.Hour, harness.Steer{ID: "a", Text: "one"}, harness.Steer{ID: "b", Text: "two"})
	w.handleLine(replay("one\ntwo"))
	got := ofKind(drain(w, 50*time.Millisecond), harness.EventSteerDelivered)
	if len(got) != 2 || got[0].SteerID != "a" || got[1].SteerID != "b" || len(w.pending) != 0 {
		t.Fatalf("receipts %+v pending %+v", got, w.pending)
	}
}

func TestUnreadSteersDropAfterResultGrace(t *testing.T) {
	w := testProc(30*time.Millisecond, harness.Steer{ID: "a", Text: "late"})
	w.handleLine(resultLine)
	got := ofKind(drain(w, 150*time.Millisecond), harness.EventSteerDropped)
	if len(got) != 1 || got[0].SteerID != "a" || len(w.pending) != 0 {
		t.Fatalf("dropped %+v pending %+v", got, w.pending)
	}
}

func TestReplayWithinGraceKeepsSteers(t *testing.T) {
	w := testProc(80*time.Millisecond, harness.Steer{ID: "a", Text: "next"}, harness.Steer{ID: "b", Text: "after"})
	w.handleLine(resultLine)
	w.handleLine(replay("next"))
	evs := drain(w, 200*time.Millisecond)
	if d := ofKind(evs, harness.EventSteerDropped); len(d) != 0 {
		t.Fatalf("dropped %+v although the next turn started", d)
	}
	if len(w.pending) != 1 || w.pending[0].ID != "b" {
		t.Fatalf("pending %+v", w.pending)
	}
}

func TestInterruptAckArmsGrace(t *testing.T) {
	w := testProc(30*time.Millisecond, harness.Steer{ID: "a", Text: "queued"})
	w.interrupts = map[string]bool{"coord_int_1": true}
	w.handleLine([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{}}}`))
	if d := ofKind(drain(w, 80*time.Millisecond), harness.EventSteerDropped); len(d) != 0 {
		t.Fatalf("an unrelated control_response dropped steers: %+v", d)
	}
	w.handleLine([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"coord_int_1","response":{"still_queued":[]}}}`))
	if d := ofKind(drain(w, 150*time.Millisecond), harness.EventSteerDropped); len(d) != 1 {
		t.Fatalf("dropped %+v", d)
	}
}

func TestEmitAfterCloseIsSafe(t *testing.T) {
	w := testProc(time.Millisecond, harness.Steer{ID: "a", Text: "x"})
	w.closeEvents()
	w.handleLine(resultLine)
	time.Sleep(30 * time.Millisecond)
}
