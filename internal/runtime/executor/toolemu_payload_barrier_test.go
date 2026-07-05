package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestToolEmulationPayloadBarrier(t *testing.T) {
	for _, provider := range []string{"openai-compatibility", "claude", "codex"} {
		for _, mode := range []string{"execute", "stream", "retry"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				stream := mode == "stream"
				model := "gpt-5.4"
				format := sdktranslator.FormatOpenAI
				history := "messages"
				payload := `{"model":"gpt-5.4","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"tool_choice":"required"}`
				choicePath := "tool_choice"
				var configuredChoice any = "none"
				if provider == "claude" {
					model = "claude-test"
					format = sdktranslator.FormatClaude
					choicePath = "tool_choice.type"
					configuredChoice = map[string]any{"type": "none"}
					payload = `{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"first"}]},{"role":"assistant","content":[{"type":"text","text":"second"}]},{"role":"user","content":[{"type":"text","text":"third"}]}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`
				} else if provider == "codex" {
					format = sdktranslator.FormatOpenAIResponse
					history = "input"
					payload = `{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"third"}]}],"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"tool_choice":"required"}`
				}
				cfg := &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{
					Enabled: true, ParseRetry: 1,
					Rules: []toolemu.ToolEmulationRule{{Provider: provider, Models: []string{model}}},
				}}
				// These rules must match after folding removes native tools, not before it.
				models := []config.PayloadModelRule{{Name: model, NotExist: []string{"tools"}}}
				cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{
					"tool_choice": configuredChoice, "metadata.barrier": "configured", "max_tokens": 1,
				}}}
				// Removing an array element is deliberately non-idempotent: each send must apply it once.
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{history + ".0", "system", "instructions"}}}
				toolemu.Default.Replace(cfg.ToolEmulation)
				t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

				requests := make(chan []byte, 2)
				var attempts atomic.Int32
				toolText := rawToolBlockForExecutorTest("get_weather", nil)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request: %v", errRead)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					requests <- body
					text := toolText
					if attempts.Add(1) == 1 && mode == "retry" {
						text = "retry without a tool call"
					}
					writeToolEmulationBarrierResponse(w, provider, stream, text)
				}))
				defer server.Close()
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
				var executor cliproxyauth.ProviderExecutor
				switch provider {
				case "claude":
					executor = NewClaudeExecutor(cfg)
				case "codex":
					executor = NewCodexExecutor(cfg)
				default:
					executor = NewOpenAICompatExecutor(provider, cfg)
				}
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
				opts := cliproxyexecutor.Options{SourceFormat: format, ResponseFormat: format, OriginalRequest: []byte(payload), Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				wantAttempts := 1
				if mode == "retry" {
					wantAttempts = 2
				}
				if got := int(attempts.Load()); got != wantAttempts {
					t.Fatalf("upstream attempts = %d, want %d", got, wantAttempts)
				}
				for i := 0; i < wantAttempts; i++ {
					body := <-requests
					if gjson.GetBytes(body, "metadata.barrier").String() != "configured" || gjson.GetBytes(body, choicePath).String() != "none" || gjson.GetBytes(body, "max_tokens").Int() != 1 {
						t.Errorf("send %d lost final overrides: %s", i, body)
					}
					if strings.Contains(string(body), "<tools_doc>") || gjson.GetBytes(body, "system").Exists() || gjson.GetBytes(body, "instructions").Exists() {
						t.Errorf("send %d restored filtered fields: %s", i, body)
					}
					if got := gjson.GetBytes(body, history+".#").Int(); got != int64(2+i) {
						t.Errorf("send %d applied array filter incorrectly: count=%d body=%s", i, got, body)
					}
					if gjson.GetBytes(body, history+".0.role").String() != "assistant" {
						t.Errorf("send %d filtered the wrong item: %s", i, body)
					}
				}
			})
		}
	}
}

func writeToolEmulationBarrierResponse(w http.ResponseWriter, provider string, stream bool, text string) {
	if provider == "codex" {
		w.Header().Set("Content-Type", "text/event-stream")
		if stream {
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text)
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_barrier\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", text)
		return
	}
	if provider == "claude" {
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_barrier\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", text)
			_, _ = fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"msg_barrier","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, text)
		}
		return
	}
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chat_barrier\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", text)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chat_barrier\",\"model\":\"gpt-5.4\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
	} else {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"chat_barrier","object":"chat.completion","model":"gpt-5.4","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, text)
	}
}
