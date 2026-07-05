package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
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

func TestToolEmulationV8MigrationAndRoundTrip(t *testing.T) {
	legacy := []byte("tool-emulation:\n  enabled: true\n  parse-retry: 2\n  rules:\n    - provider: codex\n      models: [gpt-test]\n")
	migrated, changed, err := NormalizeConfigLayout(legacy, true)
	if err != nil || !changed {
		t.Fatalf("migrate tool-emulation: changed=%v error=%v", changed, err)
	}
	if !bytes.Contains(migrated, []byte("requests:")) {
		t.Fatalf("tool-emulation was not moved into requests: %s", migrated)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("validate migrated tool-emulation: %v", err)
	}
	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ToolEmulation.Enabled || cfg.ToolEmulation.ParseRetry != 2 || len(cfg.ToolEmulation.Rules) != 1 {
		t.Fatalf("migrated tool-emulation did not reach runtime config: %+v", cfg.ToolEmulation)
	}
	configFile := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(configFile, migrated, 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveConfigPreserveComments(configFile, cfg, true); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(encoded); err != nil {
		t.Fatalf("validate saved tool-emulation: %v", err)
	}
	reloaded, err := ParseConfigBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.ToolEmulation, reloaded.ToolEmulation) {
		t.Fatalf("tool-emulation changed after round trip: before=%+v after=%+v", cfg.ToolEmulation, reloaded.ToolEmulation)
	}
}
