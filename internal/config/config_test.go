package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bosskrub9992/coordinator-cli/internal/harness"
)

func TestLoadMissingIsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Fatalf("got %+v", c)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
		check   func(t *testing.T, c Config)
	}{
		{
			name: "partial class fills from defaults",
			in:   `{"classes":{"scout":{"model":"claude-sonnet-5-5"}}}`,
			check: func(t *testing.T, c Config) {
				got := c.Classes[Scout]
				want := Selection{Harness: harness.Claude, Model: "claude-sonnet-5-5", Effort: DefaultEffort}
				if got != want {
					t.Fatalf("scout = %+v", got)
				}
				if c.Classes[Ship].Model != DefaultModel {
					t.Fatalf("ship lost its default")
				}
			},
		},
		{
			name: "explicit empty may-choose stays empty",
			in:   `{"coordinator_may_choose":[]}`,
			check: func(t *testing.T, c Config) {
				if len(c.CoordinatorMayChoose) != 0 {
					t.Fatalf("got %v", c.CoordinatorMayChoose)
				}
			},
		},
		{name: "unknown field", in: `{"clases":{}}`, wantErr: "unknown field"},
		{name: "bad effort", in: `{"classes":{"ship":{"effort":"extreme"}}}`, wantErr: "classes.ship: effort"},
		{name: "unknown class", in: `{"classes":{"build":{"model":"x"}}}`, wantErr: `unknown Task class "build"`},
		{name: "unsupported harness", in: `{"coordinator":{"harness":"codex"}}`, wantErr: "not supported yet"},
		{name: "bad plan approval", in: `{"plan_approval":"never"}`, wantErr: "plan_approval"},
		{name: "may-choose overlaps forbidden", in: `{"forbidden_models":["sonnet"]}`, wantErr: "also in forbidden_models"},
		{name: "project override bad", in: `{"projects":{"api":{"plan_approval":"x"}}}`, wantErr: "projects.api.plan_approval"},
		{
			name: "retired secret paths are ignored",
			in:   `{"secret_paths":["~/vault/**"],"extra_secret_paths":["**/*.p12"],"plan_approval":"all"}`,
			check: func(t *testing.T, c Config) {
				if c.PlanApproval != PlanAll {
					t.Fatalf("plan approval %q", c.PlanApproval)
				}
			},
		},
		{name: "other unknown keys still fail", in: `{"secret_pathz":[]}`, wantErr: "unknown field"},
		{
			name: "project override ok",
			in:   `{"projects":{"api":{"classes":{"scout":{"effort":"low"}},"plan_approval":"all"}}}`,
			check: func(t *testing.T, c Config) {
				if c.Projects["api"].Classes[Scout].Effort != "low" {
					t.Fatal("override lost")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse([]byte(tt.in))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c)
		})
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Projects = map[string]ProjectSettings{"proto": {PlanApproval: PlanFYI}}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("got %+v want %+v", got, c)
	}
}

func TestIsForbidden(t *testing.T) {
	forbidden := []string{"fable", "haiku"}
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-fable-5-1", true},
		{"claude-haiku-4-5-20251001", true},
		{"fable", true},
		{"claude-opus-5-5", false},
		{"claude-sonnet-5-5", false},
		{"claude-fableish-1", false},
	}
	for _, tt := range tests {
		if got := IsForbidden(tt.model, forbidden); got != tt.want {
			t.Errorf("IsForbidden(%q) = %v", tt.model, got)
		}
	}
}

func TestResolve(t *testing.T) {
	c := Default()
	c.ForbiddenModels = []string{"fable"}
	c.Classes[ReviewCode] = Selection{Harness: harness.Claude, Model: "claude-fable-5-1", Effort: "high"}
	c.Projects = map[string]ProjectSettings{
		"api": {Classes: map[Class]Selection{Scout: {Model: "claude-sonnet-5-5"}}, PlanApproval: PlanAll},
	}
	tests := []struct {
		name    string
		class   Class
		project string
		task    TaskOverrides
		want    Resolved
		wantErr string
	}{
		{
			name: "class matrix", class: Ship, project: "other",
			want: Resolved{harness.Claude, DefaultModel, "high", PlanProductDecisions, Sources{FromClass, FromClass, FromClass, FromGlobal}},
		},
		{
			name: "captain-written class may use a forbidden model", class: ReviewCode,
			want: Resolved{harness.Claude, "claude-fable-5-1", "high", PlanProductDecisions, Sources{FromClass, FromClass, FromClass, FromGlobal}},
		},
		{
			name: "project beats class", class: Scout, project: "api",
			want: Resolved{harness.Claude, "claude-sonnet-5-5", "high", PlanAll, Sources{FromClass, FromProject, FromClass, FromProject}},
		},
		{
			name: "task beats project", class: Scout, project: "api",
			task: TaskOverrides{Model: "claude-opus-5-5", Effort: "max", PlanApproval: PlanFYI},
			want: Resolved{harness.Claude, "claude-opus-5-5", "max", PlanFYI, Sources{FromClass, FromTask, FromTask, FromTask}},
		},
		{
			name: "coordinator may not pick a forbidden model", class: Ship,
			task: TaskOverrides{Model: "claude-fable-5-1", ChosenBy: ChosenByCoordinator}, wantErr: "forbidden_models",
		},
		{
			name: "coordinator may not pick outside may-choose", class: Ship,
			task: TaskOverrides{Model: "claude-opus-4-1"}, wantErr: "coordinator_may_choose",
		},
		{
			name: "captain may pick any model", class: Ship,
			task: TaskOverrides{Model: "claude-fable-5-1", ChosenBy: ChosenByCaptain},
			want: Resolved{harness.Claude, "claude-fable-5-1", "high", PlanProductDecisions, Sources{FromClass, FromTask, FromClass, FromGlobal}},
		},
		{name: "bad task effort", class: Ship, task: TaskOverrides{Effort: "huge"}, wantErr: "effort"},
		{name: "unknown class", class: "build", wantErr: "no class"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Resolve(tt.class, tt.project, tt.task)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
