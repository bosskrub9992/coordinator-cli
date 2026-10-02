package main

import (
	"github.com/spf13/cobra"
)

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the config (models, efforts, plan approval)",
	}
	cmd.AddCommand(newConfigShowCmd(a))
	return cmd
}

func newConfigShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the effective config as JSON (defaults filled in)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.config()
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), c)
		},
	}
}
