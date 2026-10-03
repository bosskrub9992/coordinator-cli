package task

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/home"
	"github.com/bosskrub9992/coordinator-cli/internal/mrwatch"
	"github.com/bosskrub9992/coordinator-cli/internal/project"
)

type MRSource string

const (
	MRFromWorker MRSource = "worker"
	MRLinked     MRSource = "linked"
)

type MR struct {
	Ref              mrwatch.Ref      `json:"ref"`
	Project          string           `json:"project,omitempty"`
	Source           MRSource         `json:"source"`
	AddedAt          time.Time        `json:"added_at"`
	Watch            mrwatch.Position `json:"watch"`
	CommentsSinceAck int              `json:"comments_since_ack,omitempty"`
}

func (m MR) Open() bool { return m.Watch.State != mrwatch.Merged && m.Watch.State != mrwatch.Closed }

func (m MR) Merged() bool { return m.Watch.State == mrwatch.Merged }

func MRProgress(mrs []MR) (anyOpen, allMerged bool) {
	allMerged = len(mrs) > 0
	for _, m := range mrs {
		if m.Open() {
			anyOpen = true
		}
		if !m.Merged() {
			allMerged = false
		}
	}
	return anyOpen, allMerged
}

func mrKey(r mrwatch.Ref) string {
	return strings.ToLower(r.RepoKey()) + "!" + strconv.Itoa(r.Number)
}

func RepoKey(remote string) (string, error) {
	h, err := project.ParseRemote(remote)
	if err != nil {
		return "", err
	}
	return strings.ToLower(h.Host + "/" + h.Path), nil
}

func MatchProject(r mrwatch.Ref, ps []project.Project, prefer []string) string {
	want := strings.ToLower(r.RepoKey())
	var matches []string
	for _, p := range ps {
		if key, err := RepoKey(p.Origin); err == nil && key == want {
			matches = append(matches, p.Name)
		}
	}
	for _, name := range prefer {
		if slices.Contains(matches, name) {
			return name
		}
	}
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

type Ask struct {
	Seq  int64     `json:"seq"`
	URL  string    `json:"url"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

type MRFile struct {
	MRs     []MR  `json:"mrs"`
	Asks    []Ask `json:"asks,omitempty"`
	LastAsk int64 `json:"last_ask,omitempty"`
}

func (f *MRFile) AddAsk(a Ask) {
	f.LastAsk++
	a.Seq = f.LastAsk
	f.Asks = append(f.Asks, a)
}

func (s *Store) MRsPath(id ID) string { return filepath.Join(s.Dir(id), "mrs.json") }

func (s *Store) MRs(id ID) ([]MR, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	f, err := s.ReadMRFile(id)
	return f.MRs, err
}

func (s *Store) ReadMRFile(id ID) (MRFile, error) {
	var f MRFile
	found, err := home.ReadJSON(s.MRsPath(id), &f)
	if err != nil || found {
		return f, err
	}
	legacy, err := s.legacyMRs(id)
	if err != nil || len(legacy) == 0 {
		f.MRs = legacy
		return f, err
	}
	return s.UpdateMRFile(id, func(*MRFile) error { return nil })
}

func (s *Store) UpdateMRs(id ID, fn func(*[]MR) error) ([]MR, error) {
	f, err := s.UpdateMRFile(id, func(f *MRFile) error { return fn(&f.MRs) })
	return f.MRs, err
}

func (s *Store) UpdateMRFile(id ID, fn func(*MRFile) error) (MRFile, error) {
	var out MRFile
	err := home.WithFileLock(s.MRsPath(id)+".lck", func() error {
		if _, err := s.Get(id); err != nil {
			return err
		}
		var f MRFile
		found, err := home.ReadJSON(s.MRsPath(id), &f)
		if err != nil {
			return err
		}
		if !found {
			if f.MRs, err = s.legacyMRs(id); err != nil {
				return err
			}
		}
		if err := fn(&f); err != nil {
			return err
		}
		if f.MRs == nil {
			f.MRs = []MR{}
		}
		out = f
		return home.WriteJSONAtomic(s.MRsPath(id), f)
	})
	return out, err
}

func (s *Store) AddMR(id ID, source MRSource, refs ...mrwatch.Ref) ([]MR, error) {
	t, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	ps, err := project.NewRegistry(s.home).List()
	if err != nil {
		return nil, err
	}
	return s.UpdateMRs(id, func(mrs *[]MR) error {
		*mrs = addMRs(*mrs, source, s.now(), ps, t.Projects, refs)
		return nil
	})
}

func addMRs(mrs []MR, source MRSource, at time.Time, ps []project.Project, prefer []string, refs []mrwatch.Ref) []MR {
	for _, r := range refs {
		i := slices.IndexFunc(mrs, func(m MR) bool { return mrKey(m.Ref) == mrKey(r) })
		if i >= 0 {
			if mrs[i].Project == "" {
				mrs[i].Project = MatchProject(r, ps, prefer)
			}
			continue
		}
		mrs = append(mrs, MR{Ref: r, Project: MatchProject(r, ps, prefer), Source: source, AddedAt: at})
	}
	if source == MRFromWorker {
		reorderNamed(mrs, refs)
	}
	return mrs
}

func reorderNamed(mrs []MR, refs []mrwatch.Ref) {
	var order []string
	for _, r := range refs {
		if k := mrKey(r); !slices.Contains(order, k) {
			order = append(order, k)
		}
	}
	var slots []int
	byKey := map[string]MR{}
	for i, m := range mrs {
		if k := mrKey(m.Ref); m.Source == MRFromWorker && slices.Contains(order, k) {
			slots = append(slots, i)
			byKey[k] = m
		}
	}
	n := 0
	for _, k := range order {
		if m, ok := byKey[k]; ok {
			mrs[slots[n]] = m
			n++
		}
	}
}

func (s *Store) legacyMRs(id ID) ([]MR, error) {
	t, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	evs, err := s.Events(id, 0)
	if err != nil {
		return nil, err
	}
	var ps []project.Project
	loaded := false
	var mrs []MR
	for _, e := range evs {
		if e.Type != EventReport {
			continue
		}
		var d struct {
			MRs []string `json:"mr_urls"`
		}
		if json.Unmarshal(e.Data, &d) != nil || len(d.MRs) == 0 {
			continue
		}
		if !loaded {
			if ps, err = project.NewRegistry(s.home).List(); err != nil {
				return nil, err
			}
			loaded = true
		}
		var refs []mrwatch.Ref
		for _, u := range d.MRs {
			if r, err := mrwatch.ParseURL(u); err == nil {
				refs = append(refs, r)
			}
		}
		mrs = addMRs(mrs, MRFromWorker, e.Time, ps, t.Projects, refs)
	}
	return mrs, nil
}
