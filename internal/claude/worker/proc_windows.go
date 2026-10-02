//go:build windows

package worker

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}

func taskkill(pid int) error {
	c := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	return c.Run()
}

func killGroup(p *os.Process) error {
	if err := taskkill(p.Pid); err != nil {
		return p.Kill()
	}
	return nil
}

func termGroup(pid int) {}
