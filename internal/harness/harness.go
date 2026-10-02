package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type Name string

const (
	Claude Name = "claude"
	Codex  Name = "codex"
	Cursor Name = "cursor"
)

func Supported() []Name { return []Name{Claude} }

func Known() []Name { return []Name{Claude, Codex, Cursor} }

func Check(n Name) error {
	for _, s := range Supported() {
		if n == s {
			return nil
		}
	}
	for _, k := range Known() {
		if n == k {
			return fmt.Errorf("harness %q is not supported yet (supported: %s)", n, Claude)
		}
	}
	return fmt.Errorf("unknown harness %q (supported: %s)", n, Claude)
}

type CoordinatorSpec struct {
	LaunchFolder    string
	SessionID       string
	Resume          bool
	Model           string
	Effort          string
	RolePromptFile  string
	MemoryDir       string
	SettingsFile    string
	PermissionMode  string
	AllowedTools    []string
	DisallowedTools []string
	Env             []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

type CoordinatorExit struct {
	Code      int
	SessionID string
}

type CoordinatorLauncher interface {
	Name() Name
	Launch(ctx context.Context, spec CoordinatorSpec) (CoordinatorExit, error)
}

type WorkerSpec struct {
	TaskID           string
	SessionID        string
	Folder           string
	AddDirs          []string
	Model            string
	Effort           string
	SystemPromptFile string
	PermissionMode   string
	SettingsFile     string
	Env              []string
	Brief            string
	LogPath          string
}

type Steer struct {
	ID   string
	Text string
}

type EventKind string

const (
	EventSessionStarted EventKind = "session-started"
	EventAssistantText  EventKind = "assistant-text"
	EventToolUse        EventKind = "tool-use"
	EventToolResult     EventKind = "tool-result"
	EventSteerDelivered EventKind = "steer-delivered"
	EventSteerDropped   EventKind = "steer-dropped"
	EventTurnEnded      EventKind = "turn-ended"
	EventRateLimited    EventKind = "rate-limited"
	EventStderr         EventKind = "stderr"
	EventExited         EventKind = "exited"
	EventOther          EventKind = "other"
)

type TurnResult struct {
	IsError        bool
	Subtype        string
	TerminalReason string
	Text           string
	NumTurns       int
	Origin         string
	TotalCostUSD   float64
}

type WorkerEvent struct {
	Kind      EventKind
	Time      time.Time
	SessionID string
	Text      string
	ToolName  string
	SteerID   string
	Turn      *TurnResult
	Exit      *WorkerExit
	Raw       json.RawMessage
}

type WorkerExit struct {
	Code   int
	Signal string
	Err    string
}

type Worker interface {
	SessionID() string
	PID() int
	Events() <-chan WorkerEvent
	Steer(ctx context.Context, s Steer) error
	Interrupt(ctx context.Context) error
	Stop(ctx context.Context) error
	Kill() error
	Wait() (WorkerExit, error)
}

type WorkerRunner interface {
	Name() Name
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
	Resume(ctx context.Context, spec WorkerSpec) (Worker, error)
}
