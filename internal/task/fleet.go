package task

import (
	"encoding/json"
	"fmt"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

const EventUnsupervised EventType = "unsupervised"

func (s *Store) fleetLog() home.Log { return home.Log{Path: s.home.FleetEventsPath()} }

func (s *Store) AppendFleet(e Event) (Event, error) {
	err := s.fleetLog().AppendWith(func(last []byte) (any, error) {
		var prev Event
		if len(last) > 0 {
			if err := json.Unmarshal(last, &prev); err != nil {
				return nil, fmt.Errorf("read last Fleet event: %w", err)
			}
		}
		e.Seq = prev.Seq + 1
		e.Task = ""
		if e.Time.IsZero() {
			e.Time = s.now()
		}
		return e, nil
	})
	return e, err
}

func (s *Store) FleetEvents(afterSeq int64) ([]Event, error) {
	var out []Event
	err := s.fleetLog().Each(func(line []byte) error {
		var e Event
		if json.Unmarshal(line, &e) == nil && e.Seq > afterSeq {
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

func (s *Store) UnreadFleet() ([]Event, error) {
	rp, err := s.ReadPosition()
	if err != nil {
		return nil, err
	}
	return eventsAfter(s.fleetLog(), rp.Seen[FleetKey])
}
