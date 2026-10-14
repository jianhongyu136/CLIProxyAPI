package toolemu

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustRawToolBlock(t *testing.T, name, token string, args map[string]string) string {
	t.Helper()
	out, err := renderToolBlock(name, args, token)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustRawToolCallsBlock(t *testing.T, token string, blocks ...string) string {
	t.Helper()
	out, err := renderToolCallsBlock(blocks, token)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseProtocolTags(t *testing.T) {
	token := "cpa9x7q2"
	if name, ok := parseToolOpenLine("<CPA_TC|write_file|cpa9x7q2>", token); !ok || name != "write_file" {
		t.Fatalf("tool open = %q %v", name, ok)
	}
	if name, ok := parseArgOpenLine("<CPA_TA|path|cpa9x7q2>", token); !ok || name != "path" {
		t.Fatalf("arg open = %q %v", name, ok)
	}
	if !isArgCloseLine("</CPA_TA|cpa9x7q2>", token) {
		t.Fatal("arg close not recognized")
	}
	if !isToolCloseLine("</CPA_TC|cpa9x7q2>", token) {
		t.Fatal("tool close not recognized")
	}
	if index, ok := parseResultOpenLine("<CPA_TR|12|cpa9x7q2>", token); !ok || index != 12 {
		t.Fatalf("result open = %d %v", index, ok)
	}
	if !isResultCloseLine("</CPA_TR|cpa9x7q2>", token) {
		t.Fatal("result close not recognized")
	}
	if _, ok := parseToolOpenLine("<CPA_TC|write_file|wrong>", token); ok {
		t.Fatal("wrong token must not match")
	}
	if _, ok := parseArgOpenLine("<CPA_TA|bad/name|cpa9x7q2>", token); ok {
		t.Fatal("invalid arg name must not match")
	}
	if _, ok := parseResultOpenLine("<CPA_TR|name|cpa9x7q2>", token); ok {
		t.Fatal("result index must be numeric")
	}
}

func TestParseProtocolTagsRequireFirstColumn(t *testing.T) {
	token := "cpa9x7q2"
	if _, ok := parseToolOpenLine(" <CPA_TC|write_file|cpa9x7q2>", token); ok {
		t.Fatal("indented tool open must not match")
	}
	if _, ok := parseArgOpenLine(" <CPA_TA|path|cpa9x7q2>", token); ok {
		t.Fatal("indented arg open must not match")
	}
	if isArgCloseLine(" </CPA_TA|cpa9x7q2>", token) {
		t.Fatal("indented arg close must not match")
	}
	if isToolCloseLine(" </CPA_TC|cpa9x7q2>", token) {
		t.Fatal("indented tool close must not match")
	}
	if _, ok := parseResultOpenLine(" <CPA_TR|0|cpa9x7q2>", token); ok {
		t.Fatal("indented result open must not match")
	}
	if isResultCloseLine(" </CPA_TR|cpa9x7q2>", token) {
		t.Fatal("indented result close must not match")
	}
}

func TestProtocolHelpersUseCustomTagGroup(t *testing.T) {
	proto := effectiveProtocolSettings("tok9", ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"})
	if name, ok := parseToolOpenLineWithSettings("<X_TOOL|write_file|tok9>", proto); !ok || name != "write_file" {
		t.Fatalf("custom tool open = %q %v", name, ok)
	}
	if name, ok := parseArgOpenLineWithSettings("<X_ARG|path|tok9>", proto); !ok || name != "path" {
		t.Fatalf("custom arg open = %q %v", name, ok)
	}
	if index, ok := parseResultOpenLineWithSettings("<X_RESULT|2|tok9>", proto); !ok || index != 2 {
		t.Fatalf("custom result open = %d %v", index, ok)
	}
	if protocolCandidateIndexWithSettings("before <X_TOOL|write_file|tok9>", proto) != len("before ") {
		t.Fatal("custom protocol candidate index not found")
	}
}

func TestRenderToolBlockRawStrings(t *testing.T) {
	got, err := renderToolBlock("write_file", map[string]string{
		"path":    "src/main.go",
		"content": "func main() {\n    println(\"C:\\test\")\n}",
	}, "tok9")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"<CPA_TC|write_file|tok9>\n",
		"<CPA_TA|path|tok9>\nsrc/main.go\n</CPA_TA|tok9>\n",
		"<CPA_TA|content|tok9>\nfunc main() {\n    println(\"C:\\test\")\n}\n</CPA_TA|tok9>\n",
		"</CPA_TC|tok9>",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("missing %q in:\n%s", needle, got)
		}
	}
}

func TestRenderToolCallsBlockConcatenatesToolBlocks(t *testing.T) {
	got, err := renderToolCallsBlock([]string{
		mustRawToolBlock(t, "a", "tok9", nil),
		mustRawToolBlock(t, "b", "tok9", map[string]string{"x": "1"}),
	}, "tok9")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"<CPA_TC|a|tok9>\n</CPA_TC|tok9>",
		"<CPA_TC|b|tok9>\n<CPA_TA|x|tok9>\n1\n</CPA_TA|tok9>\n</CPA_TC|tok9>",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("missing %q in:\n%s", needle, got)
		}
	}
	if strings.Contains(got, "CPA_TCS") {
		t.Fatalf("wrapperless rendered block must not contain CPA_TCS:\n%s", got)
	}
}

