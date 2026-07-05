package toolemu_test

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
)

func TestStreamParser_ModelOutputProtocolErrorFormattedLogIncludesRawText(t *testing.T) {
	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() { hook.Reset() })

	var streamErr error
	p := toolemu.NewStreamParser(toolemu.StreamEvents{
		OnError: func(err error) { streamErr = err },
	}, toolemu.UpstreamMeta{ResponseID: "r", Provider: "claude", Model: "claude-opus-4.8"}, "cpa9x7q2")
	rawText := "before\n<CPA_TC|f|wrongtok>\n"

	p.Feed("before\n")
	p.Feed("<CPA_TC|f|wrongtok>\n")
	p.Close()

	if streamErr != nil {
		t.Fatalf("model output protocol diagnostics must not call OnError: %v", streamErr)
	}

	const message = "tool emulation stream model output parse failed"
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.ErrorLevel && entry.Message == message {
			formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
			if errFormat != nil {
				t.Fatalf("format log entry %q: %v", message, errFormat)
			}
			line := string(formatted)
			want := "raw model output:\n" + rawText
			if !strings.Contains(line, want) {
				t.Fatalf("formatted log = %q, want it to contain %q", line, want)
			}
			return
		}
	}
	t.Fatalf("missing error log %q; entries=%+v", message, hook.AllEntries())
}
