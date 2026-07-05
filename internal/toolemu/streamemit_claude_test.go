package toolemu

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeStreamEmitterIncludesEventLines(t *testing.T) {
	var frames [][]byte
	emitter := NewClaudeStreamEmitter(UpstreamMeta{Provider: "p", Model: "m", ResponseID: "msg_1"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	events := emitter.Events()
	events.OnProseDelta("hello")
	events.OnComplete()

	joined := bytes.Join(frames, nil)
	if !bytes.Contains(joined, []byte("event: message_start\n")) {
		t.Fatalf("missing message_start event line:\n%s", joined)
	}
	if !bytes.Contains(joined, []byte("event: content_block_delta\n")) {
		t.Fatalf("missing content_block_delta event line:\n%s", joined)
	}
	if !bytes.Contains(joined, []byte("event: message_stop\n")) {
		t.Fatalf("missing message_stop event line:\n%s", joined)
	}
}

func TestClaudeStreamEmitterIncludesDefaultUsageWhenMissing(t *testing.T) {
	var frames [][]byte
	emitter := NewClaudeStreamEmitter(UpstreamMeta{Provider: "p", Model: "m", ResponseID: "msg_1"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	events := emitter.Events()
	events.OnProseDelta("hello")
	events.OnComplete()

	start := claudeEmitterPayload(t, frames, "message_start")
	if !start.Get("message.usage").Exists() {
		t.Fatalf("message_start missing usage: %s", start.Raw)
	}
	if !start.Get("message.usage.output_tokens").Exists() {
		t.Fatalf("message_start missing usage.output_tokens: %s", start.Raw)
	}

	delta := claudeEmitterPayload(t, frames, "message_delta")
	if !delta.Get("usage").Exists() {
		t.Fatalf("message_delta missing usage: %s", delta.Raw)
	}
	if !delta.Get("usage.output_tokens").Exists() {
		t.Fatalf("message_delta missing usage.output_tokens: %s", delta.Raw)
	}
}

func TestClaudeStreamEmitter_DisableParallelRejectsSecondToolCall(t *testing.T) {
	var frames [][]byte
	emitter := NewClaudeStreamEmitter(UpstreamMeta{ResponseID: "msg_1", Model: "m"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	emitter.SetToolChoice(ToolChoice{Kind: ToolChoiceKindRequired, DisableParallel: true})
	events := emitter.Events()
	events.OnToolCallStart(0, "call_0", "first")
	events.OnArgsDelta(0, `{}`)
	events.OnToolCallEnd(0)
	events.OnToolCallStart(1, "call_1", "second")

	joined := string(bytes.Join(frames, nil))
	if !strings.Contains(joined, "parallel") {
		t.Fatalf("missing disable_parallel validation error:\n%s", joined)
	}
	if strings.Contains(joined, `"name":"second"`) {
		t.Fatalf("second tool call must not be emitted after parallel violation:\n%s", joined)
	}
}

func claudeEmitterPayload(t *testing.T, frames [][]byte, wantEvent string) gjson.Result {
	t.Helper()
	for _, frame := range frames {
		var eventType, payload string
		for _, line := range strings.Split(strings.TrimSpace(string(frame)), "\n") {
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				payload = strings.TrimPrefix(line, "data: ")
			}
		}
		if eventType != wantEvent {
			continue
		}
		if !gjson.Valid(payload) {
			t.Fatalf("invalid JSON payload for %s: %q", wantEvent, payload)
		}
		return gjson.Parse(payload)
	}
	t.Fatalf("missing event %q in frames:\n%s", wantEvent, bytes.Join(frames, nil))
	return gjson.Result{}
}
