package supervise

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/bosskrub9992/coordinator-cli/internal/task"
)

type GuardInput struct {
	HookEventName string          `json:"hook_event_name"`
	CWD           string          `json:"cwd"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolUseID     string          `json:"tool_use_id"`
}

type guardOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

type GuardScope struct {
	Allow     []string
	Protected []string
	Temp      []string
}

type GuardDecision string

const (
	GuardAllow GuardDecision = "allow"
	GuardDeny  GuardDecision = "deny"
	GuardNone  GuardDecision = ""
)

func TempRoots() []string {
	roots := []string{os.TempDir()}
	if runtime.GOOS != "windows" {
		roots = append(roots, "/tmp")
	}
	return roots
}

func Scope(t task.Task, w task.WorkerRecord, mainCheckouts []string) GuardScope {
	sc := GuardScope{Temp: TempRoots()}
	sc.Allow = append(append(sc.Allow, w.WorktreePaths()...), t.Folder)
	sc.Protected = append(sc.Protected, mainCheckouts...)
	for _, wt := range w.Worktrees {
		sc.Protected = append(sc.Protected, wt.MainCheckout)
	}
	sc.Protected = append(sc.Protected, t.LaunchFolder)
	return sc
}

func targetPath(raw json.RawMessage) string {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
	}
	json.Unmarshal(raw, &in)
	for _, p := range []string{in.FilePath, in.NotebookPath, in.Path} {
		if p != "" {
			return p
		}
	}
	return ""
}

const maxLinks = 40

func resolveExisting(p string) string {
	r, _ := resolvePath(p, 0)
	return r
}

func resolvePath(p string, depth int) (string, bool) {
	p = filepath.Clean(p)
	if depth > maxLinks {
		return p, false
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r, true
	}
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		dest, err := os.Readlink(p)
		if err != nil {
			return p, false
		}
		if !filepath.IsAbs(dest) {
			dest = filepath.Join(filepath.Dir(p), dest)
		}
		return resolvePath(dest, depth+1)
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p, true
	}
	r, ok := resolvePath(parent, depth+1)
	return filepath.Join(r, filepath.Base(p)), ok
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(p, `~\`)) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

func driveOrRootRelative(p string) bool {
	if len(p) >= 2 && p[1] == ':' && isLetter(p[0]) {
		return len(p) == 2 || (p[2] != '\\' && p[2] != '/')
	}
	if len(p) >= 1 && (p[0] == '\\' || p[0] == '/') {
		return !(len(p) >= 2 && (p[1] == '\\' || p[1] == '/'))
	}
	return false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func inClaudeDir(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.EqualFold(part, ".claude") {
			return true
		}
	}
	return false
}

func resolveRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, resolveExisting(r))
		}
	}
	return out
}

func containing(roots []string, p string) (string, bool) {
	for _, r := range roots {
		if within(r, p) {
			return r, true
		}
	}
	return "", false
}

func writeTool(tool string) bool {
	return slices.Contains([]string{"Edit", "Write", "MultiEdit", "NotebookEdit"}, tool)
}

func Guard(in GuardInput, sc GuardScope) (GuardDecision, string) {
	if !writeTool(in.ToolName) {
		return GuardNone, ""
	}
	return guardWrite(in, sc)
}

func guardWrite(in GuardInput, sc GuardScope) (GuardDecision, string) {
	p := targetPath(in.ToolInput)
	if p == "" {
		return GuardDeny, "coordinator-cli guard: " + in.ToolName + " has no file path to check"
	}
	p = expandHome(p)
	if runtime.GOOS == "windows" && driveOrRootRelative(p) {
		return GuardDeny, "coordinator-cli guard: " + p + " is relative to a drive or the drive root; use a full path inside this Task's worktrees or Task folder"
	}
	if !filepath.IsAbs(p) {
		base := in.CWD
		if base == "" {
			base, _ = os.Getwd()
		}
		p = filepath.Join(base, p)
	}
	target, ok := resolvePath(p, 0)
	if !ok {
		return GuardDeny, "coordinator-cli guard: " + p + " cannot be resolved (a symlink loop or an unreadable link)"
	}
	allow := resolveRoots(sc.Allow)
	if r, ok := containing(allow, target); ok {
		if inClaudeDir(r, target) {
			return GuardDeny, "coordinator-cli guard: " + p + " is inside a .claude folder; Claude Code settings, hooks and commands are off-limits to Workers"
		}
		return GuardAllow, ""
	}
	if r, ok := containing(resolveRoots(sc.Protected), target); ok {
		return GuardDeny, "coordinator-cli guard: " + p + " is inside " + r + ", a main checkout or the Launch folder, which is off-limits to Workers; write only under: " + strings.Join(allow, ", ")
	}
	if _, ok := containing(resolveRoots(sc.Temp), target); ok {
		return GuardNone, ""
	}
	return GuardDeny, "coordinator-cli guard: " + p + " is outside this Task's worktrees and Task folder. Main checkouts and other folders are off-limits; write only under: " + strings.Join(allow, ", ")
}

func GuardResponse(input []byte, sc GuardScope) []byte {
	var in GuardInput
	var d GuardDecision
	var reason string
	if err := json.Unmarshal(input, &in); err != nil {
		d, reason = GuardDeny, "coordinator-cli guard: unreadable hook input"
	} else {
		d, reason = Guard(in, sc)
	}
	if d == GuardAllow {
		reason = "inside this Task's worktrees or Task folder"
	}
	return encodeGuard(d, reason)
}

func CoordinatorGuardResponse(input []byte, memoryDir string) []byte {
	var in GuardInput
	if err := json.Unmarshal(input, &in); err != nil {
		return encodeGuard(GuardDeny, "coordinator-cli guard: unreadable hook input")
	}
	if !writeTool(in.ToolName) {
		return encodeGuard(GuardNone, "")
	}
	if d, _ := guardWrite(in, GuardScope{Allow: []string{memoryDir}}); d == GuardAllow {
		return encodeGuard(GuardAllow, "inside the Coordinator's own memory folder")
	}
	return encodeGuard(GuardDeny, "coordinator-cli guard: the Coordinator never edits files; only its memory folder "+memoryDir+" is writable. Delegate the change to a Worker")
}

func encodeGuard(d GuardDecision, reason string) []byte {
	if d == GuardNone {
		return []byte("{}")
	}
	var out guardOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = string(d)
	out.HookSpecificOutput.PermissionDecisionReason = reason
	b, _ := json.Marshal(out)
	return b
}
