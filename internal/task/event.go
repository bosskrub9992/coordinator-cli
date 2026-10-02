package task

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventCreated       EventType = "created"
	EventStateChanged  EventType = "state"
	EventWorkerStarted EventType = "worker-started"
	EventWorkerExited  EventType = "worker-exited"
	EventSteer         EventType = "steer"
	EventQuestion      EventType = "question"
	EventReport        EventType = "report"
	EventNote          EventType = "note"
)

type Event struct {
	Seq  int64           `json:"seq"`
	Time time.Time       `json:"time"`
	Task ID              `json:"task"`
	Type EventType       `json:"type"`
	From State           `json:"from,omitempty"`
	To   State           `json:"to,omitempty"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}
