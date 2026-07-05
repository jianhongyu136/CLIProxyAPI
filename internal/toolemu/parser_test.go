package toolemu

import (
	"encoding/json"
	"strings"
	"testing"
)

const testFence = "cpa9x7q2"

func parseArgsAsStrings(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("arguments are not a JSON string map: %v (%s)", err, raw)
	}
	return got
}

func TestParse_SingleRawToolCall(t *testing.T) {
	text := `Let me check.
<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
src/main.go
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 1 {
		t.Fatalf("got %d tool_calls, want 1", len(p.ToolCalls))
	}
	if p.ToolCalls[0].Name != "write_file" {
		t.Fatalf("name = %q", p.ToolCalls[0].Name)
	}
	args := parseArgsAsStrings(t, p.ToolCalls[0].Arguments)
	if args["path"] != "src/main.go" {
		t.Fatalf("path = %q", args["path"])
	}
	if !strings.HasPrefix(p.Prose, "Let me check.") {
		t.Fatalf("prose lost: %q", p.Prose)
	}
}

func TestParseWithTagGroupUsesCustomRawProtocolTags(t *testing.T) {
	text := "before\n<X_TOOL|get_weather|tok_9>\n<X_ARG|city|tok_9>\nTokyo\n</X_ARG|tok_9>\n</X_TOOL|tok_9>"
	parsed, err := ParseWithTagGroup(text, "tok_9", ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Prose != "before" {
		t.Fatalf("Prose = %q", parsed.Prose)
	}
	if len(parsed.ToolCalls) != 1 || parsed.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("ToolCalls = %+v", parsed.ToolCalls)
	}
	args := parseArgsAsStrings(t, parsed.ToolCalls[0].Arguments)
	if args["city"] != "Tokyo" {
		t.Fatalf("city = %q, want Tokyo", args["city"])
	}
}

func TestParse_RawArgumentsPreserveEscapingHeavyContent(t *testing.T) {
	text := `<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
C:\test\file.go
</CPA_TA|cpa9x7q2>
<CPA_TA|content|cpa9x7q2>
func main() {
    fmt.Println("C:\test")
    re := "\\d+\\s+"
    xml := "<arg path>not a protocol tag</arg>"
}
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	args := parseArgsAsStrings(t, p.ToolCalls[0].Arguments)
	if args["path"] != `C:\test\file.go` {
		t.Fatalf("path corrupted: %q", args["path"])
	}
	for _, needle := range []string{`fmt.Println("C:\test")`, `\\d+\\s+`, `<arg path>not a protocol tag</arg>`} {
		if !strings.Contains(args["content"], needle) {
			t.Fatalf("content missing %q:\n%s", needle, args["content"])
		}
	}
}

func TestParse_MultipleToolCalls(t *testing.T) {
	text := `<CPA_TC|a|cpa9x7q2>
<CPA_TA|x|cpa9x7q2>
1
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>
<CPA_TC|b|cpa9x7q2>
<CPA_TA|y|cpa9x7q2>
2
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 2 || p.ToolCalls[0].Name != "a" || p.ToolCalls[1].Name != "b" {
		t.Fatalf("unexpected calls: %+v", p.ToolCalls)
	}
}

func TestParse_WrapperlessToolCallSequence(t *testing.T) {
	text := `Let me check.
<CPA_TC|a|cpa9x7q2>
<CPA_TA|x|cpa9x7q2>
1
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>
<CPA_TC|b|cpa9x7q2>
<CPA_TA|y|cpa9x7q2>
2
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if p.Prose != "Let me check." {
		t.Fatalf("prose = %q", p.Prose)
	}
	if len(p.ToolCalls) != 2 || p.ToolCalls[0].Name != "a" || p.ToolCalls[1].Name != "b" {
		t.Fatalf("unexpected calls: %+v", p.ToolCalls)
	}
	firstArgs := parseArgsAsStrings(t, p.ToolCalls[0].Arguments)
	secondArgs := parseArgsAsStrings(t, p.ToolCalls[1].Arguments)
	if firstArgs["x"] != "1" || secondArgs["y"] != "2" {
		t.Fatalf("args: first=%+v second=%+v", firstArgs, secondArgs)
	}
}

