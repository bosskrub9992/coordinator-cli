package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

type sessionStartInput struct {
	SessionID string `json:"session_id"`
	Source    string `json:"source"`
}

type sessionStartOutput struct {
	HookSpecificOutput sessionStartContext `json:"hookSpecificOutput"`
}

type sessionStartContext struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

func newSessionStartCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "_session-start",
		Short:             "Claude Code SessionStart hook: give the Coordinator the Fleet when its session starts",
		Hidden:            true,
		Args:              cobra.NoArgs,
		PersistentPreRunE: homeOnly(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			return sessionStart(a.home, a.tasks(), os.Getenv(home.EnvToken), cmd.InOrStdin(), cmd.OutOrStdout(), time.Now())
		},
	}
}

func sessionStart(h home.Home, s *task.Store, token string, stdin io.Reader, stdout io.Writer, now time.Time) error {
	var in sessionStartInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return nil
	}
	if token != "" && h.CheckToken(token) != nil {
		return nil
	}
	trackSession(h, token, in.SessionID)
	text, err := fleetContext(h, s, in.Source != "compact", now)
	if err != nil {
		text = fmt.Sprintf("coord could not read the Fleet as this session starts: %v\nRun `coord status`, then open your first reply to the Captain with a short recap and the exact error.", err)
	}
	return json.NewEncoder(stdout).Encode(sessionStartOutput{HookSpecificOutput: sessionStartContext{
		HookEventName:     "SessionStart",
		AdditionalContext: strings.TrimRight(text, "\n"),
	}})
}

func fleetContext(h home.Home, s *task.Store, recap bool, now time.Time) (string, error) {
	v, err := buildStatus(h, false)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	b.WriteString("coord: the Fleet as this session starts (`coord status`):\n")
	if err := printStatus(&b, v, now); err != nil {
		return "", err
	}
	unread, err := unreadWaking(s)
	if err != nil {
		return "", err
	}
	if unread > 0 {
		fmt.Fprintf(&b, "%s not read yet; `coord wait` in the background returns them at once.\n", plural(unread, "event"))
	}
	if recap && len(v.Tasks) > 0 {
		b.WriteString("Open your first reply to the Captain, whatever they ask, with a short recap of these Tasks and what each needs from them; arm `coord wait` if any is in flight.\n")
	}
	return b.String(), nil
}
