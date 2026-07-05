package toolemu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FoldRequest is the canonical request-side entry point. Given an upstream
// payload (OpenAI chat-completions or Responses), it folds tools/tool_choice
// and historical tool messages into prompt text and returns the new payload.
//
// The original payload is not mutated; a new byte slice is returned. tools and
// tool_choice fields are removed from the upstream payload.
func FoldRequest(payload []byte, opts FoldOpts) ([]byte, error) {
	proto := effectiveProtocolSettings(opts.FenceToken, opts.TagGroup)
	switch opts.Shape {
	case ShapeOpenAIChat:
		return foldChat(payload, proto)
	case ShapeOpenAIResponses:
		return foldResponses(payload, proto)
	case ShapeClaudeMessages:
		return foldClaude(payload, proto)
	case ShapeGeminiGenerateContent:
		return foldGemini(payload, proto)
	default:
		return nil, fmt.Errorf("toolemu: unknown shape %d", opts.Shape)
	}
}

// ExtractToolChoice returns the effective native tool_choice before FoldRequest strips native tool fields.
func ExtractToolChoice(payload []byte, shape UpstreamShape) ToolChoice {
	switch shape {
	case ShapeOpenAIChat:
		return applyOpenAIParallelToolCalls(parseChatToolChoice(gjson.GetBytes(payload, "tool_choice")), payload)
	case ShapeOpenAIResponses:
		return applyOpenAIParallelToolCalls(parseResponsesToolChoice(gjson.GetBytes(payload, "tool_choice")), payload)
	case ShapeClaudeMessages:
		return parseClaudeToolChoice(gjson.GetBytes(payload, "tool_choice"))
	case ShapeGeminiGenerateContent:
		choiceNode := gjson.GetBytes(payload, "tool_config.function_calling_config")
		if !choiceNode.Exists() {
			choiceNode = gjson.GetBytes(payload, "toolConfig.functionCallingConfig")
		}
		return parseGeminiToolChoice(choiceNode)
	default:
		return ToolChoiceAuto
	}
}

// ExtractToolSpecs returns normalized tool declarations before FoldRequest
// strips provider-native tool fields.
func ExtractToolSpecs(payload []byte, shape UpstreamShape) ([]ToolSpec, error) {
	switch shape {
	case ShapeOpenAIChat:
		tools, _, err := extractChatTools(payload)
		return tools, err
	case ShapeOpenAIResponses:
		tools, _, err := extractResponsesTools(payload)
		return tools, err
	case ShapeClaudeMessages:
		tools, _, err := extractClaudeTools(payload)
		return tools, err
	case ShapeGeminiGenerateContent:
		tools, _, err := extractGeminiTools(payload)
		return tools, err
	default:
		return nil, fmt.Errorf("toolemu: unknown shape %d", shape)
	}
}

func foldChat(payload []byte, proto protocolSettings) ([]byte, error) {
	tools, choice, err := extractChatTools(payload)
	if err != nil {
		return nil, err
	}

	// Strip native tool fields unconditionally so the upstream never observes
	// the original protocol when toolemu activates — even on later turns where
	// the client only sends tool_calls / role=tool history without redeclaring
	// tools.
	out, _ := sjson.DeleteBytes(payload, "tools")
	out, _ = sjson.DeleteBytes(out, "tool_choice")
	out, _ = sjson.DeleteBytes(out, "parallel_tool_calls")

	messages := gjson.GetBytes(out, "messages")
	if !messages.IsArray() {
		return out, nil
	}

	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		updated, errInject := prependChatUserInjection(out, injection)
		if errInject != nil {
			return nil, errInject
		}
		out = updated
	}

	// Always fold history so historical assistant.tool_calls / role=tool
	// messages are converted into raw protocol text blocks.
	rawMessages := gjson.GetBytes(out, "messages").Raw
	folded, err := FoldChatHistoryWithTagGroup([]byte(rawMessages), proto.token, proto.tags)
	if err != nil {
		return nil, err
	}
	out, _ = sjson.SetRawBytes(out, "messages", folded)
	return out, nil
}

