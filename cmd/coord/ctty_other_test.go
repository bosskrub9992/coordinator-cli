//go:build !darwin && !linux

package main

import "os/exec"

func withControllingTerminal(c *exec.Cmd) {}
