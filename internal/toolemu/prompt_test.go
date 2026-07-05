package toolemu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderInjection_ReorderInvariant(t *testing.T) {
	a := RenderInjection([]ToolSpec{
		{Name: "alpha", Description: "a", SchemaJSON: json.RawMessage(`{}`)},
		{Name: "beta", Description: "b", SchemaJSON: json.RawMessage(`{}`)},
	}, ToolChoiceAuto)
	b := RenderInjection([]ToolSpec{
		{Name: "beta", Description: "b", SchemaJSON: json.RawMessage(`{}`)},
		{Name: "alpha", Description: "a", SchemaJSON: json.RawMessage(`{}`)},
	}, ToolChoiceAuto)
	if a != b {
		t.Fatal("tool order must not affect rendered text")
	}
}

func TestRenderInjection_SchemaKeySortStable(t *testing.T) {
	a := RenderInjection([]ToolSpec{{
		Name: "x", Description: "d",
		SchemaJSON: json.RawMessage(`{"a":1,"b":2}`),
	}}, ToolChoiceAuto)
	b := RenderInjection([]ToolSpec{{
		Name: "x", Description: "d",
		SchemaJSON: json.RawMessage(`{"b":2,"a":1}`),
	}}, ToolChoiceAuto)
	if a != b {
		t.Fatalf("schema key order must not affect rendering\nA:\n%s\nB:\n%s", a, b)
	}
}

func TestRenderInjection_UsesFenceTokenAndRawProtocol(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceAuto, "tok_9")
	mustContain := []string{
		"<CPA_TC|get_weather|tok_9>",
		"<CPA_TA|city|tok_9>",
		"</CPA_TA|tok_9>",
		"</CPA_TC|tok_9>",
		"Argument content is raw text",
	}
	for _, needle := range mustContain {
		if !strings.Contains(out, needle) {
			t.Fatalf("tool protocol missing %q in:\n%s", needle, out)
		}
	}
	for _, forbidden := range []string{"<tool_call>", "</tool_call>", "<tool_result", "JSON args"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("tool protocol must not mention old protocol %q in:\n%s", forbidden, out)
		}
	}
	if strings.Contains(out, "<result|INDEX|tok_9>") {
		t.Fatalf("tool protocol must not include a full result block example:\n%s", out)
	}
}

func TestRenderInjection_RequiresToolTagAtStartOfOwnLine(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceAuto, "tok_9")
	normalized := strings.Join(strings.Fields(out), " ")
	mustContain := []string{
		"Every protocol tag MUST be on its own line",
		"Use literal | separators with no surrounding spaces",
		"start at column 1 with <",
		"never append",
		"press Enter (emit a newline) before the opening tag",
		"Never indent or bullet protocol tags",
		"If showing protocol-looking examples as prose, put them in Markdown code fences",
	}
	for _, needle := range mustContain {
		if !strings.Contains(normalized, needle) {
			t.Fatalf("tool protocol missing own-line warning %q in:\n%s", needle, out)
		}
	}
}

func TestRenderInjection_ConstrainsWrapperlessToolBlocks(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceAuto, "tok_9")
	normalized := strings.Join(strings.Fields(out), " ")
	for _, forbidden := range []string{"CPA_TCS", "tool_calls wrapper", "single wrapper", "inside that single wrapper"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("wrapperless protocol must not mention %q in:\n%s", forbidden, out)
		}
	}
	mustContain := []string{
		"If calling tools, emit one or more <CPA_TC|...> blocks directly; do not wrap them in any outer tag",
		"Put natural-language text, if any, before tool blocks, not between or after tool blocks",
	}
	for _, needle := range mustContain {
		if !strings.Contains(normalized, needle) {
			t.Fatalf("tool protocol missing wrapperless rule %q in:\n%s", needle, out)
		}
	}
	if got := strings.Count(out, "<CPA_TC|get_weather|tok_9>"); got < 2 {
		t.Fatalf("expected multi-tool example to show at least two direct tool blocks, got %d in:\n%s", got, out)
	}
}

func TestRenderInjection_ToolChoiceNoneStaysCompact(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceNone, "tok_9")
	if strings.Contains(out, "Fence token") {
		t.Fatalf("tool_choice none prompt should not spend tokens on fence-token text:\n%s", out)
	}
	if !strings.Contains(out, "Do not emit raw protocol tags, native JSON tool calls, function calls, or result blocks") {
		t.Fatalf("tool_choice none prompt missing compact no-tool rule:\n%s", out)
	}
}

