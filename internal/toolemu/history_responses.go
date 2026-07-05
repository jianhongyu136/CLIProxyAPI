package toolemu

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// FoldResponsesInput rewrites the Responses API `input` array, folding
// function_call items into raw tool text on the preceding message item, and
// function_call_output items into raw result text on a new user item.
func FoldResponsesInput(input []byte) ([]byte, error) {
	return FoldResponsesInputWithFence(input, DefaultFenceToken)
}

func FoldResponsesInputWithFence(input []byte, fenceToken string) ([]byte, error) {
	return FoldResponsesInputWithTagGroup(input, fenceToken, ToolEmulationTagGroup{})
}

func FoldResponsesInputWithTagGroup(input []byte, fenceToken string, tagGroup ToolEmulationTagGroup) ([]byte, error) {
	proto := effectiveProtocolSettings(fenceToken, tagGroup)
	if !gjson.ValidBytes(input) || !gjson.ParseBytes(input).IsArray() {
		return nil, fmt.Errorf("toolemu: input payload is not a JSON array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		return nil, err
	}

	out := make([]json.RawMessage, 0, len(raw))
	var pendingCalls []string
	var pendingResults []string
	callIndexByID := map[string]int{}
	nextCallIndex := 0
	flushResults := func() {
		if len(pendingResults) == 0 {
			return
		}
		userItem, _ := marshalSorted(map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": strings.Join(pendingResults, "\n"),
			}},
		})
		out = append(out, userItem)
		pendingResults = pendingResults[:0]
	}

	for _, item := range raw {
		t := gjson.GetBytes(item, "type").String()
		switch t {
		case "function_call":
			callID := gjson.GetBytes(item, "call_id").String()
			itemID := gjson.GetBytes(item, "id").String()
			index := nextCallIndex
			if callID != "" {
				callIndexByID[callID] = index
			}
			if itemID != "" {
				callIndexByID[itemID] = index
			}
			nextCallIndex++
			name := gjson.GetBytes(item, "name").String()
			argsStr := gjson.GetBytes(item, "arguments").String()
			block, errBlock := renderToolBlockWithSettings(name, argsJSONToStringMap(canonicalArgs(argsStr)), proto)
			if errBlock != nil {
				return nil, errBlock
			}
			pendingCalls = append(pendingCalls, block)
		case "function_call_output":
			cid := gjson.GetBytes(item, "call_id").String()
			index := len(pendingResults)
			if mapped, ok := callIndexByID[cid]; ok {
				index = mapped
			}
			outputStr := gjson.GetBytes(item, "output").String()
			pendingResults = append(pendingResults, renderResultBlockWithSettings(index, outputStr, proto))
		default:
			if len(pendingCalls) > 0 {
				appended, err := appendToLastAssistant(out, pendingCalls, proto)
				if err != nil {
					return nil, err
				}
				out = appended
				pendingCalls = pendingCalls[:0]
			}
			flushResults()
			out = append(out, item)
		}
	}
	if len(pendingCalls) > 0 {
		appended, err := appendToLastAssistant(out, pendingCalls, proto)
		if err != nil {
			return nil, err
		}
		out = appended
	}
	flushResults()
	return marshalSorted(out)
}

func appendToLastAssistant(items []json.RawMessage, blocks []string, proto protocolSettings) ([]json.RawMessage, error) {
	extra, err := renderToolCallsBlockWithSettings(blocks, proto)
	if err != nil {
		return items, err
	}
	for i := len(items) - 1; i >= 0; i-- {
		t := gjson.GetBytes(items[i], "type").String()
		r := gjson.GetBytes(items[i], "role").String()
		if t == "message" && r == "assistant" {
			merged, err := appendOutputText(items[i], extra)
			if err != nil {
				return items, err
			}
			items[i] = merged
			return items, nil
		}
	}
	asst, _ := marshalSorted(map[string]any{
		"type": "message", "role": "assistant",
		"content": []any{map[string]any{
			"type": "output_text", "text": extra,
		}},
	})
	return append(items, asst), nil
}

func appendOutputText(item json.RawMessage, extra string) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(item, &m); err != nil {
		return nil, err
	}
	contentAny, _ := m["content"].([]any)
	contentAny = append(contentAny, map[string]any{
		"type": "output_text", "text": extra,
	})
	m["content"] = contentAny
	return marshalSorted(m)
}
