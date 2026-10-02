package treehouse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const ConfigFile = "treehouse.toml"

type Slot struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Status      string    `json:"status"`
	Branch      string    `json:"branch"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	LeasedAt    time.Time `json:"leased_at,omitzero"`
}

type Lease struct {
	Path        string    `json:"path"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	LeasedAt    time.Time `json:"leased_at"`
	BaseBranch  string    `json:"base_branch"`
	Branch      string    `json:"-"`
}

type Client struct {
	Bin   string
	Env   []string
	Force bool
}

var ErrNoPool = errors.New("no treehouse pool")

func (c Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "treehouse"
}

func (c Client) run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command(c.bin(), args...)
	cmd.Dir = dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("treehouse is not installed (or not on PATH): %w", err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("treehouse %s in %s: %s", strings.Join(args, " "), dir, msg)
	}
	return out, nil
}

func (c Client) Status(main string) ([]Slot, error) {
	out, err := c.run(main, "status", "--json")
	if err != nil {
		return nil, err
	}
	var slots []Slot
	if err := json.Unmarshal(bytes.TrimSpace(out), &slots); err != nil {
		return nil, fmt.Errorf("read treehouse status in %s: %w", main, err)
	}
	return slots, nil
}

func (c Client) CheckPool(main string) ([]Slot, error) {
	slots, err := c.Status(main)
	if err != nil {
		return nil, err
	}
	if len(slots) > 0 {
		return slots, nil
	}
	if _, err := os.Stat(filepath.Join(main, ConfigFile)); err == nil {
		return slots, nil
	}
	return nil, fmt.Errorf("%w for %s: it has no %s and no pool slots yet. Set one up from the main checkout with `treehouse init` (commit or ignore treehouse.toml as the Project prefers) and retry; coordinator-cli never makes plain git worktrees", ErrNoPool, main, ConfigFile)
}

func (c Client) Acquire(main, branch, holder string) (Lease, error) {
	slots, err := c.CheckPool(main)
	if err != nil {
		return Lease{}, err
	}
	for _, s := range slots {
		if s.Status == "leased" && s.LeaseHolder == holder {
			return Lease{Path: s.Path, LeaseID: s.LeaseID, LeaseHolder: s.LeaseHolder, LeasedAt: s.LeasedAt, Branch: s.Branch}, nil
		}
	}
	out, err := c.run(main, "get", "--lease", "--json", "-b", branch, "--lease-holder", holder)
	if err != nil {
		return Lease{}, err
	}
	var l Lease
	if err := json.Unmarshal(bytes.TrimSpace(out), &l); err != nil {
		return Lease{}, fmt.Errorf("read treehouse lease: %w (output %q)", err, out)
	}
	if l.Path == "" {
		return Lease{}, fmt.Errorf("treehouse lease returned no path (output %q)", out)
	}
	l.Branch = branch
	return l, nil
}

func (c Client) Return(path, leaseID string) error {
	args := []string{"return", path}
	if leaseID != "" {
		args = append(args, "--if-lease-id", leaseID)
	}
	if c.Force {
		args = append(args, "--force")
	}
	_, err := c.run(filepath.Dir(path), args...)
	return err
}
