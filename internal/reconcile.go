package internal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConfigMu protects concurrent read-modify-write of sing-box config.json.
var ConfigMu sync.Mutex

// processStartTime is set at init for cold-start grace period detection.
var processStartTime = time.Now()

// StartReconcileLoop starts periodic reconciliation.
// Entry: auto-configure transit/exit outbounds from topology.
// Transit: auto-configure per-exit channels from topology + discover entry upstreams.
// Exit: discover transit upstreams for chain reporting.
func StartReconcileLoop(meta *Metadata, store *TopoStore, cfClient *CFTopoClient, db *sql.DB, interval time.Duration) {
	var reconcileFunc func()

	switch meta.Role {
	case RoleEntry:
		reconcileFunc = func() {
			downstreams, degraded := getTopoFromDualView(meta, store, cfClient)
			if err := ReconcileEntryConfig(meta, downstreams, db, degraded); err != nil {
				log.Printf("[reconcile] error: %v", err)
			}
		}
	case RoleTransit:
		reconcileFunc = func() {
			ReconcileUpstreams(meta, cfClient)
			downstreams, degraded := getTopoFromDualView(meta, store, cfClient)
			if degraded {
				log.Printf("[reconcile] degraded view, skipping transit config")
				return
			}
			if err := ReconcileTransitConfig(meta, downstreams); err != nil {
				log.Printf("[reconcile] error: %v", err)
			}
		}
	case RoleExit:
		// Exit only discovers upstreams; topology changes are rare — use longer interval.
		interval = 10 * time.Minute
		reconcileFunc = func() {
			ReconcileUpstreams(meta, cfClient)
		}
	default:
		log.Printf("[reconcile] role=%s, no reconcile needed", meta.Role)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[reconcile] goroutine panic: %v — reconciliation stopped", r)
			}
		}()

		time.Sleep(10 * time.Second)
		for {
			safeReconcile(reconcileFunc)
			time.Sleep(interval)
		}
	}()
	log.Printf("[reconcile] started, role=%s, interval=%s", meta.Role, interval)
}

// safeReconcile wraps a reconcile function with panic recovery.
func safeReconcile(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[reconcile] panic recovered: %v", r)
		}
	}()
	fn()
}

// getTopoFromDualView reads topology from CF (primary) and local store (fallback).
// Returns downstreams and whether the view is degraded (CF unavailable, local-only).
// Callers should avoid destructive changes (removing exits/ports) on degraded views.
func getTopoFromDualView(meta *Metadata, store *TopoStore, cfClient *CFTopoClient) ([]TopologyTree, bool) {
	localDownstreams := store.GetDownstreams()

	if cfClient == nil {
		return localDownstreams, true
	}

	cfNodes, err := cfClient.GetAllNodes()
	if err != nil {
		log.Printf("[reconcile] CF unavailable, using local view: %v", err)
		return localDownstreams, true
	}

	cfDownstreams := cfNodesToDownstreams(cfNodes, meta)

	// Consistency check between chain and CF control planes.
	// Only meaningful for Entry: Entry has concrete upstreams whose absence in CF
	// signals a real control-plane break. Transit's local store mixes exits and
	// entries (anyone can POST /report) while cfNodesToDownstreams filters to
	// transit+exit only — comparing them directly produces false positives.
	if meta.Role == RoleEntry && len(meta.Upstreams) > 0 {
		cfIDs := map[string]bool{}
		for _, n := range cfNodes {
			cfIDs[n.NodeID] = true
		}
		// Match upstreams by Name (unique), not Address — multiple nodes can
		// share an Address when a NAT'd entry reuses another node's cert domain.
		upstreamNames := map[string]bool{}
		for _, u := range meta.Upstreams {
			upstreamNames[u.Name] = true
		}
		// ALERT: a configured upstream (transit) surfaces in CF but never reached us
		// through chain → data-plane break between this entry and that transit.
		localIDs := map[string]bool{}
		for _, d := range localDownstreams {
			localIDs[d.Self.NodeID] = true
		}
		for _, n := range cfNodes {
			if n.NodeID == meta.NodeID {
				continue
			}
			if upstreamNames[n.Name] && !localIDs[n.NodeID] {
				// Cold-start grace: skip ALERT for 10min after process start.
				// Local store is empty until chain reports arrive (~5min).
				if time.Since(processStartTime) < 10*time.Minute {
					continue
				}
				log.Printf("[reconcile] ALERT: upstream %s (%s) in CF but not in chain → data-plane break?",
					n.Name, n.Address)
			}
		}
		// WARN: chain reports a node that CF has no record of → CF channel issue.
		for _, d := range localDownstreams {
			if !cfIDs[d.Self.NodeID] {
				log.Printf("[reconcile] WARN: node %s in chain but not in CF → CF channel issue?", d.Self.Name)
			}
		}
	}

	return cfDownstreams, false
}

