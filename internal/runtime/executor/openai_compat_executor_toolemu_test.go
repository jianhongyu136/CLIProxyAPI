package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func rawToolBlockForExecutorTest(name string, args map[string]string) string {
	token := toolemu.DefaultFenceToken
	tags := toolemu.DefaultToolEmulationTagGroup
	var b strings.Builder
	b.WriteString("<")
	b.WriteString(tags.Tool)
	b.WriteString("|")
	b.WriteString(name)
	b.WriteString("|")
	b.WriteString(token)
	b.WriteString(">\n")

	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		b.WriteString("<")
		b.WriteString(tags.Arg)
		b.WriteString("|")
		b.WriteString(key)
		b.WriteString("|")
		b.WriteString(token)
		b.WriteString(">\n")
		b.WriteString(args[key])
		b.WriteString("\n</")
		b.WriteString(tags.Arg)
		b.WriteString("|")
		b.WriteString(token)
		b.WriteString(">\n")
	}
	b.WriteString("</")
	b.WriteString(tags.Tool)
	b.WriteString("|")
	b.WriteString(token)
	b.WriteString(">")
	return b.String()
}

func jsonStringForExecutorTest(t *testing.T, value string) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON string: %v", err)
	}
	return string(b)
}

func TestOpenAICompatExecutorToolEmuNonStream(t *testing.T) {
	var received []byte
	toolCall := jsonStringForExecutorTest(t, rawToolBlockForExecutorTest("get_weather", map[string]string{"loc": "sf"}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":` + toolCall + `},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{Enabled: true, Rules: []toolemu.ToolEmulationRule{{Provider: "openai-compatibility", Models: []string{"gpt-test"}}}})
	t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{Enabled: true}})
	resp, err := exec.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}, cliproxyexecutor.Request{
		Model:   "gpt-test",
		Payload: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !bytes.Contains(received, []byte("<tool_protocol>")) {
		t.Fatalf("upstream body missing tool protocol: %s", received)
	}
	if bytes.Contains(received, []byte(`"tools"`)) {
		t.Fatalf("upstream body still contains native tools: %s", received)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String(); got != "get_weather" {
		t.Fatalf("tool name = %q, response=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, response=%s", got, resp.Payload)
	}
}

func TestOpenAICompatExecutorToolEmuStream(t *testing.T) {
	toolCall := jsonStringForExecutorTest(t, rawToolBlockForExecutorTest("get_weather", map[string]string{"loc": "sf"}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"id":"chatcmpl_1","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":` + toolCall + `}}]}`,
			``,
			`data: {"id":"chatcmpl_1","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n")))
	}))
	defer server.Close()

	toolemu.Default.Replace(toolemu.ToolEmulationConfig{Enabled: true, Rules: []toolemu.ToolEmulationRule{{Provider: "openai-compatibility", Models: []string{"gpt-test"}}}})
	t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{Enabled: true}})
	result, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}, cliproxyexecutor.Request{
		Model:   "gpt-test",
		Payload: []byte(`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"weather"}],"tools":[{"type":"function","function":{"name":"get_weather","description":"weather","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var combined []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		combined = append(combined, chunk.Payload...)
	}
	if !bytes.Contains(combined, []byte("get_weather")) {
		t.Fatalf("stream missing tool name: %s", combined)
	}
}
