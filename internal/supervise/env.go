package supervise

import (
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/bosskrub9992/coordinator-cli/internal/envlist"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

const (
	EnvRole = "COORD_ROLE"
	EnvTask = "COORD_TASK"

	RoleWorker = "worker"
)

var strippedEnv = []string{
	"CLAUDECODE",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_MESSAGING_SOCKET",
	"CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_PID",
	"CLAUDE_CODE_SESSION_ATTENDED",
	"CLAUDE_EFFORT",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_EXECPATH",
	home.EnvToken,
}

func WorkerEnv(parent []string, homeRoot, taskID, coordBin string) []string {
	set := map[string]string{
		EnvRole:      RoleWorker,
		EnvTask:      taskID,
		home.EnvHome: homeRoot,
		"CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD": "1",
		"CLAUDE_CODE_DISABLE_AUTO_MEMORY":              "1",
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := envlist.Filter(parent, func(k string) bool {
		same := func(x string) bool { return envlist.Same(k, x) }
		return slices.ContainsFunc(strippedEnv, same) || slices.ContainsFunc(keys, same)
	})
	if coordBin != "" && !resolvesTo(envlist.Get(out, "PATH"), coordBin) {
		out = envlist.PrependPath(out, filepath.Dir(coordBin))
	}
	for _, k := range keys {
		out = append(out, k+"="+set[k])
	}
	return out
}

func resolvesTo(val, coordBin string) bool {
	want := canonical(coordBin)
	for _, dir := range filepath.SplitList(val) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"coord", "coord.exe"} {
			p := filepath.Join(dir, name)
			if found, err := exec.LookPath(p); err == nil {
				return canonical(found) == want
			}
		}
	}
	return false
}

func canonical(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func SupervisorEnv(parent []string) []string {
	return envlist.Filter(parent, func(k string) bool { return envlist.Same(k, home.EnvToken) })
}
