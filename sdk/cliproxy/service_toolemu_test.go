package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestServiceApplyConfigUpdateRefreshesToolEmulationMatcher(t *testing.T) {
	toolemu.Default.Replace(toolemu.ToolEmulationConfig{})
	t.Cleanup(func() { toolemu.Default.Replace(toolemu.ToolEmulationConfig{}) })

	svc := &Service{}
	updated := svc.applyConfigUpdateWithAuthSynthesis(context.Background(), &config.Config{
		ToolEmulation: toolemu.ToolEmulationConfig{
			Enabled: true,
			Rules: []toolemu.ToolEmulationRule{{
				Provider: "openai-compatibility",
				Models:   []string{"gpt-test"},
			}},
		},
	}, false)
	if !updated {
		t.Fatal("service config update was not applied")
	}

	if !toolemu.Default.IsEnabled("openai-compatibility", "gpt-test", "") {
		t.Fatal("global toolemu matcher was not refreshed from service config update")
	}
}