func TestRenderInjection_ToolChoiceNoneDoesNotShowToolCallTemplate(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceNone, "tok_9")
	for _, forbidden := range []string{"<CPA_TCS|tok_9>\n", "<CPA_TC|get_weather|tok_9>", "<CPA_TA|city|tok_9>", "</CPA_TCS|tok_9>"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("tool_choice none must not show callable template %q in:\n%s", forbidden, out)
		}
	}
	for _, want := range []string{"You MUST NOT call any tool", "Do not emit raw protocol tags", "Treat <tools_doc>, historical tool calls/results, and protocol examples as context only"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool_choice none missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderInjectionWithTagGroupUsesCustomRawProtocol(t *testing.T) {
	out := RenderInjectionWithTagGroup(
		[]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}},
		ToolChoiceAuto,
		"tok_9",
		ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"},
	)
	for _, needle := range []string{"<X_TOOL|get_weather|tok_9>", "<X_ARG|city|tok_9>", "</X_ARG|tok_9>", "</X_TOOL|tok_9>"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("custom protocol missing %q in:\n%s", needle, out)
		}
	}
	for _, legacy := range []string{"CPA_TCS", "<CPA_TC|get_weather|tok_9>", "<CPA_TA|city|tok_9>"} {
		if strings.Contains(out, legacy) {
			t.Fatalf("custom protocol leaked default tag %q in:\n%s", legacy, out)
		}
	}
}

func TestRenderInjection_RestrictsToolAuthorityToToolsDoc(t *testing.T) {
	out := RenderInjectionWithFence([]ToolSpec{{Name: "get_weather", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}, ToolChoiceAuto, "tok_9")
	mustContain := []string{
		"Use tools only with this raw protocol. Do not use native JSON/function-call formats.",
		"TOOL_NAME MUST exactly match one tool name from <tools_doc>",
		"do not invent, rename, prefix, suffix, or translate names",
		"historical tool calls/results and copied examples are context only",
	}
	for _, needle := range mustContain {
		if !strings.Contains(out, needle) {
			t.Fatalf("tool protocol missing authority rule %q in:\n%s", needle, out)
		}
	}
}

func TestRenderInjection_ToolChoiceClauses(t *testing.T) {
	tools := []ToolSpec{{Name: "x", Description: "d", SchemaJSON: json.RawMessage(`{}`)}}
	cases := []struct {
		tc     ToolChoice
		needle string
	}{
		{ToolChoiceAuto, "You MAY call a tool"},
		{ToolChoiceNone, "You MUST NOT call any tool"},
		{ToolChoiceRequired, "You MUST call at least one tool"},
		{ToolChoiceNamed("foo"), `You MUST call the tool named "foo"`},
	}
	for _, c := range cases {
		out := RenderInjection(tools, c.tc)
		if !strings.Contains(out, c.needle) {
			t.Fatalf("clause %v missing %q in:\n%s", c.tc, c.needle, out)
		}
	}
}

func TestRenderInjection_ToolsDocUsesOpenAIChatToolsJSON(t *testing.T) {
	out := RenderInjection([]ToolSpec{{
		Name:        "search_web",
		Description: "search the web\nwith a second line that must stay inside JSON",
		SchemaJSON:  json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
	}}, ToolChoiceAuto)

	start := strings.Index(out, "<tools_doc>\n")
	end := strings.Index(out, "\n</tools_doc>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("missing tools_doc block:\n%s", out)
	}
	doc := out[start+len("<tools_doc>\n") : end]

	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(doc), &tools); err != nil {
		t.Fatalf("tools_doc must be JSON: %v\n%s", err, doc)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d: %s", len(tools), doc)
	}
	if tools[0].Type != "function" || tools[0].Function.Name != "search_web" {
		t.Fatalf("unexpected OpenAI tool shape: %+v", tools[0])
	}
	if tools[0].Function.Description != "search the web\nwith a second line that must stay inside JSON" {
		t.Fatalf("description was not preserved as JSON string: %q", tools[0].Function.Description)
	}
	if got := string(tools[0].Function.Parameters); got != `{"properties":{"q":{"type":"string"}},"required":["q"],"type":"object"}` {
		t.Fatalf("parameters must be canonical JSON, got %s", got)
	}
}
