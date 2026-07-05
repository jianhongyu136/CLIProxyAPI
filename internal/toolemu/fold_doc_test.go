package toolemu

import (
	"bytes"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestFoldRequestDocPreservesPreparedMutations(t *testing.T) {
	cases := []struct {
		name          string
		shape         UpstreamShape
		payload       string
		userTextPath  string
		injectionPath string
	}{
		{
			name: "chat", shape: ShapeOpenAIChat,
			payload:      `{"messages":[{"role":"user","content":"original"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`,
			userTextPath: "messages.0.content", injectionPath: "messages.0.content",
		},
		{
			name: "responses", shape: ShapeOpenAIResponses,
			payload:      `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"original"}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`,
			userTextPath: "input.0.content.0.text", injectionPath: "input.0.content.0.text",
		},
		{
			name: "claude", shape: ShapeClaudeMessages,
			payload:      `{"messages":[{"role":"user","content":[{"type":"text","text":"original"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`,
			userTextPath: "messages.0.content.0.text", injectionPath: "messages.0.content.0.text",
		},
		{
			name: "gemini", shape: ShapeGeminiGenerateContent,
			payload:      `{"contents":[{"role":"user","parts":[{"text":"original"}]}],"tools":[{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}]}`,
			userTextPath: "contents.0.parts.0.text", injectionPath: "systemInstruction.parts.0.text",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.payload)
			original := bytes.Clone(input)
			doc := ojson.NewDoc(input)
			if err := doc.Set(tc.userTextPath, "prepared user text"); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set("metadata.prepared", true); err != nil {
				t.Fatal(err)
			}
			if !HasToolArtifactsDoc(doc) {
				t.Fatal("prepared document should activate tool emulation")
			}
			tools, err := ExtractToolSpecsDoc(doc, tc.shape)
			if err != nil || len(tools) != 1 || tools[0].Name != "lookup" {
				t.Fatalf("prepared tools = %+v, error = %v", tools, err)
			}
			if err := FoldRequestDoc(doc, FoldOpts{Shape: tc.shape}); err != nil {
				t.Fatal(err)
			}
			if doc.Get("tools").Exists() || !doc.Get("metadata.prepared").Bool() {
				t.Fatal("folding lost a prepared mutation or preserved native tools")
			}
			if !strings.Contains(doc.Get(tc.injectionPath).String(), "<tools_doc>") {
				t.Fatal("tool documentation was not injected")
			}
			if !strings.Contains(string(doc.Bytes()), "prepared user text") {
				t.Fatal("folding used the stale original payload")
			}
			if !bytes.Equal(input, original) {
				t.Fatal("folding mutated caller-owned input")
			}
		})
	}
}
