package toolemu

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func newChatBody(text string) []byte {
	return []byte(`{"id":"r","model":"m","choices":[{"message":{"role":"assistant","content":` + jsonString(text) + `}}]}`)
}

func newResponsesBody(text string) []byte {
	return []byte(`{"id":"r","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + jsonString(text) + `}]}]}`)
}

// jsonString returns a JSON-encoded string literal (including surrounding quotes)
// for embedding within hand-built JSON test fixtures.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// malformedToolCall uses a wrong fence token, which triggers ParseAndRetry's
// retry branch without the old JSON protocol.
const malformedToolCall = `<CPA_TC|f|wrongtok>`

func testCustomTagGroup() ToolEmulationTagGroup {
	return ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"}
}

func mustRawToolBlockWithTagGroup(t *testing.T, name, token string, args map[string]string, tagGroup ToolEmulationTagGroup) string {
	t.Helper()
	out, err := renderToolBlockWithSettings(name, args, effectiveProtocolSettings(token, tagGroup))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustRawToolCallsBlockWithTagGroup(t *testing.T, token string, tagGroup ToolEmulationTagGroup, blocks ...string) string {
	t.Helper()
	out, err := renderToolCallsBlockWithSettings(blocks, effectiveProtocolSettings(token, tagGroup))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseAndRetry_ChatFirstAttemptSuccess(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	calls := 0
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		calls++
		return newChatBody(mustRawToolCallsBlock(t, DefaultFenceToken, mustRawToolBlock(t, "f", DefaultFenceToken, nil))), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sender invoked %d times, want 1", calls)
	}
	if len(res.Parsed.ToolCalls) != 1 || res.Parsed.ToolCalls[0].Name != "f" {
		t.Fatalf("parsed = %+v", res.Parsed)
	}
	if res.Degraded {
		t.Fatal("must not be degraded on first-attempt success")
	}
}

func TestParseAndRetry_RetryThenSuccessAppendsInstruction(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	var captured [][]byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		captured = append(captured, append([]byte(nil), p...))
		if len(captured) == 1 {
			return newChatBody(malformedToolCall), nil
		}
		return newChatBody(mustRawToolCallsBlock(t, DefaultFenceToken, mustRawToolBlock(t, "f", DefaultFenceToken, nil))), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("want 2 sends, got %d", len(captured))
	}
	if !bytes.Contains(captured[1], []byte("valid <CPA_TC|TOOL_NAME|"+DefaultFenceToken)) || bytes.Contains(captured[1], []byte("<tool_call>")) {
		t.Fatalf("retry payload missing instruction:\n%s", captured[1])
	}
	if len(res.Parsed.ToolCalls) != 1 {
		t.Fatalf("expected one ToolCall after retry, got %+v", res.Parsed)
	}
}

func TestParseAndRetry_DegradeOnPersistentFailure(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return newChatBody(malformedToolCall), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1, OnFailure: "parse_failed_to_content"}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded {
		t.Fatal("expected Degraded=true")
	}
	if res.Parsed.Prose != malformedToolCall {
		t.Fatalf("prose = %q", res.Parsed.Prose)
	}
}

func TestParseAndRetry_NegativeAttemptsStillSendsOnce(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	calls := 0
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		calls++
		return newChatBody("plain answer"), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: -2}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("send calls = %d, want 1", calls)
	}
	if res.Parsed.Prose != "plain answer" {
		t.Fatalf("prose = %q, want plain answer", res.Parsed.Prose)
	}
}

func TestParseAndRetry_ErrorOnPersistentFailure(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return newChatBody(malformedToolCall), nil
	}
	_, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1, OnFailure: "error"}, ToolChoiceAuto)
	if err == nil {
		t.Fatal("expected ParseFailedError")
	}
	var pfe ParseFailedError
	if !errors.As(err, &pfe) {
		t.Fatalf("expected ParseFailedError, got %T: %v", err, err)
	}
	if pfe.LastText != malformedToolCall {
		t.Fatalf("LastText = %q", pfe.LastText)
	}
}

func TestParseAndRetry_ResponsesShape(t *testing.T) {
	payload := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	var captured [][]byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		captured = append(captured, append([]byte(nil), p...))
		if len(captured) == 1 {
			return newResponsesBody(malformedToolCall), nil
		}
		return newResponsesBody(mustRawToolCallsBlock(t, DefaultFenceToken, mustRawToolBlock(t, "g", DefaultFenceToken, nil))), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIResponses, RetryPolicy{Attempts: 1}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("want 2 sends, got %d", len(captured))
	}
	if !bytes.Contains(captured[1], []byte("valid <CPA_TC|TOOL_NAME|"+DefaultFenceToken)) || bytes.Contains(captured[1], []byte("<tool_call>")) {
		t.Fatalf("retry payload missing instruction:\n%s", captured[1])
	}
	if len(res.Parsed.ToolCalls) != 1 || res.Parsed.ToolCalls[0].Name != "g" {
		t.Fatalf("parsed = %+v", res.Parsed)
	}
}

func TestParseAndRetry_ToolChoiceRequiredRetriesThenReturnsValidationError(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	calls := 0
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		calls++
		return newChatBody("plain answer"), nil
	}

	_, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1, OnFailure: "parse_failed_to_content"}, ToolChoiceRequired)
	if err == nil {
		t.Fatal("expected tool_choice validation error")
	}
	if calls != 2 {
		t.Fatalf("send calls = %d, want 2", calls)
	}
	if !strings.Contains(err.Error(), "tool_choice required") {
		t.Fatalf("error = %v, want required violation", err)
	}
}

func TestParseAndRetry_UsesFenceTokenForParsingAndRetryInstruction(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	var captured [][]byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		captured = append(captured, append([]byte(nil), p...))
		if len(captured) == 1 {
			return newChatBody("<CPA_TC|f|wrongtok>"), nil
		}
		return newChatBody(mustRawToolCallsBlock(t, "tok_9", mustRawToolBlock(t, "f", "tok_9", nil))), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1, FenceToken: "tok_9"}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Parsed.ToolCalls) != 1 || res.Parsed.ToolCalls[0].Name != "f" {
		t.Fatalf("parsed = %+v", res.Parsed)
	}
	if len(captured) != 2 {
		t.Fatalf("want 2 sends, got %d", len(captured))
	}
	if !bytes.Contains(captured[1], []byte("tok_9")) || bytes.Contains(captured[1], []byte(DefaultFenceToken)) || bytes.Contains(captured[1], []byte("<tool_call>")) {
		t.Fatalf("retry instruction must use only the configured token:\n%s", captured[1])
	}
}

func TestParseAndRetry_UsesCustomTagGroupForParsingAndRetryInstruction(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	tags := testCustomTagGroup()
	var captured [][]byte
	send := func(_ context.Context, p []byte) ([]byte, error) {
		captured = append(captured, append([]byte(nil), p...))
		if len(captured) == 1 {
			return newChatBody("<X_TOOL|f|wrongtok>"), nil
		}
		return newChatBody(mustRawToolCallsBlockWithTagGroup(t, "tok_9", tags, mustRawToolBlockWithTagGroup(t, "f", "tok_9", nil, tags))), nil
	}
	res, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 1, FenceToken: "tok_9", TagGroup: tags}, ToolChoiceAuto)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Parsed.ToolCalls) != 1 || res.Parsed.ToolCalls[0].Name != "f" {
		t.Fatalf("parsed = %+v", res.Parsed)
	}
	if len(captured) != 2 {
		t.Fatalf("want 2 sends, got %d", len(captured))
	}
	retryPayload := string(captured[1])
	if !strings.Contains(retryPayload, "valid <X_TOOL|TOOL_NAME|tok_9>") || strings.Contains(retryPayload, "CPA_TCS") || strings.Contains(retryPayload, "tool_calls") || strings.Contains(retryPayload, "X_CALLS") {
		t.Fatalf("retry instruction must use custom tag group only:\n%s", retryPayload)
	}
}

func TestParseAndRetry_ToolChoiceViolationReturnsError(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return []byte(`{"id":"msg_1","model":"m","content":[{"type":"text","text":"I will answer directly"}]}`), nil
	}
	_, err := ParseAndRetry(context.Background(), payload, send, ShapeClaudeMessages, RetryPolicy{Attempts: 0, OnFailure: "parse_failed_to_content"}, ToolChoiceRequired)
	if err == nil {
		t.Fatal("expected tool_choice validation error")
	}
	if !strings.Contains(err.Error(), "tool_choice required") {
		t.Fatalf("error = %v, want required violation", err)
	}
}

func TestValidateToolChoice_DisableParallelRejectsMultipleCalls(t *testing.T) {
	parsed := Parsed{ToolCalls: []ParsedToolCall{
		{Name: "first", Arguments: []byte(`{}`)},
		{Name: "second", Arguments: []byte(`{}`)},
	}}
	err := ValidateToolChoice(parsed, ToolChoice{Kind: ToolChoiceKindRequired, DisableParallel: true})
	if err == nil {
		t.Fatal("expected disable_parallel violation")
	}
	if !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("error = %v, want parallel violation", err)
	}
}

func TestParseAndRetry_OpenAIParallelToolCallsFalseRejectsMultipleCalls(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return newChatBody(mustRawToolCallsBlock(t, DefaultFenceToken,
			mustRawToolBlock(t, "a", DefaultFenceToken, nil),
			mustRawToolBlock(t, "b", DefaultFenceToken, nil))), nil
	}
	_, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 0, OnFailure: "error"}, ToolChoice{Kind: ToolChoiceKindAuto, DisableParallel: true})
	if err == nil {
		t.Fatal("expected disable_parallel validation error")
	}
	if !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("error = %v, want parallel violation", err)
	}
}

func TestParseAndRetry_ToolChoiceNamedRejectsWrongTool(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return newChatBody(mustRawToolCallsBlock(t, DefaultFenceToken, mustRawToolBlock(t, "other", DefaultFenceToken, nil))), nil
	}

	_, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 0, OnFailure: "error"}, ToolChoiceNamed("expected"))
	if err == nil {
		t.Fatal("expected tool_choice validation error")
	}
	var validationErr ToolChoiceValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ToolChoiceValidationError, got %T: %v", err, err)
	}
}

func TestParseAndRetry_ToolChoiceNoneRejectsToolCall(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	send := func(_ context.Context, _ []byte) ([]byte, error) {
		return newChatBody(mustRawToolCallsBlock(t, DefaultFenceToken, mustRawToolBlock(t, "f", DefaultFenceToken, nil))), nil
	}

	_, err := ParseAndRetry(context.Background(), payload, send, ShapeOpenAIChat, RetryPolicy{Attempts: 0, OnFailure: "error"}, ToolChoiceNone)
	if err == nil {
		t.Fatal("expected tool_choice validation error")
	}
	var validationErr ToolChoiceValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ToolChoiceValidationError, got %T: %v", err, err)
	}
}
