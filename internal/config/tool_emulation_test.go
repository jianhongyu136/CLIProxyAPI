package config

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
)

func TestParseConfigBytesToolEmulation(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
tool-emulation:
  enabled: true
  parse-retry: 2
  on-parse-failure: parse_failed_to_content
  fence-token: custom9
  tag-group:
    tool: X_TOOL
    arg: X_ARG
    result: X_RESULT
  rules:
    - provider: openai-compatibility
      models: ["gpt-test"]
      model-aliases: ["alias-test"]
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if !cfg.ToolEmulation.Enabled {
		t.Fatal("ToolEmulation.Enabled = false, want true")
	}
	if cfg.ToolEmulation.ParseRetry != 2 {
		t.Fatalf("ParseRetry = %d, want 2", cfg.ToolEmulation.ParseRetry)
	}
	if cfg.ToolEmulation.OnParseFailure != "parse_failed_to_content" {
		t.Fatalf("OnParseFailure = %q", cfg.ToolEmulation.OnParseFailure)
	}
	if cfg.ToolEmulation.FenceToken != "custom9" {
		t.Fatalf("FenceToken = %q, want custom9", cfg.ToolEmulation.FenceToken)
	}
	wantTags := toolemu.ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	if cfg.ToolEmulation.TagGroup != wantTags {
		t.Fatalf("TagGroup = %+v, want %+v", cfg.ToolEmulation.TagGroup, wantTags)
	}
	if len(cfg.ToolEmulation.Rules) != 1 {
		t.Fatalf("rules len = %d, want 1", len(cfg.ToolEmulation.Rules))
	}
	rule := cfg.ToolEmulation.Rules[0]
	if rule.Provider != "openai-compatibility" || rule.Models[0] != "gpt-test" || rule.ModelAliases[0] != "alias-test" {
		t.Fatalf("unexpected rule: %+v", rule)
	}
}
