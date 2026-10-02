//go:build unix

package coordinator

import (
	"os"
	"syscall"
)

var handledSignals = []os.Signal{syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP}

func forward(s os.Signal) bool {
	return s == syscall.SIGTERM || s == syscall.SIGHUP
}

func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
