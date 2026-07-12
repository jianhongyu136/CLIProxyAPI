package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func StreamingPreludeKeepAliveEnabled(cfg *config.SDKConfig) bool {
	return cfg != nil && cfg.Streaming.PreludeKeepAlive && StreamingKeepAliveInterval(cfg) > 0
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
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	opts.CommitHeaders()
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
