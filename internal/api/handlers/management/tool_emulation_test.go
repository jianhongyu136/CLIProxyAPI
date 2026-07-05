package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
)

func TestGetToolEmulationStatusAppliesDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{
		Enabled: true,
		Rules: []toolemu.ToolEmulationRule{{
			Provider:     "openai-compatibility",
			Models:       []string{"gpt-test"},
			ModelAliases: []string{"alias-test"},
		}},
	}}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetToolEmulationStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got["enabled"] != true {
		t.Fatalf("enabled = %#v, want true", got["enabled"])
	}
	if got["parse-retry"].(float64) != float64(toolemu.DefaultParseRetry) {
		t.Fatalf("parse-retry = %#v, want %d", got["parse-retry"], toolemu.DefaultParseRetry)
	}
	if got["on-parse-failure"] != "parse_failed_to_content" {
		t.Fatalf("on-parse-failure = %#v", got["on-parse-failure"])
	}
	if got["fence-token"] != toolemu.DefaultFenceToken {
		t.Fatalf("fence-token = %#v, want %q", got["fence-token"], toolemu.DefaultFenceToken)
	}
	assertToolEmulationTagGroup(t, got["tag-group"], toolemu.DefaultToolEmulationTagGroup)
	rules := got["rules"].([]any)
	first := rules[0].(map[string]any)
	if first["provider"] != "openai-compatibility" {
		t.Fatalf("provider = %#v", first["provider"])
	}
}

func TestGetToolEmulationStatusReportsCustomTagGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tags := toolemu.ToolEmulationTagGroup{
		Tool:   "X_TOOL",
		Arg:    "X_ARG",
		Result: "X_RESULT",
	}
	h := &Handler{cfg: &config.Config{ToolEmulation: toolemu.ToolEmulationConfig{
		TagGroup: tags,
	}}}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetToolEmulationStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertToolEmulationTagGroup(t, got["tag-group"], tags)
}

func TestGetToolEmulationStatusNilConfigReportsDefaultTagGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.GetToolEmulationStatus(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	assertToolEmulationTagGroup(t, got["tag-group"], toolemu.DefaultToolEmulationTagGroup)
}

func assertToolEmulationTagGroup(t *testing.T, raw any, want toolemu.ToolEmulationTagGroup) {
	t.Helper()
	got, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("tag-group = %#v, want object", raw)
	}
	if _, ok := got["tool-calls"]; ok {
		t.Fatalf("tag-group.tool-calls must not be present: %#v", got["tool-calls"])
	}
	if got["tool"] != want.Tool {
		t.Fatalf("tag-group.tool = %#v, want %q", got["tool"], want.Tool)
	}
	if got["arg"] != want.Arg {
		t.Fatalf("tag-group.arg = %#v, want %q", got["arg"], want.Arg)
	}
	if got["result"] != want.Result {
		t.Fatalf("tag-group.result = %#v, want %q", got["result"], want.Result)
	}
}
