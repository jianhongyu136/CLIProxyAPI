package toolemu

import "testing"

func TestDefaultsApplied(t *testing.T) {
	got := ToolEmulationConfig{}.DefaultsApplied()
	if got.OnParseFailure != "parse_failed_to_content" {
		t.Fatalf("OnParseFailure default = %q, want parse_failed_to_content", got.OnParseFailure)
	}
	if got.FenceToken != DefaultFenceToken {
		t.Fatalf("FenceToken default = %q, want %q", got.FenceToken, DefaultFenceToken)
	}
	if got.TagGroup != DefaultToolEmulationTagGroup {
		t.Fatalf("TagGroup default = %+v, want %+v", got.TagGroup, DefaultToolEmulationTagGroup)
	}
}

func TestDefaultsApplied_PreservesExplicit(t *testing.T) {
	customTags := ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	in := ToolEmulationConfig{Enabled: true, OnParseFailure: "error", FenceToken: "custom_9", TagGroup: customTags}
	got := in.DefaultsApplied()
	if !got.Enabled {
		t.Fatal("Enabled should be preserved")
	}
	if got.OnParseFailure != "error" {
		t.Fatalf("OnParseFailure = %q, want error", got.OnParseFailure)
	}
	if got.FenceToken != "custom_9" {
		t.Fatalf("FenceToken = %q, want custom_9", got.FenceToken)
	}
	if got.TagGroup != customTags {
		t.Fatalf("TagGroup = %+v, want %+v", got.TagGroup, customTags)
	}
}

func TestDefaultsApplied_InvalidFenceTokenFallsBack(t *testing.T) {
	for _, token := range []string{"abc", "has space", "has/slash", "has<tag", "has>tag", "012345678901234567890123456789012"} {
		t.Run(token, func(t *testing.T) {
			got := ToolEmulationConfig{FenceToken: token}.DefaultsApplied()
			if got.FenceToken != DefaultFenceToken {
				t.Fatalf("FenceToken = %q, want default %q", got.FenceToken, DefaultFenceToken)
			}
		})
	}
}

func TestDefaultsApplied_InvalidTagGroupFieldsFallBackIndividually(t *testing.T) {
	in := ToolEmulationConfig{TagGroup: ToolEmulationTagGroup{
		Tool:   "CUSTOM_TOOL",
		Arg:    "",
		Result: "has/slash",
	}}

	got := in.DefaultsApplied().TagGroup
	want := ToolEmulationTagGroup{
		Tool:   "CUSTOM_TOOL",
		Arg:    DefaultToolEmulationTagGroup.Arg,
		Result: DefaultToolEmulationTagGroup.Result,
	}
	if got != want {
		t.Fatalf("TagGroup = %+v, want %+v", got, want)
	}
}
