package internal

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
)

// ReloadRoutingPolicy re-reads routing-policy.json, rebuilds the route section
// of the entry config, and restarts sing-box. Only updates policy-driven rules
// (rule_sets, domain/rule_set routing); preserves per-exit and socks5 rules.
func ReloadRoutingPolicy() error {
	meta, err := LoadMetadata()
	if err != nil {
		return fmt.Errorf("load metadata: %w", err)
	}
	if meta.Role != RoleEntry {
		return fmt.Errorf("reload only supported on entry nodes (current role: %s)", meta.Role)
	}

	ConfigMu.Lock()
	defer ConfigMu.Unlock()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Extract current exits from outbounds (tags starting with "exit-")
	exits := extractExitInfoFromConfig(config)

	// Preserve existing per-exit inbound rules and socks5 rules
	socks5Rule := extractSocks5Rules(config)
	var perExitRules []interface{}
	if route, ok := config["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			for _, r := range rules {
				rm, ok := r.(map[string]interface{})
				if !ok {
					continue
				}
				// Per-exit rules have "inbound" with socks-exit-*/vless-exit-*/hy2-exit-*
				if inbound, ok := rm["inbound"]; ok {
					if ibs, ok := inbound.([]interface{}); ok && len(ibs) > 0 {
						if s, ok := ibs[0].(string); ok && isPerExitTag(s) {
							perExitRules = append(perExitRules, r)
						}
					}
				}
			}
		}
	}

	// Rebuild route from policy
	route := BuildEntryRoute(exits, 0, nil)

	// Re-insert preserved per-exit and socks5 rules
	SplicePerExitRules(route, perExitRules, socks5Rule)

	config["route"] = route

	newData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	log.Println("[reload] routing policy applied, restarting sing-box")
	return validateAndApplyConfig(newData, "[reload]")
}

// extractExitInfoFromConfig extracts ExitInfo from current config outbounds.
// Only needs the Name field (for per-exit rule matching in BuildEntryRoute).
func extractExitInfoFromConfig(config map[string]interface{}) []ExitInfo {
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return nil
	}
	var exits []ExitInfo
	for _, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := obMap["tag"].(string)
		if len(tag) > 5 && tag[:5] == "exit-" {
			exits = append(exits, ExitInfo{TransitInfo: TransitInfo{Name: tag[5:]}})
		}
	}
	return exits
}
