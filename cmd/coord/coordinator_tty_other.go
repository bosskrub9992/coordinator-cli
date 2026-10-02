//go:build !windows

package main

import (
	"os"
	"slices"

	"golang.org/x/term"
)

func ttyFile(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func processTerminal() bool {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		f.Close()
		return true
	}
	return slices.ContainsFunc([]*os.File{os.Stdin, os.Stdout, os.Stderr}, ttyFile)
}
