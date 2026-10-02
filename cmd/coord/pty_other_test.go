//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func openPTY() (*os.File, *os.File, error) {
	return nil, nil, errors.New("no pty helper on this platform")
}
