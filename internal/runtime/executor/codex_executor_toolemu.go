package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *CodexExecutor) buildCodexToolEmuSend(
	from sdktranslator.Format,
	auth *cliproxyauth.Auth,
	req cliproxyexecutor.Request,
	userPayload []byte,
	url, apiKey, baseModel string,
	incomingHeaders http.Header,
	replayScope codexReasoningReplayScope,
	optimizeMultiAgentV2 bool,
) toolemu.UpstreamSendFunc {
	provider := e.Identifier()
	return func(ctx context.Context, body []byte) ([]byte, error) {
		httpReq, upstreamBody, identityState, errReq := e.cacheHelper(ctx, from, url, auth, req, userPayload, body, incomingHeaders)
		if errReq != nil {
			return nil, errReq
		}
		applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
		applyModelHeaderOverrides(httpReq.Header, baseModel)
		applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)
		var authID, authLabel, authType, authValue string
		if auth != nil {
			authID = auth.ID
			authLabel = auth.Label
			authType, authValue = auth.AccountInfo()
		}
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      upstreamBody,
			Provider:  provider,
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})
		client := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, errDo := client.Do(httpReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			return nil, errDo
		}
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		data, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		data = applyCodexIdentityConfuseResponsePayload(data, identityState)
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, httpResp.StatusCode, data); errClearReplay != nil {
				return nil, errClearReplay
			}
			return nil, newCodexStatusErr(httpResp.StatusCode, data)
		}

		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for _, line := range bytes.Split(data, []byte("\n")) {
			if !bytes.HasPrefix(line, dataTag) {
				continue
			}
			eventData := bytes.TrimSpace(line[5:])
			eventData = helps.RestoreCodexMultiAgentV2Response(eventData, optimizeMultiAgentV2)
			if streamErr, terminalBody, ok := codexTerminalFailureErr(eventData); ok {
				if errClearReplay := clearCodexReasoningReplayOnInvalidSignature(ctx, replayScope, streamErr.StatusCode(), terminalBody); errClearReplay != nil {
					return nil, errClearReplay
				}
				return nil, streamErr
			}
			switch gjson.GetBytes(eventData, "type").String() {
			case "response.output_item.done":
				collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.completed":
				completed := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				completed = applyCodexIdentityExposeResponsePayload(completed, identityState)
				response := gjson.GetBytes(completed, "response")
				if !response.Exists() {
					return completed, nil
				}
				return []byte(response.Raw), nil
			}
		}
		return nil, newCodexIncompleteStreamError()
	}
}

func restoreCodexToolEmuStreamFrame(frame []byte, optimizeMultiAgentV2 bool) []byte {
	if !optimizeMultiAgentV2 || len(frame) == 0 {
		return frame
	}
	parts := bytes.SplitAfter(frame, []byte("\n"))
	out := make([]byte, 0, len(frame))
	changed := false
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		line := part
		suffix := []byte(nil)
		if bytes.HasSuffix(line, []byte("\n")) {
			suffix = []byte("\n")
			line = line[:len(line)-1]
			if bytes.HasSuffix(line, []byte("\r")) {
				suffix = []byte("\r\n")
				line = line[:len(line)-1]
			}
		}
		if bytes.HasPrefix(line, dataTag) {
			data := bytes.TrimSpace(line[5:])
			restored := helps.RestoreCodexMultiAgentV2Response(data, true)
			if !bytes.Equal(restored, data) {
				line = append([]byte("data: "), restored...)
				changed = true
			}
		}
		out = append(out, line...)
		out = append(out, suffix...)
	}
	if !changed {
		return frame
	}
	return out
}

func codexToolEmuStreamFrameData(frame []byte) []byte {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		if bytes.HasPrefix(line, dataTag) {
			return bytes.TrimSpace(line[5:])
		}
	}
	return nil
}
