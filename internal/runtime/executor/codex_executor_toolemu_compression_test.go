package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexToolEmulation_CompressionTransport(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "retry"} {
		for _, tc := range []struct {
			name             string
			responseEncoding string
			rejectStatus     int
		}{
			{name: "gzip", responseEncoding: "gzip"},
			{name: "brotli", responseEncoding: "br"},
			{name: "zstd", responseEncoding: "zstd"},
			{name: "fallback-400", responseEncoding: "zstd", rejectStatus: http.StatusBadRequest},
			{name: "fallback-415", responseEncoding: "br", rejectStatus: http.StatusUnsupportedMediaType},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				stream := mode == "stream"
				cfg := codexCompressionConfig()
				cfg.ToolEmulation = toolemu.ToolEmulationConfig{
					Enabled: true, ParseRetry: 1,
					Rules: []toolemu.ToolEmulationRule{{Provider: "codex", Models: []string{"gpt-5.4"}}},
				}
				models := []config.PayloadModelRule{{Name: "gpt-5.4", NotExist: []string{"tools"}}}
				cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{"audit.-1": "once"}}}
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{"input.0", "instructions"}}}
				toolemu.Default.Replace(cfg.ToolEmulation)
				t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

				toolText := rawToolBlockForExecutorTest("get_weather", nil)
				completion := httptest.NewRecorder()
				writeToolEmulationBarrierResponse(completion, "codex", stream, toolText)
				completedWire := codexCompressedResponse(t, tc.responseEncoding, completion.Body.Bytes())
				retry := httptest.NewRecorder()
				writeToolEmulationBarrierResponse(retry, "codex", stream, "retry without a tool call")
				retryWire := codexCompressedResponse(t, tc.responseEncoding, retry.Body.Bytes())

				type recordedRequest struct {
					encoding string
					body     []byte
					headers  http.Header
				}
				requests := make(chan recordedRequest, 4)
				var accepted atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request: %v", errRead)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					encoding := r.Header.Get("Content-Encoding")
					if encoding == "zstd" {
						decoder, errDecoder := zstd.NewReader(nil)
						if errDecoder != nil {
							t.Errorf("create decoder: %v", errDecoder)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						defer decoder.Close()
						body, errRead = decoder.DecodeAll(body, nil)
						if errRead != nil {
							t.Errorf("decode request: %v", errRead)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
					}
					requests <- recordedRequest{encoding: encoding, body: body, headers: r.Header.Clone()}
					if encoding == "zstd" && tc.rejectStatus != 0 {
						w.WriteHeader(tc.rejectStatus)
						return
					}
					wire := completedWire
					if attempt := accepted.Add(1); mode == "retry" && attempt == 1 {
						wire = retryWire
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Content-Encoding", tc.responseEncoding)
					w.Header().Set("Content-Length", strconv.Itoa(len(wire)))
					if _, errWrite := w.Write(wire); errWrite != nil {
						t.Errorf("write response: %v", errWrite)
					}
				}))
				defer server.Close()
				localURL, errURL := url.Parse(server.URL)
				if errURL != nil {
					t.Fatal(errURL)
				}
				// Preserve the real ChatGPT URL for compression eligibility and redirect only the transport.
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					mapped := req.Clone(req.Context())
					mapped.URL.Scheme, mapped.URL.Host = localURL.Scheme, localURL.Host
					mapped.Host = localURL.Host
					return http.DefaultTransport.RoundTrip(mapped)
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", transport)
				payload := []byte(`{"model":"gpt-5.4","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("compressible input ", 400) + `"}]}],"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],"tool_choice":"required"}`)
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: stream}
				executor := NewCodexExecutor(cfg)
				var output []byte
				if stream {
					result, errStream := executor.ExecuteStream(ctx, codexCompressionAuth(), req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
					if result.Headers.Get("Content-Encoding") != "" || result.Headers.Get("Content-Length") != "" {
						t.Errorf("decoded response retained encoding or length: %v", result.Headers)
					}
				} else {
					result, errExecute := executor.Execute(ctx, codexCompressionAuth(), req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					output = result.Payload
				}
				if !bytes.Contains(output, []byte("function_call")) || !bytes.Contains(output, []byte("get_weather")) {
					t.Fatalf("missing emulated tool call: %s", output)
				}
				wantAccepted := 1
				if mode == "retry" {
					wantAccepted = 2
				}
				perAttempt := 1
				if tc.rejectStatus != 0 {
					perAttempt = 2
				}
				if got := int(accepted.Load()); got != wantAccepted {
					t.Fatalf("accepted requests = %d, want %d", got, wantAccepted)
				}
				if got := len(requests); got != wantAccepted*perAttempt {
					t.Fatalf("upstream requests = %d, want %d", got, wantAccepted*perAttempt)
				}
				var previous []byte
				for i := 0; i < wantAccepted*perAttempt; i++ {
					recorded := <-requests
					wantEncoding := "zstd"
					if perAttempt == 2 && i%2 == 1 {
						wantEncoding = ""
						if !bytes.Equal(previous, recorded.body) {
							t.Errorf("compression fallback changed the final business payload")
						}
					}
					if recorded.encoding != wantEncoding {
						t.Errorf("request %d encoding = %q, want %q", i, recorded.encoding, wantEncoding)
					}
					if got := gjson.GetBytes(recorded.body, "audit").Raw; got != `["once"]` {
						t.Errorf("request %d did not apply payload rules exactly once: audit=%s", i, got)
					}
					if gjson.GetBytes(recorded.body, "instructions").Exists() || gjson.GetBytes(recorded.body, "tools").Exists() {
						t.Errorf("request %d restored filtered fields", i)
					}
					if got, want := gjson.GetBytes(recorded.body, "input.#").Int(), int64(2+i/perAttempt); got != want {
						t.Errorf("request %d history length = %d, want %d", i, got, want)
					}
					if recorded.headers.Get("Authorization") != "Bearer test-oauth" || recorded.headers.Get(codexRoutingHintHeader) == "" {
						t.Errorf("request %d lost authentication or routing headers", i)
					}
					previous = recorded.body
				}
			})
		}
	}
}
