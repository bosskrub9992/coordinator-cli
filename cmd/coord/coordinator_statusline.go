package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

func newStatusLineCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:               "_statusline",
		Short:             "Claude Code status line: the Captain's own status line, then one line of Fleet counts",
		Hidden:            true,
		Args:              cobra.NoArgs,
		PersistentPreRunE: homeOnly(a),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, _ := io.ReadAll(cmd.InOrStdin())
			var in statusInput
			json.Unmarshal(input, &in)
			token := os.Getenv(home.EnvToken)
			trackSession(a.home, token, in.SessionID)
			folder := statusFolder(a.home, token, in)
			out := cmd.OutOrStdout()
			if base := captainStatusLine(folder, input); base != "" {
				fmt.Fprintln(out, base)
			}
			line, err := statusLine(a.home, a.tasks(), token, folder)
			if err != nil {
				line = "coord · " + oneLine(err.Error(), 80)
			}
			fmt.Fprintln(out, line)
			return nil
		},
	}
}

type statusInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
}

func statusFolder(h home.Home, token string, in statusInput) string {
	if l, err := h.ReadLock(); err == nil && l != nil && token != "" && l.Token == token && l.LaunchFolder != "" {
		return l.LaunchFolder
	}
	for _, d := range []string{in.Workspace.ProjectDir, in.Workspace.CurrentDir, in.CWD} {
		if d != "" {
			return d
		}
	}
	d, _ := os.Getwd()
	return d
}

func statusLine(h home.Home, s *task.Store, token, folder string) (string, error) {
	l, err := h.ReadLock()
	if err != nil {
		return "", err
	}
	parts := []string{"coord", filepath.Base(folder)}
	if token != "" && (l == nil || l.Token != token) {
		return strings.Join(append(parts, "superseded: another Coordinator holds the Fleet"), " · "), nil
	}
	if _, err := s.ReapLostSupervisors(); err != nil {
		return "", err
	}
	tasks, err := s.List()
	if err != nil {
		return "", err
	}
	var running, needYou, failed, review, merged, queued int
	for _, t := range tasks {
		switch {
		case t.State == task.Running:
			running++
		case t.State == task.NeedsDecision, t.State == task.Blocked:
			needYou++
		case t.State == task.Failed:
			failed++
		case t.State == task.WaitingReview:
			review++
		case t.State == task.Merged:
			merged++
		case t.State == task.Queued:
			queued++
		}
	}
	fleet, err := s.UnreadFleet()
	if err != nil {
		return "", err
	}
	needLabel := "need you"
	if needYou == 1 {
		needLabel = "needs you"
	}
	n := len(parts)
	for _, c := range []struct {
		n     int
		label string
	}{{running, "running"}, {needYou, needLabel}, {failed, "failed"}, {review, "in review"}, {merged, "merged"}, {queued, "queued"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.label))
		}
	}
	if slices.ContainsFunc(fleet, func(e task.Event) bool { return e.Type == task.EventUnsupervised }) {
		parts = append(parts, "unsupervised: arm coord wait")
	}
	if len(parts) == n {
		parts = append(parts, "no Tasks in flight")
	}
	return strings.Join(parts, " · "), nil
}
