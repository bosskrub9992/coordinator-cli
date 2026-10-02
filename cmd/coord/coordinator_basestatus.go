package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var baseStatusTimeout = 2 * time.Second

type statusLineSetting struct {
	StatusLine *struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"statusLine"`
}

func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".claude")
}

func captainStatusCommand(folder string) string {
	var files []string
	if folder != "" {
		files = append(files, filepath.Join(folder, ".claude", "settings.local.json"), filepath.Join(folder, ".claude", "settings.json"))
	}
	if d := claudeConfigDir(); d != "" {
		files = append(files, filepath.Join(d, "settings.json"))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s statusLineSetting
		if json.Unmarshal(b, &s) != nil || s.StatusLine == nil {
			continue
		}
		c := strings.TrimSpace(s.StatusLine.Command)
		if s.StatusLine.Type != "command" || c == "" || strings.Contains(c, "_statusline") {
			return ""
		}
		return c
	}
	return ""
}

func captainStatusLine(folder string, input []byte) string {
	command := captainStatusCommand(folder)
	if command == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), baseStatusTimeout)
	defer cancel()
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = "bash"
	}
	c := exec.CommandContext(ctx, shell, "-c", command)
	c.Dir = folder
	c.Stdin = bytes.NewReader(input)
	out, err := c.Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return strings.TrimRight(string(out), "\r\n")
}
