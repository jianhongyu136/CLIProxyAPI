package helps

import (
	"context"
	"fmt"
	"io"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/toolemu"
)

// ToolEmuActive reports whether toolemu should activate for the given request.
// Wraps the toolemu.Default matcher + HasToolArtifacts probe so executors don't
// need to import the toolemu package solely for the activation check. Emits an
// info log on each activation so operators can confirm toolemu took over the
// request — including subsequent turns where the client only sends tool_calls /
// role=tool history without redeclaring tools.
func ToolEmuActive(ctx context.Context, provider, model, alias string, payload []byte) bool {
	return ToolEmuActiveDoc(ctx, provider, model, alias, ojson.NewDoc(payload))
}

// ToolEmuActiveDoc checks the prepared document without serializing it.
func ToolEmuActiveDoc(ctx context.Context, provider, model, alias string, doc *ojson.Doc) bool {
	if !toolemu.Default.IsEnabled(provider, model, alias) || !toolemu.HasToolArtifactsDoc(doc) {
		return false
	}
	LogWithRequestID(ctx).Infof("toolemu: activated for provider=%s model=%s alias=%s", provider, model, alias)
	return true
}

// ToolEmuOutcome bundles the fold→parse loop result with derived response artefacts.
// BuiltBody is populated for non-streaming callers. Folded is the upstream payload
// actually sent (post-fold).
type ToolEmuOutcome struct {
	Folded           []byte
	BuiltBody        []byte
	FakeStreamChunks [][]byte
	Result           toolemu.ParseResult
}

// RunToolEmu performs the full fold → send → parse → retry → build cycle for a
// single upstream request. The caller supplies a send closure that executes
// the actual HTTP POST against the upstream and returns the response body.
//
// For each shape the helper also constructs the corresponding upstream-format
// non-stream body (BuiltBody).
func RunToolEmu(ctx context.Context, payload []byte, shape toolemu.UpstreamShape, provider string, policy toolemu.RetryPolicy, send toolemu.UpstreamSendFunc) (ToolEmuOutcome, error) {
	return RunToolEmuDoc(ctx, ojson.NewDoc(payload), shape, provider, policy, send)
}

// RunToolEmuDoc folds the prepared document before freezing the retry template.
func RunToolEmuDoc(
	ctx context.Context,
	doc *ojson.Doc,
	shape toolemu.UpstreamShape,
	provider string,
	policy toolemu.RetryPolicy,
	send toolemu.UpstreamSendFunc,
) (ToolEmuOutcome, error) {
	choice := toolemu.ExtractToolChoiceDoc(doc, shape)
	tools, err := toolemu.ExtractToolSpecsDoc(doc, shape)
	if err != nil {
		return ToolEmuOutcome{}, err
	}
	if len(policy.Tools) == 0 {
		policy.Tools = tools
	}
	if errFold := toolemu.FoldRequestDoc(doc, toolemu.FoldOpts{Shape: shape, Provider: provider, FenceToken: policy.FenceToken, TagGroup: policy.TagGroup}); errFold != nil {
		return ToolEmuOutcome{}, errFold
	}
	// Retries rebuild from this unconfigured snapshot, never from a finalized send.
	folded := doc.Bytes()
	res, err := toolemu.ParseAndRetry(ctx, folded, send, shape, policy, choice)
	if err != nil {
		return ToolEmuOutcome{Folded: folded}, err
	}
	res.Meta.Provider = provider

	out := ToolEmuOutcome{Folded: folded, Result: res}
	switch shape {
	case toolemu.ShapeOpenAIChat:
		out.BuiltBody, _ = toolemu.BuildChatCompletion(res.Parsed, res.Meta)
	case toolemu.ShapeOpenAIResponses:
		out.BuiltBody, _ = toolemu.BuildResponses(res.Parsed, res.Meta)
	case toolemu.ShapeClaudeMessages:
		out.BuiltBody, _ = toolemu.BuildClaudeMessage(res.Parsed, res.Meta)
	case toolemu.ShapeGeminiGenerateContent:
		out.BuiltBody, _ = toolemu.BuildGeminiGenerateContent(res.Parsed, res.Meta)
	}
	return out, nil
}

func RunToolEmuStream(ctx context.Context, meta toolemu.UpstreamMeta, shape toolemu.UpstreamShape, upstreamResp io.Reader, choice toolemu.ToolChoice, policy toolemu.RetryPolicy, onFrame func([]byte), onCompleted ...func()) (toolemu.UpstreamMeta, error) {
	if upstreamResp == nil {
		return toolemu.UpstreamMeta{}, fmt.Errorf("toolemu stream: nil upstream response")
	}
	if meta.Provider == "" {
		meta.Provider = "unknown"
	}
	var events toolemu.StreamEvents
	switch shape {
	case toolemu.ShapeOpenAIChat:
		emitter := toolemu.NewChatStreamEmitter(meta, onFrame)
		emitter.SetToolChoice(choice)
		events = emitter.Events()
	case toolemu.ShapeOpenAIResponses:
		emitter := toolemu.NewResponsesStreamEmitter(meta, onFrame)
		emitter.SetToolChoice(choice)
		events = emitter.Events()
	case toolemu.ShapeClaudeMessages:
		emitter := toolemu.NewClaudeStreamEmitter(meta, onFrame)
		emitter.SetToolChoice(choice)
		events = emitter.Events()
	default:
		return toolemu.UpstreamMeta{}, fmt.Errorf("toolemu stream: unsupported shape %d", shape)
	}
	parser := toolemu.NewStreamParserWithTagGroup(events, meta, policy.FenceToken, policy.TagGroup)
	parser.SetToolSpecs(policy.Tools)
	pump := toolemu.UpstreamPump{Reader: upstreamResp, Shape: shape, Parser: parser}
	if len(onCompleted) > 0 {
		pump.OnCompleted = onCompleted[0]
	}
	upMeta, err := pump.Run(ctx)
	if upMeta.Provider == "" {
		upMeta.Provider = meta.Provider
	}
	if upMeta.Model == "" {
		upMeta.Model = meta.Model
	}
	if upMeta.ResponseID == "" {
		upMeta.ResponseID = meta.ResponseID
	}
	if upMeta.Created == 0 {
		upMeta.Created = meta.Created
	}
	return upMeta, err
}

// ToolEmuRetryPolicy resolves a runtime retry policy from a config snapshot.
// When ParseRetry is zero and OnParseFailure is unset, DefaultParseRetry is
// applied so opt-in users get the documented "one extra attempt" behavior.
func ToolEmuRetryPolicy(cfg toolemu.ToolEmulationConfig) toolemu.RetryPolicy {
	c := cfg.DefaultsApplied()
	attempts := c.ParseRetry
	if attempts == 0 && cfg.ParseRetry == 0 && cfg.OnParseFailure == "" {
		attempts = toolemu.DefaultParseRetry
	}
	if attempts < 0 {
		attempts = 0
	}
	return toolemu.RetryPolicy{Attempts: attempts, OnFailure: c.OnParseFailure, FenceToken: c.FenceToken, TagGroup: c.TagGroup}
}
