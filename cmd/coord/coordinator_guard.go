package main

import (
	"fmt"
	"io"

	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/spf13/cobra"
)

func newCoordinatorGuardCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "_coordinator-guard",
		Short:  "PreToolUse hook: let the Coordinator's Edit/Write touch only its own memory folder",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			input, _ := io.ReadAll(cmd.InOrStdin())
			fmt.Fprintln(cmd.OutOrStdout(), string(supervise.CoordinatorGuardResponse(input, a.home.MemoryDir())))
			return nil
		},
	}
}
