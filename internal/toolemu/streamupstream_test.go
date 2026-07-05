package toolemu

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type errorReader struct {
	err error
}

func (r errorReader) Read(_ []byte) (int, error) {
	return 0, r.err
}

func TestUpstreamPump_ChatShape(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"m","choices":[{"delta":{"role":"assistant"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":"Hello"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":" world"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{},"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{
		Reader: bytes.NewReader([]byte(sse)),
		Shape:  ShapeOpenAIChat,
		Parser: parser,
	}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spy.allProse() != "Hello world" {
		t.Fatalf("got prose %q", spy.allProse())
	}
	if meta.ResponseID != "r1" {
		t.Fatalf("got response id %q", meta.ResponseID)
	}
	if meta.Model != "m" {
		t.Fatalf("got model %q", meta.Model)
	}
}

func TestUpstreamPump_ResponsesShape(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"r2","model":"m2"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hi "}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"there"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r2","model":"m2","usage":{"input_tokens":8}}}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{
		Reader: bytes.NewReader([]byte(sse)),
		Shape:  ShapeOpenAIResponses,
		Parser: parser,
	}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if spy.allProse() != "Hi there" {
		t.Fatalf("got prose %q", spy.allProse())
	}
	if meta.ResponseID != "r2" {
		t.Fatalf("got response id %q", meta.ResponseID)
	}
}

func TestUpstreamPump_ChatReasoningDelta(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"m","choices":[{"delta":{"reasoning_content":"think"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":"answer"},"index":0}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: bytes.NewReader([]byte(sse)), Shape: ShapeOpenAIChat, Parser: parser}
	if _, err := pump.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spy.allReasoning() != "think" {
		t.Fatalf("got reasoning %q", spy.allReasoning())
	}
	if spy.allProse() != "answer" {
		t.Fatalf("got prose %q", spy.allProse())
	}
}

func TestUpstreamPump_ResponsesReasoningDelta(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: bytes.NewReader([]byte(sse)), Shape: ShapeOpenAIResponses, Parser: parser}
	if _, err := pump.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spy.allReasoning() != "think" {
		t.Fatalf("got reasoning %q", spy.allReasoning())
	}
	if spy.allProse() != "answer" {
		t.Fatalf("got prose %q", spy.allProse())
	}
}

func TestUpstreamPump_ResponsesCompletedStopsReading(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"before"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"r2","model":"m2","usage":{"input_tokens":8}}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":" after"}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: bytes.NewReader([]byte(sse)), Shape: ShapeOpenAIResponses, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.allProse(); got != "before" {
		t.Fatalf("got prose %q", got)
	}
	if meta.ResponseID != "r2" {
		t.Fatalf("got response id %q", meta.ResponseID)
	}
	if len(meta.UsagePayload) == 0 {
		t.Fatal("missing usage from completed event")
	}
}

func TestUpstreamPump_ResponsesIncompleteStopsReadingAndEmitsIncomplete(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"before"}`,
		``,
		`event: response.incomplete`,
		`data: {"type":"response.incomplete","response":{"id":"r2","model":"m2","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":8}}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":" after"}`,
		``,
	}, "\n")

	var frames [][]byte
	emitter := NewResponsesStreamEmitter(UpstreamMeta{ResponseID: "r2", Model: "m2"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "r2", Model: "m2"}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIResponses, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.ResponseStatus != "incomplete" {
		t.Fatalf("response status = %q, want incomplete", meta.ResponseStatus)
	}
	if !strings.Contains(string(meta.IncompleteDetails), `"max_output_tokens"`) {
		t.Fatalf("incomplete details not captured: %s", meta.IncompleteDetails)
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, " after") {
		t.Fatalf("response.incomplete must stop reading later deltas:\n%s", joined)
	}
	if !strings.Contains(joined, "event: response.incomplete") || !strings.Contains(joined, `"status":"incomplete"`) {
		t.Fatalf("expected response.incomplete terminal frame:\n%s", joined)
	}
	if strings.Contains(joined, "event: response.completed") {
		t.Fatalf("incomplete response must not emit completed terminal frame:\n%s", joined)
	}
}