func foldResponses(payload []byte, proto protocolSettings) ([]byte, error) {
	tools, choice, err := extractResponsesTools(payload)
	if err != nil {
		return nil, err
	}

	out, _ := sjson.DeleteBytes(payload, "tools")
	out, _ = sjson.DeleteBytes(out, "tool_choice")
	out, _ = sjson.DeleteBytes(out, "parallel_tool_calls")

	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		updated, errInject := prependResponsesUserInjection(out, injection)
		if errInject != nil {
			return nil, errInject
		}
		out = updated
	}

	if input := gjson.GetBytes(out, "input"); input.IsArray() {
		folded, err := FoldResponsesInputWithTagGroup([]byte(input.Raw), proto.token, proto.tags)
		if err != nil {
			return nil, err
		}
		out, _ = sjson.SetRawBytes(out, "input", folded)
	}
	return out, nil
}

func foldClaude(payload []byte, proto protocolSettings) ([]byte, error) {
	tools, choice, err := extractClaudeTools(payload)
	if err != nil {
		return nil, err
	}
	out, _ := sjson.DeleteBytes(payload, "tools")
	out, _ = sjson.DeleteBytes(out, "tool_choice")

	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		updated, errInject := prependClaudeUserInjection(out, injection)
		if errInject != nil {
			return nil, errInject
		}
		out = updated
	}
	if messages := gjson.GetBytes(out, "messages"); messages.IsArray() {
		folded, errFold := FoldClaudeMessagesWithTagGroup([]byte(messages.Raw), proto.token, proto.tags)
		if errFold != nil {
			return nil, errFold
		}
		out, _ = sjson.SetRawBytes(out, "messages", folded)
	}
	return out, nil
}

func foldGemini(payload []byte, proto protocolSettings) ([]byte, error) {
	tools, choice, err := extractGeminiTools(payload)
	if err != nil {
		return nil, err
	}
	out, _ := sjson.DeleteBytes(payload, "tools")
	out, _ = sjson.DeleteBytes(out, "tool_config")
	out, _ = sjson.DeleteBytes(out, "toolConfig")

	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		part, errPart := marshalSorted(map[string]any{"text": injection})
		if errPart != nil {
			return nil, fmt.Errorf("toolemu: marshal Gemini system injection: %w", errPart)
		}
		out, _ = sjson.SetRawBytes(out, "systemInstruction.parts.-1", part)
		if !gjson.GetBytes(out, "systemInstruction.role").Exists() {
			out, _ = sjson.SetBytes(out, "systemInstruction.role", "system")
		}
	}
	if contents := gjson.GetBytes(out, "contents"); contents.IsArray() {
		folded, errFold := FoldGeminiContentsWithTagGroup([]byte(contents.Raw), proto.token, proto.tags)
		if errFold != nil {
			return nil, errFold
		}
		out, _ = sjson.SetRawBytes(out, "contents", folded)
	}
	return out, nil
}

func extractChatTools(payload []byte) ([]ToolSpec, ToolChoice, error) {
	toolsRes := gjson.GetBytes(payload, "tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t gjson.Result) bool {
		fn := t.Get("function")
		if !fn.Exists() {
			return true
		}
		schema := json.RawMessage(fn.Get("parameters").Raw)
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		specs = append(specs, ToolSpec{
			Name: fn.Get("name").String(), Description: fn.Get("description").String(),
			SchemaJSON: schema,
		})
		return true
	})
	choice := applyOpenAIParallelToolCalls(parseChatToolChoice(gjson.GetBytes(payload, "tool_choice")), payload)
	return specs, choice, nil
}

func extractResponsesTools(payload []byte) ([]ToolSpec, ToolChoice, error) {
	toolsRes := gjson.GetBytes(payload, "tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t gjson.Result) bool {
		// Responses tool schema is flatter than chat-completions:
		// {"type":"function","name":"...","description":"...","parameters":{...}}
		if t.Get("type").String() != "function" {
			return true
		}
		schema := json.RawMessage(t.Get("parameters").Raw)
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		specs = append(specs, ToolSpec{
			Name: t.Get("name").String(), Description: t.Get("description").String(),
			SchemaJSON: schema,
		})
		return true
	})
	choice := applyOpenAIParallelToolCalls(parseResponsesToolChoice(gjson.GetBytes(payload, "tool_choice")), payload)
	return specs, choice, nil
}

