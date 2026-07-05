package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestEnforceCacheControlLimit_PreservesToolEmuInjectionBreakpoint(t *testing.T) {
	payload := []byte(`{
		"model":"m",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"<tools_doc>...</tools_doc>\n<tool_protocol>...</tool_protocol>","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"c","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"d","cache_control":{"type":"ephemeral"}}]}
		]
	}`)

	out := enforceCacheControlLimit(payload, 4)
	if got := countCacheControls(out); got != 4 {
		t.Fatalf("cache_control count = %d, want 4: %s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("toolemu injection cache_control should be preserved, got %q: %s", got, out)
	}
}

func TestClaudeExecutor_ToolEmuNonStream(t *testing.T) {
	var receivedBodies [][]byte
	var receivedAnthropicVersions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, body)
		receivedAnthropicVersions = append(receivedAnthropicVersions, r.Header.Get("Anthropic-Version"))
		w.Header().Set("Content-Type", "application/json")
		assistantText := "I'll check that.\n" + rawToolBlockForExecutorTest("get_weather", map[string]string{"loc": "sf"})
		upstreamBody, _ := sjson.SetBytes([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":""}],"usage":{"input_tokens":3,"output_tokens":5}}`), "content.0.text", assistantText)
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules: []toolemu.ToolEmulationRule{{
			Provider: "claude",
			Models:   []string{"claude-test"},
		}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"weather in sf"}]}],"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"loc":{"type":"string"}},"required":["loc"]}}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-test",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
		Headers:         http.Header{"Anthropic-Version": []string{"2026-07-19"}},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(receivedBodies) == 0 {
		t.Fatal("expected at least one upstream request")
	}
	if gjson.GetBytes(receivedBodies[0], "tools").Exists() {
		t.Fatalf("folded upstream body still contains tools array: %s", string(receivedBodies[0]))
	}
	if got := receivedAnthropicVersions[0]; got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q, want %q", got, "2023-06-01")
	}
	if toolName := gjson.GetBytes(resp.Payload, "content.#(type==tool_use).name").String(); toolName != "get_weather" {
		t.Fatalf("expected tool_use name get_weather, got %q (resp=%s)", toolName, string(resp.Payload))
	}
	if stopReason := gjson.GetBytes(resp.Payload, "stop_reason").String(); stopReason != "tool_use" {
		t.Fatalf("expected stop_reason tool_use, got %q (resp=%s)", stopReason, string(resp.Payload))
	}
}

func TestClaudeExecutor_ToolEmuNonStream_RestoresOAuthToolName(t *testing.T) {
	var receivedBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, body)
		w.Header().Set("Content-Type", "application/json")
		assistantText := rawToolBlockForExecutorTest(claudeOAuthAliasFromBodyForTest(body), map[string]string{"command": "pwd"})
		upstreamBody, _ := sjson.SetBytes([]byte(`{"id":"msg_oauth","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":""}],"usage":{"input_tokens":1,"output_tokens":1}}`), "content.0.text", assistantText)
		_, _ = w.Write(upstreamBody)
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "sk-ant-oat-test-token",
		"base_url": server.URL,
	}, Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
	payload := []byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"run pwd"}]}],"tools":[{"name":"bash","description":"run shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(receivedBodies) == 0 {
		t.Fatal("expected upstream request")
	}
	foundBashInPrompt := false
	gjson.GetBytes(receivedBodies[0], "messages.0.content").ForEach(func(_, part gjson.Result) bool {
		if strings.Contains(part.Get("text").String(), "mcp__") {
			foundBashInPrompt = true
			return false
		}
		return true
	})
	if !foundBashInPrompt {
		t.Fatalf("OAuth request should expose remapped Bash to upstream prompt/body: %s", receivedBodies[0])
	}
	if got := gjson.GetBytes(resp.Payload, "content.#(type==tool_use).name").String(); got != "bash" {
		t.Fatalf("tool_use name = %q, want restored original bash; response=%s", got, resp.Payload)
	}
}

