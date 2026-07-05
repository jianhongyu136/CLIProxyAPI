package toolemu

import (
	"encoding/json"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

type spyEvents struct {
	proseDeltas     []string
	reasoningDeltas []string
	toolStarts      []struct {
		index    int
		id, name string
	}
	argsDeltas []struct {
		index int
		delta string
	}
	toolEnds  []int
	completed bool
	err       error
}

func (s *spyEvents) events() StreamEvents {
	return StreamEvents{
		OnProseDelta:     func(d string) { s.proseDeltas = append(s.proseDeltas, d) },
		OnReasoningDelta: func(d string) { s.reasoningDeltas = append(s.reasoningDeltas, d) },
		OnToolCallStart: func(index int, id, name string) {
			s.toolStarts = append(s.toolStarts, struct {
				index    int
				id, name string
			}{index, id, name})
		},
		OnArgsDelta: func(index int, delta string) {
			s.argsDeltas = append(s.argsDeltas, struct {
				index int
				delta string
			}{index, delta})
		},
		OnToolCallEnd: func(index int) { s.toolEnds = append(s.toolEnds, index) },
		OnComplete:    func() { s.completed = true },
		OnError:       func(err error) { s.err = err },
	}
}

func (s *spyEvents) allProse() string {
	return strings.Join(s.proseDeltas, "")
}

func (s *spyEvents) allReasoning() string {
	return strings.Join(s.reasoningDeltas, "")
}

func (s *spyEvents) allArgs(index int) string {
	var sb strings.Builder
	for _, a := range s.argsDeltas {
		if a.index == index {
			sb.WriteString(a.delta)
		}
	}
	return sb.String()
}

func newRawStreamParser(spy *spyEvents) *StreamParser {
	return NewStreamParser(spy.events(), UpstreamMeta{ResponseID: "r", Provider: "p", Model: "m"}, testFence)
}

func newToolEmuLogHook(t *testing.T) *test.Hook {
	t.Helper()

	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() { hook.Reset() })
	return hook
}

func assertToolEmuErrorLog(t *testing.T, hook *test.Hook, message, rawText string) {
	t.Helper()

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.ErrorLevel && entry.Message == message {
			if _, ok := entry.Data["error"]; !ok {
				t.Fatalf("log entry %q missing error field", message)
			}
			if entry.Data["raw_text"] != rawText {
				t.Fatalf("raw_text = %q, want %q", entry.Data["raw_text"], rawText)
			}
			return
		}
	}
	t.Fatalf("missing error log %q; entries=%+v", message, hook.AllEntries())
}

func assertToolEmuWarnLog(t *testing.T, hook *test.Hook, message string, resultIndex int) {
	t.Helper()

	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && entry.Message == message {
			if entry.Data["result_index"] != resultIndex {
				t.Fatalf("result_index = %v, want %d", entry.Data["result_index"], resultIndex)
			}
			return
		}
	}
	t.Fatalf("missing warn log %q; entries=%+v", message, hook.AllEntries())
}

func TestStreamParser_ProseOnly(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("Hello world")
	p.Close()
	if spy.allProse() != "Hello world" {
		t.Fatalf("got prose %q", spy.allProse())
	}
	if len(spy.toolStarts) != 0 {
		t.Fatal("unexpected tool starts")
	}
	if !spy.completed {
		t.Fatal("OnComplete not called")
	}
}

func TestStreamParser_SkipsCopiedResultBlockOutsideToolCall(t *testing.T) {
	hook := newToolEmuLogHook(t)
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "before\nuser<CPA_TR|0|cpa9x7q2>\nsecret output\n</CPA_TR|cpa9x7q2>\nafter"
	p.Feed(rawText)
	p.Close()
	if spy.err != nil {
		t.Fatalf("copied result block must not call OnError: %v", spy.err)
	}
	if prose := spy.allProse(); prose != "before\nafter" {
		t.Fatalf("prose = %q, want copied result block skipped", prose)
	}
	if len(spy.toolStarts) != 0 {
		t.Fatalf("copied result block must not start tool calls: %+v", spy.toolStarts)
	}
	if !spy.completed {
		t.Fatal("OnComplete must be called after skipping copied result block")
	}
	assertToolEmuWarnLog(t, hook, "tool emulation stream copied result block skipped", 0)
}

