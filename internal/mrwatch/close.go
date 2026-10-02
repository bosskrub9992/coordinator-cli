package mrwatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Closer struct {
	GLab string
	GH   string
	Env  []string
}

func (c Closer) command(r Ref) (string, []string, error) {
	n := strconv.Itoa(r.Number)
	switch r.Kind {
	case GitLab:
		scheme := "https://"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.URL)), "http://") {
			scheme = "http://"
		}
		return orDefault(c.GLab, "glab"), []string{"mr", "close", n, "--repo", scheme + r.RepoKey()}, nil
	case GitHub:
		return orDefault(c.GH, "gh"), []string{"pr", "close", n, "--repo", r.RepoKey()}, nil
	}
	return "", nil, fmt.Errorf("%s: unknown code host kind %q", r.URL, r.Kind)
}

func orDefault(bin, def string) string {
	if bin != "" {
		return bin
	}
	return def
}

func (c Closer) Close(ctx context.Context, r Ref) error {
	bin, args, err := c.command(r)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if c.Env != nil {
		cmd.Env = c.Env
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("close %s: %s is not installed (or not on PATH): %w", r.URL, bin, err)
		}
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("close %s: %s %s: %s", r.URL, bin, strings.Join(args, " "), msg)
	}
	return nil
}