func extractClaudeTools(payload []byte) ([]ToolSpec, ToolChoice, error) {
	toolsRes := gjson.GetBytes(payload, "tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t gjson.Result) bool {
		schema := json.RawMessage(t.Get("input_schema").Raw)
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		specs = append(specs, ToolSpec{
			Name: t.Get("name").String(), Description: t.Get("description").String(),
			SchemaJSON: schema,
		})
		return true
	})
	return specs, parseClaudeToolChoice(gjson.GetBytes(payload, "tool_choice")), nil
}

func extractGeminiTools(payload []byte) ([]ToolSpec, ToolChoice, error) {
	toolsRes := gjson.GetBytes(payload, "tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, tool gjson.Result) bool {
		decls := tool.Get("functionDeclarations")
		if !decls.IsArray() {
			return true
		}
		decls.ForEach(func(_, d gjson.Result) bool {
			schema := json.RawMessage(d.Get("parameters").Raw)
			if len(schema) == 0 {
				schema = json.RawMessage(`{}`)
			}
			specs = append(specs, ToolSpec{
				Name: d.Get("name").String(), Description: d.Get("description").String(),
				SchemaJSON: schema,
			})
			return true
		})
		return true
	})
	choiceNode := gjson.GetBytes(payload, "tool_config.function_calling_config")
	if !choiceNode.Exists() {
		choiceNode = gjson.GetBytes(payload, "toolConfig.functionCallingConfig")
	}
	return specs, parseGeminiToolChoice(choiceNode), nil
}
func applyOpenAIParallelToolCalls(choice ToolChoice, payload []byte) ToolChoice {
	parallel := gjson.GetBytes(payload, "parallel_tool_calls")
	if parallel.Exists() && parallel.Type == gjson.False {
		choice.DisableParallel = true
	}
	return choice
}

func parseChatToolChoice(v gjson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	if v.Type == gjson.String {
		switch v.String() {
		case "none":
			return ToolChoiceNone
		case "required":
			return ToolChoiceRequired
		}
		return ToolChoiceAuto
	}
	if v.IsObject() {
		if name := v.Get("function.name").String(); name != "" {
			return ToolChoiceNamed(name)
		}
	}
	return ToolChoiceAuto
}

func parseResponsesToolChoice(v gjson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	if v.Type == gjson.String {
		switch v.String() {
		case "none":
			return ToolChoiceNone
		case "required":
			return ToolChoiceRequired
		}
		return ToolChoiceAuto
	}
	if v.IsObject() {
		if name := v.Get("name").String(); name != "" {
			return ToolChoiceNamed(name)
		}
	}
	return ToolChoiceAuto
}

func parseClaudeToolChoice(v gjson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	disableParallel := v.Get("disable_parallel_tool_use").Bool()
	switch v.Get("type").String() {
	case "none":
		return ToolChoice{Kind: ToolChoiceKindNone, DisableParallel: disableParallel}
	case "any":
		return ToolChoice{Kind: ToolChoiceKindRequired, DisableParallel: disableParallel}
	case "tool":
		if name := v.Get("name").String(); name != "" {
			return ToolChoice{Kind: ToolChoiceKindNamed, Name: name, DisableParallel: disableParallel}
		}
	}
	return ToolChoice{Kind: ToolChoiceKindAuto, DisableParallel: disableParallel}
}

func parseGeminiToolChoice(v gjson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	switch strings.ToUpper(v.Get("mode").String()) {
	case "NONE":
		return ToolChoiceNone
	case "ANY":
		names := v.Get("allowed_function_names")
		if !names.IsArray() {
			names = v.Get("allowedFunctionNames")
		}
		if names.IsArray() && len(names.Array()) == 1 {
			return ToolChoiceNamed(names.Array()[0].String())
		}
		return ToolChoiceRequired
	}
	return ToolChoiceAuto
}

