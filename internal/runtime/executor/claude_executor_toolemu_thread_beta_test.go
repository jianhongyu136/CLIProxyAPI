package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeToolEmulationThreadBetas(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "retry"} {
		for _, filter := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/filter=%v", mode, filter), func(t *testing.T) {
				cfg := &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{
					Enabled: true, ParseRetry: 1,
					Rules: []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-opus-5"}}},
				}}
				toolemu.Default.Replace(cfg.ToolEmulation)
				t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })
				var sent []string
				transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					body, errRead := io.ReadAll(request.Body)
					if errRead != nil {
						return nil, errRead
					}
					if ojson.GetBytesNoCopy(body, "tools").Exists() {
						t.Errorf("native tools survived tool-emulation folding: %s", body)
					}
					sent = append(sent, strings.Join(helps.HeaderValuesCaseInsensitive(request.Header, "Anthropic-Beta"), ","))
					text := rawToolBlockForExecutorTest("lookup", nil)
					if mode == "retry" && len(sent) == 1 {
						text = "retry without a tool call"
					}
					id := fmt.Sprintf("msg_emulated_%d", len(sent))
					if ojson.GetBytesNoCopy(body, "stream").Bool() {
						prefix, rest, _ := strings.Cut(claudeThreadTestSSE(id), "event: content_block_delta\ndata: ")
						payload, suffix, _ := strings.Cut(rest, "\n\n")
						delta := ojson.NewDocString(payload)
						if errSet := delta.Set("delta.text", text); errSet != nil {
							return nil, errSet
						}
						stream := prefix + "event: content_block_delta\ndata: " + delta.String() + "\n\n" + suffix
						return claudeThreadTestResponse(request, http.StatusOK, "text/event-stream", stream), nil
					}
					response := ojson.NewDocString(claudeThreadTestMessage(id))
					if errSet := response.Set("content.0.text", text); errSet != nil {
						return nil, errSet
					}
					return claudeThreadTestResponse(request, http.StatusOK, "application/json", response.String()), nil
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(cfg)
				auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-emulated-thread"}}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatClaude,
					Headers:      http.Header{"Anthropic-Beta": []string{"message-threads-2026-08-12"}},
				}
				request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","max_tokens":2048,"thread":{"type":"create"},"betas":["effort-2025-11-24"],"messages":[{"role":"user","content":"first"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)}
				firstAttempts := 1
				if mode == "retry" {
					firstAttempts = 2
				}
				continued := false
				continueThread := func() {
					continued = true
					doc := ojson.NewDoc(request.Payload)
					if errSet := doc.Set("thread", map[string]string{"type": "continue", "previous_message_id": fmt.Sprintf("msg_emulated_%d", firstAttempts)}); errSet != nil {
						t.Fatal(errSet)
					}
					next := executor
					if filter {
						nextCfg := *cfg
						nextCfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: []string{"betas"}}}
						next = executor.WithConfig(&nextCfg)
					} else if errDelete := doc.Delete("betas"); errDelete != nil {
						t.Fatal(errDelete)
					}
					request.Payload = doc.Bytes()
					var errExecute error
					if mode == "stream" {
						var result *cliproxyexecutor.StreamResult
						result, errExecute = next.ExecuteStream(ctx, auth, request, opts)
						if errExecute == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									errExecute = chunk.Err
								}
							}
						}
					} else {
						_, errExecute = next.Execute(ctx, auth, request, opts)
					}
					if filter {
						var conflict helps.ClaudeThreadBetaConflictError
						if !errors.As(errExecute, &conflict) {
							t.Fatalf("filtered body beta restored from Thread snapshot: error=%v", errExecute)
						}
					} else if errExecute != nil {
						t.Fatalf("continuation after emulated response: %v", errExecute)
					}
				}
				if mode == "stream" {
					result, errStream := executor.ExecuteStream(ctx, auth, request, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						if !continued && claudeThreadTestTerminal(chunk.Payload) {
							continueThread()
						}
					}
				} else {
					if _, errExecute := executor.Execute(ctx, auth, request, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
					continueThread()
				}
				wantAttempts := firstAttempts
				if !filter {
					wantAttempts++
				}
				if !continued || len(sent) != wantAttempts {
					t.Fatalf("continued=%v, upstream sends=%d, want %d", continued, len(sent), wantAttempts)
				}
				for _, betas := range sent {
					if betas != sent[0] || !strings.Contains(betas, "effort-2025-11-24") {
						t.Fatalf("Thread wire beta profile changed: %q", sent)
					}
				}
			})
		}
	}
}
