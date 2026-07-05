package toolemu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// FoldRequest is the canonical request-side entry point. Given an upstream
// payload (OpenAI chat-completions or Responses), it folds tools/tool_choice
// and historical tool messages into prompt text and returns the new payload.
//
// The original payload is not mutated; a new byte slice is returned. tools and
// tool_choice fields are removed from the upstream payload.
func FoldRequest(payload []byte, opts FoldOpts) ([]byte, error) {
	doc := ojson.NewDoc(payload)
	if err := FoldRequestDoc(doc, opts); err != nil {
		return nil, err
	}
	return doc.Bytes(), nil
}

// FoldRequestDoc folds tools and history in place across the request pipeline.
func FoldRequestDoc(doc *ojson.Doc, opts FoldOpts) error {
	if doc == nil {
		return fmt.Errorf("toolemu: nil request document")
	}
	proto := effectiveProtocolSettings(opts.FenceToken, opts.TagGroup)
	switch opts.Shape {
	case ShapeOpenAIChat:
		return foldChatDoc(doc, proto)
	case ShapeOpenAIResponses:
		return foldResponsesDoc(doc, proto)
	case ShapeClaudeMessages:
		return foldClaudeDoc(doc, proto)
	case ShapeGeminiGenerateContent:
		return foldGeminiDoc(doc, proto)
	default:
		return fmt.Errorf("toolemu: unknown shape %d", opts.Shape)
	}
}

// ExtractToolChoice returns the effective native tool_choice before folding.
func ExtractToolChoice(payload []byte, shape UpstreamShape) ToolChoice {
	return ExtractToolChoiceDoc(ojson.NewDoc(payload), shape)
}

// ExtractToolChoiceDoc reads native tool_choice from the prepared document.
func ExtractToolChoiceDoc(doc *ojson.Doc, shape UpstreamShape) ToolChoice {
	if doc == nil {
		return ToolChoiceAuto
	}
	switch shape {
	case ShapeOpenAIChat:
		return applyOpenAIParallelToolCalls(parseChatToolChoice(doc.Get("tool_choice")), doc.Get("parallel_tool_calls"))
	case ShapeOpenAIResponses:
		return applyOpenAIParallelToolCalls(parseResponsesToolChoice(doc.Get("tool_choice")), doc.Get("parallel_tool_calls"))
	case ShapeClaudeMessages:
		return parseClaudeToolChoice(doc.Get("tool_choice"))
	case ShapeGeminiGenerateContent:
		choiceNode := doc.Get("tool_config.function_calling_config")
		if !choiceNode.Exists() {
			choiceNode = doc.Get("toolConfig.functionCallingConfig")
		}
		return parseGeminiToolChoice(choiceNode)
	default:
		return ToolChoiceAuto
	}
}

// ExtractToolSpecs returns normalized tool declarations before folding.
func ExtractToolSpecs(payload []byte, shape UpstreamShape) ([]ToolSpec, error) {
	return ExtractToolSpecsDoc(ojson.NewDoc(payload), shape)
}

// ExtractToolSpecsDoc reads tool declarations without serializing the request.
func ExtractToolSpecsDoc(doc *ojson.Doc, shape UpstreamShape) ([]ToolSpec, error) {
	if doc == nil {
		return nil, fmt.Errorf("toolemu: nil request document")
	}
	switch shape {
	case ShapeOpenAIChat:
		tools, _, err := extractChatToolsDoc(doc)
		return tools, err
	case ShapeOpenAIResponses:
		tools, _, err := extractResponsesToolsDoc(doc)
		return tools, err
	case ShapeClaudeMessages:
		tools, _, err := extractClaudeToolsDoc(doc)
		return tools, err
	case ShapeGeminiGenerateContent:
		tools, _, err := extractGeminiToolsDoc(doc)
		return tools, err
	default:
		return nil, fmt.Errorf("toolemu: unknown shape %d", shape)
	}
}

func foldChatDoc(doc *ojson.Doc, proto protocolSettings) error {
	tools, choice, err := extractChatToolsDoc(doc)
	if err != nil {
		return err
	}
	// Strip native tool fields even on history-only continuation turns.
	_ = doc.Delete("tools")
	_ = doc.Delete("tool_choice")
	_ = doc.Delete("parallel_tool_calls")
	if !doc.Get("messages").IsArray() {
		return nil
	}
	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		if errInject := prependChatUserInjectionDoc(doc, injection); errInject != nil {
			return errInject
		}
	}
	folded, err := FoldChatHistoryWithTagGroup([]byte(doc.Get("messages").Raw), proto.token, proto.tags)
	if err != nil {
		return err
	}
	return doc.SetRawBytes("messages", folded)
}

