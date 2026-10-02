package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/claude/coordinator"
	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/role"
	"github.com/spf13/cobra"
)

var (
	coordExecutable = os.Executable
	isTerminal      = func(r io.Reader) bool {
		f, ok := r.(*os.File)
		return ok && ttyFile(f)
	}
)

type launchOptions struct {
	resume   bool
	takeover bool
}

type sessionRecord struct {
	SessionID    string    `json:"session_id"`
	LaunchFolder string    `json:"launch_folder"`
	StartedAt    time.Time `json:"started_at"`
}

func sessionPath(h home.Home) string  { return filepath.Join(h.CoordinatorRunDir(), "session.json") }
func rolePath(h home.Home) string     { return filepath.Join(h.CoordinatorRunDir(), "role.md") }
func settingsPath(h home.Home) string { return filepath.Join(h.CoordinatorRunDir(), "settings.json") }

func launch(a *app, cmd *cobra.Command, o launchOptions) error {
	h := a.home
	cfg, err := a.config()
	if err != nil {
		return err
	}
	if err := harness.Check(cfg.Coordinator.Harness); err != nil {
		return fmt.Errorf("config coordinator: %w", err)
	}
	folder, err := cwd()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(folder); err == nil {
		folder = resolved
	}
	bin, err := coordBinary()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.CoordinatorRunDir(), 0o755); err != nil {
		return err
	}
	sessionID, err := pickSession(h, folder, o.resume)
	if err != nil {
		return err
	}
	lock, err := acquire(a, cmd, home.Owner{LaunchFolder: folder, SessionID: sessionID}, o.takeover)
	if err != nil {
		return err
	}
	defer func() {
		if err := h.ReleaseLock(lock.Token); err != nil && !errors.Is(err, home.ErrSuperseded) {
			fmt.Fprintln(cmd.ErrOrStderr(), "coord: release the Coordinator lock:", err)
		}
	}()
	if err := home.WriteJSONAtomic(sessionPath(h), sessionRecord{SessionID: sessionID, LaunchFolder: folder, StartedAt: time.Now().UTC()}); err != nil {
		return err
	}
	if err := writeRole(h); err != nil {
		return err
	}
	watchAfter(a, cmd)
	if base := strings.TrimSuffix(filepath.Base(bin), ".exe"); base != "coord" {
		fmt.Fprintf(cmd.ErrOrStderr(), "coord: warning: this binary is named %q, so `coord` in the Coordinator's shell may run another copy\n", base)
	}
	spec := harness.CoordinatorSpec{
		LaunchFolder:   folder,
		SessionID:      sessionID,
		Resume:         o.resume,
		Model:          cfg.Coordinator.Model,
		Effort:         cfg.Coordinator.Effort,
		RolePromptFile: rolePath(h),
		MemoryDir:      h.MemoryDir(),
		SettingsFile:   settingsPath(h),
		PermissionMode: coordinator.DefaultPermissionMode,
		Env:            launchEnv(os.Environ(), h, lock.Token, bin),
		Stdin:          cmd.InOrStdin(),
		Stdout:         cmd.OutOrStdout(),
		Stderr:         cmd.ErrOrStderr(),
	}
	exit, err := coordinator.New(bin).Launch(cmd.Context(), spec)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Resume this Coordinator with: coord --continue")
	if exit.Code != 0 {
		return fmt.Errorf("claude exited with code %d", exit.Code)
	}
	return nil
}

func coordBinary() (string, error) {
	bin, err := coordExecutable()
	if err != nil {
		return "", fmt.Errorf("find the coord binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	return filepath.Abs(bin)
}

func pickSession(h home.Home, folder string, resume bool) (string, error) {
	if !resume {
		return newUUID()
	}
	var last sessionRecord
	found, err := home.ReadJSON(sessionPath(h), &last)
	if err != nil {
		return "", err
	}
	if !found || last.SessionID == "" {
		return "", errors.New("no earlier Coordinator conversation to continue; run coord without --continue")
	}
	if last.LaunchFolder != folder {
		return "", fmt.Errorf("the last Coordinator conversation was launched in %s; run coord --continue from there", last.LaunchFolder)
	}
	return last.SessionID, nil
}

func acquire(a *app, cmd *cobra.Command, owner home.Owner, takeover bool) (home.Lock, error) {
	h := a.home
	lock, err := h.AcquireLock(owner, takeover)
	var held *home.HeldError
	if !errors.As(err, &held) {
		return lock, err
	}
	n, err := tasksInFlight(a)
	if err != nil {
		return home.Lock{}, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "A Coordinator is already live — in %s since %s (pid %d), %s in flight. Take over here? [y/N] ",
		held.Holder.LaunchFolder, held.Holder.AcquiredAt.Local().Format(time.DateTime), held.Holder.PID, plural(n, "Task"))
	answer, err := readLine(cmd.InOrStdin())
	if err != nil && !errors.Is(err, io.EOF) {
		return home.Lock{}, err
	}
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		return home.Lock{}, errors.New("not taking over; the live Coordinator keeps the Fleet")
	}
	lock, err = h.AcquireLock(owner, true)
	if err != nil {
		return lock, err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Took over from pid %d; its coord calls are refused from now on.\n", held.Holder.PID)
	return lock, nil
}

func readLine(r io.Reader) (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return b.String(), nil
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			return b.String(), err
		}
	}
}

func tasksInFlight(a *app) (int, error) {
	tasks, err := a.tasks().List()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range tasks {
		if t.State.Supervised() {
			n++
		}
	}
	return n, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func writeRole(h home.Home) error {
	text := role.Coordinator()
	extra, err := os.ReadFile(h.CoordinatorMDPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s := strings.TrimSpace(string(extra)); s != "" {
		text = strings.TrimRight(text, "\n") + "\n\n" + s + "\n"
	}
	return home.WriteFileAtomic(rolePath(h), []byte(text), 0o644)
}

func launchEnv(base []string, h home.Home, token, bin string) []string {
	env := envlist.Set(base, home.EnvHome, h.Root)
	env = envlist.Set(env, home.EnvToken, token)
	if !resolvesTo(envlist.Get(env, "PATH"), bin) {
		env = envlist.PrependPath(env, filepath.Dir(bin))
	}
	return env
}

func resolvesTo(path, bin string) bool {
	want, err := os.Stat(bin)
	if err != nil {
		return false
	}
	names := []string{"coord"}
	if filepath.Ext(bin) == ".exe" {
		names = []string{"coord.exe", "coord"}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		for _, n := range names {
			st, err := os.Stat(filepath.Join(dir, n))
			if err != nil || st.IsDir() {
				continue
			}
			return os.SameFile(st, want)
		}
	}
	return false
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate a session id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
