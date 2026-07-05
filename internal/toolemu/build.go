package toolemu

import "encoding/json"

func claudeUsageObject(raw []byte) any {
	usage := map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
	}
	if len(raw) == 0 {
		return usage
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
		return usage
	}
	if _, ok := parsed["input_tokens"]; !ok {
		parsed["input_tokens"] = 0
	}
	if _, ok := parsed["output_tokens"]; !ok {
		parsed["output_tokens"] = 0
	}
	return parsed
}

// BuildChatCompletion constructs a full OpenAI chat-completion JSON body from
// a Parsed result. tool_calls (if any) are placed under
// choices[0].message.tool_calls; finish_reason is "tool_calls" or "stop".
func BuildChatCompletion(p Parsed, meta UpstreamMeta) ([]byte, error) {
	msg := map[string]any{"role": "assistant"}
	if p.Prose != "" {
		msg["content"] = p.Prose
	} else {
		msg["content"] = nil
	}

	finishReason := meta.FinishOverride
	if finishReason == "" {
		finishReason = "stop"
	}
	if len(p.ToolCalls) > 0 {
		finishReason = "tool_calls"
		var tcs []any
		for _, c := range p.ToolCalls {
			id := c.ID
			if id == "" {
				id = DeriveID(meta, len(tcs))
			}
			tcs = append(tcs, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": string(c.Arguments),
				},
			})
		}
		msg["tool_calls"] = tcs
	}

	body := map[string]any{
		"id":      meta.ResponseID,
		"object":  "chat.completion",
		"created": meta.Created,
		"model":   meta.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
	}
	if len(meta.UsagePayload) > 0 {
		var u any
		if err := json.Unmarshal(meta.UsagePayload, &u); err == nil {
			body["usage"] = u
		}
	}
	return json.Marshal(body)
}

// BuildResponses constructs a full OpenAI Responses response body from a
// Parsed result. Prose (if any) is placed as a message output_item with one
// output_text content part. Each tool call becomes a function_call output_item.
func BuildResponses(p Parsed, meta UpstreamMeta) ([]byte, error) {
	status := meta.ResponseStatus
	if status == "" {
		status = "completed"
	}
	var output []any
	if p.Prose != "" {
		output = append(output, map[string]any{
			"type": "message", "role": "assistant",
			"id":     "msg_" + meta.ResponseID,
			"status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": p.Prose,
			}},
		})
	}
	for i, c := range p.ToolCalls {
		id := c.ID
		if id == "" {
			id = DeriveID(meta, i)
		}
		output = append(output, map[string]any{
			"type":      "function_call",
			"id":        id,
			"call_id":   id,
			"name":      c.Name,
			"arguments": string(c.Arguments),
			"status":    "completed",
		})
	}
	body := map[string]any{
		"id":         meta.ResponseID,
		"object":     "response",
		"created_at": meta.Created,
		"model":      meta.Model,
		"status":     status,
		"output":     output,
	}
	if len(meta.IncompleteDetails) > 0 {
		var details any
		if err := json.Unmarshal(meta.IncompleteDetails, &details); err == nil {
			body["incomplete_details"] = details
		}
	}
	if len(meta.UsagePayload) > 0 {
		var u any
		if err := json.Unmarshal(meta.UsagePayload, &u); err == nil {
			body["usage"] = u
		}
	}
	return json.Marshal(body)
}

func BuildClaudeMessage(p Parsed, meta UpstreamMeta) ([]byte, error) {
	content := make([]any, 0, 1+len(p.ToolCalls))
	if p.Prose != "" {
		content = append(content, map[string]any{"type": "text", "text": p.Prose})
	}
	for i, c := range p.ToolCalls {
		id := c.ID
		if id == "" {
			id = DeriveClaudeID(meta, i)
		}
		var input any = map[string]any{}
		_ = json.Unmarshal(c.Arguments, &input)
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  c.Name,
			"input": input,
		})
	}
	stopReason := meta.FinishOverride
	if stopReason == "" {
		stopReason = "end_turn"
	}
	if len(p.ToolCalls) > 0 {
		stopReason = "tool_use"
	}
	body := map[string]any{
		"id":            meta.ResponseID,
		"type":          "message",
		"role":          "assistant",
		"model":         meta.Model,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
	}
	body["usage"] = claudeUsageObject(meta.UsagePayload)
	return json.Marshal(body)
}

func BuildGeminiGenerateContent(p Parsed, meta UpstreamMeta) ([]byte, error) {
	parts := make([]any, 0, 1+len(p.ToolCalls))
	if p.Prose != "" {
		parts = append(parts, map[string]any{"text": p.Prose})
	}
	for _, c := range p.ToolCalls {
		var args any = map[string]any{}
		_ = json.Unmarshal(c.Arguments, &args)
		parts = append(parts, map[string]any{"functionCall": map[string]any{
			"name": c.Name,
			"args": args,
		}})
	}
	body := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": "STOP",
		}},
	}
	if meta.Model != "" {
		body["modelVersion"] = meta.Model
	}
	if len(meta.UsagePayload) > 0 {
		var u any
		if err := json.Unmarshal(meta.UsagePayload, &u); err == nil {
			body["usageMetadata"] = u
		}
	}
	return json.Marshal(body)
}
