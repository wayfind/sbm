package internal

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"time"
)

// FailoverConfig describes BWG-enabled transits for the entry node.
type FailoverConfig struct {
	Transits []FailoverTransit `json:"transits"`
}

type FailoverTransit struct {
	Name   string     `json:"name"`   // matches transit tag suffix
	BWG    *BWGConfig `json:"bwg"`    // BWG API credentials
}

// TransitBWStatus holds bandwidth status for a single transit.
type TransitBWStatus struct {
	Name         string
	UsedBW       int64
	MonthlyBW    int64
	RemainingPct float64
	Progress     float64 // used / monthly (0..1), for balancing
	NextReset    time.Time
	Available    bool // false if API call failed or bw exhausted
}

// CheckAllTransitBandwidth queries BWG API for all transits (one call per transit).
func CheckAllTransitBandwidth(transits []FailoverTransit) []TransitBWStatus {
	results := make([]TransitBWStatus, len(transits))
	for i, t := range transits {
		results[i].Name = t.Name
		if t.BWG == nil {
			results[i].Available = true // no BWG tracking, assume available
			continue
		}

		info, err := GetBWGServiceInfo(t.BWG.VEID, t.BWG.APIKey)
		if err != nil {
			log.Printf("[failover] BWG check %s failed: %v", t.Name, err)
			results[i].Available = true // assume available on API error
			continue
		}

		monthlyBW := int64(float64(info.PlanMonthlyData) * info.MonthlyDataMultiplier)
		if monthlyBW == 0 {
			monthlyBW = info.PlanMonthlyData
		}

		remaining := float64(0)
		if monthlyBW > 0 {
			remaining = float64(monthlyBW-info.DataCounter) / float64(monthlyBW) * 100
			if remaining < 0 {
				remaining = 0
			}
		}

		results[i].UsedBW = info.DataCounter
		results[i].MonthlyBW = monthlyBW
		results[i].RemainingPct = remaining
		results[i].NextReset = time.Unix(info.DataNextReset, 0)

		if monthlyBW > 0 {
			results[i].Progress = float64(info.DataCounter) / float64(monthlyBW)
		}

		// Exhausted if remaining < 5%
		results[i].Available = remaining >= 5.0
	}
	return results
}

// BalancedTransitOrder returns transit names ordered by usage progress (least used first).
// Exhausted transits (remaining < 5%) are excluded.
func BalancedTransitOrder(statuses []TransitBWStatus) []string {
	// Filter available
	available := make([]TransitBWStatus, 0)
	for _, s := range statuses {
		if s.Available {
			available = append(available, s)
		}
	}

	// Sort by progress ascending (least used first)
	sort.Slice(available, func(i, j int) bool {
		return available[i].Progress < available[j].Progress
	})

	names := make([]string, len(available))
	for i, s := range available {
		names[i] = s.Name
	}
	return names
}

// RebalanceEntryConfig reads the current entry config, reorders the urltest outbound
// group based on transit bandwidth usage, and saves + reloads.
func RebalanceEntryConfig(failoverCfg *FailoverConfig) error {
	statuses := CheckAllTransitBandwidth(failoverCfg.Transits)

	ordered := BalancedTransitOrder(statuses)
	if len(ordered) == 0 {
		log.Println("[failover] WARNING: all transits exhausted!")
		return fmt.Errorf("all transits exhausted")
	}

	// Log status
	for _, s := range statuses {
		status := "OK"
		if !s.Available {
			status = "EXHAUSTED"
		}
		log.Printf("[failover] %s: used=%s / total=%s remaining=%.1f%% %s",
			s.Name, FormatBytes(s.UsedBW), FormatBytes(s.MonthlyBW), s.RemainingPct, status)
	}
	log.Printf("[failover] balanced order: %v", ordered)

	// Read current config
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Find and update gia-auto urltest outbound
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return fmt.Errorf("config outbounds not found")
	}

	updated := false
	for i, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		if obMap["tag"] == "gia-auto" && obMap["type"] == "urltest" {
			// Rebuild outbounds list with transit- prefix
			tags := make([]interface{}, len(ordered))
			for j, name := range ordered {
				tags[j] = "transit-" + name
			}
			obMap["outbounds"] = tags
			outbounds[i] = obMap
			updated = true
			break
		}
	}

	if !updated {
		return fmt.Errorf("gia-auto urltest outbound not found in config")
	}

	config["outbounds"] = outbounds

	// Validate with sing-box check
	newData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Write to temp file for validation
	tmpFile := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmpFile, newData, 0600); err != nil {
		return fmt.Errorf("write temp config: %w", err)
	}
	defer os.Remove(tmpFile)

	if err := exec.Command("sing-box", "check", "-c", tmpFile).Run(); err != nil {
		return fmt.Errorf("config validation failed: %w", err)
	}

	// Atomic replace
	if err := os.WriteFile(ConfigPath(), newData, 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Reload sing-box via systemctl (sends SIGHUP, keeps connections alive)
	log.Println("[failover] config updated, reloading sing-box")
	if err := SystemctlOp("reload", "sing-box"); err != nil {
		log.Printf("[failover] reload failed, falling back to restart: %v", err)
		return SystemctlOp("restart", "sing-box")
	}
	return nil
}

// StartFailoverLoop runs periodic bandwidth balancing on the entry node.
// It checks every hour and rebalances if needed.
func StartFailoverLoop(failoverCfg *FailoverConfig, interval time.Duration) {
	if len(failoverCfg.Transits) == 0 {
		return
	}

	go func() {
		// Initial check after 1 minute
		time.Sleep(1 * time.Minute)
		if err := RebalanceEntryConfig(failoverCfg); err != nil {
			log.Printf("[failover] initial rebalance error: %v", err)
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := RebalanceEntryConfig(failoverCfg); err != nil {
				log.Printf("[failover] rebalance error: %v", err)
			}
		}
	}()

	log.Printf("[failover] bandwidth balancer started, interval=%s, transits=%d",
		interval, len(failoverCfg.Transits))
}
