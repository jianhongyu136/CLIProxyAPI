package config

import "testing"

func TestToolEmulationTagGroupAlias(t *testing.T) {
	group := ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	cfg := ToolEmulationConfig{TagGroup: group}
	if cfg.TagGroup != group {
		t.Fatalf("TagGroup = %+v, want %+v", cfg.TagGroup, group)
	}
}