func prependChatUserInjection(payload []byte, injection string) ([]byte, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, nil
	}

	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(messages.Raw), &raw); err != nil {
		return nil, fmt.Errorf("toolemu: parse chat messages: %w", err)
	}

	userIdx := -1
	for i, msg := range raw {
		if gjson.GetBytes(msg, "role").String() == "user" {
			userIdx = i
			break
		}
	}

	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		insertIdx := 0
		for insertIdx < len(raw) && gjson.GetBytes(raw[insertIdx], "role").String() == "system" {
			insertIdx++
		}
		userMsg, err := marshalSorted(map[string]any{"role": "user", "content": injection})
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal injected chat user message: %w", err)
		}
		for i, msg := range raw {
			if i == insertIdx {
				items = append(items, json.RawMessage(userMsg))
			}
			items = append(items, json.RawMessage(msg))
		}
		if insertIdx == len(raw) {
			items = append(items, json.RawMessage(userMsg))
		}
		merged, err := marshalSorted(items)
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal chat messages with injected user: %w", err)
		}
		out, _ := sjson.SetRawBytes(payload, "messages", merged)
		return out, nil
	}

	rebuiltUser, err := prependChatMessageContent(raw[userIdx], injection)
	if err != nil {
		return nil, err
	}
	for i, msg := range raw {
		if i == userIdx {
			items = append(items, json.RawMessage(rebuiltUser))
			continue
		}
		items = append(items, json.RawMessage(msg))
	}
	merged, err := marshalSorted(items)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal chat messages after user injection: %w", err)
	}
	out, _ := sjson.SetRawBytes(payload, "messages", merged)
	return out, nil
}

func prependChatMessageContent(orig json.RawMessage, injection string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse chat message: %w", err)
	}

	content := gjson.GetBytes(orig, "content")
	switch {
	case content.IsArray():
		parts := []any{map[string]any{"type": "text", "text": injection}}
		content.ForEach(func(_, existing gjson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
		obj["content"] = parts
	case content.Exists() && content.Type == gjson.String:
		combined := injection
		if existing := content.String(); existing != "" {
			combined += "\n" + existing
		}
		obj["content"] = combined
	case content.Exists() && content.Raw != "null":
		obj["content"] = []any{map[string]any{"type": "text", "text": injection}, json.RawMessage(content.Raw)}
	default:
		obj["content"] = injection
	}
	out, err := marshalSorted(obj)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal chat message content: %w", err)
	}
	return out, nil
}

func prependResponsesUserInjection(payload []byte, injection string) ([]byte, error) {
	input := gjson.GetBytes(payload, "input")
	if input.Exists() && input.Type == gjson.String {
		userMsg, err := marshalSorted(map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": injection},
				map[string]any{"type": "input_text", "text": input.String()},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal injected responses string input: %w", err)
		}
		items, err := marshalSorted([]any{json.RawMessage(userMsg)})
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal responses string input array: %w", err)
		}
		out, _ := sjson.SetRawBytes(payload, "input", items)
		return out, nil
	}
	if !input.IsArray() {
		return payload, nil
	}

	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(input.Raw), &raw); err != nil {
		return nil, fmt.Errorf("toolemu: parse responses input: %w", err)
	}

	userIdx := -1
	for i, item := range raw {
		if gjson.GetBytes(item, "type").String() == "message" && gjson.GetBytes(item, "role").String() == "user" {
			userIdx = i
			break
		}
	}

	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		userMsg, err := marshalSorted(map[string]any{
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": injection}},
		})
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal injected responses user message: %w", err)
		}
		items = append(items, json.RawMessage(userMsg))
		for _, item := range raw {
			items = append(items, json.RawMessage(item))
		}
		merged, err := marshalSorted(items)
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal responses input with injected user: %w", err)
		}
		out, _ := sjson.SetRawBytes(payload, "input", merged)
		return out, nil
	}

	rebuiltUser, err := prependResponsesInputContent(raw[userIdx], injection)
	if err != nil {
		return nil, err
	}
	for i, item := range raw {
		if i == userIdx {
			items = append(items, json.RawMessage(rebuiltUser))
			continue
		}
		items = append(items, json.RawMessage(item))
	}
	merged, err := marshalSorted(items)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal responses input after user injection: %w", err)
	}
	out, _ := sjson.SetRawBytes(payload, "input", merged)
	return out, nil
}

func prependResponsesInputContent(orig json.RawMessage, injection string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse responses user message: %w", err)
	}

	content := gjson.GetBytes(orig, "content")
	injectionPart := map[string]any{"type": "input_text", "text": injection}
	switch {
	case content.IsArray():
		parts := []any{injectionPart}
		content.ForEach(func(_, existing gjson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
		obj["content"] = parts
	case content.Exists() && content.Type == gjson.String:
		parts := []any{injectionPart}
		if existing := content.String(); existing != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": existing})
		}
		obj["content"] = parts
	case content.Exists() && content.Raw != "null":
		obj["content"] = []any{injectionPart, json.RawMessage(content.Raw)}
	default:
		obj["content"] = []any{injectionPart}
	}
	out, err := marshalSorted(obj)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal responses user content: %w", err)
	}
	return out, nil
}

