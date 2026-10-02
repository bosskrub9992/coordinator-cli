//go:build windows

package coordinator

import "os"

var handledSignals = []os.Signal{os.Interrupt}

func forward(os.Signal) bool { return false }

func terminate(p *os.Process) error {
	return p.Kill()
}
