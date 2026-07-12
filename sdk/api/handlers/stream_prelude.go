package handlers

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/context"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func StreamingPreludeKeepAliveEnabled(cfg *config.SDKConfig) bool {
	return cfg != nil && !cfg.PassthroughHeaders && cfg.Streaming.PreludeKeepAlive && StreamingKeepAliveInterval(cfg) > 0
}

const preludeKeepAlivePaddingBytes = 4096

func writePreludeKeepAlive(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	payload := make([]byte, 0, len(": keep-alive\n")+preludeKeepAlivePaddingBytes+1)
	payload = append(payload, ": keep-alive\n"...)
	for range preludeKeepAlivePaddingBytes {
		payload = append(payload, ' ')
	}
	payload = append(payload, '\n')
	_, _ = c.Writer.Write(payload)
}

// StartStreamingPreludeKeepAlive commits SSE headers and emits heartbeats while the caller
// is still waiting for the upstream stream to be established. The returned stop function
// waits until the heartbeat goroutine has exited so the caller can write the response.
func (h *BaseAPIHandler) StartStreamingPreludeKeepAlive(c *gin.Context, flusher http.Flusher, ctx context.Context, commitHeaders func()) func() {
	if h == nil || c == nil || flusher == nil || commitHeaders == nil || !StreamingPreludeKeepAliveEnabled(h.Cfg) {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	commitHeaders()
	c.Header("X-Accel-Buffering", "no")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Status(http.StatusOK)
	writePreludeKeepAlive(c)
	flusher.Flush()

	stopChan := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(StreamingKeepAliveInterval(h.Cfg))
		defer ticker.Stop()
		for {
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				writePreludeKeepAlive(c)
				flusher.Flush()
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
		wg.Wait()
	}
}

type StreamPreludeOptions struct {
	// CommitHeaders commits streaming response headers before waiting for the first payload.
	CommitHeaders func()

	// WriteFirstChunk writes the first payload chunk to the response body. It should not flush.
	WriteFirstChunk func(chunk []byte)

	// WriteClosedBeforeData writes the terminal response when the upstream closes before the first payload.
	// It should not flush.
	WriteClosedBeforeData func()

	// WritePreludeError writes an error payload to the response body when the stream fails before the first payload.
	// It should not flush.
	WritePreludeError func(errMsg *interfaces.ErrorMessage)

	// WriteKeepAlive optionally writes a keep-alive heartbeat before the first payload. It should not flush.
	// When nil, a standard SSE comment heartbeat is used.
	WriteKeepAlive func()

	// Continue continues normal streaming after the first payload chunk has been written and flushed.
	Continue func(data <-chan []byte, errs <-chan *interfaces.ErrorMessage)
}

func (h *BaseAPIHandler) HandleStreamPrelude(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, _ http.Header, opts StreamPreludeOptions) bool {
	if h == nil || c == nil || flusher == nil || cancel == nil || !StreamingPreludeKeepAliveEnabled(h.Cfg) {
		return false
	}
	if opts.CommitHeaders == nil || opts.WriteFirstChunk == nil || opts.WriteClosedBeforeData == nil || opts.WritePreludeError == nil || opts.Continue == nil {
		return false
	}

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			writePreludeKeepAlive(c)
		}
	}

	opts.CommitHeaders()
	if !c.Writer.Written() {
		c.Header("X-Accel-Buffering", "no")
		c.Header("Cache-Control", "no-cache, no-transform")
		c.Status(http.StatusOK)
		writePreludeKeepAlive(c)
	}
	flusher.Flush()

	keepAliveInterval := StreamingKeepAliveInterval(h.Cfg)
	var keepAlive *time.Ticker
	var keepAliveC <-chan time.Time
	if keepAliveInterval > 0 {
		keepAlive = time.NewTicker(keepAliveInterval)
		defer keepAlive.Stop()
		keepAliveC = keepAlive.C
	}

	for {
		select {
		case <-c.Request.Context().Done():
			cancel(c.Request.Context().Err())
			return true
		case errMsg, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			opts.WritePreludeError(errMsg)
			flusher.Flush()
			if errMsg != nil {
				cancel(errMsg.Error)
			} else {
				cancel(nil)
			}
			return true
		case chunk, ok := <-data:
			if !ok {
				if errMsg, okPendingErr := pendingStreamPreludeError(errs); okPendingErr {
					opts.WritePreludeError(errMsg)
					flusher.Flush()
					cancel(errMsg.Error)
					return true
				}
				opts.WriteClosedBeforeData()
				flusher.Flush()
				cancel(nil)
				return true
			}
			opts.WriteFirstChunk(chunk)
			flusher.Flush()
			opts.Continue(data, errs)
			return true
		case <-keepAliveC:
			writeKeepAlive()
			flusher.Flush()
		}
	}
}

func pendingStreamPreludeError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if errs == nil {
		return nil, false
	}
	select {
	case errMsg, ok := <-errs:
		if !ok || errMsg == nil {
			return nil, false
		}
		return errMsg, true
	default:
		return nil, false
	}
}
