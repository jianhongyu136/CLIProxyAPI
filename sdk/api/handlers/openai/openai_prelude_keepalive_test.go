package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestOpenAICompatPreludeErrorUsesOpenAISSEDataError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1, PreludeKeepAlive: true}}, nil)
	h := NewOpenAIAPIHandler(base)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected flusher")
	}
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream unavailable")}
	close(errs)

	base.HandleStreamPrelude(c, flusher, func(error) {}, data, errs, nil, handlers.StreamPreludeOptions{
		CommitHeaders: func() {
			c.Header("Content-Type", "text/event-stream")
		},
		WriteFirstChunk: func(chunk []byte) {
			_, _ = c.Writer.Write([]byte("data: "))
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n\n"))
		},
		WriteClosedBeforeData: func() {
			_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
		},
		WritePreludeError: func(errMsg *interfaces.ErrorMessage) {
			status := http.StatusInternalServerError
			if errMsg != nil && errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg != nil && errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			body := handlers.BuildErrorResponseBody(status, errText)
			_, _ = c.Writer.Write([]byte("data: "))
			_, _ = c.Writer.Write(body)
			_, _ = c.Writer.Write([]byte("\n\n"))
		},
		Continue: func(data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
			h.handleStreamResult(c, flusher, func(error) {}, data, errs, false)
		},
	})

	body := recorder.Body.String()
	if !strings.Contains(body, "data: {") || !strings.Contains(body, `"error"`) || !strings.Contains(body, "upstream unavailable") {
		t.Fatalf("expected OpenAI SSE data error, got %q", body)
	}
}

func TestPreludePreservesFirstChunkFinishReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/completions"} {
		for _, complete := range []bool{false, true} {
			name := endpoint + "/truncated"
			chunk := `{"choices":[{"index":0,"delta":{"content":"hello"}}]}`
			if complete {
				name = endpoint + "/complete"
				chunk = `{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`
			}
			t.Run(name, func(t *testing.T) {
				executor := &mockStreamExecutor{chunks: []string{chunk}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.RegisterExecutor(executor)
				auth := &coreauth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: coreauth.StatusActive}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "prelude-finish-model"}})
				defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
				base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1, PreludeKeepAlive: true}}, manager)
				h := NewOpenAIAPIHandler(base)
				router := gin.New()
				router.POST("/v1/chat/completions", h.ChatCompletions)
				router.POST("/v1/completions", h.Completions)
				body := `{"model":"prelude-finish-model","messages":[{"role":"user","content":"hi"}],"prompt":"hi","stream":true}`
				request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				out := recorder.Body.String()
				if !strings.Contains(out, ": keep-alive") {
					t.Fatalf("missing prelude heartbeat: %q", out)
				}
				if strings.Contains(out, "data: [DONE]") != complete {
					t.Fatalf("stream completion = %v, want %v: %q", !complete, complete, out)
				}
				if strings.Contains(out, "upstream stream closed before any chunk carried finish_reason") == complete {
					t.Fatalf("incorrect truncation detection: %q", out)
				}
			})
		}
	}
}
