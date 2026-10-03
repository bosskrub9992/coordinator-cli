package coordinator

import (
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/shellquote"
)

const StopHookTimeout = 10

const GuardTimeout = 10

const SessionStartTimeout = 10

const GuardMatcher = "Edit|Write|MultiEdit"

type Settings struct {
	AutoMemoryDirectory string                 `json:"autoMemoryDirectory"`
	StatusLine          StatusLine             `json:"statusLine"`
	Hooks               map[string][]HookGroup `json:"hooks"`
}

type StatusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type HookGroup struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []Hook `json:"hooks"`
}

type Hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func BuildSettings(coordBin, memoryDir string) Settings {
	bin := shellquote.Quote(coordBin)
	return Settings{
		AutoMemoryDirectory: memoryDir,
		StatusLine:          StatusLine{Type: "command", Command: bin + " _statusline"},
		Hooks: map[string][]HookGroup{
			"Stop":         {{Hooks: []Hook{{Type: "command", Command: bin + " _stop-hook", Timeout: StopHookTimeout}}}},
			"SessionStart": {{Hooks: []Hook{{Type: "command", Command: bin + " _session-start", Timeout: SessionStartTimeout}}}},
			"PreToolUse":   {{Matcher: GuardMatcher, Hooks: []Hook{{Type: "command", Command: bin + " _coordinator-guard", Timeout: GuardTimeout}}}},
		},
	}
}

func WriteSettings(path string, s Settings) error {
	return home.WriteJSONAtomic(path, s)
}
