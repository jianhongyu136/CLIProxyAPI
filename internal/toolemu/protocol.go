package toolemu

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func effectiveFenceToken(token string) string {
	if validFenceToken(token) {
		return token
	}
	return DefaultFenceToken
}

type protocolSettings struct {
	token string
	tags  ToolEmulationTagGroup
}

func effectiveProtocolSettings(token string, tags ToolEmulationTagGroup) protocolSettings {
	return protocolSettings{
		token: effectiveFenceToken(token),
		tags:  defaultTagGroup(tags),
	}
}

func isProtocolName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

func parseToolOpenLine(line, token string) (string, bool) {
	return parseToolOpenLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func parseToolOpenLineWithSettings(line string, proto protocolSettings) (string, bool) {
	return parseProtocolOpenLine(line, proto.tags.Tool, proto)
}

func parseProtocolOpenLine(line, tag string, proto protocolSettings) (string, bool) {
	trimmed := trimProtocolLineEnd(line)
	prefix := "<" + tag + "|"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, ">") {
		return "", false
	}
	inside := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), ">")
	parts := strings.Split(inside, "|")
	if len(parts) != 2 || parts[1] != proto.token || !isProtocolName(parts[0]) {
		return "", false
	}
	return parts[0], true
}

func parseArgOpenLine(line, token string) (string, bool) {
	return parseArgOpenLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func parseArgOpenLineWithSettings(line string, proto protocolSettings) (string, bool) {
	return parseProtocolOpenLine(line, proto.tags.Arg, proto)
}

func isArgCloseLine(line, token string) bool {
	return isArgCloseLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func isArgCloseLineWithSettings(line string, proto protocolSettings) bool {
	return trimProtocolLineEnd(line) == "</"+proto.tags.Arg+"|"+proto.token+">"
}

func isToolCloseLine(line, token string) bool {
	return isToolCloseLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func isToolCloseLineWithSettings(line string, proto protocolSettings) bool {
	return trimProtocolLineEnd(line) == "</"+proto.tags.Tool+"|"+proto.token+">"
}

func parseResultOpenLine(line, token string) (int, bool) {
	return parseResultOpenLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func parseResultOpenLineWithSettings(line string, proto protocolSettings) (int, bool) {
	trimmed := trimProtocolLineEnd(line)
	prefix := "<" + proto.tags.Result + "|"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, ">") {
		return 0, false
	}
	inside := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), ">")
	parts := strings.Split(inside, "|")
	if len(parts) != 2 || parts[1] != proto.token {
		return 0, false
	}
	index, err := strconv.Atoi(parts[0])
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
}

func isResultCloseLine(line, token string) bool {
	return isResultCloseLineWithSettings(line, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func isResultCloseLineWithSettings(line string, proto protocolSettings) bool {
	return trimProtocolLineEnd(line) == "</"+proto.tags.Result+"|"+proto.token+">"
}

func isProtocolCandidateLine(line string) bool {
	return isProtocolCandidateLineWithSettings(line, effectiveProtocolSettings(DefaultFenceToken, ToolEmulationTagGroup{}))
}

func isProtocolCandidateLineWithSettings(line string, proto protocolSettings) bool {
	return protocolCandidateIndexWithSettings(trimProtocolLineEnd(line), proto) == 0
}

func containsProtocolCandidateTag(line string) bool {
	return containsProtocolCandidateTagWithSettings(line, effectiveProtocolSettings(DefaultFenceToken, ToolEmulationTagGroup{}))
}

func containsProtocolCandidateTagWithSettings(line string, proto protocolSettings) bool {
	trimmed := trimProtocolLineEnd(line)
	idx := protocolCandidateIndexWithSettings(trimmed, proto)
	if idx < 0 {
		return false
	}
	return strings.TrimSpace(trimmed[:idx]) != ""
}

func isCopiedResultRolePrefix(prefix string) bool {
	return strings.TrimSpace(prefix) == "user"
}

func protocolCandidateIndex(line string) int {
	return protocolCandidateIndexWithSettings(line, effectiveProtocolSettings(DefaultFenceToken, ToolEmulationTagGroup{}))
}

func protocolCandidateIndexWithSettings(line string, proto protocolSettings) int {
	prefixes := []string{
		"<" + proto.tags.Tool + "|",
		"<" + proto.tags.Arg + "|",
		"<" + proto.tags.Result + "|",
		"</" + proto.tags.Tool + "|",
		"</" + proto.tags.Arg + "|",
		"</" + proto.tags.Result + "|",
	}
	first := -1
	for _, prefix := range prefixes {
		idx := strings.Index(line, prefix)
		if idx >= 0 && (first == -1 || idx < first) {
			first = idx
		}
	}
	return first
}

func trimProtocolLineEnd(line string) string {
	return strings.TrimRight(line, " \t\r\n")
}

func markdownFenceOpenLine(line string) (byte, int, bool) {
	body, ok := markdownFenceBody(line)
	if !ok {
		return 0, 0, false
	}
	if strings.HasPrefix(body, "```") {
		return '`', countLeadingMarkdownFenceMarkers(body, '`'), true
	}
	if strings.HasPrefix(body, "~~~") {
		return '~', countLeadingMarkdownFenceMarkers(body, '~'), true
	}
	return 0, 0, false
}

func isMarkdownFenceCloseLine(line string, marker byte, minCount int) bool {
	body, ok := markdownFenceBody(line)
	if !ok || body == "" || body[0] != marker {
		return false
	}
	count := countLeadingMarkdownFenceMarkers(body, marker)
	if count < minCount {
		return false
	}
	return strings.TrimSpace(body[count:]) == ""
}

func countLeadingMarkdownFenceMarkers(body string, marker byte) int {
	count := 0
	for count < len(body) && body[count] == marker {
		count++
	}
	return count
}

func markdownFenceBody(line string) (string, bool) {
	body := trimProtocolLineEnd(line)
	spaces := 0
	for spaces < len(body) && body[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return "", false
	}
	return body[spaces:], true
}

func isMarkdownIndentedCodeLine(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

func renderToolBlock(name string, args map[string]string, token string) (string, error) {
	return renderToolBlockWithSettings(name, args, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func renderToolBlockWithSettings(name string, args map[string]string, proto protocolSettings) (string, error) {
	if !isProtocolName(name) {
		return "", fmt.Errorf("toolemu: invalid tool name %q", name)
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		if !isProtocolName(k) {
			return "", fmt.Errorf("toolemu: invalid argument name %q", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "<%s|%s|%s>\n", proto.tags.Tool, name, proto.token)
	for _, k := range keys {
		fmt.Fprintf(&b, "<%s|%s|%s>\n", proto.tags.Arg, k, proto.token)
		b.WriteString(args[k])
		b.WriteByte('\n')
		fmt.Fprintf(&b, "</%s|%s>\n", proto.tags.Arg, proto.token)
	}
	fmt.Fprintf(&b, "</%s|%s>", proto.tags.Tool, proto.token)
	return b.String(), nil
}

func renderToolCallsBlock(blocks []string, token string) (string, error) {
	return renderToolCallsBlockWithSettings(blocks, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func renderToolCallsBlockWithSettings(blocks []string, proto protocolSettings) (string, error) {
	var b strings.Builder
	for i, block := range blocks {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(block)
	}
	return b.String(), nil
}

func splitAdjacentProtocolTagsWithSettings(line string, proto protocolSettings) []string {
	trimmed := trimProtocolLineEnd(line)
	if trimmed == "" || strings.TrimLeft(trimmed, " \t") != trimmed {
		return nil
	}
	var tags []string
	for rest := trimmed; rest != ""; {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return nil
		}
		tag := rest[:end+1]
		if !isCompleteProtocolTagWithSettings(tag, proto) {
			return nil
		}
		tags = append(tags, tag)
		rest = rest[end+1:]
	}
	if len(tags) < 2 {
		return nil
	}
	return tags
}

func cutFirstCompleteProtocolTagWithSettings(line string, proto protocolSettings) (before, tag, after string, ok bool) {
	trimmed := trimProtocolLineEnd(line)
	lineEnd := line[len(trimmed):]
	offset := 0
	for offset < len(trimmed) {
		idx := protocolCandidateIndexWithSettings(trimmed[offset:], proto)
		if idx < 0 {
			return "", "", "", false
		}
		idx += offset
		rest := trimmed[idx:]
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return "", "", "", false
		}
		candidate := rest[:end+1]
		if isCompleteProtocolTagWithSettings(candidate, proto) {
			after = rest[end+1:]
			if after != "" {
				after += lineEnd
			}
			return trimmed[:idx], candidate, after, true
		}
		offset = idx + 1
	}
	return "", "", "", false
}

func isCompleteProtocolTagWithSettings(tag string, proto protocolSettings) bool {
	if _, ok := parseToolOpenLineWithSettings(tag, proto); ok {
		return true
	}
	if isToolCloseLineWithSettings(tag, proto) {
		return true
	}
	if _, ok := parseArgOpenLineWithSettings(tag, proto); ok {
		return true
	}
	if isArgCloseLineWithSettings(tag, proto) {
		return true
	}
	if _, ok := parseResultOpenLineWithSettings(tag, proto); ok {
		return true
	}
	return isResultCloseLineWithSettings(tag, proto)
}

func renderResultBlock(index int, content, token string) string {
	return renderResultBlockWithSettings(index, content, effectiveProtocolSettings(token, ToolEmulationTagGroup{}))
}

func renderResultBlockWithSettings(index int, content string, proto protocolSettings) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s|%d|%s>\n", proto.tags.Result, index, proto.token)
	b.WriteString(content)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "</%s|%s>", proto.tags.Result, proto.token)
	return b.String()
}

func argsJSONToStringMap(raw []byte) map[string]string {
	out := map[string]string{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return out
	}
	var obj map[string]any
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return out
	}
	for k, v := range obj {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			b, err := marshalSorted(t)
			if err != nil {
				out[k] = fmt.Sprint(t)
				continue
			}
			out[k] = string(b)
		}
	}
	return out
}

func marshalStringArgs(args map[string]string) ([]byte, error) {
	asAny := make(map[string]any, len(args))
	for k, v := range args {
		asAny[k] = v
	}
	return marshalSorted(asAny)
}
