package home

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type Log struct {
	Path string
}

func (l Log) lockPath() string { return l.Path + ".lck" }

func (l Log) Append(v any) error {
	return l.AppendWith(func([]byte) (any, error) { return v, nil })
}

func (l Log) AppendWith(build func(last []byte) (any, error)) error {
	return WithFileLock(l.lockPath(), func() error {
		last, err := l.Last()
		if err != nil {
			return err
		}
		v, err := build(last)
		if err != nil {
			return err
		}
		line, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode %s entry: %w", l.Path, err)
		}
		return l.appendLine(line)
	})
}

func (l Log) appendLine(line []byte) error {
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", l.Path, err)
	}
	defer f.Close()
	buf := make([]byte, 0, len(line)+2)
	torn, err := endsTorn(l.Path)
	if err != nil {
		return err
	}
	if torn {
		buf = append(buf, '\n')
	}
	buf = append(buf, line...)
	buf = append(buf, '\n')
	if _, err := f.Write(buf); err != nil {
		return fmt.Errorf("append %s: %w", l.Path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", l.Path, err)
	}
	return nil
}

func endsTorn(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() == 0 {
		return false, nil
	}
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, st.Size()-1); err != nil {
		return false, err
	}
	return b[0] != '\n', nil
}

func (l Log) Each(fn func(line []byte) error) error {
	f, err := os.Open(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", l.Path, err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !json.Valid(line) {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
}

const reverseChunk = 8 << 10

func (l Log) Last() ([]byte, error) {
	var last []byte
	err := l.Reverse(func(line []byte) bool {
		last = append([]byte(nil), line...)
		return false
	})
	return last, err
}

func (l Log) Reverse(fn func(line []byte) bool) error {
	f, err := os.Open(l.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	emit := func(seg []byte) bool {
		seg = bytes.TrimSpace(seg)
		if len(seg) == 0 || !json.Valid(seg) {
			return true
		}
		return fn(seg)
	}
	pos := st.Size()
	var carry []byte
	terminated := false
	for pos > 0 {
		n := min(int64(reverseChunk), pos)
		pos -= n
		buf := make([]byte, n, int(n)+len(carry))
		if _, err := f.ReadAt(buf, pos); err != nil {
			return fmt.Errorf("read %s: %w", l.Path, err)
		}
		data := append(buf, carry...)
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			seg := data[i+1:]
			data = data[:i]
			if terminated && !emit(seg) {
				return nil
			}
			terminated = true
		}
		carry = data
	}
	if terminated {
		emit(carry)
	}
	return nil
}