func TestUpstreamPump_ResponsesFailedReturnsErrorWithoutCompleted(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"id":"r2","status":"failed","error":{"message":"boom","type":"server_error"}}}`,
		``,
	}, "\n")

	var frames [][]byte
	emitter := NewResponsesStreamEmitter(UpstreamMeta{ResponseID: "r2", Model: "m2"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "r2", Model: "m2"}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIResponses, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error = %v, want boom", err)
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, "event: response.completed") {
		t.Fatalf("response.failed must not emit completed terminal frame:\n%s", joined)
	}
}

func TestUpstreamPump_ChatFinishReasonStopsReading(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":"before"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{},"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":" after"},"index":0}]}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: bytes.NewReader([]byte(sse)), Shape: ShapeOpenAIChat, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.allProse(); got != "before" {
		t.Fatalf("got prose %q", got)
	}
	if meta.ResponseID != "r1" {
		t.Fatalf("got response id %q", meta.ResponseID)
	}
	if len(meta.UsagePayload) == 0 {
		t.Fatal("missing usage from finish event")
	}
}

func TestUpstreamPump_ContextCancellation(t *testing.T) {
	line := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"
	sse := strings.Repeat(line, 1000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{
		Reader: bytes.NewReader([]byte(sse)),
		Shape:  ShapeOpenAIChat,
		Parser: parser,
	}
	_, err := pump.Run(ctx)
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestUpstreamPump_ContextCancellationDoesNotComplete(t *testing.T) {
	line := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"
	sse := strings.Repeat(line, 1000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var frames [][]byte
	emitter := NewChatStreamEmitter(UpstreamMeta{ResponseID: "r1", Model: "m"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "r1", Model: "m"}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: bytes.NewReader([]byte(sse)), Shape: ShapeOpenAIChat, Parser: parser}
	_, err := pump.Run(ctx)
	if err == nil {
		t.Fatal("expected context error")
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, `"finish_reason":"stop"`) || strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("canceled upstream must not emit normal completion frames:\n%s", joined)
	}
}

func TestUpstreamPump_ScannerErrorDoesNotComplete(t *testing.T) {
	readErr := errors.New("read failed")
	var frames [][]byte
	emitter := NewChatStreamEmitter(UpstreamMeta{ResponseID: "r1", Model: "m"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "r1", Model: "m"}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: errorReader{err: readErr}, Shape: ShapeOpenAIChat, Parser: parser}
	_, err := pump.Run(context.Background())
	if !errors.Is(err, readErr) && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want read failure", err)
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, `"finish_reason":"stop"`) || strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("scanner error must not emit normal completion frames:\n%s", joined)
	}
}

func TestUpstreamPump_NoSpaceSSEFields(t *testing.T) {
	sse := strings.Join([]string{
		`event:response.output_text.delta`,
		`data:{"type":"response.output_text.delta","delta":"Hi"}`,
		``,
		`event:response.completed`,
		`data:{"type":"response.completed","response":{"id":"resp_1","model":"m","usage":{"input_tokens":1}}}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIResponses, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.allProse(); got != "Hi" {
		t.Fatalf("prose = %q, want Hi", got)
	}
	if meta.ResponseID != "resp_1" {
		t.Fatalf("response id = %q, want resp_1", meta.ResponseID)
	}
}

