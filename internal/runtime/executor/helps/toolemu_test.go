package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	"github.com/tidwall/gjson"
)

func TestRunToolEmuBuildsM2Bodies(t *testing.T) {
	cases := []struct {
		name      string
		shape     toolemu.UpstreamShape
		payload   []byte
		body      []byte
		wantPath  string
		wantValue string
	}{
		{
			name:      "claude",
			shape:     toolemu.ShapeClaudeMessages,
			payload:   []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","input_schema":{}}]}`),
			body:      []byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"<CPA_TC|f|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"}]}`),
			wantPath:  "content.0.type",
			wantValue: "tool_use",
		},
		{
			name:      "gemini",
			shape:     toolemu.ShapeGeminiGenerateContent,
			payload:   []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parameters":{}}]}]}`),
			body:      []byte(`{"modelVersion":"gemini-test","candidates":[{"content":{"parts":[{"text":"<CPA_TC|f|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"}]}}]}`),
			wantPath:  "candidates.0.content.parts.0.functionCall.name",
			wantValue: "f",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			send := func(_ context.Context, _ []byte) ([]byte, error) { return c.body, nil }
			outcome, err := RunToolEmu(context.Background(), c.payload, c.shape, "p", toolemu.RetryPolicy{}, send)
			if err != nil {
				t.Fatal(err)
			}
			if len(outcome.BuiltBody) == 0 {
				t.Fatal("BuiltBody must be populated")
			}
			if got := gjson.GetBytes(outcome.BuiltBody, c.wantPath).String(); got != c.wantValue {
				t.Fatalf("%s = %q, want %q: %s", c.wantPath, got, c.wantValue, outcome.BuiltBody)
			}
		})
	}
}

