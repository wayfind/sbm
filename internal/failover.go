package internal

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"time"
)

// FailoverConfig describes BWG-enabled transits for the entry node.
type FailoverConfig struct {
	Transits []FailoverTransit `json:"transits"`
}

type FailoverTransit struct {
	Name string     `json:"name"` // matches transit tag suffix
	BWG  *BWGConfig `json:"bwg"`  // BWG API credentials
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

// =============================================================================
// Shared config rewrite: reorder gia-auto urltest outbound
// =============================================================================

// reorderGiaAuto updates the gia-auto urltest outbound with the given transit
// order. Skips restart if the order hasn't changed. Returns nil on no-op.
func reorderGiaAuto(ordered []string, logPrefix string) error {
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

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return fmt.Errorf("config outbounds not found")
	}

	// Find gia-auto and check if order actually changed
	for i, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		if obMap["tag"] != "gia-auto" || obMap["type"] != "urltest" {
			continue
		}

		// Compare current order with desired
		current, _ := obMap["outbounds"].([]interface{})
		if tagsEqual(current, ordered) {
			log.Printf("%s order unchanged, skipping restart", logPrefix)
			return nil
		}

		tags := make([]interface{}, len(ordered))
		for j, name := range ordered {
			tags[j] = "transit-" + name
		}
		obMap["outbounds"] = tags
		outbounds[i] = obMap

		config["outbounds"] = outbounds
		newData, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		return validateAndApplyConfig(newData, logPrefix)
	}

	return fmt.Errorf("gia-auto urltest outbound not found in config")
}

// tagsEqual checks if current outbound tags match the desired transit names.
func tagsEqual(current []interface{}, desired []string) bool {
	if len(current) != len(desired) {
		return false
	}
	for i, v := range current {
		s, ok := v.(string)
		if !ok || s != "transit-"+desired[i] {
			return false
		}
	}
	return true
}

// =============================================================================
// Failover.json-driven rebalancer (legacy path, for nodes without CF)
// =============================================================================

// RebalanceEntryConfig reads the current entry config, reorders the urltest outbound
// group based on transit bandwidth usage, and saves + reloads.
func RebalanceEntryConfig(failoverCfg *FailoverConfig) error {
	statuses := CheckAllTransitBandwidth(failoverCfg.Transits)

	ordered := BalancedTransitOrder(statuses)
	if len(ordered) == 0 {
		log.Println("[failover] WARNING: all transits exhausted!")
		return fmt.Errorf("all transits exhausted")
	}

	for _, s := range statuses {
		status := "OK"
		if !s.Available {
			status = "EXHAUSTED"
		}
		log.Printf("[failover] %s: used=%s / total=%s remaining=%.1f%% %s",
			s.Name, FormatBytes(s.UsedBW), FormatBytes(s.MonthlyBW), s.RemainingPct, status)
	}
	log.Printf("[failover] balanced order: %v", ordered)

	return reorderGiaAuto(ordered, "[failover]")
}

// StartFailoverLoop runs periodic bandwidth balancing on the entry node.
func StartFailoverLoop(failoverCfg *FailoverConfig, interval time.Duration) {
	if len(failoverCfg.Transits) == 0 {
		return
	}

	go func() {
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

// =============================================================================
// Topology-driven rebalancer (primary path, reads BWG status from CF)
// =============================================================================

// RebalanceFromTopology reads transit BWG status from CF topology and reorders
// the gia-auto urltest group accordingly. No failover.json needed.
func RebalanceFromTopology(cfClient *CFTopoClient) error {
	nodes, err := cfClient.GetAllNodes()
	if err != nil {
		return fmt.Errorf("cf get nodes: %w", err)
	}

	type transitBW struct {
		name    string
		bwg     *BWGStatus
		exhaust bool
	}
	var transits []transitBW
	for _, n := range nodes {
		if n.Role != RoleTransit {
			continue
		}
		if time.Since(n.ReportedAt) > StaleThreshold {
			continue
		}
		transits = append(transits, transitBW{
			name:    n.Name,
			bwg:     n.BWGStatus,
			exhaust: n.BWGStatus.IsExhausted(),
		})
	}

	if len(transits) == 0 {
		return fmt.Errorf("no alive transits in topology")
	}

	// Sort: exhausted last, then by BWG usage ascending (unlimited first)
	sort.SliceStable(transits, func(i, j int) bool {
		if transits[i].exhaust != transits[j].exhaust {
			return !transits[i].exhaust
		}
		return transits[i].bwg.UsedLessThan(transits[j].bwg)
	})

	// Build ordered list (exclude exhausted unless all exhausted)
	ordered := make([]string, 0, len(transits))
	for _, t := range transits {
		if !t.exhaust {
			ordered = append(ordered, t.name)
		}
	}
	if len(ordered) == 0 {
		log.Println("[topo-rebalance] WARNING: all transits exhausted, using all")
		for _, t := range transits {
			ordered = append(ordered, t.name)
		}
	}

	for _, t := range transits {
		status := "OK"
		if t.exhaust {
			status = "EXHAUSTED"
		}
		pctStr := "unlimited"
		if t.bwg.HasData() {
			pctStr = fmt.Sprintf("%.1f%% remaining", t.bwg.RemainingPct())
		}
		log.Printf("[topo-rebalance] %s: %s %s", t.name, pctStr, status)
	}
	log.Printf("[topo-rebalance] balanced order: %v", ordered)

	return reorderGiaAuto(ordered, "[topo-rebalance]")
}

// StartTopoRebalanceLoop runs periodic topology-driven bandwidth balancing.
func StartTopoRebalanceLoop(cfClient *CFTopoClient, interval time.Duration) {
	go func() {
		time.Sleep(2 * time.Minute) // let initial reconcile settle
		if err := RebalanceFromTopology(cfClient); err != nil {
			log.Printf("[topo-rebalance] initial error: %v", err)
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := RebalanceFromTopology(cfClient); err != nil {
				log.Printf("[topo-rebalance] error: %v", err)
			}
		}
	}()

	log.Printf("[topo-rebalance] started, interval=%s", interval)
}
