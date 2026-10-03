//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
	"golang.org/x/sys/windows"
)

const wmiCreate = `$s = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{ ShowWindow = [uint16]0 }; ` +
	`$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{ CommandLine = $env:COORD_DETACH_COMMAND; CurrentDirectory = $env:COORD_DETACH_DIR; ProcessStartupInformation = $s }; ` +
	`if ($r.ReturnValue -ne 0) { [Console]::Error.WriteLine("Win32_Process.Create returned $($r.ReturnValue)"); exit 1 }; ` +
	`$r.ProcessId`

func detachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func startDetached(s detachSpec) (int, error) {
	s.Env = envlist.Set(s.Env, envDetached, "1")
	pid, err := startViaWMI(s)
	if err == nil {
		return pid, nil
	}
	if logf, lerr := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); lerr == nil {
		fmt.Fprintf(logf, "coord: could not start outside this terminal's job, so closing the terminal may stop this process: %v\n", err)
		logf.Close()
	}
	return startDirect(s, detachAttr())
}

func startViaWMI(s detachSpec) (int, error) {
	f, err := os.CreateTemp(filepath.Dir(s.Log), ".detach-*.json")
	if err != nil {
		return 0, err
	}
	spec := f.Name()
	err = json.NewEncoder(f).Encode(s)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(spec)
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", wmiCreate)
	c.Env = append(os.Environ(),
		"COORD_DETACH_COMMAND="+syscall.EscapeArg(s.Bin)+" "+detachedArg+" "+syscall.EscapeArg(spec),
		"COORD_DETACH_DIR="+s.Dir,
	)
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		os.Remove(spec)
		return 0, fmt.Errorf("Win32_Process.Create through PowerShell: %w %s", err, strings.TrimSpace(stderr.String()))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("Win32_Process.Create printed %q", strings.TrimSpace(string(out)))
	}
	return pid, nil
}

func detachedArgs() []string {
	if len(os.Args) != 3 || os.Args[1] != detachedArg {
		return os.Args[1:]
	}
	args, err := becomeDetached(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "coord:", err)
		os.Exit(1)
	}
	return args
}

func becomeDetached(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return nil, err
	}
	var s detachSpec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("read the detach spec %s: %w", path, err)
	}
	os.Clearenv()
	for _, kv := range s.Env {
		if k, v, _ := strings.Cut(kv, "="); k != "" {
			os.Setenv(k, v)
		}
	}
	if err := os.Chdir(s.Dir); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(s.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(logf.Fd())
	windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, h)
	windows.SetStdHandle(windows.STD_ERROR_HANDLE, h)
	os.Stdout = logf
	os.Stderr = logf
	return s.Args, nil
}