// cfNodesToDownstreams converts flat CF node list into topology trees for Entry.
// Every exit is placed under every transit (full-mesh: any transit can reach any exit via detour).
func cfNodesToDownstreams(nodes []NodeInfo, meta *Metadata) []TopologyTree {
	now := time.Now()
	var transitNodes, exitNodes []NodeInfo
	for _, n := range nodes {
		if n.NodeID == meta.NodeID {
			continue
		}
		// Recalculate Alive from reported_at — the stored value is always true.
		n.Alive = now.Sub(n.ReportedAt) <= StaleThreshold
		switch n.Role {
		case RoleTransit:
			transitNodes = append(transitNodes, n)
		case RoleExit:
			exitNodes = append(exitNodes, n)
		}
	}

	var result []TopologyTree
	for _, t := range transitNodes {
		tree := TopologyTree{Self: t}
		for _, e := range exitNodes {
			tree.Downstreams = append(tree.Downstreams, TopologyTree{Self: e})
		}
		result = append(result, tree)
	}
	return result
}

// =============================================================================
// Entry reconcile — transit outbounds + exit outbounds (detour) + per-exit SOCKS5
// =============================================================================

func ReconcileEntryConfig(meta *Metadata, downstreams []TopologyTree, db *sql.DB, degraded bool) error {
	if len(downstreams) == 0 {
		return nil
	}

	desiredTransits := topoToTransits(downstreams)
	desiredExits := topoToExits(downstreams)

	// Apply per-transit hy2 brutal config from metadata (entry policy, not topology).
	applyHy2Brutal(desiredTransits, meta.Hy2Brutal)

	if len(desiredTransits) == 0 {
		log.Println("[reconcile] no alive transits, skipping")
		return nil
	}

	// On degraded view (CF unavailable), skip entirely. Degraded topology may be
	// incomplete — applying it causes oscillation. All updates (topo, users, version)
	// can safely wait until CF recovers (typically minutes).
	if degraded {
		log.Printf("[reconcile] degraded view (%d transits, %d exits), skipping", len(desiredTransits), len(desiredExits))
		return nil
	}

	// Load users from SQLite for inbound user sync
	var desiredUsers []User
	if db != nil {
		users, err := GetEnabledUsers(db)
		if err != nil {
			log.Printf("[reconcile] WARNING: failed to load users from DB, preserving existing: %v", err)
		} else {
			desiredUsers = users
		}
	}

	portsChanged := assignExitPorts(meta, desiredExits)

	ConfigMu.Lock()
	defer ConfigMu.Unlock()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return err
	}

	usersChanged := desiredUsers != nil && inboundUsersNeedUpdate(data, desiredUsers)
	versionChanged := meta.SBMVersion != Version

	if !entryConfigNeedsUpdate(data, desiredTransits, desiredExits, meta) && !portsChanged && !usersChanged && !versionChanged {
		return nil
	}

	if versionChanged {
		log.Printf("[reconcile] sbm version changed: %q → %q, forcing config rebuild", meta.SBMVersion, Version)
	}
	log.Printf("[reconcile] entry update: %d transits, %d exits, %d users", len(desiredTransits), len(desiredExits), len(desiredUsers))

	newData, err := rebuildEntryConfig(data, meta, desiredTransits, desiredExits, desiredUsers)
	if err != nil {
		return err
	}

	if portsChanged || versionChanged {
		if versionChanged {
			meta.SBMVersion = Version
		}
		if err := meta.Save(); err != nil {
			log.Printf("[reconcile] save metadata error: %v", err)
		}
		// Open firewall for new per-exit user-facing ports
		for name, port := range meta.ExitVlessPorts {
			if err := FirewallAddPort(port, "tcp"); err != nil {
				log.Printf("[reconcile] firewall vless port %d for %s: %v", port, name, err)
			}
		}
		for name, port := range meta.ExitHy2Ports {
			if err := FirewallAddPort(port, "udp"); err != nil {
				log.Printf("[reconcile] firewall hy2 port %d for %s: %v", port, name, err)
			}
		}
	}

	return validateAndApplyConfig(newData, "[reconcile-entry]")
}

