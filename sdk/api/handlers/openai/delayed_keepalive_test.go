package openai

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type delayedKeepAliveExecutor struct {
	initialFailureStreamExecutor
	stream func(context.Context) (*coreexecutor.StreamResult, error)
}

func (e *delayedKeepAliveExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return e.stream(ctx)
}

var delayedKeepAliveEndpoints = []struct {
	path    string
	handler func(*handlers.BaseAPIHandler) gin.HandlerFunc
	chunk   string
	error   string
}{
	{
		path:    "/v1/chat/completions",
		handler: func(base *handlers.BaseAPIHandler) gin.HandlerFunc { return NewOpenAIAPIHandler(base).ChatCompletions },
		chunk:   `{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		error:   `data: {"error":`,
	},
	{
		path:    "/v1/completions",
		handler: func(base *handlers.BaseAPIHandler) gin.HandlerFunc { return NewOpenAIAPIHandler(base).Completions },
		chunk:   `{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		error:   `data: {"error":`,
	},
	{
		path: "/v1/responses",
		handler: func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return NewOpenAIResponsesAPIHandler(base).Responses
		},
		chunk: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-delayed\",\"status\":\"completed\",\"output\":[]}}\n\n",
		error: "event: error",
	},
	{
		path: "/v1/messages",
		handler: func(base *handlers.BaseAPIHandler) gin.HandlerFunc {
			return claude.NewClaudeCodeAPIHandler(base).ClaudeMessages
		},
		chunk: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		error: "event: error",
	},
}

const delayedKeepAliveRequest = `{"model":"delayed-keepalive-model","messages":[{"role":"user","content":"hi"}],"prompt":"hi","input":"hi","max_tokens":16,"stream":true}`

func delayedKeepAliveBase(t *testing.T, stream func(context.Context) (*coreexecutor.StreamResult, error)) *handlers.BaseAPIHandler {
	t.Helper()
	executor := &delayedKeepAliveExecutor{stream: stream}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "delayed-keepalive-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 15, SendKeepAliveBeforeFT: true},
	}, manager)
}

func TestDelayedKeepAliveFastResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	})
	defer restore()

	for _, endpoint := range delayedKeepAliveEndpoints {
		for _, failure := range []bool{true, false} {
			outcome := "success"
			if failure {
				outcome = "error"
			}
			t.Run(strings.TrimPrefix(endpoint.path, "/")+"/"+outcome, func(t *testing.T) {
				base := delayedKeepAliveBase(t, func(context.Context) (*coreexecutor.StreamResult, error) {
					if failure {
						return nil, &coreexecutor.RequestTerminatedError{
							HTTPStatus: http.StatusTooManyRequests,
							Header:     http.Header{"Retry-After": {"17"}},
							Body:       []byte(`{"error":{"message":"fast upstream failure"}}`),
						}
					}
					chunks := make(chan coreexecutor.StreamChunk, 1)
					chunks <- coreexecutor.StreamChunk{Payload: []byte(endpoint.chunk)}
					close(chunks)
					return &coreexecutor.StreamResult{Chunks: chunks}, nil
				})
				router := gin.New()
				router.POST(endpoint.path, endpoint.handler(base))
				request := httptest.NewRequest(http.MethodPost, endpoint.path, strings.NewReader(delayedKeepAliveRequest))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				response := recorder.Result()
				defer response.Body.Close()
				if failure {
					if response.StatusCode != http.StatusTooManyRequests {
						t.Fatalf("status = %d, want 429 before the first heartbeat", response.StatusCode)
					}
					if response.Header.Get("Retry-After") != "17" || strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
						t.Fatalf("fast error lost its HTTP headers: %v", response.Header)
					}
				} else {
					if response.StatusCode != http.StatusOK || response.Header.Get("X-Accel-Buffering") != "no" || response.Header.Get("Cache-Control") != "no-cache, no-transform" {
						t.Fatalf("stream response = %d, headers=%v", response.StatusCode, response.Header)
					}
				}
				if strings.Contains(recorder.Body.String(), ": keep-alive") {
					t.Fatal("response was committed by a heartbeat before the first interval elapsed")
				}
			})
		}
	}
}

func TestDelayedKeepAliveFlushesWhileWaitingForUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range delayedKeepAliveEndpoints {
		t.Run(strings.TrimPrefix(endpoint.path, "/"), func(t *testing.T) {
			ticks := make(chan time.Time)
			restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(interval time.Duration) (<-chan time.Time, func()) {
				if interval != 15*time.Second {
					t.Errorf("heartbeat interval = %s, want 15s", interval)
				}
				return ticks, func() {}
			})
			defer restore()
			started := make(chan struct{})
			release := make(chan struct{})
			base := delayedKeepAliveBase(t, func(ctx context.Context) (*coreexecutor.StreamResult, error) {
				close(started)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
					return nil, preFTStatusError{status: http.StatusBadRequest, msg: "late upstream failure"}
				}
			})
			router := gin.New()
			router.POST(endpoint.path, endpoint.handler(base))
			server := httptest.NewServer(router)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+endpoint.path, strings.NewReader(delayedKeepAliveRequest))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			request.Header.Set("Content-Type", "application/json")
			type result struct {
				response *http.Response
				err      error
			}
			responseChan := make(chan result, 1)
			go func() {
				response, err := server.Client().Do(request)
				responseChan <- result{response: response, err: err}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("upstream execution never started")
			}
			sendTick := func() {
				t.Helper()
				select {
				case ticks <- time.Unix(15, 0):
				case <-ctx.Done():
					t.Fatal("heartbeat was not serviced while waiting for the upstream")
				}
			}
			sendTick()
			got := <-responseChan
			if got.err != nil {
				t.Fatal(got.err)
			}
			defer got.response.Body.Close()
			if got.response.StatusCode != http.StatusOK || got.response.Header.Get("Content-Type") != "text/event-stream" || got.response.Header.Get("X-Accel-Buffering") != "no" || got.response.Header.Get("Cache-Control") != "no-cache, no-transform" {
				t.Fatalf("heartbeat response = %d, headers=%v", got.response.StatusCode, got.response.Header)
			}
			reader := bufio.NewReader(got.response.Body)
			readHeartbeat := func() {
				t.Helper()
				for _, want := range []string{": keep-alive\n", "\n"} {
					line, errRead := reader.ReadString('\n')
					if errRead != nil || line != want {
						t.Fatalf("heartbeat line = %q, err=%v, want %q", line, errRead, want)
					}
				}
			}
			readHeartbeat()
			sendTick()
			readHeartbeat()
			close(release)
			tail, errRead := io.ReadAll(reader)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if !strings.Contains(string(tail), endpoint.error) || !strings.Contains(string(tail), "late upstream failure") {
				t.Fatalf("late upstream error was not delivered in the endpoint's SSE format: %s", tail)
			}
		})
	}
}
