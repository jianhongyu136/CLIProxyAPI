package toolemu

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Parse extracts XML-like raw tool blocks using the default fence token.
func Parse(text string) (Parsed, error) {
	return ParseWithFence(text, DefaultFenceToken)
}

// ParseWithFence extracts XML-like raw tool blocks from upstream assistant text.
func ParseWithFence(text, fenceToken string) (Parsed, error) {
	return ParseWithTagGroup(text, fenceToken, ToolEmulationTagGroup{})
}

// ParseWithTagGroup extracts XML-like raw tool blocks using the selected raw protocol tag group.
func ParseWithTagGroup(text, fenceToken string, tagGroup ToolEmulationTagGroup) (Parsed, error) {
	p := rawParser{proto: effectiveProtocolSettings(fenceToken, tagGroup)}
	parsed, err := p.parse(text)
	if err != nil {
		log.WithError(err).WithField("raw_text", text).Error("tool call parse failed")
		return Parsed{}, err
	}
	return parsed, nil
}

type rawParseState int

const (
	rawStateProse rawParseState = iota
	rawStateToolCalls
	rawStateTool
	rawStateArg
	rawStateResult
)

type rawParser struct {
	proto protocolSettings
	state rawParseState

	proseParts []string
	calls      []ParsedToolCall

	toolName string
	args     map[string]string
	argName  string
	argBuf   strings.Builder

	markdownFence    byte
	markdownFenceLen int
}

func (p *rawParser) parse(text string) (Parsed, error) {
	p.state = rawStateProse
	for _, physicalLine := range splitLinesPreserveNewline(text) {
		for _, line := range expandAdjacentProtocolTagLine(physicalLine, p.proto) {
			if err := p.processLine(line); err != nil {
				return Parsed{}, err
			}
		}
	}

	switch p.state {
	case rawStateProse, rawStateToolCalls:
		prose := strings.TrimRight(strings.TrimLeft(strings.Join(p.proseParts, ""), "\n"), "\n")
		return Parsed{Prose: prose, ToolCalls: p.calls}, nil
	case rawStateTool:
		return Parsed{}, fmt.Errorf("toolemu: unterminated tool block")
	case rawStateArg:
		return Parsed{}, fmt.Errorf("toolemu: unterminated argument %q", p.argName)
	case rawStateResult:
		return Parsed{}, fmt.Errorf("toolemu: unterminated result block")
	default:
		return Parsed{}, fmt.Errorf("toolemu: invalid parser state")
	}
}

func (p *rawParser) processLine(line rawLine) error {
	switch p.state {
	case rawStateProse:
		if p.markdownFence != 0 {
			p.proseParts = append(p.proseParts, line.full())
			if isMarkdownFenceCloseLine(line.text, p.markdownFence, p.markdownFenceLen) {
				p.markdownFence = 0
				p.markdownFenceLen = 0
			}
			return nil
		}
		if marker, count, ok := markdownFenceOpenLine(line.text); ok {
			p.markdownFence = marker
			p.markdownFenceLen = count
			p.proseParts = append(p.proseParts, line.full())
			return nil
		}
		if isMarkdownIndentedCodeLine(line.text) {
			p.proseParts = append(p.proseParts, line.full())
			return nil
		}
		if name, ok := parseToolOpenLineWithSettings(line.text, p.proto); ok {
			p.startTool(name)
			return nil
		}
		if p.processCopiedResultBlockLine(line) {
			return nil
		}
		if handled, err := p.processEmbeddedProtocolLine(line); handled || err != nil {
			return err
		}
		if isProtocolCandidateLineWithSettings(line.text, p.proto) {
			return fmt.Errorf("toolemu: invalid protocol tag line: %q", strings.TrimSpace(line.text))
		}
		p.proseParts = append(p.proseParts, line.full())
	case rawStateToolCalls:
		if name, ok := parseToolOpenLineWithSettings(line.text, p.proto); ok {
			p.startTool(name)
			return nil
		}
		if p.processCopiedResultBlockLine(line) {
			return nil
		}
		if strings.TrimSpace(line.text) == "" {
			return nil
		}
		p.state = rawStateProse
		return p.processLine(line)
	case rawStateTool:
		if name, ok := parseArgOpenLineWithSettings(line.text, p.proto); ok {
			if _, exists := p.args[name]; exists {
				return fmt.Errorf("toolemu: duplicate argument %q", name)
			}
			p.argName = name
			p.argBuf.Reset()
			p.state = rawStateArg
			return nil
		}
		if isToolCloseLineWithSettings(line.text, p.proto) {
			return p.finishTool()
		}
		if handled, err := p.processEmbeddedProtocolLine(line); handled || err != nil {
			return err
		}
		if isProtocolCandidateLineWithSettings(line.text, p.proto) {
			return fmt.Errorf("toolemu: invalid protocol tag line: %q", strings.TrimSpace(line.text))
		}
		if strings.TrimSpace(line.text) != "" {
			p.proseParts = append(p.proseParts, line.full())
		}
	case rawStateArg:
		if isArgCloseLineWithSettings(line.text, p.proto) {
			p.args[p.argName] = trimOneTrailingNewline(p.argBuf.String())
			p.argName = ""
			p.argBuf.Reset()
			p.state = rawStateTool
			return nil
		}
		p.argBuf.WriteString(line.full())
	case rawStateResult:
		if isResultCloseLineWithSettings(line.text, p.proto) {
			p.state = rawStateProse
		}
	}
	return nil
}

