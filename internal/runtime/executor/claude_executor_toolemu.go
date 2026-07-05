package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (e *ClaudeExecutor) buildClaudeToolEmuSend(auth *cliproxyauth.Auth, url, apiKey string, extraBetas []string, signCCH bool, entrypoint string, incomingHeaders http.Header, confirmedClaudeCode bool, claudeSessionID string) toolemu.UpstreamSendFunc {
	provider := e.upstreamRequestLogProvider()
	return func(ctx context.Context, body []byte) ([]byte, error) {
		bodyToSend := finalizeClaudeToolEmuFoldedBody(body)
		if signCCH {
			cchBilling := claudeCCHFallbackBillingHeader(ctx, e.cfg, bodyToSend, entrypoint)
			var errCCH error
			bodyToSend, errCCH = finalizeAnthropicMessagesBodyCCH(bodyToSend, cchBilling)
			if errCCH != nil {
				return nil, fmt.Errorf("finalize Claude CCH: %w", errCCH)
			}
		}
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyToSend))
		if errReq != nil {
			return nil, errReq
		}
		if errHeaders := applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, bodyToSend, e.cfg, incomingHeaders, confirmedClaudeCode, claudeSessionID); errHeaders != nil {
			return nil, errHeaders
		}
		fastRequest := isAnthropicUpstreamURL(httpReq.URL) && claudeRequestIsFast(httpReq, bodyToSend)
		authID, authLabel, authType, authValue := claudeAuthLogIdentity(auth)
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL:       url,
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      bodyToSend,
			Provider:  provider,
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})
		client := helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, errDo := doClaudeUpstreamRequest(client, httpReq)
		if errDo != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDo)
			return nil, wrapClaudeFastRequestError(fastRequest, 0, errDo)
		}
		helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		decodedBody, errDecode := decodeResponseBody(httpResp.Body, claudeResponseContentEncoding(httpResp.Header))
		if errDecode != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errDecode)
			return nil, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errDecode)
		}
		defer func() {
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("claude executor: close decoded response body error: %v", errClose)
			}
		}()
		data, errRead := io.ReadAll(decodedBody)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, wrapClaudeFastRequestError(fastRequest, httpResp.StatusCode, errRead)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			if fastRequest {
				return nil, newClaudeFastDirectResponseError(httpResp, data)
			}
			return nil, classifyClaudeUpstreamError(httpResp.StatusCode, data)
		}
		return data, nil
	}
}