func foldResponsesDoc(doc *ojson.Doc, proto protocolSettings) error {
	tools, choice, err := extractResponsesToolsDoc(doc)
	if err != nil {
		return err
	}
	_ = doc.Delete("tools")
	_ = doc.Delete("tool_choice")
	_ = doc.Delete("parallel_tool_calls")
	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		if errInject := prependResponsesUserInjectionDoc(doc, injection); errInject != nil {
			return errInject
		}
	}
	if input := doc.Get("input"); input.IsArray() {
		folded, err := FoldResponsesInputWithTagGroup([]byte(input.Raw), proto.token, proto.tags)
		if err != nil {
			return err
		}
		return doc.SetRawBytes("input", folded)
	}
	return nil
}

func foldClaudeDoc(doc *ojson.Doc, proto protocolSettings) error {
	tools, choice, err := extractClaudeToolsDoc(doc)
	if err != nil {
		return err
	}
	_ = doc.Delete("tools")
	_ = doc.Delete("tool_choice")
	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		if errInject := prependClaudeUserInjectionDoc(doc, injection); errInject != nil {
			return errInject
		}
	}
	if messages := doc.Get("messages"); messages.IsArray() {
		folded, errFold := FoldClaudeMessagesWithTagGroup([]byte(messages.Raw), proto.token, proto.tags)
		if errFold != nil {
			return errFold
		}
		return doc.SetRawBytes("messages", folded)
	}
	return nil
}

func foldGeminiDoc(doc *ojson.Doc, proto protocolSettings) error {
	tools, choice, err := extractGeminiToolsDoc(doc)
	if err != nil {
		return err
	}
	_ = doc.Delete("tools")
	_ = doc.Delete("tool_config")
	_ = doc.Delete("toolConfig")
	if len(tools) > 0 {
		injection := RenderInjectionWithTagGroup(tools, choice, proto.token, proto.tags)
		part, errPart := marshalSorted(map[string]any{"text": injection})
		if errPart != nil {
			return fmt.Errorf("toolemu: marshal Gemini system injection: %w", errPart)
		}
		if errSet := doc.SetRawBytes("systemInstruction.parts.-1", part); errSet != nil {
			return errSet
		}
		if !doc.Get("systemInstruction.role").Exists() {
			_ = doc.Set("systemInstruction.role", "system")
		}
	}
	if contents := doc.Get("contents"); contents.IsArray() {
		folded, errFold := FoldGeminiContentsWithTagGroup([]byte(contents.Raw), proto.token, proto.tags)
		if errFold != nil {
			return errFold
		}
		return doc.SetRawBytes("contents", folded)
	}
	return nil
}