// =============================================================================
// Transit reconcile — per-exit SOCKS5 inbound + exit outbound + route rule
// =============================================================================

// ReconcileTransitConfig syncs discovered exits into metadata for topology reporting.
// Transit is a pure relay — no config changes needed (entry uses detour for exit chaining).
func ReconcileTransitConfig(meta *Metadata, downstreams []TopologyTree) error {
	exits := collectExitNodes(downstreams)

	// Build desired exit name set
	desired := map[string]bool{}
	for _, n := range exits {
		if n.Name != "" {
			desired[n.Name] = true
		}
	}

	// Check if metadata per-exit set changed
	meta.InboundsMu.RLock()
	old := map[string]bool{}
	for _, ib := range meta.Inbounds {
		if ib.ForExit != "" {
			old[ib.ForExit] = true
		}
	}
	meta.InboundsMu.RUnlock()

	changed := len(old) != len(desired)
	if !changed {
		for name := range desired {
			if !old[name] {
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}

	// Rebuild inbound metadata: keep main, replace per-exit markers
	meta.InboundsMu.RLock()
	var inbounds []InboundMeta
	for _, ib := range meta.Inbounds {
		if ib.ForExit == "" {
			inbounds = append(inbounds, ib)
		}
	}
	meta.InboundsMu.RUnlock()
	for _, n := range exits {
		if n.Name == "" {
			continue
		}
		inbounds = append(inbounds, InboundMeta{
			Tag:     "for-exit-" + n.Name,
			ForExit: n.Name,
		})
	}
	meta.InboundsMu.Lock()
	meta.Inbounds = inbounds
	meta.InboundsMu.Unlock()

	log.Printf("[reconcile-transit] exit list updated: %d exits", len(desired))
	return meta.Save()
}

// =============================================================================
// Upstream auto-discovery from CF dashboard
// =============================================================================

// ReconcileUpstreams discovers upstream nodes from CF and updates metadata.
// exit → discovers transit, transit → discovers entry.
// Returns true if upstreams changed.
// NOTE: single-writer — only called from the reconcile loop goroutine.
// The RWMutex protects against concurrent reads from the topology reporter,
// not against multiple writers.
func ReconcileUpstreams(meta *Metadata, cfClient *CFTopoClient) bool {
	if cfClient == nil {
		return false
	}

	var targetRole NodeRole
	switch meta.Role {
	case RoleExit:
		targetRole = RoleTransit
	case RoleTransit:
		targetRole = RoleEntry
	default:
		return false
	}

	nodes, err := cfClient.GetAllNodes()
	if err != nil {
		log.Printf("[reconcile-upstream] CF error: %v", err)
		return false
	}

	now := time.Now()
	var discovered []UpstreamMeta
	for _, n := range nodes {
		if n.Role != targetRole || n.NodeID == meta.NodeID {
			continue
		}
		if now.Sub(n.ReportedAt) > StaleThreshold {
			continue
		}
		// Prefer PublicHost (client-facing reachable host) over Domain (TLS server_name).
		// When a node has both — e.g. entry behind NAT reusing another node's cert —
		// chain reporting must hit the real IP, not the cert's DNS name.
		addr := n.PublicHost
		if addr == "" {
			addr = n.Domain
		}
		if addr == "" {
			addr = n.Address
		}
		if addr == "" {
			continue
		}
		subPort := n.SubPort
		if subPort == 0 {
			subPort = 20033
		}
		discovered = append(discovered, UpstreamMeta{
			Name:    n.Name,
			Address: addr,
			SubURL:  fmt.Sprintf("https://%s:%d/sub/x", addr, subPort),
		})
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Name < discovered[j].Name })

	// Don't clear existing upstreams if CF returned no alive targets —
	// preserves chain connectivity when CF data is temporarily stale.
	if len(discovered) == 0 {
		if len(meta.Upstreams) > 0 {
			log.Printf("[reconcile-upstream] no alive %s nodes in CF, keeping %d existing upstreams", targetRole, len(meta.Upstreams))
		}
		return false
	}

	// Compare with current upstreams by address set
	meta.UpstreamsMu.RLock()
	currentAddrs := make(map[string]bool, len(meta.Upstreams))
	for _, u := range meta.Upstreams {
		currentAddrs[u.Address] = true
	}
	meta.UpstreamsMu.RUnlock()

	discoveredAddrs := make(map[string]bool, len(discovered))
	for _, u := range discovered {
		discoveredAddrs[u.Address] = true
	}

	if len(currentAddrs) == len(discoveredAddrs) {
		same := true
		for addr := range discoveredAddrs {
			if !currentAddrs[addr] {
				same = false
				break
			}
		}
		if same {
			return false
		}
	}

	meta.UpstreamsMu.Lock()
	meta.Upstreams = discovered
	meta.UpstreamsMu.Unlock()
	if err := meta.Save(); err != nil {
		log.Printf("[reconcile-upstream] save metadata error: %v", err)
		return false
	}

	names := make([]string, len(discovered))
	for i, u := range discovered {
		names[i] = u.Name
	}
	log.Printf("[reconcile-upstream] %s upstreams updated: %v", targetRole, names)
	return true
}

// =============================================================================
// Topology extraction helpers
// =============================================================================

func topoToTransits(downstreams []TopologyTree) []TransitInfo {
	var result []TransitInfo
	for _, ds := range downstreams {
		if !ds.Self.Alive || ds.Self.Role != RoleTransit {
			continue
		}
		ti := pickBestInbound(ds.Self)
		if ti != nil {
			result = append(result, *ti)
		}
	}
	// No sort — ordering is handled by BuildEntryOutbounds (hy2 preference)
	// and failover rebalance (bandwidth-based). Alphabetical sort would
	// override failover's bandwidth balancing on every reconcile cycle.
	return result
}

// applyHy2Brutal patches transits in place with brutal CC settings from metadata.
// Only applies when both UpMbps and DownMbps are positive — partial config is ignored.
// Only affects hysteria2 transits; vless entries are untouched.
func applyHy2Brutal(transits []TransitInfo, brutal map[string]Hy2BrutalConfig) {
	if len(brutal) == 0 {
		return
	}
	for i := range transits {
		if transits[i].Protocol != "hysteria2" {
			continue
		}
		cfg, ok := brutal[transits[i].Name]
		if !ok {
			continue
		}
		if cfg.UpMbps > 0 && cfg.DownMbps > 0 {
			transits[i].UpMbps = cfg.UpMbps
			transits[i].DownMbps = cfg.DownMbps
		}
	}
}

func topoToExits(downstreams []TopologyTree) []ExitInfo {
	nodes := collectExitNodes(downstreams)
	// For entry: each exit needs ViaTransit (first alive transit that has it as downstream)
	transitForExit := map[string]string{} // exit nodeID → transit name
	for _, transit := range downstreams {
		if !transit.Self.Alive || transit.Self.Role != RoleTransit {
			continue
		}
		for _, exitDS := range transit.Downstreams {
			if _, ok := transitForExit[exitDS.Self.NodeID]; !ok {
				transitForExit[exitDS.Self.NodeID] = transit.Self.Name
			}
		}
	}

	var result []ExitInfo
	for _, n := range nodes {
		ti := pickBestInbound(n)
		if ti != nil {
			result = append(result, ExitInfo{
				TransitInfo: *ti,
				ViaTransit:  transitForExit[n.NodeID],
			})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// collectExitNodes extracts unique alive exit NodeInfo from topology trees.
// Handles both nested exits (under transit trees) and top-level exits (flat CF topology).
func collectExitNodes(downstreams []TopologyTree) []NodeInfo {
	seen := map[string]bool{}
	var result []NodeInfo
	for _, tree := range downstreams {
		for _, exitDS := range tree.Downstreams {
			if !exitDS.Self.Alive || exitDS.Self.Role != RoleExit || seen[exitDS.Self.NodeID] {
				continue
			}
			seen[exitDS.Self.NodeID] = true
			result = append(result, exitDS.Self)
		}
		// Top-level exit (flat CF topology)
		if tree.Self.Alive && tree.Self.Role == RoleExit && !seen[tree.Self.NodeID] {
			seen[tree.Self.NodeID] = true
			result = append(result, tree.Self)
		}
	}
	return result
}

func pickBestInbound(node NodeInfo) *TransitInfo {
	// Connection target: prefer PublicHost over Domain (see ReconcileUpstreams addr).
	addr := node.PublicHost
	if addr == "" {
		addr = node.Domain
	}
	if addr == "" {
		addr = node.Address
	}
	if addr == "" {
		return nil
	}

	var best *InboundMeta
	for i := range node.Inbounds {
		inb := &node.Inbounds[i]
		if inb.ForExit != "" {
			continue
		}
		if inb.Protocol != "hysteria2" && inb.Protocol != "vless" {
			continue
		}
		if best == nil {
			best = inb
		} else if inb.Protocol == "hysteria2" && best.Protocol != "hysteria2" {
			best = inb
		}
	}
	if best == nil {
		return nil
	}

	sni := node.Domain
	if best.Protocol == "vless" && best.PublicKey != "" {
		if best.SNI != "" {
			sni = best.SNI
		} else {
			sni = "www.walmart.com"
		}
	}

	return &TransitInfo{
		Name:     node.Name,
		Address:  addr,
		Protocol: best.Protocol,
		Port:     best.Port,
		UUID:     best.UUID,
		Password: best.Password,
		ObfsPass: best.ObfsPass,
		SNI:      sni,
		PublicKey: best.PublicKey,
		ShortID:  best.ShortID,
		BWGStatus: node.BWGStatus,
	}
}

// =============================================================================
// Per-exit SOCKS5 port management (Entry localhost, for application routing)
// =============================================================================

func assignExitPorts(meta *Metadata, exits []ExitInfo) bool {
	if meta.ExitSocksPorts == nil {
		meta.ExitSocksPorts = make(map[string]int)
	}
	if meta.ExitVlessPorts == nil {
		meta.ExitVlessPorts = make(map[string]int)
	}
	if meta.ExitHy2Ports == nil {
		meta.ExitHy2Ports = make(map[string]int)
	}
	changed := false
	for _, e := range exits {
		if _, exists := meta.ExitSocksPorts[e.Name]; !exists {
			port := AllocatePort(10800, 10900, meta.ExitSocksPorts)
			meta.ExitSocksPorts[e.Name] = port
			changed = true
			log.Printf("[reconcile] assigned socks5 port %d for exit %s", port, e.Name)
		}
		if _, exists := meta.ExitVlessPorts[e.Name]; !exists {
			port := AllocatePort(20100, 20300, meta.ExitVlessPorts)
			meta.ExitVlessPorts[e.Name] = port
			changed = true
			log.Printf("[reconcile] assigned vless port %d for exit %s", port, e.Name)
		}
		if _, exists := meta.ExitHy2Ports[e.Name]; !exists {
			port := AllocatePort(20300, 20500, meta.ExitHy2Ports)
			meta.ExitHy2Ports[e.Name] = port
			changed = true
			log.Printf("[reconcile] assigned hy2 port %d for exit %s", port, e.Name)
		}
	}
	// Remove ports for exits no longer in topology
	activeExits := map[string]bool{}
	for _, e := range exits {
		activeExits[e.Name] = true
	}
	for name := range meta.ExitSocksPorts {
		if !activeExits[name] {
			if port, ok := meta.ExitVlessPorts[name]; ok {
				FirewallRemovePort(port, "tcp")
			}
			if port, ok := meta.ExitHy2Ports[name]; ok {
				FirewallRemovePort(port, "udp")
			}
			delete(meta.ExitSocksPorts, name)
			delete(meta.ExitVlessPorts, name)
			delete(meta.ExitHy2Ports, name)
			changed = true
			log.Printf("[reconcile] removed ports for exit %s", name)
		}
	}
	return changed
}

// =============================================================================
// Config diff detection
// =============================================================================

func entryConfigNeedsUpdate(configData []byte, transits []TransitInfo, exits []ExitInfo, meta *Metadata) bool {
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}

	// Compare outbounds: tag→JSON map (ignores urltest internal order for failover compat)
	desiredOutbounds := BuildEntryOutbounds(transits, exits)
	desiredByTag := outboundsByTag(desiredOutbounds)
	currentOutboundsList, ok := config["outbounds"].([]interface{})
	if !ok {
		return true
	}
	currentByTag := outboundsByTag(currentOutboundsList)

	if len(desiredByTag) != len(currentByTag) {
		return true
	}
	for tag, desiredJSON := range desiredByTag {
		currentJSON, ok := currentByTag[tag]
		if !ok {
			return true
		}
		if desiredJSON != currentJSON {
			return true
		}
	}

	// Check per-exit inbound count (each exit → socks + vless + hy2 = 3 inbounds)
	currentExitInbounds := 0
	if inbounds, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range inbounds {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			if isPerExitTag(tag) {
				currentExitInbounds++
			}
		}
	}
	if currentExitInbounds != len(exits)*3 {
		return true
	}

	return false
}

// outboundsByTag extracts tag→JSON map from outbounds list.
// For urltest/selector, the "outbounds" array is sorted before serialization
// to make comparison order-independent (preserves failover rebalance).
func outboundsByTag(outbounds []interface{}) map[string]string {
	result := make(map[string]string)
	for _, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := obMap["tag"].(string)
		if tag == "" {
			continue
		}
		// For urltest/selector: sort inner outbounds array for order-independent comparison
		typ, _ := obMap["type"].(string)
		if typ == "urltest" || typ == "selector" {
			// Extract inner outbounds as []string, handling both []interface{} (from JSON)
			// and []string (from code-built maps) to ensure both sides get sorted.
			var sorted []string
			switch inner := obMap["outbounds"].(type) {
			case []interface{}:
				sorted = make([]string, 0, len(inner))
				for _, v := range inner {
					if s, ok := v.(string); ok {
						sorted = append(sorted, s)
					}
				}
			case []string:
				sorted = make([]string, len(inner))
				copy(sorted, inner)
			}
			if len(sorted) > 0 {
				sort.Strings(sorted)
				sortedIface := make([]interface{}, len(sorted))
				for i, s := range sorted {
					sortedIface[i] = s
				}
				// Copy map to avoid mutating original
				cp := make(map[string]interface{})
				for k, v := range obMap {
					cp[k] = v
				}
				cp["outbounds"] = sortedIface
				obMap = cp
			}
		}
		j, _ := json.Marshal(obMap)
		result[tag] = string(j)
	}
	return result
}

// =============================================================================
// Inbound user sync — diff detection + rebuild
// =============================================================================

// inboundUsersNeedUpdate compares current config's inbound user names and
// v2ray_api stats.users against desired users.
func inboundUsersNeedUpdate(configData []byte, desiredUsers []User) bool {
	if len(desiredUsers) == 0 {
		return false // all disabled — not a "change", preserve existing
	}
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}

	desiredNames := map[string]bool{}
	for _, u := range desiredUsers {
		desiredNames[u.Name] = true
	}

	// Check inbound users
	currentNames := extractInboundUserNames(config)
	if len(currentNames) != len(desiredUsers) {
		return true
	}
	for n := range currentNames {
		if !desiredNames[n] {
			return true
		}
	}

	// Check v2ray_api stats.users (use set to handle potential duplicates)
	statsUsersList := extractStatsUsers(config)
	statsUsers := map[string]bool{}
	for _, n := range statsUsersList {
		statsUsers[n] = true
	}
	if len(statsUsers) != len(desiredUsers) {
		return true
	}
	for n := range statsUsers {
		if !desiredNames[n] {
			return true
		}
	}

	return false
}

