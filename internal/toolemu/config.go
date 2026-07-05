package toolemu

import log "github.com/sirupsen/logrus"

// ToolEmulationConfig binds the top-level `tool-emulation` block in config.yaml.
type ToolEmulationConfig struct {
	Enabled        bool                  `yaml:"enabled" json:"enabled"`
	Rules          []ToolEmulationRule   `yaml:"rules" json:"rules"`
	ParseRetry     int                   `yaml:"parse-retry" json:"parse-retry"`
	OnParseFailure string                `yaml:"on-parse-failure" json:"on-parse-failure"`
	FenceToken     string                `yaml:"fence-token" json:"fence-token"`
	TagGroup       ToolEmulationTagGroup `yaml:"tag-group" json:"tag-group"`
}

// ToolEmulationRule is a single first-match-wins gating rule.
type ToolEmulationRule struct {
	Provider     string   `yaml:"provider" json:"provider"`
	Models       []string `yaml:"models" json:"models"`
	ModelAliases []string `yaml:"model-aliases" json:"model-aliases"`
}

// ToolEmulationTagGroup defines raw protocol tag names used in model output.
type ToolEmulationTagGroup struct {
	Tool   string `yaml:"tool" json:"tool"`
	Arg    string `yaml:"arg" json:"arg"`
	Result string `yaml:"result" json:"result"`
}

// DefaultsApplied returns a copy with defaults filled in for unset fields.
func (c ToolEmulationConfig) DefaultsApplied() ToolEmulationConfig {
	out := c
	if out.OnParseFailure == "" {
		out.OnParseFailure = "parse_failed_to_content"
	}
	if out.FenceToken == "" {
		out.FenceToken = DefaultFenceToken
	} else if !validFenceToken(out.FenceToken) {
		log.WithField("fence_token", out.FenceToken).Warn("toolemu: invalid fence-token; using default")
		out.FenceToken = DefaultFenceToken
	}
	out.TagGroup = defaultTagGroup(out.TagGroup)
	return out
}

const DefaultFenceToken = "cpa9x7q2"

var DefaultToolEmulationTagGroup = ToolEmulationTagGroup{
	Tool:   "CPA_TC",
	Arg:    "CPA_TA",
	Result: "CPA_TR",
}

func defaultTagGroup(tags ToolEmulationTagGroup) ToolEmulationTagGroup {
	defaults := DefaultToolEmulationTagGroup
	out := tags
	if !validProtocolTagName(out.Tool) {
		if out.Tool != "" {
			log.WithField("tool_tag", out.Tool).Warn("toolemu: invalid tool tag; using default")
		}
		out.Tool = defaults.Tool
	}
	if !validProtocolTagName(out.Arg) {
		if out.Arg != "" {
			log.WithField("arg_tag", out.Arg).Warn("toolemu: invalid arg tag; using default")
		}
		out.Arg = defaults.Arg
	}
	if !validProtocolTagName(out.Result) {
		if out.Result != "" {
			log.WithField("result_tag", out.Result).Warn("toolemu: invalid result tag; using default")
		}
		out.Result = defaults.Result
	}
	return out
}

func validProtocolTagName(name string) bool {
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
		if c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func validFenceToken(token string) bool {
	if len(token) < 4 || len(token) > 32 {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// DefaultParseRetry is applied when the parse-retry field is absent.
const DefaultParseRetry = 1
