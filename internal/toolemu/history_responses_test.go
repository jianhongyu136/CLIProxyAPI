package toolemu

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestFoldResponsesInput_FunctionCallFolded(t *testing.T) {
	token := "resp_tok"
	in := mustJSON(t, []any{
		map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
		map[string]any{"type": "function_call", "id": "call_x", "name": "search",
			"arguments": `{"q":"foo"}`},
	})
	out, err := FoldResponsesInputWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`<CPA_TC|search|resp_tok>`)) || !bytes.Contains(out, []byte(`<CPA_TA|q|resp_tok>`)) || !bytes.Contains(out, []byte(`</CPA_TC|resp_tok>`)) {
		t.Fatalf("missing raw tool block:\n%s", out)
	}
	if bytes.Contains(out, []byte(`CPA_TCS`)) {
		t.Fatalf("wrapper tag must not be emitted:\n%s", out)
	}
	if bytes.Contains(out, []byte(`<tool_call>`)) {
		t.Fatalf("old tool_call block must not be emitted:\n%s", out)
	}
	if bytes.Contains(out, []byte(`"function_call"`)) {
		t.Fatalf("function_call item must be removed:\n%s", out)
	}
}

func TestFoldResponsesInputWithTagGroupUsesCustomRawProtocolTags(t *testing.T) {
	token := "resp_tok"
	tags := testCustomTagGroup()
	in := mustJSON(t, []any{
		map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hi"}}},
		map[string]any{"type": "function_call", "id": "call_x", "name": "search",
			"arguments": `{"q":"foo"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_x", "output": `{"ok":true}`},
	})
	out, err := FoldResponsesInputWithTagGroup(in, token, tags)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{
		[]byte(`<X_TOOL|search|resp_tok>`),
		[]byte(`<X_ARG|q|resp_tok>`),
		[]byte(`</X_TOOL|resp_tok>`),
		[]byte(`<X_RESULT|0|resp_tok>`),
	} {
		if !bytes.Contains(out, want) {
			t.Fatalf("missing %s in:\n%s", want, out)
		}
	}
	if bytes.Contains(out, []byte("CPA_TCS")) || bytes.Contains(out, []byte("X_CALLS")) || bytes.Contains(out, []byte("<tool_calls")) {
		t.Fatalf("folded input must use only custom raw protocol tags:\n%s", out)
	}
}

func TestFoldResponsesInput_FunctionCallOutputFolded(t *testing.T) {
	token := "resp_tok"
	in := mustJSON(t, []any{
		map[string]any{"type": "function_call", "id": "call_x", "name": "f", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "call_x", "output": `{"ok":true}`},
	})
	out, err := FoldResponsesInputWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	content := gjson.GetBytes(out, "1.content.0.text").String()
	want := "<CPA_TR|0|resp_tok>\n{\"ok\":true}\n</CPA_TR|resp_tok>"
	if content != want {
		t.Fatalf("missing tool_result block:\n%s", out)
	}
	if bytes.Contains(out, []byte(`call_x`)) {
		t.Fatalf("folded prompt must not contain volatile tool id:\n%s", out)
	}
}

func TestFoldResponsesInput_ResultIndexesUseCallIDBeforeItemID(t *testing.T) {
	token := "resp_tok"
	in := mustJSON(t, []any{
		map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "first", "arguments": "{}"},
		map[string]any{"type": "function_call", "id": "fc_2", "call_id": "call_2", "name": "second", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "call_2", "output": "second result"},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "first result"},
	})
	out, err := FoldResponsesInputWithFence(in, token)
	if err != nil {
		t.Fatal(err)
	}
	content := gjson.GetBytes(out, "1.content.0.text").String()
	want := renderResultBlock(1, "second result", token) + "\n" + renderResultBlock(0, "first result", token)
	if content != want {
		t.Fatalf("content = %q, want %q\nfull: %s", content, want, out)
	}
}

func TestFoldResponsesInput_Deterministic(t *testing.T) {
	a, _ := FoldResponsesInputWithFence(mustJSON(t, []any{
		map[string]any{"type": "function_call", "id": "c", "name": "n",
			"arguments": `{"a":1,"b":2}`},
	}), "resp_tok")
	b, _ := FoldResponsesInputWithFence(mustJSON(t, []any{
		map[string]any{"type": "function_call", "id": "c", "name": "n",
			"arguments": `{"b":2,"a":1}`},
	}), "resp_tok")
	if !bytes.Equal(a, b) {
		t.Fatalf("arguments key order should not change output\nA: %s\nB: %s", a, b)
	}
}