func (p *rawParser) processCopiedResultBlockLine(line rawLine) bool {
	before, tag, _, ok := cutFirstCompleteProtocolTagWithSettings(line.text, p.proto)
	if !ok {
		return false
	}
	if _, ok := parseResultOpenLineWithSettings(tag, p.proto); !ok {
		return false
	}
	if before != "" && !isCopiedResultRolePrefix(before) {
		p.proseParts = append(p.proseParts, before)
	}
	p.state = rawStateResult
	return true
}

func (p *rawParser) processEmbeddedProtocolLine(line rawLine) (bool, error) {
	before, tag, after, ok := cutFirstCompleteProtocolTagWithSettings(line.text, p.proto)
	if !ok || before == "" && after == "" {
		return false, nil
	}
	if before != "" {
		p.proseParts = append(p.proseParts, before)
	}
	if err := p.processLine(rawLine{text: tag}); err != nil {
		return true, err
	}
	if after != "" {
		return true, p.processLine(rawLine{text: after, hasNewline: line.hasNewline})
	}
	return true, nil
}

func (p *rawParser) startTool(name string) {
	p.toolName = name
	p.args = map[string]string{}
	p.state = rawStateTool
}

func (p *rawParser) finishTool() error {
	args, err := marshalStringArgs(p.args)
	if err != nil {
		return fmt.Errorf("toolemu: marshal arguments: %w", err)
	}
	p.calls = append(p.calls, ParsedToolCall{Name: p.toolName, Arguments: args})
	p.toolName = ""
	p.args = nil
	p.state = rawStateToolCalls
	return nil
}

type rawLine struct {
	text       string
	hasNewline bool
}

func (l rawLine) full() string {
	if l.hasNewline {
		return l.text + "\n"
	}
	return l.text
}

func splitLinesPreserveNewline(text string) []rawLine {
	if text == "" {
		return nil
	}
	lines := make([]rawLine, 0, strings.Count(text, "\n")+1)
	for len(text) > 0 {
		before, after, found := strings.Cut(text, "\n")
		lines = append(lines, rawLine{text: before, hasNewline: found})
		if !found {
			break
		}
		text = after
	}
	return lines
}

func expandAdjacentProtocolTagLine(line rawLine, proto protocolSettings) []rawLine {
	tags := splitAdjacentProtocolTagsWithSettings(line.text, proto)
	if len(tags) == 0 {
		return []rawLine{line}
	}
	out := make([]rawLine, 0, len(tags))
	for i, tag := range tags {
		out = append(out, rawLine{text: tag, hasNewline: i == len(tags)-1 && line.hasNewline})
	}
	return out
}

func trimOneTrailingNewline(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return strings.TrimSuffix(s, "\r\n")
	}
	if strings.HasSuffix(s, "\n") {
		return strings.TrimSuffix(s, "\n")
	}
	return s
}