func extractChatToolsDoc(doc *ojson.Doc) ([]ToolSpec, ToolChoice, error) {
	toolsRes := doc.Get("tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t ojson.Result) bool {
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
	choice := applyOpenAIParallelToolCalls(parseChatToolChoice(doc.Get("tool_choice")), doc.Get("parallel_tool_calls"))
	return specs, choice, nil
}

func extractResponsesToolsDoc(doc *ojson.Doc) ([]ToolSpec, ToolChoice, error) {
	toolsRes := doc.Get("tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t ojson.Result) bool {
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
	choice := applyOpenAIParallelToolCalls(parseResponsesToolChoice(doc.Get("tool_choice")), doc.Get("parallel_tool_calls"))
	return specs, choice, nil
}

func extractClaudeToolsDoc(doc *ojson.Doc) ([]ToolSpec, ToolChoice, error) {
	toolsRes := doc.Get("tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, t ojson.Result) bool {
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
	return specs, parseClaudeToolChoice(doc.Get("tool_choice")), nil
}

func extractGeminiToolsDoc(doc *ojson.Doc) ([]ToolSpec, ToolChoice, error) {
	toolsRes := doc.Get("tools")
	if !toolsRes.IsArray() {
		return nil, ToolChoiceAuto, nil
	}
	var specs []ToolSpec
	toolsRes.ForEach(func(_, tool ojson.Result) bool {
		decls := tool.Get("functionDeclarations")
		if !decls.IsArray() {
			return true
		}
		decls.ForEach(func(_, d ojson.Result) bool {
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
	choiceNode := doc.Get("tool_config.function_calling_config")
	if !choiceNode.Exists() {
		choiceNode = doc.Get("toolConfig.functionCallingConfig")
	}
	return specs, parseGeminiToolChoice(choiceNode), nil
}
func applyOpenAIParallelToolCalls(choice ToolChoice, parallel ojson.Result) ToolChoice {
	if parallel.Exists() && parallel.Type == ojson.False {
		choice.DisableParallel = true
	}
	return choice
}

func parseChatToolChoice(v ojson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	if v.Type == ojson.String {
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

func parseResponsesToolChoice(v ojson.Result) ToolChoice {
	if !v.Exists() {
		return ToolChoiceAuto
	}
	if v.Type == ojson.String {
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

func parseClaudeToolChoice(v ojson.Result) ToolChoice {
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

func parseGeminiToolChoice(v ojson.Result) ToolChoice {
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

func prependChatUserInjectionDoc(doc *ojson.Doc, injection string) error {
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(messages.Raw), &raw); err != nil {
		return fmt.Errorf("toolemu: parse chat messages: %w", err)
	}
	userIdx := -1
	for i, msg := range raw {
		if ojson.GetBytesNoCopy(msg, "role").String() == "user" {
			userIdx = i
			break
		}
	}
	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		insertIdx := 0
		for insertIdx < len(raw) && ojson.GetBytesNoCopy(raw[insertIdx], "role").String() == "system" {
			insertIdx++
		}
		userMsg, err := marshalSorted(map[string]any{"role": "user", "content": injection})
		if err != nil {
			return fmt.Errorf("toolemu: marshal injected chat user message: %w", err)
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
			return fmt.Errorf("toolemu: marshal chat messages with injected user: %w", err)
		}
		return doc.SetRawBytes("messages", merged)
	}
	rebuiltUser, err := prependChatMessageContent(raw[userIdx], injection)
	if err != nil {
		return err
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
		return fmt.Errorf("toolemu: marshal chat messages after user injection: %w", err)
	}
	return doc.SetRawBytes("messages", merged)
}

func prependChatMessageContent(orig json.RawMessage, injection string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse chat message: %w", err)
	}

	content := ojson.GetBytesNoCopy(orig, "content")
	switch {
	case content.IsArray():
		parts := []any{map[string]any{"type": "text", "text": injection}}
		content.ForEach(func(_, existing ojson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
		obj["content"] = parts
	case content.Exists() && content.Type == ojson.String:
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

func prependResponsesUserInjectionDoc(doc *ojson.Doc, injection string) error {
	input := doc.Get("input")
	if input.Exists() && input.Type == ojson.String {
		userMsg, err := marshalSorted(map[string]any{
			"type": "message", "role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": injection},
				map[string]any{"type": "input_text", "text": input.String()},
			},
		})
		if err != nil {
			return fmt.Errorf("toolemu: marshal injected responses string input: %w", err)
		}
		items, err := marshalSorted([]any{json.RawMessage(userMsg)})
		if err != nil {
			return fmt.Errorf("toolemu: marshal responses string input array: %w", err)
		}
		return doc.SetRawBytes("input", items)
	}
	if !input.IsArray() {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(input.Raw), &raw); err != nil {
		return fmt.Errorf("toolemu: parse responses input: %w", err)
	}
	userIdx := -1
	for i, item := range raw {
		if ojson.GetBytesNoCopy(item, "type").String() == "message" && ojson.GetBytesNoCopy(item, "role").String() == "user" {
			userIdx = i
			break
		}
	}
	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		userMsg, err := marshalSorted(map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": injection}},
		})
		if err != nil {
			return fmt.Errorf("toolemu: marshal injected responses user message: %w", err)
		}
		items = append(items, json.RawMessage(userMsg))
		for _, item := range raw {
			items = append(items, json.RawMessage(item))
		}
		merged, err := marshalSorted(items)
		if err != nil {
			return fmt.Errorf("toolemu: marshal responses input with injected user: %w", err)
		}
		return doc.SetRawBytes("input", merged)
	}
	rebuiltUser, err := prependResponsesInputContent(raw[userIdx], injection)
	if err != nil {
		return err
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
		return fmt.Errorf("toolemu: marshal responses input after user injection: %w", err)
	}
	return doc.SetRawBytes("input", merged)
}

func prependResponsesInputContent(orig json.RawMessage, injection string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse responses user message: %w", err)
	}

	content := ojson.GetBytesNoCopy(orig, "content")
	injectionPart := map[string]any{"type": "input_text", "text": injection}
	switch {
	case content.IsArray():
		parts := []any{injectionPart}
		content.ForEach(func(_, existing ojson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
		obj["content"] = parts
	case content.Exists() && content.Type == ojson.String:
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

func prependClaudeUserInjectionDoc(doc *ojson.Doc, injection string) error {
	messages := doc.Get("messages")
	if !messages.IsArray() {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(messages.Raw), &raw); err != nil {
		return fmt.Errorf("toolemu: parse Claude messages: %w", err)
	}
	userIdx := -1
	for i, msg := range raw {
		if ojson.GetBytesNoCopy(msg, "role").String() == "user" {
			userIdx = i
			break
		}
	}
	// Pin the byte-stable protocol prefix unless a later caller-owned marker
	// in the same message already covers it.
	injectPart := map[string]any{"type": "text", "text": injection}
	if userIdx < 0 || !claudeMessageHasCacheControl(raw[userIdx]) {
		injectPart["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	items := make([]any, 0, len(raw)+1)
	if userIdx < 0 {
		msg, err := marshalSorted(map[string]any{"role": "user", "content": []any{injectPart}})
		if err != nil {
			return fmt.Errorf("toolemu: marshal Claude injected user message: %w", err)
		}
		items = append(items, json.RawMessage(msg))
		for _, msg := range raw {
			items = append(items, json.RawMessage(msg))
		}
		merged, err := marshalSorted(items)
		if err != nil {
			return fmt.Errorf("toolemu: marshal Claude messages with injected user: %w", err)
		}
		return doc.SetRawBytes("messages", merged)
	}
	rebuiltUser, err := prependClaudeTextPart(raw[userIdx], injectPart)
	if err != nil {
		return err
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
		return fmt.Errorf("toolemu: marshal Claude messages after user injection: %w", err)
	}
	return doc.SetRawBytes("messages", merged)
}

func prependClaudeTextPart(orig json.RawMessage, part map[string]any) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(orig, &obj); err != nil {
		return nil, fmt.Errorf("toolemu: parse Claude message: %w", err)
	}

	content := ojson.GetBytesNoCopy(orig, "content")
	parts := []any{part}
	switch {
	case content.IsArray():
		content.ForEach(func(_, existing ojson.Result) bool {
			parts = append(parts, json.RawMessage(existing.Raw))
			return true
		})
	case content.Exists() && content.Type == ojson.String:
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
	content := ojson.GetBytesNoCopy(msg, "content")
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, part ojson.Result) bool {
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
	if !ojson.ValidBytes(messages) || !ojson.ParseBytes(messages).IsArray() {
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
		role := ojson.GetBytesNoCopy(msg, "role").String()
		content := ojson.GetBytesNoCopy(msg, "content")
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

func foldClaudeAssistantParts(content ojson.Result, callIndexByID map[string]int, nextCallIndex *int, proto protocolSettings) ([]any, bool, error) {
	if content.Type == ojson.String || !content.IsArray() {
		return nil, false, nil
	}
	parts := make([]any, 0, len(content.Array()))
	changed := false
	var foldErr error
	content.ForEach(func(_, part ojson.Result) bool {
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

func foldClaudeUserParts(content ojson.Result, callIndexByID map[string]int, orphanResultIndex *int, proto protocolSettings) ([]any, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := make([]any, 0, len(content.Array()))
	changed := false
	content.ForEach(func(_, part ojson.Result) bool {
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
	if !ojson.ValidBytes(contents) || !ojson.ParseBytes(contents).IsArray() {
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
		parts := ojson.GetBytesNoCopy(item, "parts")
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

func foldGeminiParts(parts ojson.Result, callIndexesByName map[string][]int, nextCallIndex *int, proto protocolSettings) ([]any, bool, error) {
	if !parts.IsArray() {
		return nil, false, nil
	}
	out := make([]any, 0, len(parts.Array()))
	changed := false
	var foldErr error
	parts.ForEach(func(_, part ojson.Result) bool {
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
	t := ojson.GetBytesNoCopy(payload, "tools")
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
	return hasToolArtifacts(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(payload, path)
	})
}

// HasToolArtifactsDoc probes a prepared document without serializing it.
func HasToolArtifactsDoc(doc *ojson.Doc) bool {
	return doc != nil && hasToolArtifacts(doc.Get)
}

func hasToolArtifacts(get func(string) ojson.Result) bool {
	if tools := get("tools"); tools.IsArray() && len(tools.Array()) > 0 {
		return true
	}
	if tc := get("tool_choice"); tc.IsObject() {
		return true
	}
	if messages := get("messages"); messages.IsArray() {
		found := false
		messages.ForEach(func(_, msg ojson.Result) bool {
			if msg.Get("role").String() == "tool" {
				found = true
				return false
			}
			if calls := msg.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
				found = true
				return false
			}
			if content := msg.Get("content"); content.IsArray() {
				content.ForEach(func(_, part ojson.Result) bool {
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
	if input := get("input"); input.IsArray() {
		found := false
		input.ForEach(func(_, item ojson.Result) bool {
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
	if contents := get("contents"); contents.IsArray() {
		found := false
		contents.ForEach(func(_, item ojson.Result) bool {
			if parts := item.Get("parts"); parts.IsArray() {
				parts.ForEach(func(_, part ojson.Result) bool {
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
