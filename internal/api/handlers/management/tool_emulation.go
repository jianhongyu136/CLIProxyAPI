package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/toolemu"
)

// GetToolEmulationStatus returns the active tool-emulation snapshot for diagnostics.
// The response includes the effective rules, parse-retry count, on-parse-failure
// mode, fence token, and raw protocol tag group after defaults are applied.
func (h *Handler) GetToolEmulationStatus(c *gin.Context) {
	h.mu.Lock()
	cfg := h.cfg
	h.mu.Unlock()

	if cfg == nil {
		c.JSON(http.StatusOK, gin.H{
			"enabled":          false,
			"rules":            []gin.H{},
			"parse-retry":      0,
			"on-parse-failure": "",
			"fence-token":      toolemu.DefaultFenceToken,
			"tag-group":        toolEmulationTagGroupJSON(toolemu.DefaultToolEmulationTagGroup),
		})
		return
	}
	eff := cfg.ToolEmulation.DefaultsApplied()
	rules := make([]gin.H, 0, len(eff.Rules))
	for _, r := range eff.Rules {
		rules = append(rules, gin.H{
			"provider":      r.Provider,
			"models":        append([]string(nil), r.Models...),
			"model-aliases": append([]string(nil), r.ModelAliases...),
		})
	}
	parseRetry := eff.ParseRetry
	if parseRetry == 0 && cfg.ToolEmulation.ParseRetry == 0 && cfg.ToolEmulation.OnParseFailure == "" {
		parseRetry = toolemu.DefaultParseRetry
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":          eff.Enabled,
		"rules":            rules,
		"parse-retry":      parseRetry,
		"on-parse-failure": eff.OnParseFailure,
		"fence-token":      eff.FenceToken,
		"tag-group":        toolEmulationTagGroupJSON(eff.TagGroup),
	})
}

func toolEmulationTagGroupJSON(tags toolemu.ToolEmulationTagGroup) gin.H {
	return gin.H{
		"tool":   tags.Tool,
		"arg":    tags.Arg,
		"result": tags.Result,
	}
}
