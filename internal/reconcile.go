package internal

import (
	"database/sql"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// ConfigMu protects concurrent read-modify-write of sing-box config.json.
var ConfigMu sync.Mutex

// StartReconcileLoop starts periodic reconciliation.
// Entry: auto-configure transit/exit outbounds from topology.
// Transit: only reports to CF (no config changes needed — pure relay).
func StartReconcileLoop(meta *Metadata, store *TopoStore, cfClient *CFTopoClient, db *sql.DB, interval time.Duration) {
	if meta.Role != RoleEntry {
		log.Printf("[reconcile] role=%s, no reconcile needed (transit is pure relay)", meta.Role)
		return
	}

	go func() {
		time.Sleep(10 * time.Second)

		for {
			downstreams := getTopoFromDualView(meta, store, cfClient)
			if err := ReconcileEntryConfig(meta, downstreams, db); err != nil {
				log.Printf("[reconcile] error: %v", err)
			}
			time.Sleep(interval)
		}
	}()
	log.Printf("[reconcile] started, role=%s, interval=%s", meta.Role, interval)
}

// getTopoFromDualView reads topology from CF (primary) and local store (fallback).
func getTopoFromDualView(meta *Metadata, store *TopoStore, cfClient *CFTopoClient) []TopologyTree {
	localDownstreams := store.GetDownstreams()

	if cfClient == nil {
		return localDownstreams
	}

	cfNodes, err := cfClient.GetAllNodes()
	if err != nil {
		log.Printf("[reconcile] CF unavailable, using local view: %v", err)
		return localDownstreams
	}

	cfDownstreams := cfNodesToDownstreams(cfNodes, meta)

	// Consistency check (only compare relevant roles)
	cfIDs := map[string]bool{}
	for _, d := range cfDownstreams {
		cfIDs[d.Self.NodeID] = true
	}
	localIDs := map[string]bool{}
	for _, d := range localDownstreams {
		localIDs[d.Self.NodeID] = true
	}
	for id := range cfIDs {
		if !localIDs[id] {
			log.Printf("[reconcile] ALERT: node %s in CF but not in chain → data-plane break?", id)
		}
	}
	for id := range localIDs {
		if !cfIDs[id] {
			log.Printf("[reconcile] WARN: node %s in chain but not in CF → CF channel issue?", id)
		}
	}

	return cfDownstreams
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

func ReconcileEntryConfig(meta *Metadata, downstreams []TopologyTree, db *sql.DB) error {
	if len(downstreams) == 0 {
		return nil
	}

	desiredTransits := topoToTransits(downstreams)
	desiredExits := topoToExits(downstreams)

	if len(desiredTransits) == 0 {
		log.Println("[reconcile] no alive transits, skipping")
		return nil
	}

	// Load users from SQLite for inbound user sync
	var desiredUsers []User
	if db != nil {
		users, err := GetEnabledUsers(db)
		if err != nil {
			log.Printf("[reconcile] WARNING: failed to load users from DB, preserving existing: %v", err)
			// DB error → don't touch users (fail-safe)
		} else {
			desiredUsers = users
		}
	}

	// Assign per-exit SOCKS5 ports (localhost, for application-layer routing)
	portsChanged := assignExitSocksPorts(meta, desiredExits)

	ConfigMu.Lock()
	defer ConfigMu.Unlock()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return err
	}

	usersChanged := desiredUsers != nil && inboundUsersNeedUpdate(data, desiredUsers)

	if !entryConfigNeedsUpdate(data, desiredTransits, desiredExits, meta) && !portsChanged && !usersChanged {
		return nil
	}

	log.Printf("[reconcile] entry update: %d transits, %d exits, %d users", len(desiredTransits), len(desiredExits), len(desiredUsers))

	newData, err := rebuildEntryConfig(data, meta, desiredTransits, desiredExits, desiredUsers)
	if err != nil {
		return err
	}

	if portsChanged {
		if err := meta.Save(); err != nil {
			log.Printf("[reconcile] save metadata error: %v", err)
		}
	}

	return validateAndApplyConfig(newData, "[reconcile-entry]")
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
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func topoToExits(downstreams []TopologyTree) []ExitInfo {
	var result []ExitInfo
	seen := map[string]bool{} // dedup exits across transits
	for _, transit := range downstreams {
		if !transit.Self.Alive || transit.Self.Role != RoleTransit {
			continue
		}
		for _, exitDS := range transit.Downstreams {
			if !exitDS.Self.Alive || exitDS.Self.Role != RoleExit {
				continue
			}
			if seen[exitDS.Self.NodeID] {
				continue
			}
			seen[exitDS.Self.NodeID] = true
			ti := pickBestInbound(exitDS.Self)
			if ti != nil {
				// ViaTransit: use first available transit (urltest will handle failover)
				result = append(result, ExitInfo{
					TransitInfo: *ti,
					ViaTransit:  transit.Self.Name,
				})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func pickBestInbound(node NodeInfo) *TransitInfo {
	addr := node.Domain
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
	}
}

// =============================================================================
// Per-exit SOCKS5 port management (Entry localhost, for application routing)
// =============================================================================

func assignExitSocksPorts(meta *Metadata, exits []ExitInfo) bool {
	if meta.ExitSocksPorts == nil {
		meta.ExitSocksPorts = make(map[string]int)
	}
	changed := false
	for _, e := range exits {
		if _, exists := meta.ExitSocksPorts[e.Name]; !exists {
			port := AllocatePort(10800, 10900, meta.ExitSocksPorts)
			meta.ExitSocksPorts[e.Name] = port
			changed = true
			log.Printf("[reconcile] assigned socks5 port %d for exit %s", port, e.Name)
		}
	}
	activeExits := map[string]bool{}
	for _, e := range exits {
		activeExits[e.Name] = true
	}
	for name := range meta.ExitSocksPorts {
		if !activeExits[name] {
			delete(meta.ExitSocksPorts, name)
			changed = true
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

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return true
	}
	currentTags := map[string]bool{}
	for _, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		if tag, ok := obMap["tag"].(string); ok {
			currentTags[tag] = true
		}
	}

	desiredTags := map[string]bool{"gia-auto": true, "direct": true}
	for _, t := range transits {
		desiredTags["transit-"+t.Name] = true
	}
	for _, e := range exits {
		desiredTags["exit-"+e.Name] = true
	}
	if len(exits) > 0 {
		desiredTags["residential"] = true
	}

	if len(currentTags) != len(desiredTags) {
		return true
	}
	for tag := range desiredTags {
		if !currentTags[tag] {
			return true
		}
	}

	// Check per-exit inbound count
	currentExitInbounds := 0
	if inbounds, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range inbounds {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			if len(tag) > 11 && tag[:11] == "socks-exit-" {
				currentExitInbounds++
			}
		}
	}
	if currentExitInbounds != len(exits) {
		return true
	}

	// Check residential selector default (daily rotation)
	if len(exits) > 0 {
		exitTags := make([]string, len(exits))
		for i, e := range exits {
			exitTags[i] = "exit-" + e.Name
		}
		desiredDefault := dailyStickyExit(exitTags)
		for _, ob := range outbounds {
			obMap, ok := ob.(map[string]interface{})
			if !ok {
				continue
			}
			if obMap["tag"] == "residential" {
				currentDefault, _ := obMap["default"].(string)
				if currentDefault != desiredDefault {
					return true
				}
				break
			}
		}
	}

	return false
}

// =============================================================================
// Inbound user sync — diff detection + rebuild
// =============================================================================

// inboundUsersNeedUpdate compares current config's inbound user names against desired users.
func inboundUsersNeedUpdate(configData []byte, desiredUsers []User) bool {
	if len(desiredUsers) == 0 {
		return false // all disabled — not a "change", preserve existing
	}
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}
	currentNames := extractInboundUserNames(config)
	if len(currentNames) != len(desiredUsers) {
		return true
	}
	desiredNames := map[string]bool{}
	for _, u := range desiredUsers {
		desiredNames[u.Name] = true
	}
	for n := range currentNames {
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

// syncInboundUsers replaces the users arrays in vless-in and hy2-in inbounds.
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
		switch tag {
		case "vless-in":
			ibMap["users"] = vlessUsers
			inbounds[i] = ibMap
		case "hy2-in":
			ibMap["users"] = hy2Users
			inbounds[i] = ibMap
		}
	}
	return inbounds
}

// =============================================================================
// Config rebuild — preserves inbounds, regenerates outbounds + route
// =============================================================================

func rebuildEntryConfig(currentData []byte, meta *Metadata, transits []TransitInfo, exits []ExitInfo, users []User) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(currentData, &config); err != nil {
		return nil, err
	}

	// --- Inbounds: preserve main, replace per-exit SOCKS5 ---
	var mainInbounds []interface{}
	if existingIBs, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range existingIBs {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			if len(tag) > 11 && tag[:11] == "socks-exit-" {
				continue
			}
			mainInbounds = append(mainInbounds, ib)
		}
	}
	for _, e := range exits {
		port, ok := meta.ExitSocksPorts[e.Name]
		if !ok {
			continue
		}
		mainInbounds = append(mainInbounds, map[string]interface{}{
			"type":        "socks",
			"tag":         "socks-exit-" + e.Name,
			"listen":      "127.0.0.1",
			"listen_port": port,
		})
	}
	config["inbounds"] = mainInbounds

	// --- Sync inbound users from SQLite ---
	if len(users) > 0 {
		ibs, _ := config["inbounds"].([]interface{})
		config["inbounds"] = syncInboundUsers(ibs, users)
	}

	// --- Outbounds: transit + exit (detour) + urltest groups ---
	config["outbounds"] = BuildEntryOutbounds(transits, exits)

	// --- Route: per-exit inbound rules + domain rules + CN/AI/default ---
	socks5Rule := extractSocks5Rules(config)
	route := BuildEntryRoute(exits, 0, nil)
	rules := route["rules"].([]interface{})

	// Insert per-exit inbound rules after DNS + private IP (index 2)
	var perExitRules []interface{}
	for _, e := range exits {
		perExitRules = append(perExitRules, map[string]interface{}{
			"inbound":  []string{"socks-exit-" + e.Name},
			"action":   "route",
			"outbound": "exit-" + e.Name,
		})
	}
	if len(perExitRules) > 0 {
		newRules := make([]interface{}, 0, len(rules)+len(perExitRules))
		newRules = append(newRules, rules[:2]...)
		newRules = append(newRules, perExitRules...)
		newRules = append(newRules, rules[2:]...)
		route["rules"] = newRules
	}
	if socks5Rule != nil {
		r := route["rules"].([]interface{})
		route["rules"] = append(r, socks5Rule)
	}
	config["route"] = route

	return json.MarshalIndent(config, "", "  ")
}

// =============================================================================
// Shared helpers
// =============================================================================

func validateAndApplyConfig(newData []byte, logPrefix string) error {
	tmpFile := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmpFile, newData, 0600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	if err := exec.Command("sing-box", "check", "-c", tmpFile).Run(); err != nil {
		log.Printf("%s validation failed, not applying: %v", logPrefix, err)
		return err
	}

	if err := os.WriteFile(ConfigPath(), newData, 0600); err != nil {
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
