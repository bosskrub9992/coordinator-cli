package task

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
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

type Move struct {
	Command string
	When    string
	Classes []config.Class
	From    QuestionSource
}

func (m Move) For(id ID) string {
	return strings.ReplaceAll(m.Command, "<task>", string(id))
}

func (m Move) Verb() string {
	f := strings.Fields(m.Command)
	if len(f) < 2 || f[0] != "coord" {
		return ""
	}
	if f[1] == "task" && len(f) > 2 {
		return "task " + f[2]
	}
	return f[1]
}

type StateInfo struct {
	State    State
	Meaning  string
	Next     []State
	Terminal bool
	Moves    []Move
}

var ship = []config.Class{config.Ship}

var DropMove = Move{Command: "coord drop <task>", When: "on the Captain's word; add `--close-mr` only when they said to close the MR"}

var (
	followUp  = Move{Command: "coord steer <task> <message>", When: "a follow-up; resumes the same Worker session"}
	mergeMove = Move{Command: "coord merge <task>", When: "on the Captain's word; merges the open MRs in linked order", Classes: ship}
	addMR     = Move{Command: "coord task add-mr <task> <url>", When: "link another MR, such as a CI-opened deploy-config MR", Classes: ship}
	landShip  = Move{Command: "coord land <task>", When: "once the SOP is finished (for example: deployed and post-checked)", Classes: ship}
)

var stateTable = []StateInfo{
	{Queued, "Task and Brief exist; no Worker has started yet", []State{Running, Dropped, Failed}, false, []Move{
		{Command: "coord spawn <task>", When: "start its Worker"},
	}},
	{Running, "a Worker is working on the Task", []State{Blocked, NeedsDecision, WaitingReview, Merged, Reported, Failed, Dropped}, false, []Move{
		{Command: "coord steer <task> <message>", When: "correct or add to the Brief"},
		{Command: "coord interrupt <task>", When: "halt its current turn; it waits for a steer"},
		{Command: "coord stop <task>", When: "end the Worker; the Task goes failed and can be resumed"},
	}},
	{Blocked, "the Worker cannot go on until the Coordinator answers or an outside wait ends (usage-limit reset, another Task)", []State{Running, NeedsDecision, Failed, Dropped}, false, []Move{
		{Command: "coord steer <task> <answer>", When: "answer it; a refused call needs the Captain's go-ahead, a usage limit its reset"},
	}},
	{NeedsDecision, "the Worker asked for the Captain's word (a product decision, a Plan, a merge or a discard), or the watcher raised MR facts", []State{Running, Blocked, WaitingReview, Merged, Reported, Landed, Failed, Dropped}, false, []Move{
		{Command: "coord steer <task> <answer>", When: "answer the question or approve the Plan", From: QuestionFromWorker},
		{Command: "coord steer <task> <message>", When: "resume the Worker for the fixes the Captain agreed to", From: QuestionFromWatcher},
		{Command: "coord ack <task>", When: "the facts are handled without the Worker", From: QuestionFromWatcher},
		{Command: mergeMove.Command, When: mergeMove.When, Classes: ship, From: QuestionFromWatcher},
	}},
	{WaitingReview, "a ship Task's MRs are up; the Worker has exited and the watcher polls them", []State{Running, NeedsDecision, Merged, Reported, Landed, Failed, Dropped}, false, []Move{
		mergeMove,
		followUp,
		addMR,
	}},
	{Merged, "all of a ship Task's MRs are merged; the SOP's after-merge steps (such as the deploy) remain", []State{Running, NeedsDecision, WaitingReview, Landed, Dropped, Failed}, false, []Move{
		{Command: "coord steer <task> <message>", When: "the after-merge steps, when the SOP or the Captain says so"},
		addMR,
		landShip,
	}},
	{Reported, "the Report is written (scout, review-code, or a ship Task with no MR)", []State{Running, NeedsDecision, WaitingReview, Merged, Landed, Failed, Dropped}, false, []Move{
		{Command: "coord land <task>", When: "in the same turn you give the Captain the Report's outcome; never ask first", Classes: []config.Class{config.Scout, config.ReviewCode}},
		landShip,
		followUp,
		addMR,
	}},
	{Landed, "finished: a ship Task's SOP is done, or a scout's or review-code's Report has reached the Captain", nil, true, nil},
	{Dropped, "the Captain ended the Task without it landing", nil, true, nil},
	{Failed, "the Worker died, was stopped, or gave up", []State{Running, NeedsDecision, Dropped}, false, []Move{
		{Command: "coord steer <task> <message>", When: "resume the same Worker session; ask the Captain first unless it crashed or lost its supervisor"},
	}},
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

func (t Task) Moves() []Move {
	info, _ := Info(t.State)
	var out []Move
	for _, m := range info.Moves {
		if m.Classes != nil && !slices.Contains(m.Classes, t.Class) {
			continue
		}
		if m.From != "" && m.From != t.QuestionFrom {
			continue
		}
		out = append(out, m)
	}
	if !t.State.Terminal() {
		out = append(out, DropMove)
	}
	return out
}

func (t Task) NextHint() string {
	var cmds []string
	for _, m := range t.Moves() {
		cmds = append(cmds, "`"+m.For(t.ID)+"`")
	}
	if len(cmds) == 0 {
		return fmt.Sprintf("Task %s is %s; nothing moves it on", t.ID, t.State)
	}
	return fmt.Sprintf("Task %s is %s; from here: %s", t.ID, t.State, strings.Join(slices.Compact(cmds), ", "))
}