// extractInboundUserNames extracts user names from vless-in/hy2-in inbounds.
func extractInboundUserNames(config map[string]interface{}) map[string]bool {
	names := map[string]bool{}
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return names
	}
	for _, ib := range inbounds {
		ibMap, ok := ib.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := ibMap["tag"].(string)
		if tag != "vless-in" && tag != "hy2-in" {
			continue
		}
		users, ok := ibMap["users"].([]interface{})
		if !ok {
			continue
		}
		for _, u := range users {
			uMap, ok := u.(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := uMap["name"].(string); ok && name != "" {
				names[name] = true
			}
		}
	}
	return names
}

// isPerExitTag returns true for per-exit inbound tags (socks-exit-*, vless-exit-*, hy2-exit-*).
func isPerExitTag(tag string) bool {
	return strings.HasPrefix(tag, "socks-exit-") ||
		strings.HasPrefix(tag, "vless-exit-") ||
		strings.HasPrefix(tag, "hy2-exit-")
}

// syncInboundUsers replaces the users arrays in all vless and hy2 inbounds
// (main + per-exit).
func syncInboundUsers(inbounds []interface{}, users []User) []interface{} {
	if len(users) == 0 {
		return inbounds // don't clear users if list is empty (safety)
	}

	vlessUsers := make([]interface{}, 0, len(users))
	hy2Users := make([]interface{}, 0, len(users))
	for _, u := range users {
		vlessUsers = append(vlessUsers, map[string]interface{}{
			"name": u.Name, "uuid": u.UUID, "flow": "xtls-rprx-vision",
		})
		hy2Users = append(hy2Users, map[string]interface{}{
			"name": u.Name, "password": u.Password,
		})
	}

	for i, ib := range inbounds {
		ibMap, ok := ib.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := ibMap["tag"].(string)
		typ, _ := ibMap["type"].(string)
		// Match main inbounds (vless-in, hy2-in) and per-exit (vless-exit-*, hy2-exit-*)
		switch {
		case typ == "vless" && (tag == "vless-in" || strings.HasPrefix(tag, "vless-exit-")):
			ibMap["users"] = vlessUsers
			inbounds[i] = ibMap
		case typ == "hysteria2" && (tag == "hy2-in" || strings.HasPrefix(tag, "hy2-exit-")):
			ibMap["users"] = hy2Users
			inbounds[i] = ibMap
		}
	}
	return inbounds
}

