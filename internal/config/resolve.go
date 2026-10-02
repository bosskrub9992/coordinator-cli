package config

import (
	"fmt"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

type Chooser string

const (
	ChosenByCaptain     Chooser = "captain"
	ChosenByCoordinator Chooser = "coordinator"
)

type TaskOverrides struct {
	Harness      harness.Name `json:"harness,omitempty"`
	Model        string       `json:"model,omitempty"`
	Effort       string       `json:"effort,omitempty"`
	PlanApproval PlanApproval `json:"plan_approval,omitempty"`
	ChosenBy     Chooser      `json:"chosen_by,omitempty"`
}

func (t TaskOverrides) Empty() bool {
	return t.Harness == "" && t.Model == "" && t.Effort == "" && t.PlanApproval == ""
}

type Source string

const (
	FromTask    Source = "task"
	FromProject Source = "project"
	FromClass   Source = "class"
	FromGlobal  Source = "global"
)

type Sources struct {
	Harness      Source `json:"harness"`
	Model        Source `json:"model"`
	Effort       Source `json:"effort"`
	PlanApproval Source `json:"plan_approval"`
}

type Resolved struct {
	Harness      harness.Name `json:"harness"`
	Model        string       `json:"model"`
	Effort       string       `json:"effort"`
	PlanApproval PlanApproval `json:"plan_approval"`
	Sources      Sources      `json:"sources"`
}

func (c Config) Resolve(class Class, project string, t TaskOverrides) (Resolved, error) {
	base, ok := c.Classes[class]
	if !ok {
		return Resolved{}, fmt.Errorf("no class %q in config", class)
	}
	if err := c.CheckTaskOverrides(t); err != nil {
		return Resolved{}, err
	}
	r := Resolved{
		Harness:      base.Harness,
		Model:        base.Model,
		Effort:       base.Effort,
		PlanApproval: c.PlanApproval,
		Sources:      Sources{FromClass, FromClass, FromClass, FromGlobal},
	}
	if p, ok := c.Projects[project]; ok {
		sel := p.Classes[class]
		pick(&r.Harness, &r.Sources.Harness, sel.Harness, FromProject)
		pick(&r.Model, &r.Sources.Model, sel.Model, FromProject)
		pick(&r.Effort, &r.Sources.Effort, sel.Effort, FromProject)
		pick(&r.PlanApproval, &r.Sources.PlanApproval, p.PlanApproval, FromProject)
	}
	pick(&r.Harness, &r.Sources.Harness, t.Harness, FromTask)
	pick(&r.Model, &r.Sources.Model, t.Model, FromTask)
	pick(&r.Effort, &r.Sources.Effort, t.Effort, FromTask)
	pick(&r.PlanApproval, &r.Sources.PlanApproval, t.PlanApproval, FromTask)
	return r, nil
}

func (c Config) CheckTaskOverrides(t TaskOverrides) error {
	if t.Harness != "" {
		if err := harness.Check(t.Harness); err != nil {
			return err
		}
	}
	if t.Model != "" {
		if err := validateModel(t.Model); err != nil {
			return err
		}
	}
	if t.Effort != "" {
		if err := ValidateEffort(t.Effort); err != nil {
			return fmt.Errorf("effort: %w", err)
		}
	}
	if err := validatePlanApproval(t.PlanApproval, true); err != nil {
		return fmt.Errorf("plan_approval: %w", err)
	}
	switch t.ChosenBy {
	case ChosenByCaptain:
	case "", ChosenByCoordinator:
		if t.Model != "" {
			return c.CheckCoordinatorModel(t.Model)
		}
	default:
		return fmt.Errorf("chosen_by %q is not captain or coordinator", t.ChosenBy)
	}
	return nil
}

func pick[T ~string](dst *T, src *Source, v T, from Source) {
	if v != "" {
		*dst = v
		*src = from
	}
}