func TestClaudeExecutor_ToolEmuNonStream_CCHSignedAfterFold(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_cch","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "sk-ant-oat-test-token",
		"base_url": server.URL,
	}, Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
	payload := []byte(`{"model":"claude-test","system":[{"type":"text","text":"x-anthropic-billing-header: cch=00000;"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"bash","description":"run shell","input_schema":{"type":"object"}}]}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(received) == 0 {
		t.Fatal("expected upstream body")
	}
	if gjson.GetBytes(received, "tools").Exists() {
		t.Fatalf("upstream body should be folded before send: %s", received)
	}
	resigned, errResign := signAnthropicMessagesBody(received)
	if errResign != nil {
		t.Fatalf("resign folded upstream body: %v", errResign)
	}
	if !bytes.Equal(received, resigned) {
		t.Fatalf("folded upstream body was not CCH-signed after folding\nreceived=%s\nresigned=%s", received, resigned)
	}
}

func TestClaudeExecutor_ToolEmuNonStream_FinalCacheControlGuardPreservesInjection(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_cache","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{
		"model":"claude-test",
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"weather"}]},
			{"role":"assistant","content":[{"type":"text","text":"cached assistant","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"cached user","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"cached assistant 2","cache_control":{"type":"ephemeral"}}]}
		],
		"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object"}}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	assertClaudeToolEmuInjectionCacheControl(t, received)
}

func TestClaudeExecutor_ToolEmuNonStream_FinalCacheControlGuardNormalizesTTL(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_ttl","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{
		"model":"claude-test",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"weather"}]},
			{"role":"assistant","content":[{"type":"text","text":"cached assistant","cache_control":{"type":"ephemeral","ttl":"1h"}}]},
			{"role":"user","content":[{"type":"text","text":"continue"}]}
		],
		"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object"}}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(received) == 0 {
		t.Fatal("expected upstream body")
	}
	if got := gjson.GetBytes(received, "messages.1.content.0.cache_control.ttl"); got.Exists() {
		t.Fatalf("later 1h TTL should be downgraded after toolemu adds a default-5m injection breakpoint: %s", received)
	}
}

func assertClaudeToolEmuInjectionCacheControl(t *testing.T, received []byte) {
	t.Helper()
	if len(received) == 0 {
		t.Fatal("expected upstream body")
	}
	if got := countCacheControls(received); got > 4 {
		t.Fatalf("folded upstream body has %d cache_control blocks, want <= 4: %s", got, received)
	}
	injected := gjson.GetBytes(received, "messages.0.content.0")
	if !strings.Contains(injected.Get("text").String(), "<tool_protocol>") {
		t.Fatalf("expected first user part to be the toolemu injection: %s", received)
	}
	if injected.Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("toolemu injection cache_control must be preserved: %s", injected.Raw)
	}
}

func TestClaudeExecutor_ToolEmuStream(t *testing.T) {
	var receivedBodies [][]byte
	var receivedAnthropicVersions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, body)
		receivedAnthropicVersions = append(receivedAnthropicVersions, r.Header.Get("Anthropic-Version"))
		w.Header().Set("Content-Type", "text/event-stream")

		assistantText := "I'll check that.\n" + rawToolBlockForExecutorTest("get_weather", map[string]string{"loc": "sf"})
		var sse strings.Builder
		sse.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
		sse.WriteString("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		half := len(assistantText) / 2
		for _, part := range []string{assistantText[:half], assistantText[half:]} {
			payload, _ := sjson.SetBytes([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`), "delta.text", part)
			fmt.Fprintf(&sse, "event: content_block_delta\ndata: %s\n\n", payload)
		}
		sse.WriteString("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		sse.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n\n")
		sse.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		_, _ = w.Write([]byte(sse.String()))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"weather in sf"}]}],"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"loc":{"type":"string"}},"required":["loc"]}}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		ResponseFormat:  sdktranslator.FromString("claude"),
		OriginalRequest: payload,
		Headers:         http.Header{"Anthropic-Version": []string{"2026-07-19"}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	streamed := combined.String()
	if len(receivedBodies) == 0 {
		t.Fatal("expected at least one upstream request")
	}
	if gjson.GetBytes(receivedBodies[0], "tools").Exists() {
		t.Fatalf("folded upstream body still contains tools array: %s", string(receivedBodies[0]))
	}
	if got := receivedAnthropicVersions[0]; got != "2023-06-01" {
		t.Fatalf("Anthropic-Version = %q, want %q", got, "2023-06-01")
	}
	if !strings.Contains(streamed, `"type":"content_block_start"`) {
		t.Fatalf("stream missing content_block_start: %s", streamed)
	}
	if !strings.Contains(streamed, `"name":"get_weather"`) {
		t.Fatalf("stream missing tool_use name get_weather: %s", streamed)
	}
	if !strings.Contains(streamed, `"type":"input_json_delta"`) {
		t.Fatalf("stream missing input_json_delta: %s", streamed)
	}
	var argsBuf strings.Builder
	for _, line := range strings.Split(streamed, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if !strings.HasPrefix(payload, "{") || gjson.Get(payload, "delta.type").String() != "input_json_delta" {
			continue
		}
		argsBuf.WriteString(gjson.Get(payload, "delta.partial_json").String())
	}
	if argsBuf.String() != `{"loc":"sf"}` {
		t.Fatalf("reconstructed tool arguments = %q, want {\"loc\":\"sf\"}", argsBuf.String())
	}
	for _, want := range []string{`"stop_reason":"tool_use"`, `"input_tokens":3`, `"output_tokens":5`, `"type":"message_stop"`} {
		if !strings.Contains(streamed, want) {
			t.Fatalf("stream missing %s: %s", want, streamed)
		}
	}
}