// extractStatsUsers extracts user names from experimental.v2ray_api.stats.users.
func extractStatsUsers(config map[string]interface{}) []string {
	exp, ok := config["experimental"].(map[string]interface{})
	if !ok {
		return nil
	}
	api, ok := exp["v2ray_api"].(map[string]interface{})
	if !ok {
		return nil
	}
	stats, ok := api["stats"].(map[string]interface{})
	if !ok {
		return nil
	}
	users, ok := stats["users"].([]interface{})
	if !ok {
		return nil
	}
	names := make([]string, 0, len(users))
	for _, u := range users {
		if s, ok := u.(string); ok {
			names = append(names, s)
		}
	}
	return names
}

// syncStatsUsers updates experimental.v2ray_api.stats.users in the config.
// Also removes legacy clash_api if present (migration from pre-v2.4.0).
func syncStatsUsers(config map[string]interface{}, users []User) {
	if len(users) == 0 {
		return
	}
	exp, ok := config["experimental"].(map[string]interface{})
	if !ok {
		exp = map[string]interface{}{}
		config["experimental"] = exp
	}
	delete(exp, "clash_api") // remove legacy clash_api
	api, ok := exp["v2ray_api"].(map[string]interface{})
	if !ok {
		api = map[string]interface{}{"listen": V2RayStatsAddr}
		exp["v2ray_api"] = api
	}
	stats, ok := api["stats"].(map[string]interface{})
	if !ok {
		stats = map[string]interface{}{"enabled": true}
		api["stats"] = stats
	}
	names := make([]interface{}, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	stats["users"] = names
}

// =============================================================================
// Config rebuild — preserves inbounds, regenerates outbounds + route
// =============================================================================

func rebuildEntryConfig(currentData []byte, meta *Metadata, transits []TransitInfo, exits []ExitInfo, users []User) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(currentData, &config); err != nil {
		return nil, err
	}

	// --- Extract crypto material from main inbounds (for per-exit reuse) ---
	// Deep copy via JSON round-trip to avoid shared references across inbounds.
	var vlessReality map[string]interface{} // TLS+Reality config from vless-in
	var hy2TLS map[string]interface{}       // TLS config from hy2-in
	var hy2Obfs map[string]interface{}      // obfs config from hy2-in
	if existingIBs, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range existingIBs {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			switch ibMap["tag"] {
			case "vless-in":
				if tls, ok := ibMap["tls"].(map[string]interface{}); ok {
					vlessReality = deepCopyMap(tls)
				}
			case "hy2-in":
				if tls, ok := ibMap["tls"].(map[string]interface{}); ok {
					hy2TLS = deepCopyMap(tls)
				}
				if obfs, ok := ibMap["obfs"].(map[string]interface{}); ok {
					hy2Obfs = deepCopyMap(obfs)
				}
			}
		}
	}

	// --- Inbounds: preserve main, replace per-exit (socks + vless + hy2) ---
	var mainInbounds []interface{}
	if existingIBs, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range existingIBs {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			// Strip all per-exit inbounds (socks-exit-*, vless-exit-*, hy2-exit-*)
			if isPerExitTag(tag) {
				continue
			}
			mainInbounds = append(mainInbounds, ib)
		}
	}

	// Add per-exit inbounds for each exit
	for _, e := range exits {
		// SOCKS5 (localhost, for application routing)
		if port, ok := meta.ExitSocksPorts[e.Name]; ok {
			mainInbounds = append(mainInbounds, map[string]interface{}{
				"type": "socks", "tag": "socks-exit-" + e.Name,
				"listen": "127.0.0.1", "listen_port": port,
			})
		}
		// VLESS Reality (user-facing, for manual exit selection)
		if port, ok := meta.ExitVlessPorts[e.Name]; ok && vlessReality != nil {
			mainInbounds = append(mainInbounds, map[string]interface{}{
				"type": "vless", "tag": "vless-exit-" + e.Name,
				"listen": "::", "listen_port": port,
				"sniff": true, "sniff_override_destination": true,
				"users": []interface{}{}, // populated by syncInboundUsers
				"tls":   deepCopyMap(vlessReality),
			})
		}
		// Hysteria2 (user-facing, for manual exit selection)
		if port, ok := meta.ExitHy2Ports[e.Name]; ok && hy2TLS != nil {
			hy2Inbound := map[string]interface{}{
				"type": "hysteria2", "tag": "hy2-exit-" + e.Name,
				"listen": "::", "listen_port": port,
				"sniff": true, "sniff_override_destination": true,
				"users":                   []interface{}{}, // populated by syncInboundUsers
				"ignore_client_bandwidth": true,
				"tls":                     deepCopyMap(hy2TLS),
			}
			if hy2Obfs != nil {
				hy2Inbound["obfs"] = deepCopyMap(hy2Obfs)
			}
			mainInbounds = append(mainInbounds, hy2Inbound)
		}
	}
	config["inbounds"] = mainInbounds

	// --- Sync inbound users from SQLite ---
	if len(users) > 0 {
		ibs, _ := config["inbounds"].([]interface{})
		config["inbounds"] = syncInboundUsers(ibs, users)
		syncStatsUsers(config, users)
	}

	// --- Outbounds: transit + exit (detour) + urltest groups ---
	config["outbounds"] = BuildEntryOutbounds(transits, exits)

	// --- Route: per-exit inbound rules + domain rules + CN/AI/default ---
	socks5Rule := extractSocks5Rules(config)
	route := BuildEntryRoute(exits, 0, nil, len(transits))

	// Insert per-exit inbound rules and socks5 rule
	var perExitRules []interface{}
	for _, e := range exits {
		perExitRules = append(perExitRules, map[string]interface{}{
			"inbound":  []string{"socks-exit-" + e.Name, "vless-exit-" + e.Name, "hy2-exit-" + e.Name},
			"action":   "route",
			"outbound": "exit-" + e.Name,
		})
	}
	SplicePerExitRules(route, perExitRules, socks5Rule)
	config["route"] = route

	return json.MarshalIndent(config, "", "  ")
}

