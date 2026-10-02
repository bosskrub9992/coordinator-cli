package home

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"
)

type Lock struct {
	PID          int       `json:"pid"`
	StartTime    time.Time `json:"process_start_time"`
	LaunchFolder string    `json:"launch_folder"`
	SessionID    string    `json:"session_id"`
	Token        string    `json:"token"`
	AcquiredAt   time.Time `json:"acquired_at"`
}

type Owner struct {
	PID          int
	LaunchFolder string
	SessionID    string
}

type HeldError struct {
	Holder Lock
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("a Coordinator is already live (pid %d, launched in %s since %s)",
		e.Holder.PID, e.Holder.LaunchFolder, e.Holder.AcquiredAt.Local().Format(time.DateTime))
}

var ErrSuperseded = errors.New("this Coordinator session no longer holds the Home: another Coordinator took over, so this call is refused")

type ProcessProbe func(pid int) (alive bool, start time.Time)

func defaultProbe(pid int) (bool, time.Time) {
	if !pidAlive(pid) {
		return false, time.Time{}
	}
	start, err := processStartTime(pid)
	if err != nil {
		return true, time.Time{}
	}
	return true, start
}

var probe ProcessProbe = defaultProbe

const startTimeTolerance = time.Second

func (l Lock) Live() bool {
	alive, start := probe(l.PID)
	if !alive {
		return false
	}
	if l.StartTime.IsZero() || start.IsZero() {
		return true
	}
	d := l.StartTime.Sub(start)
	return d < startTimeTolerance && d > -startTimeTolerance
}

func (h Home) lockGuardPath() string { return h.LockPath() + ".lck" }

func (h Home) ReadLock() (*Lock, error) {
	var l Lock
	found, err := ReadJSON(h.LockPath(), &l)
	if err != nil || !found {
		return nil, err
	}
	return &l, nil
}

func (h Home) AcquireLock(o Owner, takeover bool) (Lock, error) {
	if o.PID == 0 {
		o.PID = os.Getpid()
	}
	var out Lock
	err := WithFileLock(h.lockGuardPath(), func() error {
		cur, err := h.ReadLock()
		if err != nil {
			return err
		}
		if cur != nil && cur.Live() && !takeover {
			return &HeldError{Holder: *cur}
		}
		token, err := newToken()
		if err != nil {
			return err
		}
		_, start := probe(o.PID)
		out = Lock{
			PID:          o.PID,
			StartTime:    start,
			LaunchFolder: o.LaunchFolder,
			SessionID:    o.SessionID,
			Token:        token,
			AcquiredAt:   time.Now().UTC(),
		}
		return WriteJSONAtomic(h.LockPath(), out)
	})
	return out, err
}

func (h Home) UpdateLock(token string, fn func(*Lock)) (Lock, error) {
	var out Lock
	err := WithFileLock(h.lockGuardPath(), func() error {
		cur, err := h.ReadLock()
		if err != nil {
			return err
		}
		if cur == nil || cur.Token != token {
			return ErrSuperseded
		}
		fn(cur)
		cur.Token = token
		out = *cur
		return WriteJSONAtomic(h.LockPath(), out)
	})
	return out, err
}

func (h Home) ReleaseLock(token string) error {
	return WithFileLock(h.lockGuardPath(), func() error {
		cur, err := h.ReadLock()
		if err != nil {
			return err
		}
		if cur == nil {
			return nil
		}
		if cur.Token != token {
			return ErrSuperseded
		}
		if err := os.Remove(h.LockPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}

func (h Home) CheckToken(token string) error {
	if token == "" {
		return nil
	}
	cur, err := h.ReadLock()
	if err != nil {
		return err
	}
	if cur == nil || cur.Token != token {
		return ErrSuperseded
	}
	return nil
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate lock token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (h Home) CheckLiveToken(token string) error {
	if token == "" {
		return ErrSuperseded
	}
	cur, err := h.ReadLock()
	if err != nil {
		return err
	}
	if cur == nil || cur.Token != token || !cur.Live() {
		return ErrSuperseded
	}
	return nil
}
