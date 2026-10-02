package home

import "time"

func ProcessStart(pid int) time.Time {
	_, start := probe(pid)
	return start
}

func ProcessLive(pid int, start time.Time) bool {
	if pid <= 0 {
		return false
	}
	return Lock{PID: pid, StartTime: start}.Live()
}
