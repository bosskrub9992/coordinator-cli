//go:build unix

package main

import (
	"os"
	"syscall"

	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
)

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func startDetached(s detachSpec) (int, error) {
	s.Env = envlist.Set(s.Env, envDetached, "1")
	return startDirect(s, detachAttr())
}

func detachedArgs() []string {
	return os.Args[1:]
}
