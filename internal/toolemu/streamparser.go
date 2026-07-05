package toolemu

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// StreamEvents defines the callbacks emitted by StreamParser.
type StreamEvents struct {
	OnMetaUpdate     func(meta UpstreamMeta)
	OnProseDelta     func(delta string)
	OnReasoningDelta func(delta string)
	OnToolCallStart  func(index int, id, name string)
	OnArgsDelta      func(index int, delta string)
	OnToolCallEnd    func(index int)
	OnComplete       func()
	OnError          func(err error)
}

type streamState int

const (
	streamStateProse streamState = iota
	streamStateToolCalls
	streamStateTool
	streamStateArg
	streamStateResult
)

// StreamParser is an incremental line-oriented state machine for raw fenced
// tool blocks. Feed arbitrary-sized string fragments via Feed() and receive
// structured events through StreamEvents callbacks.
type StreamParser struct {
	events       StreamEvents
	meta         UpstreamMeta
	proto        protocolSettings
	argsRenderer toolArgRenderer

	state   streamState
	line    strings.Builder
	rawText strings.Builder

	toolIndex int
	toolName  string
	args      map[string]string

	argName string
	argBuf  strings.Builder

	markdownFence    byte
	markdownFenceLen int

	pendingModelOutputErr    error
	pendingModelOutputLogged bool

	failed bool
	err    error

	// UTF-8 partial rune buffer.
	pendingBytes []byte
}

// NewStreamParser creates a parser that emits events as text is fed.
func NewStreamParser(events StreamEvents, meta UpstreamMeta, fenceToken string) *StreamParser {
	return NewStreamParserWithTagGroup(events, meta, fenceToken, ToolEmulationTagGroup{})
}

// NewStreamParserWithTagGroup creates a parser using the selected raw protocol tag group.
func NewStreamParserWithTagGroup(events StreamEvents, meta UpstreamMeta, fenceToken string, tagGroup ToolEmulationTagGroup) *StreamParser {
	return &StreamParser{
		events: events,
		meta:   meta,
		proto:  effectiveProtocolSettings(fenceToken, tagGroup),
	}
}

// SetToolSpecs enables schema-aware raw argument rendering without parsing argument values.
func (p *StreamParser) SetToolSpecs(tools []ToolSpec) {
	if p == nil {
		return
	}
	p.argsRenderer = newToolArgRenderer(tools)
}

// Feed processes a chunk of text from the upstream stream.
func (p *StreamParser) Feed(chunk string) {
	if p == nil {
		return
	}
	p.rawText.WriteString(chunk)
	if p.failed {
		return
	}
	var data []byte
	if len(p.pendingBytes) > 0 {
		data = append(p.pendingBytes, []byte(chunk)...)
		p.pendingBytes = p.pendingBytes[:0]
	} else {
		data = []byte(chunk)
	}
	valid := validUTF8Prefix(data)
	if valid < len(data) {
		p.pendingBytes = append(p.pendingBytes, data[valid:]...)
		data = data[:valid]
	}
	for _, r := range string(data) {
		p.line.WriteRune(r)
		if r == '\n' {
			p.processLine(p.line.String())
			p.line.Reset()
			if p.failed {
				return
			}
		}
	}
}

// Close signals end of stream. Flushes buffered content and calls OnComplete.
func (p *StreamParser) Close() {
	if p == nil {
		return
	}
	if p.failed {
		p.flushPendingModelOutputLog()
		return
	}
	if len(p.pendingBytes) > 0 {
		for _, r := range string(p.pendingBytes) {
			p.line.WriteRune(r)
			if r == '\n' {
				p.processLine(p.line.String())
				p.line.Reset()
				if p.failed {
					return
				}
			}
		}
		p.pendingBytes = nil
	}
	if p.line.Len() > 0 {
		p.processLine(p.line.String())
		p.line.Reset()
		if p.failed {
			return
		}
	}
	switch p.state {
	case streamStateTool:
		p.emitError(fmt.Errorf("toolemu stream: unterminated tool block"))
		return
	case streamStateArg:
		p.emitError(fmt.Errorf("toolemu stream: unterminated argument block"))
		return
	case streamStateResult:
		p.emitError(fmt.Errorf("toolemu stream: unterminated result block"))
		return
	}
	if p.events.OnComplete != nil {
		p.events.OnComplete()
	}
}

// Abort signals an upstream stream failure. It deliberately does not flush
// buffered tool/prose state or call OnComplete, because doing so would emit a
// successful terminal frame before the real upstream error reaches the client.
func (p *StreamParser) Abort(err error) {
	if p == nil {
		return
	}
	p.pendingBytes = nil
	p.line.Reset()
	p.argBuf.Reset()
	p.markdownFence = 0
	p.markdownFenceLen = 0
	p.state = streamStateProse
	p.failed = true
	p.err = err
	p.flushPendingModelOutputLog()
	if p.events.OnError != nil {
		p.events.OnError(err)
	}
}