func TestRunToolEmuEmbedsRawArgumentsBySchemaWithoutParsing(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"search","parameters":{"type":"object","properties":{"limit":{"type":"integer"},"dry_run":{"type":"boolean"},"filter":{"type":"object"},"tags":{"type":"array"},"note":{"type":"string"}}}}}]}`)
	body := []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"<CPA_TC|search|cpa9x7q2>\n<CPA_TA|limit|cpa9x7q2>\n10\n</CPA_TA|cpa9x7q2>\n<CPA_TA|dry_run|cpa9x7q2>\ntrue\n</CPA_TA|cpa9x7q2>\n<CPA_TA|filter|cpa9x7q2>\n{\"kind\":\"recent\"}\n</CPA_TA|cpa9x7q2>\n<CPA_TA|tags|cpa9x7q2>\n[\"go\",\"proxy\"]\n</CPA_TA|cpa9x7q2>\n<CPA_TA|note|cpa9x7q2>\n00123\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"}}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) { return body, nil }
	outcome, err := RunToolEmu(context.Background(), payload, toolemu.ShapeOpenAIChat, "p", toolemu.RetryPolicy{}, send)
	if err != nil {
		t.Fatal(err)
	}
	args := gjson.GetBytes(outcome.BuiltBody, "choices.0.message.tool_calls.0.function.arguments").String()
	root := gjson.Parse(args)
	if got := root.Get("limit"); got.Type != gjson.Number || got.Int() != 10 {
		t.Fatalf("limit = %s (%v), want numeric 10 in %s", got.Raw, got.Type, args)
	}
	if got := root.Get("dry_run"); got.Type != gjson.True {
		t.Fatalf("dry_run = %s (%v), want boolean true in %s", got.Raw, got.Type, args)
	}
	if got := root.Get("filter.kind").String(); got != "recent" {
		t.Fatalf("filter.kind = %q, want recent in %s", got, args)
	}
	if got := root.Get("tags.1").String(); got != "proxy" {
		t.Fatalf("tags[1] = %q, want proxy in %s", got, args)
	}
	if got := root.Get("note"); got.Type != gjson.String || got.String() != "00123" {
		t.Fatalf("note = %s (%v), want string 00123 in %s", got.Raw, got.Type, args)
	}
}

func TestRunToolEmuPassesInvalidRawArgumentsToDownstream(t *testing.T) {
	rawQuestions := `[{"header":"消息是否本地持久化","options":[{"description":"轻量：只用 MMKV 存"最后已读时间/未读计数"等元数据","label":"MMKV 存元数据+服务器拉正文"}],"question":"消息数据是否需要在本地持久化存储？"}]`
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"question","parameters":{"type":"object","properties":{"questions":{"type":"array"}}}}}]}`)
	content := "<CPA_TC|question|cpa9x7q2>\n<CPA_TA|questions|cpa9x7q2>\n" + rawQuestions + "\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"
	body, err := json.Marshal(map[string]any{
		"id":    "r",
		"model": "m",
		"choices": []any{map[string]any{"message": map[string]any{
			"role":    "assistant",
			"content": content,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(_ context.Context, _ []byte) ([]byte, error) { return body, nil }

	outcome, err := RunToolEmu(context.Background(), payload, toolemu.ShapeOpenAIChat, "p", toolemu.RetryPolicy{}, send)
	if err != nil {
		t.Fatal(err)
	}
	args := gjson.GetBytes(outcome.BuiltBody, "choices.0.message.tool_calls.0.function.arguments").String()
	if gjson.Valid(args) {
		t.Fatalf("arguments should be left for downstream parsing, got valid JSON: %s", args)
	}
	if !strings.Contains(args, `存"最后已读时间/未读计数"等元数据`) {
		t.Fatalf("arguments did not preserve invalid raw array text:\n%s", args)
	}
}

func TestRunToolEmuDoesNotBuildFakeStreamChunksByDefault(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`)
	body := []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"plain answer"}}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) { return body, nil }
	outcome, err := RunToolEmu(context.Background(), payload, toolemu.ShapeOpenAIChat, "p", toolemu.RetryPolicy{}, send)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.BuiltBody) == 0 {
		t.Fatal("BuiltBody must still be populated")
	}
	if len(outcome.FakeStreamChunks) != 0 {
		t.Fatalf("FakeStreamChunks should not be eagerly built, got %d chunks", len(outcome.FakeStreamChunks))
	}
}

func TestToolEmuRetryPolicyCarriesFenceToken(t *testing.T) {
	policy := ToolEmuRetryPolicy(toolemu.ToolEmulationConfig{FenceToken: "tok_9"})
	if policy.FenceToken != "tok_9" {
		t.Fatalf("FenceToken = %q, want tok_9", policy.FenceToken)
	}
}

func TestToolEmuRetryPolicyCarriesTagGroup(t *testing.T) {
	tags := toolemu.ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	policy := ToolEmuRetryPolicy(toolemu.ToolEmulationConfig{TagGroup: tags})
	if policy.TagGroup != tags {
		t.Fatalf("TagGroup = %+v, want %+v", policy.TagGroup, tags)
	}
}

func TestToolEmuRetryPolicyClampsNegativeParseRetry(t *testing.T) {
	policy := ToolEmuRetryPolicy(toolemu.ToolEmulationConfig{ParseRetry: -3})
	if policy.Attempts != 0 {
		t.Fatalf("Attempts = %d, want 0", policy.Attempts)
	}
}

func TestRunToolEmuUsesPolicyFenceToken(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`)
	var folded []byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		folded = bytes.Clone(p)
		return []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"<CPA_TC|f|tok_9>\n</CPA_TC|tok_9>"}}]}`), nil
	}
	outcome, err := RunToolEmu(context.Background(), payload, toolemu.ShapeOpenAIChat, "p", toolemu.RetryPolicy{FenceToken: "tok_9"}, send)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(folded, []byte("CPA_TCS")) || !bytes.Contains(folded, []byte("<CPA_TC|f|tok_9>")) || bytes.Contains(folded, []byte(toolemu.DefaultFenceToken)) {
		t.Fatalf("folded request must use only configured token:\n%s", folded)
	}
	if len(outcome.Result.Parsed.ToolCalls) != 1 || outcome.Result.Parsed.ToolCalls[0].Name != "f" {
		t.Fatalf("parsed = %+v", outcome.Result.Parsed)
	}
}

func TestRunToolEmuUsesPolicyTagGroup(t *testing.T) {
	tags := toolemu.ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`)
	var folded []byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		folded = bytes.Clone(p)
		return []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":"<X_TOOL|f|tok_9>\n</X_TOOL|tok_9>"}}]}`), nil
	}
	outcome, err := RunToolEmu(context.Background(), payload, toolemu.ShapeOpenAIChat, "p", toolemu.RetryPolicy{FenceToken: "tok_9", TagGroup: tags}, send)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(folded, []byte("X_CALLS")) || !bytes.Contains(folded, []byte("<X_TOOL|f|tok_9>")) || bytes.Contains(folded, []byte("CPA_TCS")) {
		t.Fatalf("folded request must use only policy tag group:\n%s", folded)
	}
	if len(outcome.Result.Parsed.ToolCalls) != 1 || outcome.Result.Parsed.ToolCalls[0].Name != "f" {
		t.Fatalf("parsed = %+v", outcome.Result.Parsed)
	}
}

