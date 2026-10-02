//go:build darwin

package home

import (
	"time"

	"golang.org/x/sys/unix"
)

func processStartTime(pid int) (time.Time, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	tv := k.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000), nil
}