// Err returns the first parser error observed during streaming.
func (p *StreamParser) Err() error {
	if p == nil {
		return nil
	}
	return p.err
}

// RawText returns the upstream assistant text accumulated by the parser.
func (p *StreamParser) RawText() string {
	if p == nil {
		return ""
	}
	return p.rawText.String()
}

// UpdateMeta refreshes metadata learned from upstream frames before callbacks emit downstream frames.
func (p *StreamParser) UpdateMeta(meta UpstreamMeta) {
	p.meta = meta
	if p.events.OnMetaUpdate != nil {
		p.events.OnMetaUpdate(meta)
	}
}

func (p *StreamParser) processLine(line string) {
	for _, logicalLine := range expandAdjacentProtocolStreamLine(line, p.proto) {
		p.processLogicalLine(logicalLine)
		if p.failed {
			return
		}
	}
}

func expandAdjacentProtocolStreamLine(line string, proto protocolSettings) []string {
	tags := splitAdjacentProtocolTagsWithSettings(line, proto)
	if len(tags) == 0 {
		return []string{line}
	}
	return tags
}

func (p *StreamParser) processLogicalLine(line string) {
	switch p.state {
	case streamStateProse:
		p.processProseLine(line)
	case streamStateToolCalls:
		p.processToolCallsLine(line)
	case streamStateTool:
		p.processToolLine(line)
	case streamStateArg:
		p.processArgLine(line)
	case streamStateResult:
		if isResultCloseLineWithSettings(line, p.proto) {
			p.state = streamStateProse
		}
	}
}

func (p *StreamParser) processProseLine(line string) {
	if p.markdownFence != 0 {
		p.emitProse(line)
		if isMarkdownFenceCloseLine(line, p.markdownFence, p.markdownFenceLen) {
			p.markdownFence = 0
			p.markdownFenceLen = 0
		}
		return
	}
	if marker, count, ok := markdownFenceOpenLine(line); ok {
		p.markdownFence = marker
		p.markdownFenceLen = count
		p.emitProse(line)
		return
	}
	if isMarkdownIndentedCodeLine(line) {
		p.emitProse(line)
		return
	}
	if name, ok := parseToolOpenLineWithSettings(line, p.proto); ok {
		p.startTool(name)
		return
	}
	if p.processCopiedResultBlockLine(line) {
		return
	}
	if p.processEmbeddedProtocolLine(line) {
		return
	}
	if isProtocolCandidateLineWithSettings(line, p.proto) {
		p.emitModelOutputError(invalidStreamProtocolLineError(line, p.proto.token))
		return
	}
	p.emitProse(line)
}

