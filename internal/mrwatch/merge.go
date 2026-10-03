package mrwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Method string

const (
	MethodMerge  Method = "merge"
	MethodSquash Method = "squash"
	MethodRebase Method = "rebase"
	MethodAuto   Method = ""
)

func ParseMethod(s string) (Method, error) {
	switch m := Method(strings.ToLower(strings.TrimSpace(s))); m {
	case MethodMerge, MethodSquash, MethodRebase, MethodAuto:
		return m, nil
	}
	return "", fmt.Errorf("unknown merge method %q; use merge, squash or rebase", s)
}

func (m Method) Label() string {
	if m == MethodAuto {
		return "project default"
	}
	return string(m)
}

type Merger struct {
	GLab string
	GH   string
	Env  []string
}

type githubRepo struct {
	AllowMergeCommit bool `json:"mergeCommitAllowed"`
	AllowSquashMerge bool `json:"squashMergeAllowed"`
	AllowRebaseMerge bool `json:"rebaseMergeAllowed"`
}

func (g githubRepo) allowed() []Method {
	var out []Method
	if g.AllowMergeCommit {
		out = append(out, MethodMerge)
	}
	if g.AllowSquashMerge {
		out = append(out, MethodSquash)
	}
	if g.AllowRebaseMerge {
		out = append(out, MethodRebase)
	}
	return out
}

func (m Merger) AllowedMethods(ctx context.Context, r Ref) ([]Method, error) {
	if r.Kind != GitHub {
		return nil, fmt.Errorf("%s: the allowed merge methods are only read for GitHub repositories", r.URL)
	}
	bin := orDefault(m.GH, "gh")
	args := []string{"repo", "view", r.RepoKey(), "--json", "mergeCommitAllowed,squashMergeAllowed,rebaseMergeAllowed"}
	cmd := exec.CommandContext(ctx, bin, args...)
	if m.Env != nil {
		cmd.Env = m.Env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("read the merge methods %s allows: %s is not installed (or not on PATH): %w", r.RepoKey(), bin, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("read the merge methods %s allows: %s %s: %s", r.RepoKey(), bin, strings.Join(args, " "), msg)
	}
	var repo githubRepo
	if err := json.Unmarshal(out, &repo); err != nil {
		return nil, fmt.Errorf("read the merge methods %s allows: %w", r.RepoKey(), err)
	}
	return repo.allowed(), nil
}

func (m Merger) Resolve(ctx context.Context, refs []Ref, want Method) ([]Method, error) {
	out := make([]Method, len(refs))
	allowed := map[string][]Method{}
	for i, r := range refs {
		if want != MethodAuto || r.Kind != GitHub {
			out[i] = want
			continue
		}
		key := r.RepoKey()
		methods, ok := allowed[key]
		if !ok {
			var err error
			if methods, err = m.AllowedMethods(ctx, r); err != nil {
				return nil, err
			}
			allowed[key] = methods
		}
		switch len(methods) {
		case 1:
			out[i] = methods[0]
		case 0:
			return nil, fmt.Errorf("the merge methods %s allows could not be read (it reported none); nothing was merged; pass --method", key)
		default:
			names := make([]string, len(methods))
			for j, x := range methods {
				names[j] = string(x)
			}
			return nil, fmt.Errorf("%s allows several merge methods (%s); nothing was merged; pass --method as the SOP or the Captain says", key, strings.Join(names, ", "))
		}
	}
	return out, nil
}

func (m Merger) command(r Ref, method Method) (string, []string, error) {
	n := strconv.Itoa(r.Number)
	switch r.Kind {
	case GitLab:
		scheme := "https://"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.URL)), "http://") {
			scheme = "http://"
		}
		args := []string{"mr", "merge", n, "--repo", scheme + r.RepoKey(), "--yes", "--auto-merge=false"}
		switch method {
		case MethodSquash:
			args = append(args, "--squash")
		case MethodRebase:
			args = append(args, "--rebase")
		}
		return orDefault(m.GLab, "glab"), args, nil
	case GitHub:
		var flag string
		switch method {
		case MethodMerge:
			flag = "--merge"
		case MethodSquash:
			flag = "--squash"
		case MethodRebase:
			flag = "--rebase"
		default:
			return "", nil, fmt.Errorf("%s: a merge method is required for GitHub", r.URL)
		}
		return orDefault(m.GH, "gh"), []string{"pr", "merge", n, "--repo", r.RepoKey(), flag}, nil
	}
	return "", nil, fmt.Errorf("%s: unknown code host kind %q", r.URL, r.Kind)
}

func (m Merger) Merge(ctx context.Context, r Ref, method Method) error {
	bin, args, err := m.command(r, method)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if m.Env != nil {
		cmd.Env = m.Env
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("merge %s: %s is not installed (or not on PATH): %w", r.URL, bin, err)
		}
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("merge %s: %s %s: %s", r.URL, bin, strings.Join(args, " "), msg)
	}
	return nil
}