func prependClaudeUserInjection(payload []byte, injection string) ([]byte, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, nil
	}

	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(messages.Raw), &raw); err != nil {
		return nil, fmt.Errorf("toolemu: parse Claude messages: %w", err)
	}

	userIdx := -1
	for i, msg := range raw {
		if gjson.GetBytes(msg, "role").String() == "user" {
			userIdx = i
			break
		}
	}

	// Attach an Anthropic cache_control breakpoint to the injected tool-protocol
	// block. After folding, native `tools` are stripped (dropping any tools-level
	// cache_control) and this injection becomes the largest byte-stable prefix in
	// the request. Without a breakpoint here the upstream never creates a prefix
	// cache for single-turn requests, since the executor's message-level
	// cache_control only targets the second-to-last user turn. Pinning the
	// breakpoint to the injection block lets the static protocol prefix be cached
	// independent of conversation turn count.
	//
	// Skip the breakpoint when the target user message already carries a
	// cache_control on one of its existing content parts: the injection is
	// prepended as the first part of that same message, so a later breakpoint in
	// the message already caches the injected prefix. Adding our own would waste
	// one of Anthropic's four cache_control breakpoints.
	injectPart := map[string]any{
		"type": "text",
		"text": injection,
	}
	if userIdx < 0 || !claudeMessageHasCacheControl(raw[userIdx]) {
		injectPart["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		msg, err := marshalSorted(map[string]any{"role": "user", "content": []any{injectPart}})
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal Claude injected user message: %w", err)
		}
		items = append(items, json.RawMessage(msg))
		for _, msg := range raw {
			items = append(items, json.RawMessage(msg))
		}
		merged, err := marshalSorted(items)
		if err != nil {
			return nil, fmt.Errorf("toolemu: marshal Claude messages with injected user: %w", err)
		}
		out, _ := sjson.SetRawBytes(payload, "messages", merged)
		return out, nil
	}

	rebuiltUser, err := prependClaudeTextPart(raw[userIdx], injectPart)
	if err != nil {
		return nil, err
	}
	for i, msg := range raw {
		if i == userIdx {
			items = append(items, json.RawMessage(rebuiltUser))
			continue
		}
		items = append(items, json.RawMessage(msg))
	}
	merged, err := marshalSorted(items)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal Claude messages after user injection: %w", err)
	}
	out, _ := sjson.SetRawBytes(payload, "messages", merged)
	return out, nil
}

