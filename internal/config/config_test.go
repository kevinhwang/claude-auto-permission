package config

import (
	"testing"

	configpb "claude-auto-permission/internal/gen/config/v1"
)

func TestNewResolver_EmptyDefault(t *testing.T) {
	r := NewResolver(nil)
	if r.Proto() == nil {
		t.Fatal("NewResolver(nil).Proto() should be non-nil")
	}
	if len(r.Proto().GetProjects()) != 0 {
		t.Errorf("default empty config should have no projects, got %d", len(r.Proto().GetProjects()))
	}
}

func TestMatchingProjects(t *testing.T) {
	c := configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{PathPatterns: []string{"/**"}}.Build(),
			configpb.Project_builder{PathPatterns: []string{"/home/user/src/**"}}.Build(),
			configpb.Project_builder{PathPatterns: []string{"/home/user/src/server/**"}}.Build(),
		},
	}.Build()
	r := NewResolver(c)

	got := r.MatchingProjects("/home/user/src/server")
	if len(got) != 3 {
		t.Errorf("expected all 3 projects to match, got %d", len(got))
	}

	got = r.MatchingProjects("/etc")
	if len(got) != 1 {
		t.Errorf("expected only the global /** project to match /etc, got %d", len(got))
	}
}

func TestMatchingLlmClassifier_PicksMostSpecific(t *testing.T) {
	enabled := true
	c := configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns:  []string{"/**"},
				LlmClassifier: configpb.LlmClassifierConfig_builder{Enabled: &enabled}.Build(),
			}.Build(),
			configpb.Project_builder{
				PathPatterns:  []string{"/home/user/src/server/**"},
				LlmClassifier: configpb.LlmClassifierConfig_builder{Enabled: &enabled}.Build(),
			}.Build(),
		},
	}.Build()

	got := NewResolver(c).MatchingLlmClassifier("/home/user/src/server")
	if got == nil {
		t.Fatal("expected a classifier config")
	}
	// The more specific block wins; both enable, so we just check that we got back the deeper-specificity entry by
	// checking the pointer is not the global one. (Pointer equality is enough — the global config and the per-project
	// config are different allocations.)
	global := c.GetProjects()[0].GetLlmClassifier()
	if got == global {
		t.Errorf("got global classifier; expected the more specific per-project block")
	}
}
