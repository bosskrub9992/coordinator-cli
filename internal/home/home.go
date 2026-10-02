package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	EnvHome  = "COORD_HOME"
	EnvToken = "COORD_TOKEN"
	DirName  = ".coordinator-cli"
)

type Home struct {
	Root string
}

func Resolve() (Home, error) {
	if v := os.Getenv(EnvHome); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return Home{}, fmt.Errorf("resolve %s=%q: %w", EnvHome, v, err)
		}
		return Home{Root: abs}, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return Home{}, fmt.Errorf("find the user's home folder: %w", err)
	}
	return Home{Root: filepath.Join(dir, DirName)}, nil
}

func New(root string) Home {
	return Home{Root: root}
}

func (h Home) ConfigPath() string        { return filepath.Join(h.Root, "config.json") }
func (h Home) CoordinatorMDPath() string { return filepath.Join(h.Root, "COORDINATOR.md") }
func (h Home) MemoryDir() string         { return filepath.Join(h.Root, "memory") }
func (h Home) ProjectsPath() string      { return filepath.Join(h.Root, "projects.json") }
func (h Home) LockPath() string          { return filepath.Join(h.Root, "lock") }
func (h Home) TasksDir() string          { return filepath.Join(h.Root, "tasks") }
func (h Home) ReadPositionPath() string  { return filepath.Join(h.Root, "read-position.json") }

const coordinatorMDSeed = "# Coordinator notes\n\nStanding instructions from the Captain for the Coordinator, added to its role at launch.\n"

func (h Home) Ensure() error {
	if h.Root == "" {
		return errors.New("home: empty root")
	}
	for _, d := range []string{h.Root, h.MemoryDir(), h.TasksDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if _, err := os.Stat(h.CoordinatorMDPath()); errors.Is(err, os.ErrNotExist) {
		if err := WriteFileAtomic(h.CoordinatorMDPath(), []byte(coordinatorMDSeed), 0o644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}
