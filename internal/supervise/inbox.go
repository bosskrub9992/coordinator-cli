package supervise

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type InboxKind string

const (
	InboxSteer     InboxKind = "steer"
	InboxInterrupt InboxKind = "interrupt"
	InboxStop      InboxKind = "stop"
	InboxReported  InboxKind = "reported"
)

type InboxMsg struct {
	ID   string    `json:"id"`
	Kind InboxKind `json:"kind"`
	Text string    `json:"text,omitempty"`
	At   time.Time `json:"at"`
	Path string    `json:"-"`
}

const (
	InboxSent      = "sent"
	InboxCancelled = "cancelled"
)

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func Post(s *task.Store, id task.ID, kind InboxKind, text string) (InboxMsg, error) {
	dir := s.InboxDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InboxMsg{}, err
	}
	now := time.Now().UTC()
	m := InboxMsg{ID: string(kind[:2]) + "-" + randHex(4), Kind: kind, Text: text, At: now}
	name := fmt.Sprintf("%020d-%s.json", now.UnixNano(), m.ID)
	return m, home.WriteJSONAtomic(filepath.Join(dir, name), m)
}

func inboxFiles(s *task.Store, id task.ID) ([]string, error) {
	dir := s.InboxDir(id)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") && strings.HasSuffix(e.Name(), ".json") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func Queued(s *task.Store, id task.ID) ([]InboxMsg, error) {
	paths, err := inboxFiles(s, id)
	if err != nil {
		return nil, err
	}
	var out []InboxMsg
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m InboxMsg
		if err := json.Unmarshal(b, &m); err == nil {
			m.Path = p
			out = append(out, m)
		}
	}
	return out, nil
}

func Archive(m InboxMsg, outcome string) error {
	if m.Path == "" {
		return nil
	}
	dir := filepath.Join(filepath.Dir(m.Path), outcome)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	err := os.Rename(m.Path, filepath.Join(dir, filepath.Base(m.Path)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
