package envlist

import (
	"os"
	"runtime"
	"strings"
)

var foldCase = runtime.GOOS == "windows"

func Key(kv string) string {
	k, _, _ := strings.Cut(kv, "=")
	return k
}

func Same(a, b string) bool {
	if foldCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func Lookup(env []string, key string) (name, value string, ok bool) {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if Same(k, key) {
			name, value, ok = k, v, true
		}
	}
	return name, value, ok
}

func Get(env []string, key string) string {
	_, v, _ := Lookup(env, key)
	return v
}

func Set(env []string, key, value string) []string {
	name := key
	if n, _, ok := Lookup(env, key); ok {
		name = n
	}
	out := make([]string, 0, len(env)+1)
	placed := false
	for _, kv := range env {
		if !Same(Key(kv), key) {
			out = append(out, kv)
			continue
		}
		if !placed {
			out = append(out, name+"="+value)
			placed = true
		}
	}
	if !placed {
		out = append(out, name+"="+value)
	}
	return out
}

func Filter(env []string, drop func(key string) bool) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !drop(Key(kv)) {
			out = append(out, kv)
		}
	}
	return out
}

func PrependPath(env []string, dir string) []string {
	path := Get(env, "PATH")
	if path == "" {
		path = defaultPath(env)
	}
	return Set(env, "PATH", dir+string(os.PathListSeparator)+path)
}

func defaultPath(env []string) string {
	if !foldCase {
		return "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	root := Get(env, "SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return strings.Join([]string{root + `\System32`, root, root + `\System32\Wbem`, root + `\System32\WindowsPowerShell\v1.0`}, ";")
}