func TestRenderResultBlock(t *testing.T) {
	got := renderResultBlock(2, "line one\nline two", "tok9")
	want := "<CPA_TR|2|tok9>\nline one\nline two\n</CPA_TR|tok9>"
	if got != want {
		t.Fatalf("renderResultBlock = %q, want %q", got, want)
	}
}

func TestRenderProtocolBlocksWithCustomTagGroup(t *testing.T) {
	proto := effectiveProtocolSettings("tok9", ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"})
	tool, err := renderToolBlockWithSettings("write_file", map[string]string{"path": "src/main.go"}, proto)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := renderToolCallsBlockWithSettings([]string{tool}, proto)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"<X_TOOL|write_file|tok9>", "<X_ARG|path|tok9>", "</X_TOOL|tok9>"} {
		if !strings.Contains(wrapped, needle) {
			t.Fatalf("custom rendered block missing %q in:\n%s", needle, wrapped)
		}
	}
	if strings.Contains(wrapped, "X_CALLS") {
		t.Fatalf("custom rendered block must not contain wrapper tag:\n%s", wrapped)
	}
	if got := renderResultBlockWithSettings(1, "done", proto); got != "<X_RESULT|1|tok9>\ndone\n</X_RESULT|tok9>" {
		t.Fatalf("custom result block = %q", got)
	}
}

func TestRenderToolBlockPreservesTrailingArgumentNewline(t *testing.T) {
	block, err := renderToolCallsBlock([]string{mustRawToolBlock(t, "write_file", "tok9", map[string]string{"content": "line one\n"})}, "tok9")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseWithFence(block, "tok9")
	if err != nil {
		t.Fatal(err)
	}
	args := parseArgsAsStrings(t, parsed.ToolCalls[0].Arguments)
	if args["content"] != "line one\n" {
		t.Fatalf("content = %q, want trailing newline preserved", args["content"])
	}
}

func TestRenderResultBlockAddsProtocolSeparatorAfterTrailingNewline(t *testing.T) {
	got := renderResultBlock(0, "line one\n", "tok9")
	want := "<CPA_TR|0|tok9>\nline one\n\n</CPA_TR|tok9>"
	if got != want {
		t.Fatalf("renderResultBlock = %q, want %q", got, want)
	}
}

func TestArgsJSONToStringMap(t *testing.T) {
	got := argsJSONToStringMap([]byte(`{"path":"x","limit":10,"ok":true,"nested":{"a":1}}`))
	want := map[string]string{"path": "x", "limit": "10", "ok": "true", "nested": `{"a":1}`}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all=%#v)", k, got[k], v, got)
		}
	}
}

func TestMarshalStringArgs(t *testing.T) {
	got, err := marshalStringArgs(map[string]string{"path": "C:\\test", "pattern": `\d+`})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("args must be valid JSON: %v (%s)", err, got)
	}
	if decoded["path"] != `C:\test` || decoded["pattern"] != `\d+` {
		t.Fatalf("decoded args = %#v", decoded)
	}
}
