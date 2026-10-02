package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/bosskrub9992/coordinator-cli/internal/config"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/project"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
	"github.com/spf13/cobra"
)

type app struct {
	home home.Home
}

func (a *app) config() (config.Config, error) { return config.Load(a.home.ConfigPath()) }
func (a *app) projects() *project.Registry    { return project.NewRegistry(a.home) }
func (a *app) tasks() *task.Store             { return newStore(a.home) }

func newRoot() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "coord",
		Short:         "coordinator-cli: the Coordinator's Fleet of Tasks and Workers",
		Long:          "coordinator-cli keeps the Fleet (Projects, Tasks, Briefs, Reports) in the Home (" + home.EnvHome + " or ~/" + home.DirName + ").",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "help" {
				return nil
			}
			h, err := home.Resolve()
			if err != nil {
				return err
			}
			if err := h.Ensure(); err != nil {
				return err
			}
			a.home = h
			if err := authorize(a, cmd); err != nil {
				return err
			}
			if cmd.HasParent() {
				if err := h.CheckToken(os.Getenv(home.EnvToken)); err != nil {
					return err
				}
			}
			return requireAuthority(a, cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLaunch(a, cmd, args)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(newProjectCmd(a), newProjectsCmd(a), newStatusCmd(a), newConfigCmd(a), newTaskCmd(a), newShowCmd(a), newWatchMRsCmd(a))
	addLaunchFlags(root)
	root.AddCommand(coordinatorCommands(a)...)
	root.AddCommand(workerCommands(a)...)
	return root
}

func cwd() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("current folder: %w", err)
	}
	return dir, nil
}

func baseName(p string) string {
	return filepath.Base(p)
}