func prependClaudeTextPart(orig json.RawMessage, part map[string]any) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse Claude message: %w", err)
	}

	content := gjson.GetBytes(orig, "content")
	parts := []any{part}
	switch {
	case content.IsArray():
		content.ForEach(func(_, existing gjson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
	case content.Exists() && content.Type == gjson.String:
		parts = append(parts, map[string]any{"type": "text", "text": content.String()})
	case content.Exists() && content.Raw != "null":
		parts = append(parts, json.RawMessage(content.Raw))
	}
	obj["content"] = parts
	out, err := marshalSorted(obj)
	if err != nil {
		return nil, fmt.Errorf("toolemu: marshal Claude message content: %w", err)
	}
	return out, nil
}

// claudeMessageHasCacheControl reports whether a Claude message already carries
// a cache_control breakpoint on any of its array content parts. String content
// cannot hold a breakpoint, so it always reports false in that case.
func claudeMessageHasCacheControl(msg json.RawMessage) bool {
	content := gjson.GetBytes(msg, "content")
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("cache_control").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func FoldClaudeMessages(messages []byte) ([]byte, error) {
	return FoldClaudeMessagesWithFence(messages, DefaultFenceToken)
}

func FoldClaudeMessagesWithFence(messages []byte, fenceToken string) ([]byte, error) {
	return FoldClaudeMessagesWithTagGroup(messages, fenceToken, ToolEmulationTagGroup{})
}

func FoldClaudeMessagesWithTagGroup(messages []byte, fenceToken string, tagGroup ToolEmulationTagGroup) ([]byte, error) {
	proto := effectiveProtocolSettings(fenceToken, tagGroup)
	if !gjson.ValidBytes(messages) || !gjson.ParseBytes(messages).IsArray() {
		return nil, fmt.Errorf("toolemu: Claude messages payload is not a JSON array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(messages, &raw); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(raw))
	callIndexByID := map[string]int{}
	nextCallIndex := 0
	orphanResultIndex := 0
	for _, msg := range raw {
		role := gjson.GetBytes(msg, "role").String()
		content := gjson.GetBytes(msg, "content")
		switch role {
		case "assistant":
			parts, changed, errFold := foldClaudeAssistantParts(content, callIndexByID, &nextCallIndex, proto)
			if errFold != nil {
				return nil, errFold
			}
			if !changed {
				out = append(out, msg)
				continue
			}
			rebuilt, err := rebuildMessageContent(msg, parts)
			if err != nil {
				return nil, err
			}
			out = append(out, json.RawMessage(rebuilt))
		case "user":
			parts, changed := foldClaudeUserParts(content, callIndexByID, &orphanResultIndex, proto)
			if !changed {
				out = append(out, msg)
				continue
			}
			rebuilt, err := rebuildMessageContent(msg, parts)
			if err != nil {
				return nil, err
			}
			out = append(out, json.RawMessage(rebuilt))
		default:
			out = append(out, msg)
		}
	}
	return marshalSorted(out)
}

func foldClaudeAssistantParts(content gjson.Result, callIndexByID map[string]int, nextCallIndex *int, proto protocolSettings) ([]any, bool, error) {
	if content.Type == gjson.String || !content.IsArray() {
		return nil, false, nil
	}
	parts := make([]any, 0, len(content.Array()))
	changed := false
	var foldErr error
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "tool_use" {
			parts = append(parts, json.RawMessage(part.Raw))
			return true
		}
		idx := *nextCallIndex
		*nextCallIndex = *nextCallIndex + 1
		if id := part.Get("id").String(); id != "" {
			callIndexByID[id] = idx
		}
		block, errBlock := renderToolBlockWithSettings(
			part.Get("name").String(),
			argsJSONToStringMap([]byte(canonicalJSON(json.RawMessage(part.Get("input").Raw)))),
			proto,
		)
		if errBlock != nil {
			foldErr = errBlock
			return false
		}
		wrapped, errWrap := renderToolCallsBlockWithSettings([]string{block}, proto)
		if errWrap != nil {
			foldErr = errWrap
			return false
		}
		parts = append(parts, map[string]any{"type": "text", "text": wrapped})
		changed = true
		return true
	})
	return parts, changed, foldErr
}

func foldClaudeUserParts(content gjson.Result, callIndexByID map[string]int, orphanResultIndex *int, proto protocolSettings) ([]any, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := make([]any, 0, len(content.Array()))
	changed := false
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "tool_result" {
			parts = append(parts, json.RawMessage(part.Raw))
			return true
		}
		idx, ok := callIndexByID[part.Get("tool_use_id").String()]
		if !ok {
			idx = *orphanResultIndex
			*orphanResultIndex = *orphanResultIndex + 1
		}
		text := renderResultBlockWithSettings(idx, part.Get("content").String(), proto)
		parts = append(parts, map[string]any{"type": "text", "text": text})
		changed = true
		return true
	})
	return parts, changed
}

func rebuildMessageContent(orig json.RawMessage, content []any) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, err
	}
	obj["content"] = content
	return marshalSorted(obj)
}

func FoldGeminiContents(contents []byte) ([]byte, error) {
	return FoldGeminiContentsWithFence(contents, DefaultFenceToken)
}

func FoldGeminiContentsWithFence(contents []byte, fenceToken string) ([]byte, error) {
	return FoldGeminiContentsWithTagGroup(contents, fenceToken, ToolEmulationTagGroup{})
}

func FoldGeminiContentsWithTagGroup(contents []byte, fenceToken string, tagGroup ToolEmulationTagGroup) ([]byte, error) {
	proto := effectiveProtocolSettings(fenceToken, tagGroup)
	if !gjson.ValidBytes(contents) || !gjson.ParseBytes(contents).IsArray() {
		return nil, fmt.Errorf("toolemu: Gemini contents payload is not a JSON array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(contents, &raw); err != nil {
		return nil, err
	}
	out := make([]any, 0, len(raw))
	callIndexesByName := map[string][]int{}
	nextCallIndex := 0
	for _, item := range raw {
		parts := gjson.GetBytes(item, "parts")
		partsOut, changed, errFold := foldGeminiParts(parts, callIndexesByName, &nextCallIndex, proto)
		if errFold != nil {
			return nil, errFold
		}
		if !changed {
			out = append(out, item)
			continue
		}
		rebuilt, err := rebuildGeminiParts(item, partsOut)
		if err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(rebuilt))
	}
	return marshalSorted(out)
}