func TestStreamParser_SingleRawToolCall(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("Hello\n")
	p.Feed("<CPA_TC|get_weather|cpa9x7q2>\n<CPA_TA|city|cpa9x7q2>\nNYC\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n")
	p.Close()
	if spy.allProse() != "Hello\n" {
		t.Fatalf("got prose %q", spy.allProse())
	}
	if len(spy.toolStarts) != 1 || spy.toolStarts[0].name != "get_weather" {
		t.Fatalf("tool starts: %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{"city":"NYC"}` {
		t.Fatalf("got args %q", spy.allArgs(0))
	}
	if len(spy.toolEnds) != 1 || spy.toolEnds[0] != 0 {
		t.Fatalf("tool ends: %+v", spy.toolEnds)
	}
}

func TestStreamParserWithTagGroupUsesCustomRawProtocolTags(t *testing.T) {
	spy := &spyEvents{}
	p := NewStreamParserWithTagGroup(
		spy.events(),
		UpstreamMeta{ResponseID: "r", Provider: "p", Model: "m"},
		"tok_9",
		ToolEmulationTagGroup{Tool: "X_TOOL", Arg: "X_ARG", Result: "X_RESULT"},
	)
	p.Feed("before\n<X_TOOL|get_weather|tok_9>\n<X_ARG|city|tok_9>\nTokyo\n</X_ARG|tok_9>\n</X_TOOL|tok_9>")
	p.Close()
	if spy.allProse() != "before\n" {
		t.Fatalf("prose = %q", spy.allProse())
	}
	if len(spy.toolStarts) != 1 || spy.toolStarts[0].name != "get_weather" {
		t.Fatalf("tool starts = %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{"city":"Tokyo"}` {
		t.Fatalf("args = %q", spy.allArgs(0))
	}
}

func TestStreamParser_FragmentedRawToolCall(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	fragments := []string{"He", "llo\n<CPA_", "TC|f|cpa", "9x7q2>\n<CPA_TA|x|c", "pa9x7q2>\n", "1\n</CPA_TA|cpa9x7q2>\n", "</CPA_TC|cpa9x7q2>"}
	for _, f := range fragments {
		p.Feed(f)
	}
	p.Close()
	if spy.allProse() != "Hello\n" {
		t.Fatalf("got prose %q", spy.allProse())
	}
	if len(spy.toolStarts) != 1 || spy.toolStarts[0].name != "f" {
		t.Fatalf("tool starts: %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{"x":"1"}` {
		t.Fatalf("got args %q", spy.allArgs(0))
	}
}

func TestStreamParser_EmitsStartOnToolOpenAndArgsAtClose(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|f|cpa9x7q2>\n")
	if len(spy.toolStarts) != 1 || spy.toolStarts[0].name != "f" {
		t.Fatalf("tool start after open = %+v", spy.toolStarts)
	}
	if len(spy.argsDeltas) != 0 || len(spy.toolEnds) != 0 {
		t.Fatalf("args/end must wait for close, args=%+v ends=%+v", spy.argsDeltas, spy.toolEnds)
	}
	p.Feed("<CPA_TA|x|cpa9x7q2>\nvalue\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n")
	if spy.allArgs(0) != `{"x":"value"}` {
		t.Fatalf("args = %q", spy.allArgs(0))
	}
	if len(spy.toolEnds) != 1 || spy.toolEnds[0] != 0 {
		t.Fatalf("tool end = %+v", spy.toolEnds)
	}
}

func TestStreamParser_MultipleRawToolCalls(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|a|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n<CPA_TC|b|cpa9x7q2>\n<CPA_TA|y|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()
	if len(spy.toolStarts) != 2 {
		t.Fatalf("expected 2 tool starts, got %d", len(spy.toolStarts))
	}
	if spy.toolStarts[0].name != "a" || spy.toolStarts[1].name != "b" {
		t.Fatalf("names: %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{}` || spy.allArgs(1) != `{"y":"2"}` {
		t.Fatalf("args: first=%q second=%q", spy.allArgs(0), spy.allArgs(1))
	}
}

func TestStreamParser_ParsesToolCallsAroundInterleavedProse(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("替换拖拽区域。<CPA_TC|edit|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n\n")
	p.Feed("Now the window control buttons. maximizeOrRestore needs custom logic.\n\n")
	p.Feed("<CPA_TC|edit|cpa9x7q2>\n<CPA_TA|filePath|cpa9x7q2>\nlib/shell.dart\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()

	if spy.err != nil {
		t.Fatal(spy.err)
	}
	if len(spy.toolStarts) != 2 || spy.toolStarts[0].name != "edit" || spy.toolStarts[1].name != "edit" {
		t.Fatalf("tool starts: %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{}` || spy.allArgs(1) != `{"filePath":"lib/shell.dart"}` {
		t.Fatalf("args: first=%q second=%q", spy.allArgs(0), spy.allArgs(1))
	}
	prose := spy.allProse()
	if !strings.Contains(prose, "替换拖拽区域。") || !strings.Contains(prose, "Now the window control buttons") {
		t.Fatalf("interleaved prose was not preserved: %q", spy.allProse())
	}
	if strings.Contains(prose, "Tool emulation stream parse error") {
		t.Fatalf("interleaved prose must not produce diagnostic: %q", prose)
	}
}

func TestStreamParser_WrapperlessToolCallSequence(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("Hello\n")
	p.Feed("<CPA_TC|a|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n")
	p.Feed("<CPA_TC|b|cpa9x7q2>\n<CPA_TA|y|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()
	if spy.err != nil {
		t.Fatal(spy.err)
	}
	if spy.allProse() != "Hello\n" {
		t.Fatalf("prose = %q", spy.allProse())
	}
	if len(spy.toolStarts) != 2 || spy.toolStarts[0].name != "a" || spy.toolStarts[1].name != "b" {
		t.Fatalf("tool starts: %+v", spy.toolStarts)
	}
	if spy.allArgs(0) != `{"x":"1"}` || spy.allArgs(1) != `{"y":"2"}` {
		t.Fatalf("args: first=%q second=%q", spy.allArgs(0), spy.allArgs(1))
	}
}

func TestStreamParser_MarkdownFencedCodeBlockKeepsProtocolTagsAsProse(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "before\n```go\n<CPA_TC|write_file|cpa9x7q2>\n<CPA_TA|content|cpa9x7q2>\nfmt.Println(1)\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n```\nafter"

	p.Feed(rawText)
	p.Close()

	if spy.err != nil {
		t.Fatalf("fenced code block must not produce parser error: %v", spy.err)
	}
	if len(spy.toolStarts) != 0 {
		t.Fatalf("fenced code block must not start tool calls: %+v", spy.toolStarts)
	}
	if spy.allProse() != rawText {
		t.Fatalf("prose = %q, want raw text", spy.allProse())
	}
}

func TestStreamParser_LongMarkdownFenceKeepsShorterInnerFenceAsProse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  string
		inner string
		close string
	}{
		{name: "backtick", open: "````markdown", inner: "```", close: "````"},
		{name: "tilde", open: "~~~~markdown", inner: "~~~", close: "~~~~"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyEvents{}
			p := newRawStreamParser(spy)
			rawText := "before\n" + tc.open + "\n" + tc.inner + "\n<CPA_TC|write_file|cpa9x7q2>\n</CPA_TC|cpa9x7q2>\n" + tc.close + "\nafter"

			p.Feed(rawText)
			p.Close()

			if spy.err != nil {
				t.Fatalf("long fenced code block must not produce parser error: %v", spy.err)
			}
			if len(spy.toolStarts) != 0 || spy.allProse() != rawText {
				t.Fatalf("long fenced code block must remain prose: starts=%+v prose=%q", spy.toolStarts, spy.allProse())
			}
		})
	}
}

func TestStreamParser_IndentedCodeBlockKeepsProtocolTagsAsProse(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "before\n    <CPA_TC|write_file|cpa9x7q2>\n    <CPA_TA|content|cpa9x7q2>\n    fmt.Println(1)\n    </CPA_TA|cpa9x7q2>\n    </CPA_TC|cpa9x7q2>\nafter"

	p.Feed(rawText)
	p.Close()

	if spy.err != nil {
		t.Fatalf("indented code block must not produce parser error: %v", spy.err)
	}
	if len(spy.toolStarts) != 0 {
		t.Fatalf("indented code block must not start tool calls: %+v", spy.toolStarts)
	}
	if spy.allProse() != rawText {
		t.Fatalf("prose = %q, want raw text", spy.allProse())
	}
}

func TestStreamParser_IndentedArgumentCloseIsRawContent(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|write_file|cpa9x7q2>\n<CPA_TA|content|cpa9x7q2>\nline one\n    </CPA_TA|cpa9x7q2>\nline two\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()

	if spy.err != nil {
		t.Fatal(spy.err)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(spy.allArgs(0)), &args); err != nil {
		t.Fatalf("args JSON invalid: %v: %q", err, spy.allArgs(0))
	}
	want := "line one\n    </CPA_TA|cpa9x7q2>\nline two"
	if args["content"] != want {
		t.Fatalf("content = %q, want %q", args["content"], want)
	}
}

func TestStreamParser_WrongFenceCompletesAsModelVisibleDiagnostic(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|f|wrongtok>\n</CPA_TC|wrongtok>")
	p.Close()
	if spy.err != nil {
		t.Fatalf("protocol diagnostics must not call OnError: %v", spy.err)
	}
	prose := spy.allProse()
	for _, want := range []string{"Tool emulation stream parse error", "invalid protocol line", `[CPA_TC|f|wrongtok]`, `expected fence token "cpa9x7q2"`} {
		if !strings.Contains(prose, want) {
			t.Fatalf("model-visible prose = %q, want substring %q", prose, want)
		}
	}
	for _, forbidden := range []string{"<CPA_TC|", "</CPA_TC|", "<CPA_TA|", "</CPA_TA|", "<CPA_TR|", "</CPA_TR|"} {
		if strings.Contains(prose, forbidden) {
			t.Fatalf("model-visible prose leaked protocol tag %q: %q", forbidden, prose)
		}
	}
	if !spy.completed {
		t.Fatal("OnComplete must be called after model-visible protocol diagnostic")
	}
}

func TestStreamParser_UnterminatedToolReportsError(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|f|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>")
	p.Close()
	if spy.err == nil || !strings.Contains(spy.err.Error(), "unterminated tool block") {
		t.Fatalf("err = %v, want unterminated tool block", spy.err)
	}
	if spy.completed {
		t.Fatal("OnComplete must not be called after unterminated tool")
	}
}

func TestStreamParser_OldJSONToolCallIsProse(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed(`<tool_call>{"name":"f","arguments":{}}</tool_call>`)
	p.Close()
	if len(spy.toolStarts) != 0 {
		t.Fatalf("old protocol should not parse: %+v", spy.toolStarts)
	}
	if !strings.Contains(spy.allProse(), "<tool_call>") {
		t.Fatalf("old protocol should remain prose: %q", spy.allProse())
	}
}

func TestStreamParser_RawArgumentsPreserveEscapingHeavyContent(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|write_file|cpa9x7q2>\n<CPA_TA|content|cpa9x7q2>\npath: C:\\Temp\\x\nregex: \\\\d+\nxml: <tag attr=\"v\">\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()
	var args map[string]string
	if err := json.Unmarshal([]byte(spy.allArgs(0)), &args); err != nil {
		t.Fatalf("args JSON invalid: %v: %q", err, spy.allArgs(0))
	}
	want := "path: C:\\Temp\\x\nregex: \\\\d+\nxml: <tag attr=\"v\">"
	if args["content"] != want {
		t.Fatalf("content = %q, want %q", args["content"], want)
	}
}

func TestStreamParser_UTF8Boundary(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	// "中" is 3 bytes: 0xE4 0xB8 0xAD, split across two Feed calls.
	b := []byte("中文")
	p.Feed(string(b[:1]))
	p.Feed(string(b[1:]))
	p.Close()
	if spy.allProse() != "中文" {
		t.Fatalf("got prose %q", spy.allProse())
	}
}

func TestStreamParser_DuplicateArgumentReportsError(t *testing.T) {
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	p.Feed("<CPA_TC|f|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>")
	p.Close()
	if spy.err == nil || !strings.Contains(spy.err.Error(), "duplicate argument") {
		t.Fatalf("err = %v, want duplicate argument", spy.err)
	}
}

func TestStreamParser_LogsFatalParseError(t *testing.T) {
	hook := newToolEmuLogHook(t)
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "<CPA_TC|f|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n1\n</CPA_TA|cpa9x7q2>\n<CPA_TA|x|cpa9x7q2>\n2\n</CPA_TA|cpa9x7q2>\n</CPA_TC|cpa9x7q2>"

	p.Feed(rawText)
	p.Close()

	if spy.err == nil {
		t.Fatal("expected stream parse error")
	}
	assertToolEmuErrorLog(t, hook, "tool emulation stream parse failed", rawText)
}

func TestStreamParser_LogsModelOutputProtocolError(t *testing.T) {
	hook := newToolEmuLogHook(t)
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "before\n<CPA_TC|f|wrongtok>\n"

	p.Feed("before\n")
	p.Feed("<CPA_TC|f|wrongtok>\n")
	p.Close()

	if spy.err != nil {
		t.Fatalf("model output protocol diagnostics must not call OnError: %v", spy.err)
	}
	assertToolEmuErrorLog(t, hook, "tool emulation stream model output parse failed", rawText)
}

func TestStreamParser_LogsModelOutputProtocolErrorIncludesPostDiagnosticDeltas(t *testing.T) {
	hook := newToolEmuLogHook(t)
	spy := &spyEvents{}
	p := newRawStreamParser(spy)
	rawText := "before\n<CPA_TC|f|wrongtok>\n"
	postDiagnostic := "after diagnostic\n"

	p.Feed("before\n")
	p.Feed("<CPA_TC|f|wrongtok>\n")
	p.Feed(postDiagnostic)
	p.Close()

	if spy.err != nil {
		t.Fatalf("model output protocol diagnostics must not call OnError: %v", spy.err)
	}
	assertToolEmuErrorLog(t, hook, "tool emulation stream model output parse failed", rawText+postDiagnostic)
}