func TestUpstreamPump_MultilineDataEvent(t *testing.T) {
	sse := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta",`,
		`data: "delta":"Hi"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_2","model":"m"}}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIResponses, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.allProse(); got != "Hi" {
		t.Fatalf("prose = %q, want Hi", got)
	}
	if meta.ResponseID != "resp_2" {
		t.Fatalf("response id = %q, want resp_2", meta.ResponseID)
	}
}

func TestUpstreamPump_ClaudeErrorEventReturnsError(t *testing.T) {
	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeClaudeMessages, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("error = %v, want overloaded", err)
	}
}

func TestUpstreamPump_UpstreamErrorEventDoesNotUseToolEmuPrefix(t *testing.T) {
	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"rate_limit_error","message":"Concurrency limit exceeded for account"}}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeClaudeMessages, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "upstream error event: Concurrency limit exceeded for account") {
		t.Fatalf("error = %v, want upstream concurrency limit error", err)
	}
	if strings.Contains(err.Error(), "toolemu stream") {
		t.Fatalf("error = %v, must not use toolemu stream prefix for upstream request errors", err)
	}
}

func TestUpstreamPump_ClaudePreservesMaxTokensStopReason(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":2}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeClaudeMessages, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if meta.FinishOverride != "max_tokens" {
		t.Fatalf("finish override = %q, want max_tokens", meta.FinishOverride)
	}
}

func TestUpstreamPump_ChatErrorEventDoesNotComplete(t *testing.T) {
	var frames [][]byte
	emitter := NewChatStreamEmitter(UpstreamMeta{ResponseID: "r1", Model: "m"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "r1", Model: "m"}, DefaultFenceToken)
	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
		``,
	}, "\n")

	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIChat, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("error = %v, want overloaded", err)
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, `"finish_reason":"stop"`) || strings.Contains(joined, "data: [DONE]") {
		t.Fatalf("upstream error must not emit normal completion frames:\n%s", joined)
	}
}

func TestUpstreamPump_ClaudeErrorEventDoesNotEmitMessageStop(t *testing.T) {
	var frames [][]byte
	emitter := NewClaudeStreamEmitter(UpstreamMeta{ResponseID: "msg_1", Model: "m"}, func(frame []byte) {
		frames = append(frames, bytes.Clone(frame))
	})
	parser := NewStreamParser(emitter.Events(), UpstreamMeta{ResponseID: "msg_1", Model: "m"}, DefaultFenceToken)
	sse := strings.Join([]string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`,
		``,
	}, "\n")

	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeClaudeMessages, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("error = %v, want overloaded", err)
	}
	joined := string(bytes.Join(frames, nil))
	if strings.Contains(joined, "message_stop") || strings.Contains(joined, `"stop_reason":"end_turn"`) {
		t.Fatalf("upstream error must not emit normal Claude stop frames:\n%s", joined)
	}
}

func TestUpstreamPump_ChatUsageAfterFinishReasonIsCaptured(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":"before"},"index":0}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"r1","model":"m","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIChat, Parser: parser}
	meta, err := pump.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := spy.allProse(); got != "before" {
		t.Fatalf("prose = %q, want before", got)
	}
	if !strings.Contains(string(meta.UsagePayload), `"total_tokens":15`) {
		t.Fatalf("usage payload not captured after finish: %s", meta.UsagePayload)
	}
}

func TestUpstreamPump_ParseErrorIncludesRawModelOutput(t *testing.T) {
	rawText := "<CPA_TC|f|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"
	sse := strings.Join([]string{
		`data: {"id":"r1","model":"m","choices":[{"delta":{"content":"<CPA_TC|f|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"},"index":0}]}`,
		``,
	}, "\n")

	spy := &spyEvents{}
	parser := NewStreamParser(spy.events(), UpstreamMeta{}, DefaultFenceToken)
	pump := &UpstreamPump{Reader: strings.NewReader(sse), Shape: ShapeOpenAIChat, Parser: parser}
	_, err := pump.Run(context.Background())
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), `duplicate argument "x"`) {
		t.Fatalf("error = %v, want duplicate argument", err)
	}
	if !strings.Contains(err.Error(), "raw model output") {
		t.Fatalf("error = %v, want raw model output section", err)
	}
	if !strings.Contains(err.Error(), rawText) {
		t.Fatalf("error = %v, want raw model output %q", err, rawText)
	}
}