func TestParse_MarkdownFencedCodeBlockKeepsProtocolTagsAsProse(t *testing.T) {
	text := "before\n```go\n<CPA_TC|write_file|cpa9x7q2>\n<CPA_TA|content|cpa9x7q2>\nfmt.Println(1)\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n```\nafter"
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 0 {
		t.Fatalf("fenced code block must not parse tool calls: %+v", p.ToolCalls)
	}
	for _, needle := range []string{"```go", "<CPA_TC|write_file|cpa9x7q2>", "</CPA_TC|cpa9x7q2>", "after"} {
		if !strings.Contains(p.Prose, needle) {
			t.Fatalf("prose missing %q:\n%s", needle, p.Prose)
		}
	}
}

func TestParse_LongMarkdownFenceKeepsShorterInnerFenceAsProse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  string
		inner string
		close string
	}{
		{name: "backtick", open: "````markdown", inner: "```", close: "````"},
		{name: "tilde", open: "~~~~markdown", inner: "~~~", close: "~~~~"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "before\n" + tc.open + "\n" + tc.inner + "\n<CPA_TC|write_file|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n" + tc.close + "\nafter"
			p, err := ParseWithFence(text, testFence)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.ToolCalls) != 0 || p.Prose != text {
				t.Fatalf("long fenced code block must remain prose: %+v", p)
			}
		})
	}
}

func TestParse_IndentedCodeBlockKeepsProtocolTagsAsProse(t *testing.T) {
	text := "before\n    <CPA_TC|write_file|cpa9x7q2>\n    <CPA_TA|content|cpa9x7q2>\n    fmt.Println(1)\n    </CPA_TA|cpa9x7q2>\n    </CPA_TC|cpa9x7q2>\nafter"
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 0 {
		t.Fatalf("indented code block must not parse tool calls: %+v", p.ToolCalls)
	}
	for _, needle := range []string{"    <CPA_TC|write_file|cpa9x7q2>", "    </CPA_TC|cpa9x7q2>", "after"} {
		if !strings.Contains(p.Prose, needle) {
			t.Fatalf("prose missing %q:\n%s", needle, p.Prose)
		}
	}
}

func TestParse_IndentedArgumentCloseIsRawContent(t *testing.T) {
	text := `<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|content|cpa9x7q2>
line one
    </CPA_TA|cpa9x7q2>
line two
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	args := parseArgsAsStrings(t, p.ToolCalls[0].Arguments)
	want := "line one\n    </CPA_TA|cpa9x7q2>\nline two"
	if args["content"] != want {
		t.Fatalf("content = %q, want %q", args["content"], want)
	}
}

func TestParse_SkipsCopiedResultBlockOutsideToolCall(t *testing.T) {
	p, err := ParseWithFence("before\nuser<CPA_TR|0|cpa9x7q2>\nsecret\n</CPA_TR|cpa9x7q2>\nafter", testFence)
	if err != nil {
		t.Fatal(err)
	}
	if p.Prose != "before\nafter" || len(p.ToolCalls) != 0 {
		t.Fatalf("parsed = %+v, want copied result block skipped", p)
	}
}

func TestParse_WrongFenceToolCallFails(t *testing.T) {
	_, err := ParseWithFence(`<CPA_TC|write_file|wrong>
<CPA_TA|path|wrong>
x
</CPA_TA|wrong>
</CPA_TC|wrong>`, testFence)
	if err == nil {
		t.Fatal("expected parse failure for wrong fence")
	}
}

func TestParse_BareAndEmbeddedProtocolTagsOutsideToolCallsAreProse(t *testing.T) {
	text := "先确认服务器状态。<tool Bash cpa9x7q2>\n<arg command cpa9x7q2>\nls\n</arg cpa9x7q2>\n</tool cpa9x7q2>"
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 0 || p.Prose != text {
		t.Fatalf("protocol-looking text outside tool_calls should remain prose: %+v", p)
	}
}

func TestParse_OldLowercaseToolBlockIsPlainText(t *testing.T) {
	text := `<tool write_file cpa9x7q2>
<arg path cpa9x7q2>
x
</arg cpa9x7q2>
</tool cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 0 || p.Prose != text {
		t.Fatalf("old lowercase tool block should be prose: %+v", p)
	}
}

