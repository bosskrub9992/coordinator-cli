package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

const maxStopBlocks = 1

type stopHookInput struct {
	SessionID       string           `json:"session_id"`
	StopHookActive  bool             `json:"stop_hook_active"`
	BackgroundTasks []backgroundTask `json:"background_tasks"`
}

type backgroundTask struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Command string `json:"command"`
}

type stopHookState struct {
	SessionID string `json:"session_id"`
	Blocks    int    `json:"blocks"`
}

type stopDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func homeOnly(a *app) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		h, err := home.Resolve()
		if err != nil {
			return err
		}
		if err := h.Ensure(); err != nil {
			return err
		}
		a.home = h
		return nil
	}
}

func newStopHookCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "_stop-hook",
		Short:             "Claude Code Stop hook: keep a coord wait armed while Tasks are in flight",
		Hidden:            true,
		Args:              cobra.NoArgs,
		PersistentPreRunE: homeOnly(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			ensureWatcher(a)
			return stopHook(a.home, a.tasks(), os.Getenv(home.EnvToken), cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

func stopHookStatePath(h home.Home) string {
	return filepath.Join(h.CoordinatorRunDir(), "stop-hook.json")
}

func stopHook(h home.Home, s *task.Store, token string, stdin io.Reader, stdout io.Writer) error {
	var in stopHookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	if token != "" && h.CheckToken(token) != nil {
		return nil
	}
	trackSession(h, token, in.SessionID)
	if err := os.MkdirAll(h.CoordinatorRunDir(), 0o755); err != nil {
		return err
	}
	if _, err := s.ReapLostSupervisors(); err != nil {
		return err
	}
	n, err := tasksInFlight(&app{home: h})
	if err != nil {
		return err
	}
	block := n > 0 && !waitArmed(in.BackgroundTasks)
	unread := 0
	if block {
		if unread, err = unreadWaking(s); err != nil {
			return err
		}
	}
	var decision *stopDecision
	err = home.WithFileLock(stopHookStatePath(h)+".lck", func() error {
		var st stopHookState
		if _, err := home.ReadJSON(stopHookStatePath(h), &st); err != nil {
			st = stopHookState{}
		}
		if st.SessionID != in.SessionID || !in.StopHookActive {
			st = stopHookState{SessionID: in.SessionID}
		}
		switch {
		case !block:
			st.Blocks = 0
		case st.Blocks >= maxStopBlocks:
			st.Blocks = 0
			if _, err := s.AppendFleet(task.Event{
				Type: task.EventUnsupervised,
				Text: fmt.Sprintf("the Coordinator (session %s) ended its turn without arming coord wait after a reminder; %s in flight are unsupervised", in.SessionID, plural(n, "Task")),
			}); err != nil {
				return err
			}
		default:
			st.Blocks++
			decision = &stopDecision{Decision: "block", Reason: stopReason(n, unread)}
		}
		return home.WriteJSONAtomic(stopHookStatePath(h), st)
	})
	if err != nil {
		return err
	}
	if decision != nil {
		return json.NewEncoder(stdout).Encode(decision)
	}
	return nil
}

func stopReason(inFlight, unread int) string {
	if unread > 0 {
		return fmt.Sprintf("coord: %s waiting for you and no coord wait is armed. Run `coord wait` with run_in_background=true now; it returns them at once. Handle them, re-arm, then repeat your update to the Captain in your final message.",
			plural(unread, "unread event"))
	}
	return fmt.Sprintf("coord: %s in flight and no coord wait is armed. Arm `coord wait` with run_in_background=true, then repeat your update to the Captain in your final message, then end your turn.",
		plural(inFlight, "Task"))
}

func unreadWaking(s *task.Store) (int, error) {
	evs, err := s.Unread()
	n := 0
	for _, e := range evs {
		if !quiet(e) {
			n++
		}
	}
	return n, err
}

func waitArmed(bg []backgroundTask) bool {
	for _, t := range bg {
		if t.Type == "shell" && t.Status == "running" && isCoordWait(t.Command) {
			return true
		}
	}
	return false
}

func isCoordWait(command string) bool {
	bin, rest := firstWord(strings.TrimSpace(command))
	if f := strings.Fields(rest); len(f) == 0 || f[0] != "wait" {
		return false
	}
	if i := strings.LastIndexAny(bin, `/\`); i >= 0 {
		bin = bin[i+1:]
	}
	return strings.TrimSuffix(bin, ".exe") == "coord"
}

func firstWord(s string) (string, string) {
	if s != "" && (s[0] == '\'' || s[0] == '"') {
		if end := strings.IndexByte(s[1:], s[0]); end >= 0 {
			return s[1 : end+1], s[end+2:]
		}
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}
