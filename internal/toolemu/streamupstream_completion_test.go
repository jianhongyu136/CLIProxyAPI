package toolemu

import (
	"context"
	"strings"
	"testing"
)

func TestUpstreamPumpClaudeCompletionCallbackRequiresValidMessageStop(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{name: "valid", data: `{"type":"message_stop"}`, want: true},
		{name: "malformed", data: `{"type":"message_stop"`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := false
			parser := NewStreamParserWithTagGroup(StreamEvents{}, UpstreamMeta{}, DefaultFenceToken, ToolEmulationTagGroup{})
			pump := UpstreamPump{
				Reader: strings.NewReader("event: message_stop\ndata: " + tc.data + "\n\n"),
				Shape:  ShapeClaudeMessages, Parser: parser,
				OnCompleted: func() { completed = true },
			}
			meta, err := pump.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if completed != tc.want || meta.StreamCompleted != tc.want {
				t.Fatalf("callback = %v, completed = %v, want %v", completed, meta.StreamCompleted, tc.want)
			}
		})
	}
}
