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
	RuleSetGeositeCN    = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-cn.srs"
	RuleSetGeositeNotCN = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/geolocation-!cn.srs"
	RuleSetGeoipCN      = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geoip/cn.srs"

	// Local rule set paths (pre-downloaded to avoid startup blocking)
	RuleSetLocalDir = "/etc/sing-box/rules"
)

// BuildRuleSetDefs returns rule_set definitions.
// Uses local files if they exist (avoids sing-box startup blocking on download),
// falls back to remote URLs.
func BuildRuleSetDefs() []map[string]interface{} {
	geositePath := RuleSetLocalDir + "/geosite-cn.srs"
	geoipPath := RuleSetLocalDir + "/geoip-cn.srs"

	geosite := map[string]interface{}{"tag": "geosite-cn", "format": "binary"}
	geoip := map[string]interface{}{"tag": "geoip-cn", "format": "binary"}

	if fileExists(geositePath) {
		geosite["type"] = "local"
		geosite["path"] = geositePath
	} else {
		geosite["type"] = "remote"
		geosite["url"] = RuleSetGeositeCN
	}

	if fileExists(geoipPath) {
		geoip["type"] = "local"
		geoip["path"] = geoipPath
	} else {
		geoip["type"] = "remote"
		geoip["url"] = RuleSetGeoipCN
	}

	return []map[string]interface{}{geosite, geoip}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
