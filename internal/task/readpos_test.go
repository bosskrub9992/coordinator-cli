package task

import (
	"os"
	"testing"
)

func TestUnreadIncludesFleetEvents(t *testing.T) {
	s, launch := newStore(t)
	tk := create(t, s, launch, "a")
	if _, err := s.AppendFleet(Event{Type: EventUnsupervised, Text: "nobody is watching"}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Unread()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || count(evs, EventUnsupervised) != 1 {
		t.Fatalf("unread %+v", evs)
	}
	if err := s.MarkRead(evs); err != nil {
		t.Fatal(err)
	}
	rp, _ := s.ReadPosition()
	if rp.Seen[FleetKey] != 1 || rp.Seen[tk.ID] != 1 {
		t.Fatalf("read position %+v", rp)
	}
	if evs, _ := s.Unread(); len(evs) != 0 {
		t.Fatalf("fleet event delivered twice: %+v", evs)
	}
	s.AppendFleet(Event{Type: EventUnsupervised, Text: "again"})
	if evs, _ := s.Unread(); len(evs) != 1 || evs[0].Seq != 2 || evs[0].Task != "" {
		t.Fatalf("second fleet event %+v", evs)
	}
}

func TestUnreadSkipsClosedTasks(t *testing.T) {
	s, launch := newStore(t)
	open := create(t, s, launch, "open")
	gone := create(t, s, launch, "gone")
	s.Transition(gone.ID, Dropped, "")
	evs, _ := s.Unread()
	if err := s.MarkRead(evs); err != nil {
		t.Fatal(err)
	}
	rp, _ := s.ReadPosition()
	if !rp.Done[gone.ID] || rp.Done[open.ID] {
		t.Fatalf("done %+v", rp.Done)
	}
	if err := os.WriteFile(s.EventsPath(gone.ID), []byte("{\"seq\":99,\"task\":\""+string(gone.ID)+"\",\"type\":\"note\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Append(open.ID, Event{Type: EventNote, Text: "x"})
	evs, _ = s.Unread()
	if len(evs) != 1 || evs[0].Task != open.ID {
		t.Fatalf("unread %+v", evs)
	}
}
