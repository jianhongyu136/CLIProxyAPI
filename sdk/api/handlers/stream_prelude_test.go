package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestStreamingPreludeKeepAliveEnabledRequiresSwitchAndInterval(t *testing.T) {
	tests := []struct {
		name string
		cfg  *sdkconfig.SDKConfig
		want bool
	}{
		{name: "nil config", cfg: nil, want: false},
		{name: "switch off", cfg: &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1, PreludeKeepAlive: false}}, want: false},
		{name: "interval disabled", cfg: &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0, PreludeKeepAlive: true}}, want: false},
		{name: "negative interval", cfg: &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: -1, PreludeKeepAlive: true}}, want: false},
		{name: "enabled", cfg: &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1, PreludeKeepAlive: true}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StreamingPreludeKeepAliveEnabled(tt.cfg); got != tt.want {
				t.Fatalf("StreamingPreludeKeepAliveEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleStreamPrelude(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newContext := func() (*httptest.ResponseRecorder, *gin.Context, http.Flusher) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		return recorder, c, recorder
	}

	newEnabledHandler := func() *BaseAPIHandler {
		return NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1, PreludeKeepAlive: true}}, nil)
	}

	newValidOptions := func(c *gin.Context) StreamPreludeOptions {
		return StreamPreludeOptions{
			CommitHeaders:         func() {},
			WriteFirstChunk:       func([]byte) {},
			WriteClosedBeforeData: func() {},
			WritePreludeError:     func(*interfaces.ErrorMessage) {},
			Continue:              func(<-chan []byte, <-chan *interfaces.ErrorMessage) {},
		}
	}

	t.Run("returns false when disabled", func(t *testing.T) {
		h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
		recorder, c, flusher := newContext()
		_ = recorder
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)

		if got := h.HandleStreamPrelude(c, flusher, func(error) {}, data, errs, nil, newValidOptions(c)); got {
			t.Fatal("expected disabled prelude helper to return false")
		}
	})

	t.Run("returns false when receiver or required arguments are missing", func(t *testing.T) {
		recorder, c, flusher := newContext()
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)
		opts := newValidOptions(c)

		if got := (*BaseAPIHandler)(nil).HandleStreamPrelude(c, flusher, func(error) {}, data, errs, nil, opts); got {
			t.Fatal("expected false for nil handler")
		}
		if got := newEnabledHandler().HandleStreamPrelude(nil, flusher, func(error) {}, data, errs, nil, opts); got {
			t.Fatal("expected false for nil context")
		}
		if got := newEnabledHandler().HandleStreamPrelude(c, nil, func(error) {}, data, errs, nil, opts); got {
			t.Fatal("expected false for nil flusher")
		}
		if got := newEnabledHandler().HandleStreamPrelude(c, flusher, nil, data, errs, nil, opts); got {
			t.Fatal("expected false for nil cancel")
		}
		_ = recorder
	})

	t.Run("returns false when required callbacks are missing", func(t *testing.T) {
		h := newEnabledHandler()
		_, c, flusher := newContext()
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)

		cases := []struct {
			name string
			opts StreamPreludeOptions
		}{
			{name: "missing commit headers", opts: StreamPreludeOptions{WriteFirstChunk: func([]byte) {}, WriteClosedBeforeData: func() {}, WritePreludeError: func(*interfaces.ErrorMessage) {}, Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {}}},
			{name: "missing first chunk", opts: StreamPreludeOptions{CommitHeaders: func() {}, WriteClosedBeforeData: func() {}, WritePreludeError: func(*interfaces.ErrorMessage) {}, Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {}}},
			{name: "missing closed before data", opts: StreamPreludeOptions{CommitHeaders: func() {}, WriteFirstChunk: func([]byte) {}, WritePreludeError: func(*interfaces.ErrorMessage) {}, Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {}}},
			{name: "missing prelude error", opts: StreamPreludeOptions{CommitHeaders: func() {}, WriteFirstChunk: func([]byte) {}, WriteClosedBeforeData: func() {}, Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {}}},
			{name: "missing continue", opts: StreamPreludeOptions{CommitHeaders: func() {}, WriteFirstChunk: func([]byte) {}, WriteClosedBeforeData: func() {}, WritePreludeError: func(*interfaces.ErrorMessage) {}}},
		}

		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				if got := h.HandleStreamPrelude(c, flusher, func(error) {}, data, errs, nil, tt.opts); got {
					t.Fatalf("expected false when %s", tt.name)
				}
			})
		}
	})

	t.Run("commits headers before waiting and continues after first chunk", func(t *testing.T) {
		h := newEnabledHandler()
		recorder, c, flusher := newContext()
		data := make(chan []byte, 1)
		errs := make(chan *interfaces.ErrorMessage)
		continueCalled := false
		commitHeadersCalled := false
		firstChunkCalled := false

		go func() {
			time.Sleep(10 * time.Millisecond)
			data <- []byte("first-chunk")
		}()

		got := h.HandleStreamPrelude(c, flusher, func(error) {
			t.Fatal("did not expect cancel on first chunk")
		}, data, errs, nil, StreamPreludeOptions{
			CommitHeaders: func() {
				commitHeadersCalled = true
				c.Header("Content-Type", "text/event-stream")
			},
			WriteFirstChunk: func(chunk []byte) {
				firstChunkCalled = true
				if !commitHeadersCalled {
					t.Fatal("expected headers committed before first chunk")
				}
				_, _ = c.Writer.Write(chunk)
			},
			WriteClosedBeforeData: func() {
				t.Fatal("did not expect closed-before-data path")
			},
			WritePreludeError: func(*interfaces.ErrorMessage) {
				t.Fatal("did not expect prelude error path")
			},
			Continue: func(gotData <-chan []byte, gotErrs <-chan *interfaces.ErrorMessage) {
				continueCalled = true
				if gotData != data {
					t.Fatal("expected original data channel in Continue")
				}
				if gotErrs != errs {
					t.Fatal("expected original err channel in Continue")
				}
			},
		})
		if !got {
			t.Fatal("expected prelude helper to handle first chunk")
		}
		if !commitHeadersCalled {
			t.Fatal("expected CommitHeaders to be called")
		}
		if !recorder.Flushed {
			t.Fatal("expected flush after committing headers and first chunk")
		}
		if recorder.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("content-type = %q, want text/event-stream", recorder.Header().Get("Content-Type"))
		}
		if !firstChunkCalled {
			t.Fatal("expected first chunk callback")
		}
		if !continueCalled {
			t.Fatal("expected Continue to be called after first chunk")
		}
		if body := recorder.Body.String(); !strings.Contains(body, "first-chunk") {
			t.Fatalf("expected first chunk in body, got %q", body)
		}
	})

	t.Run("prefers pending buffered error over closed without data", func(t *testing.T) {
		h := newEnabledHandler()
		recorder, c, flusher := newContext()
		data := make(chan []byte)
		close(data)

		wantErr := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("buffered prelude error")}
		errs := make(chan *interfaces.ErrorMessage, 1)
		errs <- wantErr
		close(errs)

		preludeErrorCalls := 0
		closedBeforeDataCalls := 0
		var cancelErr error

		got := h.HandleStreamPrelude(c, flusher, func(err error) {
			cancelErr = err
		}, data, errs, nil, StreamPreludeOptions{
			CommitHeaders: func() {},
			WriteFirstChunk: func([]byte) {
				t.Fatal("did not expect first chunk")
			},
			WriteClosedBeforeData: func() {
				closedBeforeDataCalls++
			},
			WritePreludeError: func(errMsg *interfaces.ErrorMessage) {
				preludeErrorCalls++
				if errMsg != wantErr {
					t.Fatalf("prelude error = %p, want %p", errMsg, wantErr)
				}
				_, _ = c.Writer.Write([]byte("prelude-error"))
			},
			Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {
				t.Fatal("did not expect Continue")
			},
		})
		if !got {
			t.Fatal("expected prelude helper to handle pending buffered error")
		}
		if preludeErrorCalls != 1 {
			t.Fatalf("prelude error calls = %d, want 1", preludeErrorCalls)
		}
		if closedBeforeDataCalls != 0 {
			t.Fatalf("closed-before-data calls = %d, want 0", closedBeforeDataCalls)
		}
		if !recorder.Flushed {
			t.Fatal("expected response to be flushed")
		}
		if cancelErr == nil || cancelErr.Error() != "buffered prelude error" {
			t.Fatalf("cancel err = %v, want buffered prelude error", cancelErr)
		}
	})

	t.Run("flushes and cancels when prelude error arrives before first payload", func(t *testing.T) {
		h := newEnabledHandler()
		recorder, c, flusher := newContext()
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage, 1)
		wantErr := &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("stream bootstrap failed")}
		errs <- wantErr
		close(errs)

		var gotErr *interfaces.ErrorMessage
		var cancelErr error

		got := h.HandleStreamPrelude(c, flusher, func(err error) {
			cancelErr = err
		}, data, errs, nil, StreamPreludeOptions{
			CommitHeaders: func() {},
			WriteFirstChunk: func([]byte) {
				t.Fatal("did not expect first chunk")
			},
			WriteClosedBeforeData: func() {
				t.Fatal("expected prelude error to win before closed-before-data callback")
			},
			WritePreludeError: func(errMsg *interfaces.ErrorMessage) {
				gotErr = errMsg
				_, _ = c.Writer.Write([]byte("error"))
			},
			Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {
				t.Fatal("did not expect Continue")
			},
		})
		if !got {
			t.Fatal("expected prelude helper to handle early error")
		}
		if gotErr != wantErr {
			t.Fatalf("got err = %p, want %p", gotErr, wantErr)
		}
		if !recorder.Flushed {
			t.Fatal("expected response to be flushed")
		}
		if cancelErr == nil || cancelErr.Error() != "stream bootstrap failed" {
			t.Fatalf("cancel err = %v, want stream bootstrap failed", cancelErr)
		}
	})

	t.Run("calls closed before data callback then flushes and cancels nil", func(t *testing.T) {
		h := newEnabledHandler()
		recorder, c, flusher := newContext()
		data := make(chan []byte)
		close(data)
		errs := make(chan *interfaces.ErrorMessage)
		close(errs)

		closedBeforeDataCalls := 0
		var cancelErr error

		got := h.HandleStreamPrelude(c, flusher, func(err error) {
			cancelErr = err
		}, data, errs, nil, StreamPreludeOptions{
			CommitHeaders: func() {},
			WriteFirstChunk: func([]byte) {
				t.Fatal("did not expect first chunk")
			},
			WriteClosedBeforeData: func() {
				closedBeforeDataCalls++
				_, _ = c.Writer.Write([]byte("closed"))
			},
			WritePreludeError: func(*interfaces.ErrorMessage) {
				t.Fatal("did not expect prelude error")
			},
			Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {
				t.Fatal("did not expect Continue")
			},
		})
		if !got {
			t.Fatal("expected prelude helper to handle closed-before-data")
		}
		if closedBeforeDataCalls != 1 {
			t.Fatalf("closed-before-data calls = %d, want 1", closedBeforeDataCalls)
		}
		if !recorder.Flushed {
			t.Fatal("expected response to be flushed")
		}
		if cancelErr != nil {
			t.Fatalf("cancel err = %v, want nil", cancelErr)
		}
	})

	t.Run("writes default keepalive frame before first payload", func(t *testing.T) {
		h := newEnabledHandler()
		recorder, c, flusher := newContext()
		data := make(chan []byte, 1)
		errs := make(chan *interfaces.ErrorMessage)

		go func() {
			time.Sleep(1100 * time.Millisecond)
			data <- []byte("first-chunk")
		}()

		got := h.HandleStreamPrelude(c, flusher, func(error) {
			t.Fatal("did not expect cancel on first payload")
		}, data, errs, nil, StreamPreludeOptions{
			CommitHeaders: func() {},
			WriteFirstChunk: func(chunk []byte) {
				_, _ = c.Writer.Write(chunk)
			},
			WriteClosedBeforeData: func() {
				t.Fatal("did not expect closed-before-data callback")
			},
			WritePreludeError: func(*interfaces.ErrorMessage) {
				t.Fatal("did not expect prelude error")
			},
			Continue: func(<-chan []byte, <-chan *interfaces.ErrorMessage) {},
		})
		if !got {
			t.Fatal("expected prelude helper to handle keepalive prelude")
		}
		body := recorder.Body.String()
		if !strings.Contains(body, ": keep-alive\n\n") {
			t.Fatalf("expected default keepalive frame in body, got %q", body)
		}
		if !strings.Contains(body, "first-chunk") {
			t.Fatalf("expected first chunk in body, got %q", body)
		}
	})
}
