package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
	"github.com/bosskrub9992/coordinator-cli/internal/home"
)

type Class string

const (
	Ship       Class = "ship"
	Scout      Class = "scout"
	ReviewCode Class = "review-code"
)

func Classes() []Class { return []Class{Ship, Scout, ReviewCode} }

func ParseClass(s string) (Class, error) {
	for _, c := range Classes() {
		if string(c) == s {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown Task class %q (one of %s)", s, joinClasses())
}

func joinClasses() string {
	var parts []string
	for _, c := range Classes() {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}

var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

type PlanApproval string

const (
	PlanProductDecisions PlanApproval = "product-decisions"
	PlanFYI              PlanApproval = "fyi"
	PlanAll              PlanApproval = "all"
)

var PlanApprovals = []PlanApproval{PlanProductDecisions, PlanFYI, PlanAll}

type Selection struct {
	Harness harness.Name `json:"harness,omitempty"`
	Model   string       `json:"model,omitempty"`
	Effort  string       `json:"effort,omitempty"`
}

type ProjectSettings struct {
	Classes      map[Class]Selection `json:"classes,omitempty"`
	PlanApproval PlanApproval        `json:"plan_approval,omitempty"`
}

type Config struct {
	Coordinator          Selection                  `json:"coordinator"`
	Classes              map[Class]Selection        `json:"classes"`
	CoordinatorMayChoose []string                   `json:"coordinator_may_choose"`
	ForbiddenModels      []string                   `json:"forbidden_models"`
	PlanApproval         PlanApproval               `json:"plan_approval"`
	Projects             map[string]ProjectSettings `json:"projects,omitempty"`
	MRPollInterval       string                     `json:"mr_poll_interval"`
}

var retiredKeys = []string{"secret_paths", "extra_secret_paths"}

const (
	DefaultModel  = "claude-opus-5-5"
	DefaultEffort = "high"
)

const (
	DefaultMRPollInterval = 2 * time.Minute
	MinMRPollInterval     = 30 * time.Second
)

func Default() Config {
	sel := Selection{Harness: harness.Claude, Model: DefaultModel, Effort: DefaultEffort}
	return Config{
		Coordinator: sel,
		Classes: map[Class]Selection{
			Ship:       sel,
			Scout:      sel,
			ReviewCode: sel,
		},
		CoordinatorMayChoose: []string{"claude-opus-5-5", "claude-sonnet-5-5"},
		ForbiddenModels:      []string{},
		PlanApproval:         PlanProductDecisions,
		MRPollInterval:       DefaultMRPollInterval.String(),
	}
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	return Parse(data)
}

func dropRetired(data []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return data
	}
	found := false
	for _, k := range retiredKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			found = true
		}
	}
	if !found {
		return data
	}
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

func Parse(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(dropRetired(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	c.fillDefaults()
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return home.WriteJSONAtomic(path, c)
}

func (c *Config) fillDefaults() {
	d := Default()
	c.Coordinator = c.Coordinator.over(d.Coordinator)
	if c.Classes == nil {
		c.Classes = map[Class]Selection{}
	}
	for _, cl := range Classes() {
		c.Classes[cl] = c.Classes[cl].over(d.Classes[cl])
	}
	if c.CoordinatorMayChoose == nil {
		c.CoordinatorMayChoose = d.CoordinatorMayChoose
	}
	if c.ForbiddenModels == nil {
		c.ForbiddenModels = d.ForbiddenModels
	}
	if c.PlanApproval == "" {
		c.PlanApproval = d.PlanApproval
	}
	if c.MRPollInterval == "" {
		c.MRPollInterval = d.MRPollInterval
	}
}

func (c Config) PollInterval() time.Duration {
	d, err := time.ParseDuration(c.MRPollInterval)
	if err != nil || d < MinMRPollInterval {
		return DefaultMRPollInterval
	}
	return d
}

func validatePollInterval(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 1m or 90s", s)
	}
	if d < MinMRPollInterval {
		return fmt.Errorf("%s is shorter than the %s minimum", d, MinMRPollInterval)
	}
	return nil
}

func (s Selection) over(base Selection) Selection {
	if s.Harness != "" {
		base.Harness = s.Harness
	}
	if s.Model != "" {
		base.Model = s.Model
	}
	if s.Effort != "" {
		base.Effort = s.Effort
	}
	return base
}

func (c Config) Validate() error {
	var errs []error
	add := func(field string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", field, err))
		}
	}
	add("coordinator", validateSelection(c.Coordinator, true))
	if c.MRPollInterval != "" {
		add("mr_poll_interval", validatePollInterval(c.MRPollInterval))
	}
	for cl, sel := range c.Classes {
		if _, err := ParseClass(string(cl)); err != nil {
			add("classes", err)
			continue
		}
		add("classes."+string(cl), validateSelection(sel, true))
	}
	for _, cl := range Classes() {
		if _, ok := c.Classes[cl]; !ok {
			add("classes", fmt.Errorf("missing class %q", cl))
		}
	}
	for i, m := range c.CoordinatorMayChoose {
		f := fmt.Sprintf("coordinator_may_choose[%d]", i)
		add(f, validateModel(m))
		if IsForbidden(m, c.ForbiddenModels) {
			add(f, fmt.Errorf("%q is also in forbidden_models", m))
		}
	}
	for i, m := range c.ForbiddenModels {
		if strings.TrimSpace(m) == "" {
			add(fmt.Sprintf("forbidden_models[%d]", i), errors.New("empty entry"))
		}
	}
	add("plan_approval", validatePlanApproval(c.PlanApproval, false))
	for name, p := range c.Projects {
		f := "projects." + name
		if strings.TrimSpace(name) == "" {
			add("projects", errors.New("empty Project name"))
		}
		for cl, sel := range p.Classes {
			if _, err := ParseClass(string(cl)); err != nil {
				add(f+".classes", err)
				continue
			}
			add(f+".classes."+string(cl), validateSelection(sel, false))
		}
		add(f+".plan_approval", validatePlanApproval(p.PlanApproval, true))
	}
	return errors.Join(errs...)
}

func validateSelection(s Selection, complete bool) error {
	var errs []error
	if s.Harness != "" || complete {
		if err := harness.Check(s.Harness); err != nil {
			errs = append(errs, fmt.Errorf("harness: %w", err))
		}
	}
	if s.Model != "" || complete {
		if err := validateModel(s.Model); err != nil {
			errs = append(errs, fmt.Errorf("model: %w", err))
		}
	}
	if s.Effort != "" {
		if err := ValidateEffort(s.Effort); err != nil {
			errs = append(errs, fmt.Errorf("effort: %w", err))
		}
	}
	return errors.Join(errs...)
}

func validateModel(m string) error {
	if strings.TrimSpace(m) == "" {
		return errors.New("model id is empty")
	}
	if strings.ContainsAny(m, " \t\n") {
		return fmt.Errorf("model id %q contains whitespace", m)
	}
	return nil
}

func ValidateEffort(e string) error {
	if slices.Contains(Efforts, e) {
		return nil
	}
	return fmt.Errorf("%q is not one of %s", e, strings.Join(Efforts, ", "))
}

func ParsePlanApproval(s string) (PlanApproval, error) {
	p := PlanApproval(s)
	if err := validatePlanApproval(p, false); err != nil {
		return "", err
	}
	return p, nil
}

func validatePlanApproval(p PlanApproval, optional bool) error {
	if p == "" && optional {
		return nil
	}
	if slices.Contains(PlanApprovals, p) {
		return nil
	}
	return fmt.Errorf("%q is not one of product-decisions, fyi, all", p)
}

func IsForbidden(model string, forbidden []string) bool {
	m := strings.ToLower(model)
	segments := strings.Split(m, "-")
	for _, f := range forbidden {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if m == f || slices.Contains(segments, f) {
			return true
		}
	}
	return false
}

func (c Config) CheckCoordinatorModel(model string) error {
	if IsForbidden(model, c.ForbiddenModels) {
		return fmt.Errorf("model %q is in forbidden_models; only the Captain can choose it", model)
	}
	if !slices.Contains(c.CoordinatorMayChoose, model) {
		return fmt.Errorf("model %q is not in coordinator_may_choose (%s); only the Captain can choose it", model, strings.Join(c.CoordinatorMayChoose, ", "))
	}
	return nil
}
