package project

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

type CodeHostKind string

const (
	GitLab CodeHostKind = "gitlab"
	GitHub CodeHostKind = "github"
)

func ParseCodeHostKind(s string) (CodeHostKind, error) {
	switch CodeHostKind(s) {
	case GitLab, GitHub:
		return CodeHostKind(s), nil
	}
	return "", fmt.Errorf("code host %q is not gitlab or github", s)
}

type CodeHost struct {
	Kind CodeHostKind `json:"kind"`
	Host string       `json:"host"`
	Path string       `json:"path"`
}

type Project struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Origin   string    `json:"origin"`
	CodeHost CodeHost  `json:"code_host"`
	AddedAt  time.Time `json:"added_at"`
}

var ErrNotFound = errors.New("Project not registered")

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("Project name %q must start with a letter or digit and use only letters, digits, '.', '_' or '-'", name)
	}
	return nil
}

type file struct {
	Projects []Project `json:"projects"`
}

type Registry struct {
	path string
}

func NewRegistry(h home.Home) *Registry {
	return &Registry{path: h.ProjectsPath()}
}

func (r *Registry) load() ([]Project, error) {
	var f file
	if _, err := home.ReadJSON(r.path, &f); err != nil {
		return nil, err
	}
	sort.Slice(f.Projects, func(i, j int) bool { return f.Projects[i].Name < f.Projects[j].Name })
	return f.Projects, nil
}

func (r *Registry) List() ([]Project, error) {
	return r.load()
}

func (r *Registry) Get(name string) (Project, error) {
	ps, err := r.load()
	if err != nil {
		return Project{}, err
	}
	for _, p := range ps {
		if p.Name == name {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %q", ErrNotFound, name)
}

func (r *Registry) Add(p Project) error {
	if err := ValidateName(p.Name); err != nil {
		return err
	}
	if !filepath.IsAbs(p.Path) {
		return fmt.Errorf("Project path %q is not absolute", p.Path)
	}
	if _, err := ParseCodeHostKind(string(p.CodeHost.Kind)); err != nil {
		return err
	}
	if p.AddedAt.IsZero() {
		p.AddedAt = time.Now().UTC()
	}
	return home.WithFileLock(r.path+".lck", func() error {
		ps, err := r.load()
		if err != nil {
			return err
		}
		for _, q := range ps {
			if q.Name == p.Name {
				return fmt.Errorf("a Project named %q is already registered (%s)", p.Name, q.Path)
			}
			if samePath(q.Path, p.Path) {
				return fmt.Errorf("%s is already registered as Project %q", p.Path, q.Name)
			}
		}
		ps = append(ps, p)
		sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
		return home.WriteJSONAtomic(r.path, file{Projects: ps})
	})
}

func (r *Registry) ForFolder(dir string) (Project, bool, error) {
	real, err := canonical(dir)
	if err != nil {
		return Project{}, false, err
	}
	ps, err := r.load()
	if err != nil {
		return Project{}, false, err
	}
	var best Project
	found := false
	for _, p := range ps {
		if within(p.Path, real) && (!found || len(p.Path) > len(best.Path)) {
			best, found = p, true
		}
	}
	return best, found, nil
}

type Inspection struct {
	Path     string
	Origin   string
	CodeHost CodeHost
}

func Inspect(dir string) (Inspection, error) {
	abs, err := canonical(dir)
	if err != nil {
		return Inspection{}, err
	}
	out, err := git(abs, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return Inspection{}, fmt.Errorf("%s is not inside a git checkout: %w", abs, err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return Inspection{}, fmt.Errorf("unexpected git rev-parse output for %s", abs)
	}
	top, gitDir, commonDir := lines[0], lines[1], lines[2]
	if !samePath(gitDir, commonDir) {
		if filepath.Base(commonDir) != ".git" {
			return Inspection{}, fmt.Errorf("%s is a linked worktree of a repository with no main checkout", abs)
		}
		top = filepath.Dir(commonDir)
	}
	top, err = canonical(top)
	if err != nil {
		return Inspection{}, err
	}
	origin, err := git(top, "remote", "get-url", "origin")
	if err != nil {
		return Inspection{}, fmt.Errorf("%s has no origin remote: %w", top, err)
	}
	origin = strings.TrimSpace(origin)
	host, err := ParseRemote(origin)
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{Path: top, Origin: origin, CodeHost: host}, nil
}

var scpLike = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

func ParseRemote(remote string) (CodeHost, error) {
	var host, path string
	switch {
	case strings.Contains(remote, "://"):
		rest := remote[strings.Index(remote, "://")+3:]
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			rest = rest[at+1:]
		}
		hostPort, p, ok := strings.Cut(rest, "/")
		if !ok {
			return CodeHost{}, fmt.Errorf("remote %q has no repository path", remote)
		}
		host = hostPort
		if h, _, ok := strings.Cut(hostPort, ":"); ok {
			host = h
		}
		path = p
		if host == "" && strings.HasPrefix(remote, "file://") {
			host = "localhost"
		}
	default:
		m := scpLike.FindStringSubmatch(remote)
		if m == nil {
			return CodeHost{}, fmt.Errorf("cannot read remote %q", remote)
		}
		host, path = m[1], m[2]
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return CodeHost{}, fmt.Errorf("cannot read remote %q", remote)
	}
	return CodeHost{Kind: DetectKind(host), Host: strings.ToLower(host), Path: path}, nil
}

func DetectKind(host string) CodeHostKind {
	if strings.Contains(strings.ToLower(host), "github") {
		return GitHub
	}
	return GitLab
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return string(out), nil
}

func canonical(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%s does not exist", abs)
		}
		return "", err
	}
	return filepath.Clean(real), nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
