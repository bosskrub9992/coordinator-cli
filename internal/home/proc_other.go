//go:build !darwin && !linux && !windows

package home

import (
	"errors"
	"time"
)

func processStartTime(int) (time.Time, error) {
	return time.Time{}, errors.New("process start time is not available on this platform")
}