func foldGeminiParts(parts gjson.Result, callIndexesByName map[string][]int, nextCallIndex *int, proto protocolSettings) ([]any, bool, error) {
	if !parts.IsArray() {
		return nil, false, nil
	}
	out := make([]any, 0, len(parts.Array()))
	changed := false
	var foldErr error
	parts.ForEach(func(_, part gjson.Result) bool {
		if fc := part.Get("functionCall"); fc.Exists() {
			name := fc.Get("name").String()
			index := *nextCallIndex
			*nextCallIndex = *nextCallIndex + 1
			if name != "" {
				callIndexesByName[name] = append(callIndexesByName[name], index)
			}
			block, errBlock := renderToolBlockWithSettings(
				name,
				argsJSONToStringMap([]byte(canonicalJSON(json.RawMessage(fc.Get("args").Raw)))),
				proto,
			)
			if errBlock != nil {
				foldErr = errBlock
				return false
			}
			wrapped, errWrap := renderToolCallsBlockWithSettings([]string{block}, proto)
			if errWrap != nil {
				foldErr = errWrap
				return false
			}
			out = append(out, map[string]any{"text": wrapped})
			changed = true
			return true
		}
		if fr := part.Get("functionResponse"); fr.Exists() {
			result := fr.Get("response").Raw
			if result == "" {
				result = "{}"
			}
			index := *nextCallIndex
			name := fr.Get("name").String()
			if queued := callIndexesByName[name]; len(queued) > 0 {
				index = queued[0]
				if len(queued) == 1 {
					delete(callIndexesByName, name)
				} else {
					callIndexesByName[name] = queued[1:]
				}
			} else {
				*nextCallIndex = *nextCallIndex + 1
			}
			text := renderResultBlockWithSettings(index, result, proto)
			out = append(out, map[string]any{"text": text})
			changed = true
			return true
		}
		out = append(out, json.RawMessage(part.Raw))
		return true
	})
	return out, changed, foldErr
}

func rebuildGeminiParts(orig json.RawMessage, parts []any) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, err
	}
	obj["parts"] = parts
	return marshalSorted(obj)
}

// HasTools reports whether the upstream payload still carries a non-empty tools array.
func HasTools(payload []byte) bool {
	t := gjson.GetBytes(payload, "tools")
	return t.IsArray() && len(t.Array()) > 0
}

// HasToolArtifacts reports whether the upstream payload carries any native
// tool-calling structures: a non-empty `tools` array, an object-form
// `tool_choice`, historical `assistant.tool_calls` or `role=tool` messages
// (chat-completions), or `function_call` / `function_call_output` items
// (Responses). Used by executors to decide whether toolemu folding is
// necessary — even when the current turn omits the tools declaration but
// the conversation history still references native tool calls.
func HasToolArtifacts(payload []byte) bool {
	if HasTools(payload) {
		return true
	}
	if tc := gjson.GetBytes(payload, "tool_choice"); tc.IsObject() {
		return true
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		found := false
		messages.ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() == "tool" {
				found = true
				return false
			}
			if calls := msg.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
				found = true
				return false
			}
			if content := msg.Get("content"); content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					switch part.Get("type").String() {
					case "tool_use", "tool_result":
						found = true
						return false
					}
					return true
				})
			}
			return !found
		})
		if found {
			return true
		}
	}
	if input := gjson.GetBytes(payload, "input"); input.IsArray() {
		found := false
		input.ForEach(func(_, item gjson.Result) bool {
			switch item.Get("type").String() {
			case "function_call", "function_call_output":
				found = true
				return false
			}
			return true
		})
		if found {
			return true
		}
	}
	if contents := gjson.GetBytes(payload, "contents"); contents.IsArray() {
		found := false
		contents.ForEach(func(_, item gjson.Result) bool {
			if parts := item.Get("parts"); parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					if part.Get("functionCall").Exists() || part.Get("functionResponse").Exists() {
						found = true
						return false
					}
					return true
				})
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}
