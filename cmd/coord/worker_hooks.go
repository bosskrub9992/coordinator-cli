package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/spf13/cobra"
)

func newGuardCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "_guard <task>",
		Short:  "PreToolUse hook: let Edit/Write touch only the Task's worktrees and Task folder; temp goes through the normal permission flow",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, _ := io.ReadAll(cmd.InOrStdin())
			var scope supervise.GuardScope
			s := a.tasks()
			if t, err := s.Find(args[0]); err == nil {
				if w, err := s.Worker(t.ID); err == nil {
					scope = supervise.Scope(t, w, mainCheckouts(a))
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(supervise.GuardResponse(input, scope)))
			return nil
		},
	}
}

func mainCheckouts(a *app) []string {
	ps, _ := a.projects().List()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Path)
	}
	return out
}

func newHookCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "_hook",
		Short:  "Harness hooks for Workers",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "instructions <task>",
		Short: "InstructionsLoaded hook: record which instruction files the Worker loaded",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, _ := io.ReadAll(cmd.InOrStdin())
			input = bytes.TrimSpace(input)
			s := a.tasks()
			t, err := s.Find(args[0])
			if err != nil || !json.Valid(input) {
				return nil
			}
			home.Log{Path: s.InstructionsPath(t.ID)}.Append(json.RawMessage(input))
			return nil
		},
	})
	return cmd
}
