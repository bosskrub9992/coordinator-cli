package task

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

type QuestionSource string

const (
	QuestionFromWorker  QuestionSource = "worker"
	QuestionFromWatcher QuestionSource = "watcher"
)

const AckNote = "acknowledged"

var ErrWorkerLive = errors.New("the Task's Worker is live")

func (s *Store) writeQuestion(id ID, text string) error {
	return home.WriteFileAtomic(s.QuestionPath(id), []byte(strings.TrimRight(text, "\n")+"\n"), 0o644)
}

func (s *Store) SetQuestion(id ID, from QuestionSource, text string) (Task, error) {
	return s.Update(id, func(t *Task) error {
		if err := s.writeQuestion(id, text); err != nil {
			return err
		}
		t.QuestionFrom = from
		return nil
	})
}

func headline(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

func MRSettledState(f MRFile) State {
	if len(f.Asks) > 0 {
		return NeedsDecision
	}
	anyOpen, merged := false, false
	for _, m := range f.MRs {
		anyOpen = anyOpen || m.Open()
		merged = merged || m.Merged()
	}
	switch {
	case anyOpen:
		return WaitingReview
	case merged:
		return Merged
	}
	return Reported
}

func askText(asks []Ask) string {
	parts := make([]string, len(asks))
	for i, a := range asks {
		parts[i] = a.Text
	}
	return strings.Join(parts, "\n\n")
}

func settleable(t Task) bool {
	if t.Class != config.Ship {
		return false
	}
	switch t.State {
	case WaitingReview, Merged, Reported:
		return true
	case NeedsDecision:
		return t.QuestionFrom == QuestionFromWatcher
	}
	return false
}

func (s *Store) Settle(id ID, note string) (Task, error) {
	var evs []Event
	return s.update(id, func(t *Task) error {
		evs = nil
		ev, err := s.settle(t, note)
		if ev != nil {
			evs = append(evs, *ev)
		}
		return err
	}, func(Task) error { return s.appendAll(id, evs) })
}

func (s *Store) appendAll(id ID, evs []Event) error {
	for _, e := range evs {
		if _, err := s.Append(id, e); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) settle(t *Task, note string) (*Event, error) {
	if !settleable(*t) {
		return nil, nil
	}
	f, err := s.ReadMRFile(t.ID)
	if err != nil || len(f.MRs) == 0 {
		return nil, err
	}
	to := MRSettledState(f)
	if to == NeedsDecision {
		if err := s.writeQuestion(t.ID, askText(f.Asks)); err != nil {
			return nil, err
		}
		t.QuestionFrom = QuestionFromWatcher
		if note == "" {
			note = headline(f.Asks[len(f.Asks)-1].Text)
		}
	} else if t.QuestionFrom == QuestionFromWatcher {
		if err := os.Remove(s.QuestionPath(t.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		t.QuestionFrom = ""
	}
	if t.State == to {
		return nil, nil
	}
	if !CanTransition(t.State, to) {
		return nil, fmt.Errorf("Task %s cannot go from %s to %s", t.ID, t.State, to)
	}
	if note == "" {
		note = "MRs " + string(to)
	}
	ev := &Event{Type: EventStateChanged, From: t.State, To: to, Text: note}
	t.State = to
	t.StateSince = s.now()
	return ev, nil
}

func (s *Store) Ack(id ID, note string) (Task, error) {
	var evs []Event
	return s.update(id, func(t *Task) error {
		evs = nil
		w, err := s.Worker(id)
		if err != nil {
			return err
		}
		if w.SupervisorLive() {
			return fmt.Errorf("%w: Task %s's Worker is still running; coord ack is for MR facts on a Task whose Worker has exited", ErrWorkerLive, id)
		}
		f, err := s.ReadMRFile(id)
		if err != nil {
			return err
		}
		if len(f.MRs) == 0 {
			return fmt.Errorf("Task %s has no MRs; coord ack is for the facts the watcher reports about a Task's MRs", id)
		}
		switch t.State {
		case NeedsDecision:
			if t.QuestionFrom != QuestionFromWatcher {
				return fmt.Errorf("Task %s's pending question is the Worker's own; answer it with coord steer %s <message>", id, id)
			}
		case WaitingReview, Merged, Reported:
		default:
			return fmt.Errorf("Task %s is %s; coord ack is for a Task waiting on its MRs or holding the watcher's question", id, t.State)
		}
		if _, err := s.UpdateMRFile(id, func(f *MRFile) error {
			f.Asks = nil
			for i := range f.MRs {
				f.MRs[i].CommentsSinceAck = 0
			}
			return nil
		}); err != nil {
			return err
		}
		text := strings.TrimSpace(note)
		if text == "" {
			text = "MR facts handled"
		}
		evs = append(evs, Event{Type: EventAck, Text: text})
		ev, err := s.settle(t, AckNote)
		if ev != nil {
			evs = append(evs, *ev)
		}
		return err
	}, func(Task) error { return s.appendAll(id, evs) })
}
