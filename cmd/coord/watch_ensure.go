package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/bosskrub9992/coordinator-cli/internal/watcher"
	"github.com/spf13/cobra"
)

var watcherStarter = startWatcher

func ensureWatcher(a *app) error {
	_, err := watcher.Ensure(a.home, func() error { return watcherStarter(a) })
	return err
}

func watchAfter(a *app, cmd *cobra.Command) {
	if err := ensureWatcher(a); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "coord: warning: the MR watcher is not running:", err)
	}
}

func watcherEnv(parent []string) []string {
	return envlist.Filter(parent, func(k string) bool {
		return envlist.Same(k, home.EnvToken) || envlist.Same(k, supervise.EnvTask) || envlist.Same(k, supervise.EnvRole)
	})
}

func startWatcher(a *app) error {
	bin, err := coordBinary()
	if err != nil {
		return err
	}
	if _, err := startDetached(detachSpec{Bin: bin, Args: []string{"_watch"}, Dir: a.home.Root, Env: watcherEnv(os.Environ()), Log: watcher.LogPath(a.home)}); err != nil {
		return fmt.Errorf("start the MR watcher: %w", err)
	}
	return nil
}

func newWatchMRsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:    "_watch",
		Short:  "Poll every open MR of the Fleet until none is left (started on demand by coord)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := log.New(cmd.ErrOrStderr(), "watcher ", log.LstdFlags|log.Lmicroseconds)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			w := &watcher.Watcher{
				Home:   a.home,
				Store:  a.tasks(),
				Client: mrwatch.NewClients().For,
				Interval: func() time.Duration {
					cfg, err := a.config()
					if err != nil {
						logger.Printf("config: %v", err)
					}
					return cfg.PollInterval()
				},
				Now:   time.Now,
				Sleep: watcher.SleepContext,
				Logf:  logger.Printf,
			}
			err := w.Run(ctx)
			if errors.Is(err, watcher.ErrRunning) {
				return nil
			}
			return err
		},
	}
}
