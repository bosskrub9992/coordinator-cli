//go:build unix

package worker

import (
	"os"
	"os/exec"
	"syscall"
)

func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}

func termGroup(pid int) {
	syscall.Kill(-pid, syscall.SIGTERM)
}
