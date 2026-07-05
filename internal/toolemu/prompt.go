package toolemu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ToolSpec is a tool declaration normalized from upstream payload's `tools` array.
type ToolSpec struct {
	Name        string
	Description string
	SchemaJSON  json.RawMessage
}

// ToolChoiceKind enumerates the four supported tool_choice modes.
type ToolChoiceKind int

const (
	ToolChoiceKindAuto ToolChoiceKind = iota
	ToolChoiceKindNone
	ToolChoiceKindRequired
	ToolChoiceKindNamed
)

// ToolChoice is the locked-down tool_choice value used during rendering.
type ToolChoice struct {
	Kind            ToolChoiceKind
	Name            string // only for Kind == ToolChoiceKindNamed
	DisableParallel bool
}

// Convenience constructors.
var (
	ToolChoiceAuto     = ToolChoice{Kind: ToolChoiceKindAuto}
	ToolChoiceNone     = ToolChoice{Kind: ToolChoiceKindNone}
	ToolChoiceRequired = ToolChoice{Kind: ToolChoiceKindRequired}
)

// ToolChoiceNamed returns a Named tool_choice.
func ToolChoiceNamed(name string) ToolChoice {
	return ToolChoice{Kind: ToolChoiceKindNamed, Name: name}
}

const protocolCallableText = `<tool_protocol>
Use tools only with this raw protocol. Do not use native JSON/function-call formats.
Fence token: {{token}}

Format:

<{{tool}}|TOOL_NAME|{{token}}>
<{{arg}}|ARG_NAME|{{token}}>
raw argument text
</{{arg}}|{{token}}>
</{{tool}}|{{token}}>

Example with optional prose followed by two tool calls (note the line break before the first tag):

I'll check both cities.
<{{tool}}|{{example_tool}}|{{token}}>
<{{arg}}|city|{{token}}>
Tokyo
</{{arg}}|{{token}}>
</{{tool}}|{{token}}>
<{{tool}}|{{example_tool}}|{{token}}>
<{{arg}}|city|{{token}}>
Osaka
</{{arg}}|{{token}}>
</{{tool}}|{{token}}>

Rules:
- TOOL_NAME MUST exactly match one tool name from <tools_doc>; do not invent, rename, prefix, suffix, or translate names.
- If calling tools, emit one or more <{{tool}}|...> blocks directly; do not wrap them in any outer tag.
- Every protocol tag MUST be on its own line: begin a NEW line, start at column 1 with <, put nothing else (no prose, no other tag) on that line, and use exactly fence token {{quoted_token}}.
- Use literal | separators with no surrounding spaces: write <{{tool}}|TOOL_NAME|{{token}}> and <{{arg}}|ARG_NAME|{{token}}> exactly.
- CRITICAL: never append <{{tool}}|...> to the end of a sentence or the same line as any other text. If natural-language text comes first, finish it and press Enter (emit a newline) before the opening tag; otherwise the tag is treated as prose and the call fails.
- Never indent or bullet protocol tags.
- Use one <{{arg}}|ARG_NAME|{{token}}> block per argument; omit args for tools with no arguments.
- Argument content is raw text between arg tags; preserve quotes, backslashes, angle brackets, and newlines when they are part of the value.
- Put natural-language text, if any, before tool blocks, not between or after tool blocks.
- Do not emit result blocks; historical tool calls/results and copied examples are context only.
- Wrong: never copy history/result text such as "user<{{result}}|INDEX|{{token}}>..."; result blocks are previous tool results, not assistant output.
- If showing protocol-looking examples as prose, put them in Markdown code fences.

{{choice_clause}}
</tool_protocol>`

const protocolNoToolText = `<tool_protocol>
Tool calls are disabled for this response.

Rules:
- You MUST NOT call any tool in this response. Answer in natural language only.
- Do not emit raw protocol tags, native JSON tool calls, function calls, or result blocks.
- Treat <tools_doc>, historical tool calls/results, and protocol examples as context only.
- If showing protocol-looking examples as prose, put them in Markdown code fences.

{{choice_clause}}
</tool_protocol>`

// RenderInjection builds the deterministic injection text (tools_doc + tool_protocol).
// Inputs in any order produce the same output (R2). Output never contains
// volatile fields (no timestamps, request ids, nonces).
func RenderInjection(tools []ToolSpec, choice ToolChoice) string {
	return RenderInjectionWithFence(tools, choice, DefaultFenceToken)
}

