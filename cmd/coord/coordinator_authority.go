package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/supervise"
	"github.com/spf13/cobra"
)

var openCommands = []string{
	"status", "show", "projects", "project list", "config show", "watch", "wait", "help",
	"report", "_supervise", "_guard", "_hook", "_stop-hook", "_statusline", "_coordinator-guard", "_watch",
}

func commandPath(cmd *cobra.Command) string {
	var parts []string
	for c := cmd; c.HasParent(); c = c.Parent() {
		parts = append([]string{c.Name()}, parts...)
	}
	return strings.Join(parts, " ")
}

func isOpen(cmd *cobra.Command) bool {
	p := commandPath(cmd)
	return slices.ContainsFunc(openCommands, func(o string) bool { return p == o || strings.HasPrefix(p, o+" ") })
}

func coordManaged() bool {
	for _, k := range []string{home.EnvToken, supervise.EnvTask, supervise.EnvRole, envDetached} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

var captainTerminal = processTerminal

func captainAtTerminal() bool {
	return !coordManaged() && captainTerminal()
}

func requireAuthority(a *app, cmd *cobra.Command) error {
	if !cmd.HasParent() {
		if coordManaged() {
			return errors.New("coord cannot start a Coordinator from inside a Coordinator or a Worker; run it in your own terminal")
		}
		if !isTerminal(cmd.InOrStdin()) {
			return errors.New("coord starts the Coordinator only from an interactive terminal")
		}
		return nil
	}
	if isOpen(cmd) {
		return nil
	}
	if tok := os.Getenv(home.EnvToken); tok != "" {
		return a.home.CheckLiveToken(tok)
	}
	if captainAtTerminal() {
		return nil
	}
	return fmt.Errorf("coord %s changes the Fleet; only the live Coordinator or the Captain at a terminal can run it", commandPath(cmd))
}

func msysPTYName(name string) bool {
	p := strings.Split(strings.TrimPrefix(name, `\`), "-")
	return len(p) >= 5 && (p[0] == "msys" || p[0] == "cygwin") && strings.HasPrefix(p[2], "pty") &&
		(p[3] == "from" || p[3] == "to") && p[4] == "master"
}
