package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorToolEmuStreamClosesUpstreamAtTerminalEvent(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
		t.Run(format.String(), func(t *testing.T) {
			cfg := &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{
				Enabled: true,
				Rules:   []toolemu.ToolEmulationRule{{Provider: "claude", Models: []string{"claude-opus-5"}}},
			}}
			toolemu.Default.Replace(cfg.ToolEmulation)
			t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

			body := &claudeToolEmuTerminalBody{data: strings.NewReader(claudeThreadTestSSE("msg_terminal")), closed: make(chan struct{})}
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				response := claudeThreadTestResponse(req, http.StatusOK, "text/event-stream", "")
				response.Body = body
				return response, nil
			})
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport)), 5*time.Second)
			defer cancel()
			payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"stream":true}`)
			if format == sdktranslator.FormatOpenAIResponse {
				payload = []byte(`{"model":"claude-opus-5","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"stream":true}`)
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "key-terminal"}}
			result, errStream := NewClaudeExecutor(cfg).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model: "claude-opus-5", Payload: payload,
			}, cliproxyexecutor.Options{SourceFormat: format, ResponseFormat: format, Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream: %v", errStream)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream: %v", chunk.Err)
				}
			}
			select {
			case <-body.closed:
			case <-ctx.Done():
				t.Fatal("upstream body did not close after the terminal event")
			}
			if got := body.tailReads.Load(); got != 0 {
				t.Fatalf("reads after message_stop = %d, want no post-stream draining", got)
			}
		})
	}
}

type claudeToolEmuTerminalBody struct {
	data      *strings.Reader
	closed    chan struct{}
	closeOnce sync.Once
	tailReads atomic.Int32
}

func (b *claudeToolEmuTerminalBody) Read(p []byte) (int, error) {
	if b.data.Len() == 0 {
		b.tailReads.Add(1)
		return 0, io.EOF
	}
	return b.data.Read(p)
}

func (b *claudeToolEmuTerminalBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}
