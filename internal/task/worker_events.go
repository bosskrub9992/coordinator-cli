package task

const (
	EventRateLimited        EventType = "rate-limited"
	EventInstructionsLoaded EventType = "instructions-loaded"
	EventSteerCancelled     EventType = "steer-cancelled"
	EventWorktreeReturned   EventType = "worktree-returned"
	EventAck                EventType = "ack"
	EventMRLinked           EventType = "mr-linked"
	EventDroppedWork        EventType = "dropped-work"
	EventPlan               EventType = "plan"
)
