package mrwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
)

type api struct {
	name string
	bin  string
	env  []string
}

func (a api) get(ctx context.Context, host, path string, paginate bool) ([]byte, error) {
	bin := a.bin
	if bin == "" {
		bin = a.name
	}
	args := []string{"api", "--hostname", host, "--method", "GET"}
	if paginate {
		args = append(args, "--paginate")
	}
	args = append(args, path)
	cmd := exec.CommandContext(ctx, bin, args...)
	if a.env != nil {
		cmd.Env = a.env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		line := a.name + " " + strings.Join(args, " ")
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: %s is not installed (or not on PATH): %w", line, a.name, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if body := strings.TrimSpace(string(out)); body != "" {
			msg = strings.TrimSpace(msg + " " + clip(body, 300))
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", line, msg)
	}
	return out, nil
}

func (a api) object(ctx context.Context, host, path string, v any) error {
	out, err := a.get(ctx, host, path, false)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("read %s api %s: %w", a.name, path, err)
	}
	return nil
}

func list[T any](ctx context.Context, a api, host, path string) ([]T, error) {
	out, err := a.get(ctx, host, path, true)
	if err != nil {
		return nil, err
	}
	items, err := decodePages[T](out)
	if err != nil {
		return nil, fmt.Errorf("read %s api %s: %w", a.name, path, err)
	}
	return items, nil
}

func decodePages[T any](out []byte) ([]T, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var items []T
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
			return items, nil
		} else if err != nil {
			return nil, err
		}
		if string(raw) == "null" {
			continue
		}
		if raw[0] == '[' {
			var page []T
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, err
			}
			items = append(items, page...)
			continue
		}
		var item T
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "..."
}

type selfUser struct {
	mu     sync.Mutex
	byHost map[string]string
}

func (u *selfUser) name(host string, lookup func() (string, error)) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if name, ok := u.byHost[host]; ok {
		return name, nil
	}
	name, err := lookup()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("cannot tell which user is signed in on %s", host)
	}
	if u.byHost == nil {
		u.byHost = map[string]string{}
	}
	u.byHost[host] = name
	return name, nil
}
