package internal

import "os"

// AI services that require residential IP exit (住宅IP出口).
var AIResidentialDomains = []string{
	// OpenAI
	"openai.com",
	"chat.openai.com",
	"api.openai.com",
	"platform.openai.com",
	"auth0.openai.com",
	"chatgpt.com",
	// Anthropic
	"anthropic.com",
	"claude.ai",
	"api.anthropic.com",
	"console.anthropic.com",
	// Google AI
	"gemini.google.com",
	"aistudio.google.com",
	"generativelanguage.googleapis.com",
	"ai.google.dev",
	// Meta AI
	"meta.ai",
	"ai.meta.com",
}

// Rule set URLs — using jsdelivr CDN mirror (accessible from China).
const (
	RuleSetGeositeCN     = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-cn.srs"
	RuleSetGeositeNotCN  = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-!cn.srs"
	RuleSetGeoipCN       = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geoip/cn.srs"
	RuleSetGeositeGoogle = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/google.srs"

	// Local rule set paths (pre-downloaded to avoid startup blocking)
	RuleSetLocalDir = "/etc/sing-box/rules"
)

// BuildRuleSetDefs returns rule_set definitions.
// Uses local files if they exist (avoids sing-box startup blocking on download),
// falls back to remote URLs.
func BuildRuleSetDefs() []map[string]interface{} {
	type rsDef struct {
		tag       string
		localFile string
		remoteURL string
	}
	defs := []rsDef{
		{"geosite-cn", RuleSetLocalDir + "/geosite-cn.srs", RuleSetGeositeCN},
		{"geoip-cn", RuleSetLocalDir + "/geoip-cn.srs", RuleSetGeoipCN},
		{"geosite-google", RuleSetLocalDir + "/geosite-google.srs", RuleSetGeositeGoogle},
	}

	var result []map[string]interface{}
	for _, d := range defs {
		rs := map[string]interface{}{"tag": d.tag, "format": "binary"}
		if fileExists(d.localFile) {
			rs["type"] = "local"
			rs["path"] = d.localFile
		} else {
			rs["type"] = "remote"
			rs["url"] = d.remoteURL
		}
		result = append(result, rs)
	}
	return result
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
