package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/bosskrub9992/coordinator-cli/internal/project"
	"github.com/spf13/cobra"
)

func newProjectCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Register and list Projects (git checkouts Workers get worktrees of)",
	}
	cmd.AddCommand(newProjectAddCmd(a), newProjectListCmd(a, "list"))
	return cmd
}

func newProjectsCmd(a *app) *cobra.Command {
	c := newProjectListCmd(a, "projects")
	c.Short = "List Projects (same as coord project list)"
	return c
}

func newProjectAddCmd(a *app) *cobra.Command {
	var name, host string
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Register the git checkout at path (default: current folder) as a Project",
		Long: "Register the main checkout containing path as a Project. A linked worktree resolves to its main checkout.\n" +
			"The code host comes from the origin remote: hosts containing \"github\" are GitHub, all others GitLab.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := cwd()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				dir = args[0]
			}
			in, err := project.Inspect(dir)
			if err != nil {
				return err
			}
			if host != "" {
				kind, err := project.ParseCodeHostKind(host)
				if err != nil {
					return err
				}
				in.CodeHost.Kind = kind
			}
			if name == "" {
				name = baseName(in.Path)
			}
			p := project.Project{Name: name, Path: in.Path, Origin: in.Origin, CodeHost: in.CodeHost}
			if err := a.projects().Add(p); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added Project %s: %s (%s %s/%s)\n", p.Name, p.Path, p.CodeHost.Kind, p.CodeHost.Host, p.CodeHost.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Project name (default: folder name)")
	cmd.Flags().StringVar(&host, "code-host", "", "override the detected code host: gitlab or github")
	return cmd
}

func newProjectListCmd(a *app, use string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "List registered Projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ps, err := a.projects().List()
			if err != nil {
				return err
			}
			return printProjects(cmd.OutOrStdout(), ps, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func printProjects(w io.Writer, ps []project.Project, asJSON bool) error {
	if asJSON {
		if ps == nil {
			ps = []project.Project{}
		}
		return writeJSON(w, ps)
	}
	if len(ps) == 0 {
		fmt.Fprintln(w, "No Projects registered. Add one with: coord project add <path>")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tCODE HOST\tPATH")
	for _, p := range ps {
		fmt.Fprintf(tw, "%s\t%s %s/%s\t%s\n", p.Name, p.CodeHost.Kind, p.CodeHost.Host, p.CodeHost.Path, p.Path)
	}
	return tw.Flush()
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
