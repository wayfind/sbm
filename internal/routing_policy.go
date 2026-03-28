package internal

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// RoutingPolicyPath returns the path to the routing policy file.
func RoutingPolicyPath() string { return filepath.Join(DeployDir, "routing-policy.json") }

// RoutingPolicy defines domain/rule_set based routing rules.
// Lives in /etc/sing-box/routing-policy.json, editable without recompiling sbm.
type RoutingPolicy struct {
	// RuleSets maps tag → remote URL (e.g. "geosite-cn" → "https://.../*.srs")
	RuleSets map[string]string `json:"rule_sets"`
	// Rules are evaluated in order; first match wins.
	Rules []PolicyRule `json:"rules"`
}

// PolicyRule maps traffic matchers to an outbound.
// Supported outbound values: "direct", "residential", "gia-auto".
type PolicyRule struct {
	RuleSet      jsonStringOrSlice `json:"rule_set,omitempty"`
	Domain       []string          `json:"domain,omitempty"`
	DomainSuffix []string          `json:"domain_suffix,omitempty"`
	Outbound     string            `json:"outbound"`
}

// jsonStringOrSlice handles JSON that can be either "foo" or ["foo","bar"].
type jsonStringOrSlice []string

func (s *jsonStringOrSlice) UnmarshalJSON(data []byte) error {
	// Try string first
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = []string{single}
		return nil
	}
	// Then try array
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*s = arr
	return nil
}

func (s jsonStringOrSlice) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return json.Marshal([]string(nil)) // omitempty on the field handles nil; explicit empty → []
	}
	if len(s) == 1 {
		return json.Marshal(s[0])
	}
	return json.Marshal([]string(s))
}

// LoadRoutingPolicy reads the policy file. Returns default if file doesn't exist.
func LoadRoutingPolicy() *RoutingPolicy {
	data, err := os.ReadFile(RoutingPolicyPath())
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[routing-policy] read error, using default: %v", err)
		}
		return DefaultRoutingPolicy()
	}
	var p RoutingPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("[routing-policy] parse error, using default: %v", err)
		return DefaultRoutingPolicy()
	}
	return &p
}

// DefaultRoutingPolicy returns the built-in routing policy (matches pre-policy behavior).
func DefaultRoutingPolicy() *RoutingPolicy {
	return &RoutingPolicy{
		RuleSets: map[string]string{
			"geosite-cn":     RuleSetGeositeCN,
			"geoip-cn":       RuleSetGeoipCN,
			"geosite-google": "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@sing/geo/geosite/google.srs",
		},
		Rules: []PolicyRule{
			{RuleSet: jsonStringOrSlice{"geoip-cn", "geosite-cn"}, Outbound: "direct"},
			{RuleSet: jsonStringOrSlice{"geosite-google"}, Outbound: "residential"},
			{DomainSuffix: []string{
				"openai.com", "chatgpt.com",
				"anthropic.com", "claude.ai",
				"meta.ai", "ai.meta.com",
			}, Outbound: "residential"},
			{Domain: []string{
				"gemini.google.com", "aistudio.google.com", "ai.google.dev",
			}, Outbound: "residential"},
			{DomainSuffix: []string{
				"generativelanguage.googleapis.com",
			}, Outbound: "residential"},
		},
	}
}

// BuildRuleSetDefs generates sing-box rule_set definitions from the policy.
// Output is sorted by tag for deterministic config generation.
func (p *RoutingPolicy) BuildRuleSetDefs() []map[string]interface{} {
	tags := make([]string, 0, len(p.RuleSets))
	for tag := range p.RuleSets {
		tags = append(tags, tag)
	}
	sort.Strings(tags)

	result := make([]map[string]interface{}, 0, len(tags))
	for _, tag := range tags {
		url := p.RuleSets[tag]
		rs := map[string]interface{}{"tag": tag, "format": "binary"}
		localPath := filepath.Join(RuleSetLocalDir, tag+".srs")
		if FileExists(localPath) {
			rs["type"] = "local"
			rs["path"] = localPath
		} else {
			rs["type"] = "remote"
			rs["url"] = url
		}
		result = append(result, rs)
	}
	return result
}

// BuildPolicyRules generates sing-box route rules from the policy.
// Rules referencing "residential" outbound are only included when hasExits is true.
func (p *RoutingPolicy) BuildPolicyRules(hasExits bool) []interface{} {
	var rules []interface{}
	for _, r := range p.Rules {
		if r.Outbound == "residential" && !hasExits {
			continue
		}
		rule := map[string]interface{}{
			"action":   "route",
			"outbound": r.Outbound,
		}
		if len(r.RuleSet) == 1 {
			rule["rule_set"] = r.RuleSet[0]
		} else if len(r.RuleSet) > 1 {
			rule["rule_set"] = []string(r.RuleSet)
		}
		if len(r.Domain) > 0 {
			rule["domain"] = r.Domain
		}
		if len(r.DomainSuffix) > 0 {
			rule["domain_suffix"] = r.DomainSuffix
		}
		rules = append(rules, rule)
	}
	return rules
}

// SaveRoutingPolicy writes the policy to disk.
func SaveRoutingPolicy(p *RoutingPolicy) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(RoutingPolicyPath(), data, 0644)
}
