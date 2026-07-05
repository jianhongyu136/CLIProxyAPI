package toolemu

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFoldChatHistory_AssistantWithToolCalls(t *testing.T) {
	token := "hist_tok"
	in := mustJSON(t, []any{
		map[string]any{"role": "assistant", "content": "Let me check.", "tool_calls": []any{
			map[string]any{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "get_weather",
					"arguments": `{"city":"SF"}`,
				},
			},
		}},
	})
	out, err := FoldChatHistoryWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`<CPA_TC|get_weather|hist_tok>`)) || !bytes.Contains(out, []byte(`<CPA_TA|city|hist_tok>`)) || !bytes.Contains(out, []byte(`</CPA_TC|hist_tok>`)) {
		t.Fatalf("expected folded raw tool block in:\n%s", out)
	}
	if bytes.Contains(out, []byte(`CPA_TCS`)) {
		t.Fatalf("wrapper tag must not be emitted:\n%s", out)
	}
	if bytes.Contains(out, []byte(`<tool_call>`)) {
		t.Fatalf("old tool_call block must not be emitted:\n%s", out)
	}
	if bytes.Contains(out, []byte(`"tool_calls"`)) {
		t.Fatalf("original tool_calls field must be removed:\n%s", out)
	}
}

func TestFoldChatHistoryWithTagGroupUsesCustomRawProtocolTags(t *testing.T) {
	token := "hist_tok"
	tags := testCustomTagGroup()
	in := mustJSON(t, []any{
		map[string]any{"role": "assistant", "content": "Let me check.", "tool_calls": []any{
			map[string]any{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "get_weather",
					"arguments": `{"city":"SF"}`,
				},
			},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "sunny"},
	})
	out, err := FoldChatHistoryWithTagGroup(in, token, tags)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{
		[]byte(`<X_TOOL|get_weather|hist_tok>`),
		[]byte(`<X_ARG|city|hist_tok>`),
		[]byte(`</X_TOOL|hist_tok>`),
		[]byte(`<X_RESULT|0|hist_tok>`),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("missing %s in:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("CPA_TCS")) || bytes.Contains(out, []byte("X_CALLS")) || bytes.Contains(out, []byte("<tool_calls")) {
		t.Fatalf("folded history must use only custom raw protocol tags:\n%s", out)
	}
}

func TestFoldChatHistory_ToolResultBecomesUserMessage(t *testing.T) {
	token := "hist_tok"
	in := mustJSON(t, []any{
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "call_a", "type": "function",
				"function": map[string]any{"name": "f", "arguments": "{}"}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_a", "content": `{"ok":true}`},
	})
	out, err := FoldChatHistoryWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	content := gjson.GetBytes(out, "1.content").String()
	want := "<CPA_TR|0|hist_tok>\n{\"ok\":true}\n</CPA_TR|hist_tok>"
	if content != want {
		t.Fatalf("missing tool_result block:\n%s", out)
	}
	if bytes.Contains(out, []byte(`call_a`)) {
		t.Fatalf("folded prompt must not contain volatile tool id:\n%s", out)
	}
	if bytes.Contains(out, []byte(`"role":"tool"`)) || bytes.Contains(out, []byte(`"role": "tool"`)) {
		t.Fatalf("role=tool message must be replaced with role=user:\n%s", out)
	}
}

func TestFoldChatHistory_PreservesAssistantArrayContent(t *testing.T) {
	token := "hist_tok"
	in := mustJSON(t, []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "intro"},
			map[string]any{"type": "text", "text": "details"},
		}, "tool_calls": []any{
			map[string]any{"id": "call_a", "type": "function",
				"function": map[string]any{"name": "f", "arguments": "{}"}},
		}},
	})
	out, err := FoldChatHistoryWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	content := gjson.GetBytes(out, "0.content")
	if !content.IsArray() {
		t.Fatalf("assistant content must stay as an array:\n%s", out)
	}
	parts := content.Array()
	if len(parts) != 3 {
		t.Fatalf("expected 3 content parts, got %d:\n%s", len(parts), out)
	}
	if parts[0].Get("text").String() != "intro" || parts[1].Get("text").String() != "details" {
		t.Fatalf("original assistant parts must be preserved:\n%s", out)
	}
	if parts[2].Get("type").String() != "text" || bytes.Contains([]byte(parts[2].Get("text").String()), []byte(`CPA_TCS`)) || !bytes.Contains([]byte(parts[2].Get("text").String()), []byte(`<CPA_TC|f|hist_tok>`)) {
		t.Fatalf("folded raw tool block must be appended as a new text part:\n%s", out)
	}
}

func TestFoldChatHistory_ArgumentsKeyOrderStable(t *testing.T) {
	a, _ := FoldChatHistoryWithFence(mustJSON(t, []any{
		map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "c", "type": "function",
				"function": map[string]any{"name": "n", "arguments": `{"a":1,"b":2}`}},
		}},
	}), "hist_tok")
	b, _ := FoldChatHistoryWithFence(mustJSON(t, []any{
		map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": "c", "type": "function",
				"function": map[string]any{"name": "n", "arguments": `{"b":2,"a":1}`}},
		}},
	}), "hist_tok")
	if !bytes.Equal(a, b) {
		t.Fatalf("arguments key order must not affect fold output\nA: %s\nB: %s", a, b)
	}
}