// RenderInjectionWithFence builds the deterministic injection text using the
// supplied raw-protocol fence token.
func RenderInjectionWithFence(tools []ToolSpec, choice ToolChoice, fenceToken string) string {
	return RenderInjectionWithTagGroup(tools, choice, fenceToken, ToolEmulationTagGroup{})
}

// RenderInjectionWithTagGroup builds the deterministic injection text using the
// supplied raw-protocol fence token and tag group.
func RenderInjectionWithTagGroup(tools []ToolSpec, choice ToolChoice, fenceToken string, tagGroup ToolEmulationTagGroup) string {
	proto := effectiveProtocolSettings(fenceToken, tagGroup)
	sorted := append([]ToolSpec(nil), tools...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	exampleToolName := "TOOL_NAME"
	if len(sorted) > 0 && sorted[0].Name != "" {
		exampleToolName = sorted[0].Name
	}

	var b strings.Builder
	b.WriteString("<tools_doc>\n")
	toolsJSON := make([]any, 0, len(sorted))
	for _, t := range sorted {
		var schema any
		if err := json.Unmarshal([]byte(canonicalJSON(t.SchemaJSON)), &schema); err != nil {
			schema = map[string]any{}
		}
		toolsJSON = append(toolsJSON, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
			},
		})
	}
	toolsDoc, err := marshalSorted(toolsJSON)
	if err != nil {
		toolsDoc = []byte("[]")
	}
	b.Write(toolsDoc)
	b.WriteString("\n</tools_doc>\n")
	choiceClause := renderToolChoiceClause(choice)
	if choice.Kind == ToolChoiceKindNone {
		b.WriteString(renderProtocolTemplate(protocolNoToolText, proto, choiceClause, exampleToolName))
	} else {
		b.WriteString(renderProtocolTemplate(protocolCallableText, proto, choiceClause, exampleToolName))
	}
	return b.String()
}

func renderProtocolTemplate(template string, proto protocolSettings, choiceClause, exampleToolName string) string {
	replacer := strings.NewReplacer(
		"{{token}}", proto.token,
		"{{quoted_token}}", fmt.Sprintf("%q", proto.token),
		"{{tool}}", proto.tags.Tool,
		"{{arg}}", proto.tags.Arg,
		"{{result}}", proto.tags.Result,
		"{{choice_clause}}", choiceClause,
		"{{example_tool}}", exampleToolName,
	)
	return replacer.Replace(template)
}

func renderToolChoiceClause(c ToolChoice) string {
	switch c.Kind {
	case ToolChoiceKindNone:
		return "You MUST NOT call any tool in this response. Answer in natural language only."
	case ToolChoiceKindRequired:
		return "You MUST call at least one tool in this response."
	case ToolChoiceKindNamed:
		return fmt.Sprintf(`You MUST call the tool named %q in this response.`, c.Name)
	default:
		return "You MAY call a tool when helpful, or answer directly without a tool call."
	}
}

// canonicalJSON re-serializes raw JSON with sorted object keys, no extra whitespace.
// Returns the input string verbatim on parse failure (defense in depth).
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	out, err := marshalSorted(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func marshalSorted(v any) ([]byte, error) {
	switch t := v.(type) {
	case json.RawMessage:
		// RawMessage is already canonical bytes; emit verbatim.
		return []byte(t), nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b bytes.Buffer
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kj, err := marshalLeafNoEscape(k)
			if err != nil {
				return nil, err
			}
			b.Write(kj)
			b.WriteByte(':')
			vj, err := marshalSorted(t[k])
			if err != nil {
				return nil, err
			}
			b.Write(vj)
		}
		b.WriteByte('}')
		return b.Bytes(), nil
	case []any:
		var b bytes.Buffer
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			ej, err := marshalSorted(e)
			if err != nil {
				return nil, err
			}
			b.Write(ej)
		}
		b.WriteByte(']')
		return b.Bytes(), nil
	default:
		return marshalLeafNoEscape(v)
	}
}

// marshalLeafNoEscape JSON-encodes a leaf value (string, number, bool, nil)
// without escaping `<`, `>`, `&` to their `\u00xx` forms. Protocol blocks must
// remain literal so the upstream model sees the same tag text after a JSON
// round-trip.
func marshalLeafNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// TemplateVersion is bumped whenever the built-in template body changes; used
// by management status output to help debug cache-rate stepdowns after reloads.
const builtinTemplateVersion = 11

// TemplateVersion returns the current template version.
func TemplateVersion() int { return builtinTemplateVersion }
