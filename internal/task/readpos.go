package task

import (
	"encoding/json"
	"slices"
	"sort"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

const FleetKey ID = "_fleet"

type ReadPosition struct {
	Seen      map[ID]int64 `json:"seen"`
	Done      map[ID]bool  `json:"done,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}

func (s *Store) ReadPosition() (ReadPosition, error) {
	var rp ReadPosition
	if _, err := home.ReadJSON(s.home.ReadPositionPath(), &rp); err != nil {
		return ReadPosition{}, err
	}
	if rp.Seen == nil {
		rp.Seen = map[ID]int64{}
	}
	if rp.Done == nil {
		rp.Done = map[ID]bool{}
	}
	return rp, nil
}

func readKey(id ID) ID {
	if id == "" {
		return FleetKey
	}
	return id
}

func eventsAfter(l home.Log, after int64) ([]Event, error) {
	var out []Event
	err := l.Reverse(func(line []byte) bool {
		var e Event
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		if e.Seq <= after {
			return false
		}
		out = append(out, e)
		return true
	})
	slices.Reverse(out)
	return out, err
}

func (s *Store) Unread() ([]Event, error) {
	rp, err := s.ReadPosition()
	if err != nil {
		return nil, err
	}
	ids, err := s.ids()
	if err != nil {
		return nil, err
	}
	out, err := eventsAfter(s.fleetLog(), rp.Seen[FleetKey])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if rp.Done[id] {
			continue
		}
		evs, err := eventsAfter(s.log(id), rp.Seen[id])
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		if out[i].Task != out[j].Task {
			return out[i].Task.Number() < out[j].Task.Number()
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

func (s *Store) MarkRead(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	return home.WithFileLock(s.home.ReadPositionPath()+".lck", func() error {
		rp, err := s.ReadPosition()
		if err != nil {
			return err
		}
		touched := map[ID]bool{}
		for _, e := range events {
			k := readKey(e.Task)
			if e.Seq > rp.Seen[k] {
				rp.Seen[k] = e.Seq
			}
			if e.Task != "" {
				touched[e.Task] = true
			}
		}
		for id := range touched {
			if s.closed(id, rp.Seen[id]) {
				rp.Done[id] = true
			}
		}
		rp.UpdatedAt = s.now()
		return home.WriteJSONAtomic(s.home.ReadPositionPath(), rp)
	})
}

func (s *Store) closed(id ID, seen int64) bool {
	t, err := s.Get(id)
	if err != nil || !t.State.Terminal() {
		return false
	}
	if w, err := s.Worker(id); err != nil || w.SupervisorLive() {
		return false
	}
	last, err := s.log(id).Last()
	if err != nil {
		return false
	}
	var e Event
	if len(last) > 0 && json.Unmarshal(last, &e) != nil {
		return false
	}
	return e.Seq <= seen
}

func (s *Store) Backward(id ID, fn func(Event) bool) error {
	l := s.fleetLog()
	if id != "" {
		l = s.log(id)
	}
	return l.Reverse(func(line []byte) bool {
		var e Event
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		return fn(e)
	})
}
