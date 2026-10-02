package task

import (
	"fmt"
	"slices"
)

type State string

const (
	Queued        State = "queued"
	Running       State = "running"
	Blocked       State = "blocked"
	NeedsDecision State = "needs-decision"
	WaitingReview State = "waiting-review"
	Merged        State = "merged"
	Reported      State = "reported"
	Landed        State = "landed"
	Dropped       State = "dropped"
	Failed        State = "failed"
)

type StateInfo struct {
	State    State
	Meaning  string
	Next     []State
	Terminal bool
}

var stateTable = []StateInfo{
	{Queued, "Task and Brief exist; no Worker has started yet", []State{Running, Dropped, Failed}, false},
	{Running, "a Worker is working on the Task", []State{Blocked, NeedsDecision, WaitingReview, Merged, Reported, Failed, Dropped}, false},
	{Blocked, "the Worker cannot go on until the Coordinator answers or an outside wait ends (usage-limit reset, another Task)", []State{Running, NeedsDecision, Failed, Dropped}, false},
	{NeedsDecision, "the Worker asked for the Captain's word (coord report --status needs-decision): a product decision, a Plan, a merge or a discard; or the watcher raised MR facts for the Coordinator to handle (coord ack)", []State{Running, Blocked, WaitingReview, Merged, Reported, Landed, Failed, Dropped}, false},
	{WaitingReview, "a ship Task's MRs are up; the Worker has exited and the watcher polls them", []State{Running, NeedsDecision, Merged, Reported, Landed, Failed, Dropped}, false},
	{Merged, "all of a ship Task's MRs are merged; the SOP's after-merge steps (such as the deploy) remain", []State{Running, NeedsDecision, WaitingReview, Landed, Dropped, Failed}, false},
	{Reported, "the Report is written (scout, review-code, or a ship Task with no open or merged MR); resuming it for a follow-up runs it again", []State{Running, NeedsDecision, WaitingReview, Merged, Landed, Failed, Dropped}, false},
	{Landed, "the Task's SOP is finished (for example: deployed to prod and post-checked); the Coordinator lands it", nil, true},
	{Dropped, "the Captain ended the Task without it landing", nil, true},
	{Failed, "the Worker died or gave up; the Coordinator decides whether to retry", []State{Running, NeedsDecision, Dropped}, false},
}

func States() []StateInfo { return slices.Clone(stateTable) }

func Info(s State) (StateInfo, bool) {
	for _, i := range stateTable {
		if i.State == s {
			return i, true
		}
	}
	return StateInfo{}, false
}

func ParseState(s string) (State, error) {
	if _, ok := Info(State(s)); ok {
		return State(s), nil
	}
	return "", fmt.Errorf("unknown Task state %q", s)
}

func (s State) Terminal() bool {
	i, ok := Info(s)
	return ok && i.Terminal
}

func CanTransition(from, to State) bool {
	i, ok := Info(from)
	return ok && slices.Contains(i.Next, to)
}
