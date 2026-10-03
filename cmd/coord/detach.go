package main

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	detachedArg = "_detached"
	envDetached = "COORD_DETACHED"
)

type detachSpec struct {
	Bin  string   `json:"bin"`
	Args []string `json:"args"`
	Dir  string   `json:"dir"`
	Env  []string `json:"env"`
	Log  string   `json:"log"`
}

func startDirect(s detachSpec, attr *syscall.SysProcAttr) (int, error) {
	logf, err := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	c := exec.Command(s.Bin, s.Args...)
	c.Dir = s.Dir
	c.Env = s.Env
	c.Stdout = logf
	c.Stderr = logf
	c.SysProcAttr = attr
	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := c.Process.Pid
	return pid, c.Process.Release()
}