func TestRunToolEmuStreamUsesPolicyTagGroup(t *testing.T) {
	tags := toolemu.ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl_1","created":123,"model":"m1","choices":[{"delta":{"content":"<X_TOOL|f|tok_9>\n</X_TOOL|tok_9>"},"index":0}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	var frames [][]byte
	_, err := RunToolEmuStream(context.Background(), toolemu.UpstreamMeta{Provider: "p"}, toolemu.ShapeOpenAIChat, strings.NewReader(sse), toolemu.ToolChoiceAuto, toolemu.RetryPolicy{FenceToken: "tok_9", TagGroup: tags}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := string(bytes.Join(frames, nil))
	if !strings.Contains(joined, `"tool_calls"`) || !strings.Contains(joined, `"name":"f"`) {
		t.Fatalf("stream output should emit native tool_calls after parsing custom tags:\n%s", joined)
	}
	if strings.Contains(joined, "Tool emulation stream parse error") {
		t.Fatalf("custom tag group should not be treated as a parse error:\n%s", joined)
	}
}

func TestRunToolEmuStreamEmbedsRawArgumentsBySchemaWithoutParsing(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl_1","created":123,"model":"m1","choices":[{"delta":{"content":"<CPA_TC|search|cpa9x7q2>\n<CPA_TA|limit|cpa9x7q2>\n10\n</CPA_TA|cpa9x7q2>\n<CPA_TA|note|cpa9x7q2>\n00123\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"},"index":0}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	policy := toolemu.RetryPolicy{
		FenceToken: toolemu.DefaultFenceToken,
		Tools: []toolemu.ToolSpec{{
			Name:       "search",
			SchemaJSON: []byte(`{"type":"object","properties":{"limit":{"type":"integer"},"note":{"type":"string"}}}`),
		}},
	}
	var frames [][]byte
	_, err := RunToolEmuStream(context.Background(), toolemu.UpstreamMeta{Provider: "p"}, toolemu.ShapeOpenAIChat, strings.NewReader(sse), toolemu.ToolChoiceAuto, policy, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := string(bytes.Join(frames, nil))
	if !strings.Contains(joined, `\"limit\":10,\"note\":\"00123\"`) {
		t.Fatalf("stream tool arguments were not rendered by schema:\n%s", joined)
	}
}

func TestRunToolEmuStreamUsesUpstreamMetadataAndUsage(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl_1","created":123,"model":"m1","choices":[{"delta":{"content":"<CPA_TC|f|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"},"index":0}]}`,
		``,
		`data: {"id":"chatcmpl_1","created":123,"model":"m1","choices":[{"delta":{},"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	var frames [][]byte
	meta, err := RunToolEmuStream(context.Background(), toolemu.UpstreamMeta{Provider: "p"}, toolemu.ShapeOpenAIChat, strings.NewReader(sse), toolemu.ToolChoiceAuto, toolemu.RetryPolicy{FenceToken: toolemu.DefaultFenceToken}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	if err != nil {
		t.Fatal(err)
	}
	if meta.ResponseID != "chatcmpl_1" || meta.Model != "m1" || meta.Created != 123 {
		t.Fatalf("meta = %+v", meta)
	}
	if got := gjson.GetBytes(meta.UsagePayload, "total_tokens").Int(); got != 5 {
		t.Fatalf("usage total_tokens = %d", got)
	}
	if len(frames) == 0 {
		t.Fatal("no frames emitted")
	}
	firstPayload := bytes.TrimPrefix(bytes.TrimSpace(frames[0]), []byte("data: "))
	if got := gjson.GetBytes(firstPayload, "id").String(); got != "chatcmpl_1" {
		t.Fatalf("frame id = %q", got)
	}
	if got := gjson.GetBytes(firstPayload, "created").Int(); got != 123 {
		t.Fatalf("frame created = %d", got)
	}
}

func TestRunToolEmuStreamCompletesProtocolErrorAsModelVisibleDiagnostic(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"chatcmpl_bad","created":123,"model":"m1","choices":[{"delta":{"content":"<CPA_TC|f|wrongtok>\n"},"index":0}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	var frames [][]byte
	_, err := RunToolEmuStream(context.Background(), toolemu.UpstreamMeta{Provider: "p"}, toolemu.ShapeOpenAIChat, strings.NewReader(sse), toolemu.ToolChoiceAuto, toolemu.RetryPolicy{FenceToken: toolemu.DefaultFenceToken}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	if err != nil {
		t.Fatalf("protocol diagnostics must be model output, not API error: %v", err)
	}
	var visible strings.Builder
	for _, frame := range frames {
		payload := bytes.TrimPrefix(bytes.TrimSpace(frame), []byte("data: "))
		if content := gjson.GetBytes(payload, "choices.0.delta.content"); content.Exists() {
			visible.WriteString(content.String())
		}
	}
	visibleText := visible.String()
	for _, want := range []string{"Tool emulation stream parse error", "invalid protocol line", `expected fence token "cpa9x7q2"`} {
		if !strings.Contains(visibleText, want) {
			t.Fatalf("model-visible stream text = %q, want substring %q", visibleText, want)
		}
	}
	for _, forbidden := range []string{"<CPA_TC|", "</CPA_TC|", "<CPA_TA|", "</CPA_TA|", "<CPA_TR|", "</CPA_TR|"} {
		if strings.Contains(visibleText, forbidden) {
			t.Fatalf("model-visible stream text leaked protocol tag %q: %q", forbidden, visibleText)
		}
	}
	joined := string(bytes.Join(frames, nil))
	if !strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("protocol diagnostic must finish as a normal chat stream:\n%s", joined)
	}
	if strings.Contains(joined, `"upstream_error"`) || strings.Contains(joined, `"error":{`) {
		t.Fatalf("protocol diagnostic must not emit an SSE error frame:\n%s", joined)
	}
}
