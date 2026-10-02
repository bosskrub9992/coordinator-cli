package task

import (
	"encoding/json"
	"fmt"
	"time"
)

const ReasonSupervisorLost = "supervisor-lost"

func (s State) Supervised() bool {
	switch s {
	case Queued, Running, Blocked, NeedsDecision, WaitingReview, Merged:
		return true
	}
	return false
}

func (s State) holdsSupervisor() bool {
	switch s {
	case Running, Blocked, NeedsDecision:
		return true
	}
	return false
}

func lost(t Task, w WorkerRecord) bool {
	if t.State == NeedsDecision && t.QuestionFrom == QuestionFromWatcher {
		return false
	}
	return t.State.holdsSupervisor() && w.SupervisorPID != 0 && !w.SupervisorLive()
}

func (s *Store) ReapLostSupervisors() ([]ID, error) {
	rp, err := s.ReadPosition()
	if err != nil {
		return nil, err
	}
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	var reaped []ID
	for _, id := range ids {
		if rp.Done[id] {
			continue
		}
		ok, err := s.reapLost(id)
		if err != nil {
			return reaped, err
		}
		if ok {
			reaped = append(reaped, id)
		}
	}
	return reaped, nil
}

func (s *Store) reapLost(id ID) (bool, error) {
	t, err := s.Get(id)
	if err != nil || !t.State.holdsSupervisor() || (t.State == NeedsDecision && t.QuestionFrom == QuestionFromWatcher) {
		return false, nil
	}
	if w, err := s.Worker(id); err != nil || !lost(t, w) {
		return false, err
	}
	reaped := false
	_, err = s.Update(id, func(t *Task) error {
		var pid int
		_, err := s.UpdateWorker(id, func(w *WorkerRecord) error {
			if !lost(*t, *w) {
				return nil
			}
			pid = w.SupervisorPID
			w.SupervisorPID = 0
			w.SupervisorStart = time.Time{}
			w.WorkerPID = 0
			w.ExitedAt = s.now()
			reaped = true
			return nil
		})
		if err != nil || !reaped {
			return err
		}
		data, _ := json.Marshal(map[string]any{"reason": ReasonSupervisorLost, "supervisor_pid": pid})
		if _, err := s.Append(id, Event{Type: EventWorkerExited, Text: fmt.Sprintf("the supervisor (pid %d) is gone; the Worker is no longer supervised", pid), Data: data}); err != nil {
			return err
		}
		if !CanTransition(t.State, Failed) {
			return nil
		}
		if _, err := s.Append(id, Event{Type: EventStateChanged, From: t.State, To: Failed, Text: ReasonSupervisorLost}); err != nil {
			return err
		}
		t.State = Failed
		t.StateSince = s.now()
		return nil
	})
	return reaped, err
}