func (p *StreamParser) processToolCallsLine(line string) {
	if name, ok := parseToolOpenLineWithSettings(line, p.proto); ok {
		p.startTool(name)
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	p.state = streamStateProse
	p.processProseLine(line)
}

func (p *StreamParser) processToolLine(line string) {
	if name, ok := parseArgOpenLineWithSettings(line, p.proto); ok {
		if _, exists := p.args[name]; exists {
			p.emitError(fmt.Errorf("toolemu stream: duplicate argument %q", name))
			return
		}
		p.argName = name
		p.argBuf.Reset()
		p.state = streamStateArg
		return
	}
	if isToolCloseLineWithSettings(line, p.proto) {
		p.finishTool()
		return
	}
	if p.processEmbeddedProtocolLine(line) {
		return
	}
	if isProtocolCandidateLineWithSettings(line, p.proto) {
		p.emitModelOutputError(invalidStreamProtocolLineError(line, p.proto.token))
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	p.emitProse(line)
}

func (p *StreamParser) processCopiedResultBlockLine(line string) bool {
	before, tag, _, ok := cutFirstCompleteProtocolTagWithSettings(line, p.proto)
	if !ok {
		return false
	}
	resultIndex, ok := parseResultOpenLineWithSettings(tag, p.proto)
	if !ok {
		return false
	}
	rolePrefix := isCopiedResultRolePrefix(before)
	if before != "" && !rolePrefix {
		p.emitProse(before)
	}
	p.logCopiedResultBlock(resultIndex, before != "", rolePrefix)
	p.state = streamStateResult
	return true
}

func (p *StreamParser) logCopiedResultBlock(resultIndex int, hasPrefix, rolePrefix bool) {
	log.WithFields(log.Fields{
		"fence_token":  p.proto.token,
		"tag_group":    p.proto.tags,
		"provider":     p.meta.Provider,
		"model":        p.meta.Model,
		"response_id":  p.meta.ResponseID,
		"result_index": resultIndex,
		"has_prefix":   hasPrefix,
		"role_prefix":  rolePrefix,
	}).Warn("tool emulation stream copied result block skipped")
}

func (p *StreamParser) processEmbeddedProtocolLine(line string) bool {
	before, tag, after, ok := cutFirstCompleteProtocolTagWithSettings(line, p.proto)
	if !ok || before == "" && after == "" {
		return false
	}
	if before != "" {
		p.emitProse(before)
	}
	p.processLogicalLine(tag)
	if p.failed {
		return true
	}
	if after != "" {
		p.processLogicalLine(after)
	}
	return true
}

func (p *StreamParser) processArgLine(line string) {
	if isArgCloseLineWithSettings(line, p.proto) {
		p.args[p.argName] = trimOneTrailingNewline(p.argBuf.String())
		p.argName = ""
		p.argBuf.Reset()
		p.state = streamStateTool
		return
	}
	p.argBuf.WriteString(line)
}

func (p *StreamParser) startTool(name string) {
	p.toolName = name
	p.args = map[string]string{}
	p.argName = ""
	p.argBuf.Reset()
	p.state = streamStateTool
	if p.events.OnToolCallStart != nil {
		p.events.OnToolCallStart(p.toolIndex, DeriveID(p.meta, p.toolIndex), name)
	}
}

func (p *StreamParser) finishTool() {
	args, err := p.argsRenderer.renderArgs(p.toolName, p.args)
	if err != nil {
		p.emitError(fmt.Errorf("toolemu stream: marshal arguments: %w", err))
		return
	}
	if p.events.OnArgsDelta != nil {
		p.events.OnArgsDelta(p.toolIndex, string(args))
	}
	if p.events.OnToolCallEnd != nil {
		p.events.OnToolCallEnd(p.toolIndex)
	}
	p.toolIndex++
	p.toolName = ""
	p.args = nil
	p.state = streamStateToolCalls
}

func (p *StreamParser) emitProse(s string) {
	if s == "" || p.events.OnProseDelta == nil {
		return
	}
	p.events.OnProseDelta(s)
}

func (p *StreamParser) emitError(err error) {
	if p.err == nil {
		p.err = err
	}
	p.logParseError(err, "tool emulation stream parse failed")
	p.emitProse(modelVisibleStreamParseError(err))
	p.failed = true
	p.pendingBytes = nil
	p.line.Reset()
	p.argBuf.Reset()
	p.state = streamStateProse
	if p.events.OnError != nil {
		p.events.OnError(err)
	}
}

func (p *StreamParser) emitModelOutputError(err error) {
	if p.pendingModelOutputErr == nil {
		p.pendingModelOutputErr = err
	}
	p.emitProse(modelVisibleStreamParseError(err))
	p.failed = true
	p.pendingBytes = nil
	p.line.Reset()
	p.argBuf.Reset()
	p.markdownFence = 0
	p.markdownFenceLen = 0
	p.state = streamStateProse
	if p.events.OnComplete != nil {
		p.events.OnComplete()
	}
}

func (p *StreamParser) flushPendingModelOutputLog() {
	if p.pendingModelOutputErr == nil || p.pendingModelOutputLogged {
		return
	}
	p.logParseError(p.pendingModelOutputErr, "tool emulation stream model output parse failed")
	p.pendingModelOutputLogged = true
}

func (p *StreamParser) logParseError(err error, message string) {
	log.WithError(err).WithFields(log.Fields{
		"fence_token": p.proto.token,
		"tag_group":   p.proto.tags,
		"provider":    p.meta.Provider,
		"model":       p.meta.Model,
		"response_id": p.meta.ResponseID,
		"raw_text":    p.rawText.String(),
	}).Error(message)
}

func invalidStreamProtocolLineError(line, token string) error {
	return fmt.Errorf("toolemu stream: invalid protocol line %s (expected fence token %q)", quoteStreamProtocolLine(line), token)
}

func embeddedStreamProtocolTagError(line string) error {
	return fmt.Errorf("toolemu stream: protocol tag must be alone on its line: %s", quoteStreamProtocolLine(line))
}

func modelVisibleStreamParseError(err error) string {
	return "\nTool emulation stream parse error: " + sanitizeModelVisibleProtocolText(err.Error()) + "\nPlease retry using the configured raw tool protocol.\n"
}

func sanitizeModelVisibleProtocolText(s string) string {
	return strings.NewReplacer("<", "[", ">", "]").Replace(s)
}

func quoteStreamProtocolLine(line string) string {
	trimmed := strings.TrimSpace(line)
	const maxRunes = 200
	runes := []rune(trimmed)
	if len(runes) > maxRunes {
		trimmed = string(runes[:maxRunes]) + "..."
	}
	return strconv.Quote(trimmed)
}

// validUTF8Prefix returns the length of the longest valid UTF-8 prefix of b.
func validUTF8Prefix(b []byte) int {
	n := len(b)
	if n == 0 || utf8.Valid(b) {
		return n
	}
	// An incomplete rune is at most 3 trailing bytes; check 1-3 from the end.
	for i := 1; i <= 3 && i < n; i++ {
		if utf8.Valid(b[:n-i]) {
			return n - i
		}
	}
	return 0
}
