package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorInstallationIDReachesUpstream(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "compact"} {
		for _, clientID := range []string{"", "client-installation"} {
			t.Run(mode+"/"+clientID, func(t *testing.T) {
				requests := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
					}
					requests <- body
					if mode == "compact" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"resp_install","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_install\",\"object\":\"response\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
				}))
				defer server.Close()

				auth := &cliproxyauth.Auth{ID: "installation-test-auth", Attributes: map[string]string{
					"base_url": server.URL,
					"api_key":  "test",
				}}
				executor := NewCodexExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":[]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				wantID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:installation:"+auth.ID)).String()
				if clientID != "" {
					opts.OriginalRequest = []byte(`{"model":"gpt-5.4","input":[],"client_metadata":{"x-codex-installation-id":"client-installation"}}`)
					wantID = clientID
				}
				if mode == "stream" {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if mode == "compact" {
						opts.Alt = "responses/compact"
					}
					if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
				body := <-requests
				if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != wantID {
					t.Fatalf("upstream installation id = %q, want %q; body=%s", got, wantID, body)
				}
				if gjson.GetBytes(req.Payload, "client_metadata.x-codex-installation-id").Exists() {
					t.Fatal("client payload was mutated")
				}
			})
		}
	}
}