func TestParse_UnterminatedToolFails(t *testing.T) {
	_, err := ParseWithFence(`<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
x
</CPA_TA|cpa9x7q2>`, testFence)
	if err == nil || !strings.Contains(err.Error(), "unterminated tool block") {
		t.Fatalf("err = %v, want unterminated tool block", err)
	}
}

func TestParse_AllowsProseBetweenToolCalls(t *testing.T) {
	p, err := ParseWithFence(`<CPA_TC|first|cpa9x7q2>
</CPA_TC|cpa9x7q2>
explain after
<CPA_TC|second|cpa9x7q2>
<CPA_TA|value|cpa9x7q2>
2
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 2 || p.ToolCalls[0].Name != "first" || p.ToolCalls[1].Name != "second" {
		t.Fatalf("tool calls = %+v", p.ToolCalls)
	}
	if !strings.Contains(p.Prose, "explain after") {
		t.Fatalf("prose = %q, want interleaved prose", p.Prose)
	}
}

func TestParse_ArgumentCanContainProtocolMarkersOutsideClosers(t *testing.T) {
	text := `<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|content|cpa9x7q2>
<CPA_TC|fake|cpa9x7q2>
</CPA_TC|fake|cpa9x7q2>
abc</CPA_TA|cpa9x7q2>
    </CPA_TA|cpa9x7q2>
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	args := parseArgsAsStrings(t, p.ToolCalls[0].Arguments)
	for _, want := range []string{"<CPA_TC|fake|cpa9x7q2>", "</CPA_TC|fake|cpa9x7q2>", "abc</CPA_TA|cpa9x7q2>", "    </CPA_TA|cpa9x7q2>"} {
		if !strings.Contains(args["content"], want) {
			t.Fatalf("content missing %q:\n%s", want, args["content"])
		}
	}
}

func TestParse_DuplicateArgumentFails(t *testing.T) {
	_, err := ParseWithFence(`<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
a
</CPA_TA|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
b
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`, testFence)
	if err == nil {
		t.Fatal("expected duplicate argument error")
	}
}

func TestParse_UnterminatedBlocksFail(t *testing.T) {
	for _, tc := range []struct {
		text string
		want string
	}{
		{`<CPA_TC|write_file|cpa9x7q2>`, "unterminated tool block"},
		{`<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|path|cpa9x7q2>
x`, "unterminated argument"},
		{`user<CPA_TR|0|cpa9x7q2>
secret`, "unterminated result block"},
	} {
		if _, err := ParseWithFence(tc.text, testFence); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("err = %v, want %q for %q", err, tc.want, tc.text)
		}
	}
}

func TestParse_InvalidNamesFail(t *testing.T) {
	for _, text := range []string{
		`<CPA_TC|bad/name|cpa9x7q2>
</CPA_TC|cpa9x7q2>`,
		`<CPA_TC|write_file|cpa9x7q2>
<CPA_TA|bad/name|cpa9x7q2>
x
</CPA_TA|cpa9x7q2>
</CPA_TC|cpa9x7q2>`,
	} {
		if _, err := ParseWithFence(text, testFence); err == nil {
			t.Fatalf("expected invalid name error for %q", text)
		}
	}
}

func TestParse_OldJSONToolCallIsPlainText(t *testing.T) {
	text := `<tool_call>{"name":"x","arguments":{}}</tool_call>`
	p, err := ParseWithFence(text, testFence)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ToolCalls) != 0 || p.Prose != text {
		t.Fatalf("old JSON protocol must not parse: %+v", p)
	}
}