// =============================================================================
// Shared helpers
// =============================================================================

// deepCopyMap makes an independent copy of a map via JSON round-trip.
func deepCopyMap(m map[string]interface{}) map[string]interface{} {
	data, err := json.Marshal(m)
	if err != nil {
		return m
	}
	var cp map[string]interface{}
	if err := json.Unmarshal(data, &cp); err != nil {
		return m
	}
	return cp
}

func validateAndApplyConfig(newData []byte, logPrefix string) error {
	// Skip restart if config is byte-identical to current file.
	// This relies on json.MarshalIndent producing deterministic output (Go sorts
	// map keys). If a future rebuild introduces non-deterministic fields (timestamps,
	// random values), this guard will silently break — keep rebuild output pure.
	if current, err := os.ReadFile(ConfigPath()); err == nil && bytes.Equal(current, newData) {
		log.Printf("%s config unchanged, skipping restart", logPrefix)
		return nil
	}

	tmpFile := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmpFile, newData, 0600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer checkCancel()
	if err := exec.CommandContext(checkCtx, "sing-box", "check", "-c", tmpFile).Run(); err != nil {
		log.Printf("%s validation failed, not applying: %v", logPrefix, err)
		return err
	}

	// Atomic write: rename .tmp → config.json (crash-safe, no partial writes)
	if err := os.Rename(tmpFile, ConfigPath()); err != nil {
		return err
	}

	log.Printf("%s config updated, restarting sing-box", logPrefix)
	return SystemctlOp("restart", "sing-box")
}

func extractSocks5Rules(config map[string]interface{}) interface{} {
	route, ok := config["route"].(map[string]interface{})
	if !ok {
		return nil
	}
	rules, ok := route["rules"].([]interface{})
	if !ok {
		return nil
	}
	for _, r := range rules {
		rMap, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if inbound, ok := rMap["inbound"]; ok {
			if inbList, ok := inbound.([]interface{}); ok {
				for _, v := range inbList {
					if s, ok := v.(string); ok && s == "socks-in" {
						return r
					}
				}
			}
		}
	}
	return nil
}

func AllocatePort(min, max int, exclude map[string]int) int {
	used := map[int]bool{}
	for _, p := range exclude {
		used[p] = true
	}
	for i := 0; i < 100; i++ {
		p := GenerateRandomPort(min, max)
		if !used[p] {
			return p
		}
	}
	return GenerateRandomPort(min, max)
}
