package toolemu

import (
	"bytes"
	"encoding/json"
	"sort"
)

type toolArgRenderer struct {
	byTool map[string]map[string]string
}

func newToolArgRenderer(tools []ToolSpec) toolArgRenderer {
	if len(tools) == 0 {
		return toolArgRenderer{}
	}
	byTool := make(map[string]map[string]string, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || len(tool.SchemaJSON) == 0 {
			continue
		}
		argTypes := schemaArgTypes(tool.SchemaJSON)
		if len(argTypes) == 0 {
			continue
		}
		byTool[tool.Name] = argTypes
	}
	return toolArgRenderer{byTool: byTool}
}

func (r toolArgRenderer) renderArgs(toolName string, args map[string]string) ([]byte, error) {
	argSchema, ok := r.byTool[toolName]
	if !ok {
		return marshalStringArgs(args)
	}
	return renderRawArgsWithSchema(args, argSchema)
}

func (r toolArgRenderer) renderParsed(parsed Parsed) Parsed {
	if len(r.byTool) == 0 || len(parsed.ToolCalls) == 0 {
		return parsed
	}
	out := parsed
	out.ToolCalls = append([]ParsedToolCall(nil), parsed.ToolCalls...)
	for i, call := range out.ToolCalls {
		if _, ok := r.byTool[call.Name]; !ok {
			continue
		}
		var args map[string]string
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			continue
		}
		rendered, err := r.renderArgs(call.Name, args)
		if err != nil {
			continue
		}
		out.ToolCalls[i].Arguments = rendered
	}
	return out
}

func schemaArgTypes(raw json.RawMessage) map[string]string {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return nil
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}
	out := map[string]string{}
	for name, propRaw := range props {
		prop, ok := propRaw.(map[string]any)
		if !ok {
			continue
		}
		if typ := preferredSchemaType(prop["type"]); typ != "" {
			out[name] = typ
		}
	}
	return out
}

func preferredSchemaType(raw any) string {
	switch t := raw.(type) {
	case string:
		if isRenderSchemaType(t) {
			return t
		}
	case []any:
		chosen := ""
		for _, item := range t {
			s, ok := item.(string)
			if !ok || !isRenderSchemaType(s) {
				continue
			}
			if s == "string" {
				return "string"
			}
			if chosen == "" {
				chosen = s
			}
		}
		return chosen
	}
	return ""
}

func isRenderSchemaType(typ string) bool {
	switch typ {
	case "string", "number", "integer", "boolean", "object", "array":
		return true
	default:
		return false
	}
}

func renderRawArgsWithSchema(args map[string]string, argSchema map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		keyJSON, err := marshalLeafNoEscape(k)
		if err != nil {
			return nil, err
		}
		b.Write(keyJSON)
		b.WriteByte(':')
		if shouldEmbedRawArgument(argSchema[k]) {
			b.WriteString(args[k])
			continue
		}
		valueJSON, err := marshalLeafNoEscape(args[k])
		if err != nil {
			return nil, err
		}
		b.Write(valueJSON)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func shouldEmbedRawArgument(typ string) bool {
	switch typ {
	case "number", "integer", "boolean", "object", "array":
		return true
	default:
		return false
	}
}
