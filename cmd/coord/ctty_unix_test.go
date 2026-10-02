//go:build darwin || linux

package main

import (
	"os/exec"
	"syscall"
)

func withControllingTerminal(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}
