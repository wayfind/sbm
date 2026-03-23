package internal

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

// Rule set URLs for remote binary rule sets.
const (
	RuleSetGeositeCN    = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-cn.srs"
	RuleSetGeositeNotCN = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-geolocation-!cn.srs"
	RuleSetGeoipCN      = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs"
)
