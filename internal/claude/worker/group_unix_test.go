//go:build unix

package worker

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

func TestKillTakesTheWorkersChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	r := Runner{Bin: fakeBin(t)}
	brief := "BASH sleep 60 >/dev/null 2>&1 & echo $! > " + pidFile + "\nSLEEP 30"
	w, err := r.Start(context.Background(), harness.WorkerSpec{SessionID: "sid-g", Folder: dir, Brief: brief, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for range 200 {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, _ = strconv.Atoi(strings.TrimSpace(string(b))); pid > 0 {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the Worker's child never started")
	}
	if pgid, _ := syscall.Getpgid(w.PID()); pgid != w.PID() {
		t.Fatalf("claude is not its own process group leader (pgid %d, pid %d)", pgid, w.PID())
	}
	if err := w.Kill(); err != nil {
		t.Fatal(err)
	}
	w.Wait()
	for range 100 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child %d outlived the killed Worker", pid)
}