func TestClaudeExecutor_ToolEmuStream_RestoresOAuthToolName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		assistantText := rawToolBlockForExecutorTest(claudeOAuthAliasFromBodyForTest(body), map[string]string{"command": "pwd"})
		var sse strings.Builder
		sse.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_oauth_stream\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		payload, _ := sjson.SetBytes([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`), "delta.text", assistantText)
		fmt.Fprintf(&sse, "event: content_block_delta\ndata: %s\n\n", payload)
		sse.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		sse.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		_, _ = w.Write([]byte(sse.String()))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-oat-test-token", "base_url": server.URL}, Metadata: map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}
	payload := []byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"run pwd"}]}],"tools":[{"name":"bash","description":"run shell","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"), ResponseFormat: sdktranslator.FromString("claude"), OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var combined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	streamed := combined.String()
	if !strings.Contains(streamed, `"name":"bash"`) {
		t.Fatalf("stream should restore OAuth tool name to bash: %s", streamed)
	}
	if strings.Contains(streamed, `"name":"Bash"`) {
		t.Fatalf("stream leaked remapped OAuth tool name Bash: %s", streamed)
	}
}

func claudeOAuthAliasFromBodyForTest(body []byte) string {
	text := string(body)
	start := strings.Index(text, "mcp__")
	if start < 0 {
		return "Bash"
	}
	end := start
	for end < len(text) {
		switch text[end] {
		case '"', '\\', ',', '}', ' ':
			return text[start:end]
		default:
			end++
		}
	}
	return text[start:end]
}

func TestClaudeExecutor_ToolEmuStream_ErrorBodyPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"quota exhausted"}}`))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"f","description":"f","input_schema":{"type":"object"}}]}`)
	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"), ResponseFormat: sdktranslator.FromString("claude"), OriginalRequest: payload,
	})
	if err == nil {
		t.Fatal("expected upstream status error")
	}
	if !strings.Contains(err.Error(), "quota exhausted") {
		t.Fatalf("error = %v, want upstream body", err)
	}
}

func TestClaudeExecutor_ToolEmuStream_FinalCacheControlGuardPreservesInjection(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cache_stream\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-test"}}},
	})
	defer toolemu.Default.Replace(toolemu.ToolEmulationConfig{})

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": server.URL}}
	payload := []byte(`{
		"model":"claude-test",
		"system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"weather"}]},
			{"role":"assistant","content":[{"type":"text","text":"cached assistant","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"cached user","cache_control":{"type":"ephemeral"}}]},
			{"role":"assistant","content":[{"type":"text","text":"cached assistant 2","cache_control":{"type":"ephemeral"}}]}
		],
		"tools":[{"name":"get_weather","description":"weather","input_schema":{"type":"object"}}]
	}`)
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-test", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"), ResponseFormat: sdktranslator.FromString("claude"), OriginalRequest: payload,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	assertClaudeToolEmuInjectionCacheControl(t, received)
}
