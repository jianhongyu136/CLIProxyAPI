package toolemu

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestFakeStreamChat_OrderAndStructure(t *testing.T) {
	prose := strings.Repeat("a", proseChunkSize*2+5)
	p := Parsed{
		Prose: prose,
		ToolCalls: []ParsedToolCall{{
			Name: "f", Arguments: []byte(strings.Repeat(`"`, argChunkSize+10)),
		}},
	}
	chunks, err := FakeStreamChat(p, UpstreamMeta{ResponseID: "r", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 4 {
		t.Fatalf("too few chunks: %d", len(chunks))
	}
	// last chunk is DONE
	if string(chunks[len(chunks)-1]) != "data: [DONE]\n\n" {
		t.Fatalf("last chunk not DONE: %q", chunks[len(chunks)-1])
	}
	// each data chunk must begin with "data: " and end with "\n\n"
	for i, c := range chunks {
		if !bytes.HasPrefix(c, []byte("data: ")) {
			t.Fatalf("chunk %d missing data: prefix: %q", i, c)
		}
		if !bytes.HasSuffix(c, []byte("\n\n")) {
			t.Fatalf("chunk %d missing trailing \\n\\n: %q", i, c)
		}
	}

	// first chunk has delta.role == "assistant"
	first := jsonPart(chunks[0])
	if got := ojson.GetBytesNoCopy(first, "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("first chunk delta.role = %q want assistant", got)
	}

	// somewhere we must see at least one content delta and one tool_calls header
	foundContent := false
	foundToolHeader := false
	foundArgDelta := false
	foundFinishToolCalls := false
	for _, c := range chunks {
		if bytes.Equal(c, []byte("data: [DONE]\n\n")) {
			continue
		}
		payload := jsonPart(c)
		if ojson.GetBytesNoCopy(payload, "choices.0.delta.content").Exists() {
			foundContent = true
		}
		if hdr := ojson.GetBytesNoCopy(payload, "choices.0.delta.tool_calls.0.function.name"); hdr.Exists() && hdr.String() == "f" {
			if ojson.GetBytesNoCopy(payload, "choices.0.delta.tool_calls.0.function.arguments").String() == "" {
				foundToolHeader = true
			}
		}
		if ojson.GetBytesNoCopy(payload, "choices.0.delta.tool_calls.0.function.arguments").Exists() {
			if !ojson.GetBytesNoCopy(payload, "choices.0.delta.tool_calls.0.function.name").Exists() {
				foundArgDelta = true
			}
		}
		if ojson.GetBytesNoCopy(payload, "choices.0.finish_reason").String() == "tool_calls" {
			foundFinishToolCalls = true
		}
	}
	if !foundContent {
		t.Fatal("missing content delta chunk")
	}
	if !foundToolHeader {
		t.Fatal("missing tool_calls header chunk")
	}
	if !foundArgDelta {
		t.Fatal("missing arguments delta chunk")
	}
	if !foundFinishToolCalls {
		t.Fatal("missing finish_reason=tool_calls chunk")
	}
}

func TestFakeStreamChat_UsageBeforeDone(t *testing.T) {
	chunks, err := FakeStreamChat(Parsed{Prose: "x"}, UpstreamMeta{
		ResponseID:   "r",
		Model:        "m",
		UsagePayload: []byte(`{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":7}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	payload := jsonPart(chunks[len(chunks)-2])
	if got := ojson.GetBytesNoCopy(payload, "usage.prompt_tokens_details.cached_tokens").Int(); got != 7 {
		t.Fatalf("cached_tokens = %d", got)
	}
	if choices := ojson.GetBytesNoCopy(payload, "choices"); !choices.Exists() || len(choices.Array()) != 0 {
		t.Fatalf("usage frame choices = %s", choices.Raw)
	}
}

func TestFakeStreamChat_PreservesUTF8AcrossChunkBoundaries(t *testing.T) {
	prose := strings.Repeat("a", proseChunkSize-1) + "中文结尾"
	argPrefix := strings.Repeat("a", argChunkSize-1-len(`{"text":"`))
	args := []byte(`{"text":"` + argPrefix + `中文参数"}`)
	p := Parsed{
		Prose: prose,
		ToolCalls: []ParsedToolCall{{
			Name: "f", Arguments: args,
		}},
	}

	chunks, err := FakeStreamChat(p, UpstreamMeta{ResponseID: "r", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}

	var gotProse strings.Builder
	var gotArgs strings.Builder
	for _, c := range chunks {
		if bytes.Equal(c, []byte("data: [DONE]\n\n")) {
			continue
		}
		payload := jsonPart(c)
		if delta := ojson.GetBytesNoCopy(payload, "choices.0.delta.content"); delta.Exists() {
			gotProse.WriteString(delta.String())
		}
		if delta := ojson.GetBytesNoCopy(payload, "choices.0.delta.tool_calls.0.function.arguments"); delta.Exists() {
			gotArgs.WriteString(delta.String())
		}
	}

	if gotProse.String() != prose {
		t.Fatalf("prose mismatch\n got: %q\nwant: %q", gotProse.String(), prose)
	}
	if gotArgs.String() != string(args) {
		t.Fatalf("arguments mismatch\n got: %q\nwant: %q", gotArgs.String(), string(args))
	}
}

// jsonPart returns the JSON payload of a "data: <json>\n\n" SSE line.
func jsonPart(chunk []byte) []byte {
	chunk = bytes.TrimPrefix(chunk, []byte("data: "))
	return bytes.TrimSuffix(chunk, []byte("\n\n"))
}
