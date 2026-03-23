package internal

import (
	"crypto/sha256"
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

// StartReconcileLoop starts periodic reconciliation for entry or transit nodes.
func StartReconcileLoop(meta *Metadata, store *TopoStore, interval time.Duration) {
	go func() {
		time.Sleep(10 * time.Second) // wait for initial reports

		for {
			var err error
			switch meta.Role {
			case RoleEntry:
				err = ReconcileEntryConfig(meta, store)
			case RoleTransit:
				err = ReconcileTransitConfig(meta, store)
			}
			if err != nil {
				log.Printf("[reconcile] error: %v", err)
			}
			time.Sleep(interval)
		}
	}()
	log.Printf("[reconcile] started, role=%s, interval=%s", meta.Role, interval)
}

// =============================================================================
// Entry reconcile
// =============================================================================

func ReconcileEntryConfig(meta *Metadata, store *TopoStore) error {
	downstreams := store.GetDownstreams()
	if len(downstreams) == 0 {
		return nil
	}

	desiredTransits := topoToTransits(downstreams)
	desiredExits := topoToExits(downstreams)
	perExitChannels := topoToPerExitChannels(downstreams)

	if len(desiredTransits) == 0 {
		log.Println("[reconcile] no alive transits, skipping")
		return nil
	}

	// Assign/persist socks5 ports for per-exit channels
	portsChanged := assignExitSocksPorts(meta, perExitChannels)

	ConfigMu.Lock()
	defer ConfigMu.Unlock()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return err
	}

	if !entryConfigNeedsUpdate(data, desiredTransits, desiredExits, perExitChannels, meta) && !portsChanged {
		return nil
	}

	log.Printf("[reconcile] entry update: %d transits, %d exits, %d per-exit channels",
		len(desiredTransits), len(desiredExits), len(perExitChannels))

	newData, err := rebuildEntryConfig(data, meta, desiredTransits, desiredExits, perExitChannels)
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

// PerExitChannel represents a per-exit channel on a transit node.
type PerExitChannel struct {
	ExitName       string // exit node name (e.g. "lisa")
	TransitName    string // transit that has the per-exit inbound
	TransitAddress string // transit domain/IP
	Port           int    // transit's per-exit inbound port
	Username       string // socks5 auth username
	Password       string // socks5 auth password
}

// topoToPerExitChannels extracts per-exit channel info from transit inbounds.
func topoToPerExitChannels(downstreams []TopologyTree) []PerExitChannel {
	var result []PerExitChannel
	for _, transit := range downstreams {
		if !transit.Self.Alive || transit.Self.Role != RoleTransit {
			continue
		}
		addr := transit.Self.Domain
		if addr == "" {
			addr = transit.Self.Address
		}
		for _, inb := range transit.Self.Inbounds {
			if inb.ForExit != "" {
				result = append(result, PerExitChannel{
					ExitName:       inb.ForExit,
					TransitName:    transit.Self.Name,
					TransitAddress: addr,
					Port:           inb.Port,
					Username:       inb.Username,
					Password:       inb.Password,
				})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExitName < result[j].ExitName })
	return result
}

// assignExitSocksPorts assigns persistent localhost SOCKS5 ports for per-exit channels.
// Returns true if any new ports were assigned (metadata needs saving).
func assignExitSocksPorts(meta *Metadata, channels []PerExitChannel) bool {
	if meta.ExitSocksPorts == nil {
		meta.ExitSocksPorts = make(map[string]int)
	}
	changed := false
	for _, ch := range channels {
		if _, exists := meta.ExitSocksPorts[ch.ExitName]; !exists {
			port := AllocatePort(10800, 10900, meta.ExitSocksPorts)
			meta.ExitSocksPorts[ch.ExitName] = port
			changed = true
			log.Printf("[reconcile] assigned socks5 port %d for exit %s", port, ch.ExitName)
		}
	}
	// Clean up ports for exits no longer present
	activeExits := map[string]bool{}
	for _, ch := range channels {
		activeExits[ch.ExitName] = true
	}
	for name := range meta.ExitSocksPorts {
		if !activeExits[name] {
			delete(meta.ExitSocksPorts, name)
			changed = true
		}
	}
	return changed
}

func entryConfigNeedsUpdate(configData []byte, transits []TransitInfo, exits []ExitInfo, channels []PerExitChannel, meta *Metadata) bool {
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}

	// Extract current outbound tags (semantic comparison, not JSON hash)
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

	// Desired tags
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
	for _, ch := range channels {
		desiredTags["via-transit-exit-"+ch.ExitName] = true
	}

	// Compare tag sets
	if len(currentTags) != len(desiredTags) {
		return true
	}
	for tag := range desiredTags {
		if !currentTags[tag] {
			return true
		}
	}

	// Also check per-exit inbound count
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
	if currentExitInbounds != len(channels) {
		return true
	}

	return false
}

// buildPerExitSocksInbounds creates localhost SOCKS5 inbound configs for per-exit channels.
func buildPerExitSocksInbounds(channels []PerExitChannel, meta *Metadata) []map[string]interface{} {
	var result []map[string]interface{}
	for _, ch := range channels {
		port, ok := meta.ExitSocksPorts[ch.ExitName]
		if !ok {
			continue
		}
		result = append(result, map[string]interface{}{
			"type":        "socks",
			"tag":         "socks-exit-" + ch.ExitName,
			"listen":      "127.0.0.1",
			"listen_port": port,
		})
	}
	return result
}

// rebuildEntryConfig preserves existing main inbounds, adds per-exit SOCKS5 inbounds,
// regenerates outbounds + route.
func rebuildEntryConfig(currentData []byte, meta *Metadata, transits []TransitInfo, exits []ExitInfo, channels []PerExitChannel) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(currentData, &config); err != nil {
		return nil, err
	}

	// Preserve main inbounds (vless-in, hy2-in, socks-in), remove old socks-exit-* inbounds
	var mainInbounds []interface{}
	if existingIBs, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range existingIBs {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			if len(tag) > 11 && tag[:11] == "socks-exit-" {
				continue // remove old per-exit inbounds, will re-add below
			}
			mainInbounds = append(mainInbounds, ib)
		}
	}

	// Add per-exit SOCKS5 inbounds
	for _, socksIB := range buildPerExitSocksInbounds(channels, meta) {
		mainInbounds = append(mainInbounds, socksIB)
	}
	config["inbounds"] = mainInbounds

	// Build outbounds: standard entry outbounds (strip trailing direct),
	// add per-exit channel outbounds, then re-add direct.
	outbounds := BuildEntryOutbounds(transits, exits)
	outbounds = outbounds[:len(outbounds)-1] // remove trailing direct

	for _, ch := range channels {
		outbounds = append(outbounds, map[string]interface{}{
			"type":        "socks",
			"tag":         "via-transit-exit-" + ch.ExitName,
			"server":      ch.TransitAddress,
			"server_port": ch.Port,
			"username":    ch.Username,
			"password":    ch.Password,
		})
	}

	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
	)
	config["outbounds"] = outbounds

	// Build route with per-exit inbound rules at highest priority
	socks5Rule := extractSocks5Rules(config)
	route := BuildEntryRoute(exits, 0, nil)
	rules := route["rules"].([]interface{})

	// Insert per-exit inbound rules after DNS + private IP rules (index 2)
	var perExitRules []interface{}
	for _, ch := range channels {
		perExitRules = append(perExitRules, map[string]interface{}{
			"inbound":  []string{"socks-exit-" + ch.ExitName},
			"action":   "route",
			"outbound": "via-transit-exit-" + ch.ExitName,
		})
	}
	if len(perExitRules) > 0 {
		newRules := make([]interface{}, 0, len(rules)+len(perExitRules))
		newRules = append(newRules, rules[:2]...)    // DNS + private IP
		newRules = append(newRules, perExitRules...)  // per-exit (highest priority)
		newRules = append(newRules, rules[2:]...)     // CN, AI, default
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
// Transit reconcile
// =============================================================================

func ReconcileTransitConfig(meta *Metadata, store *TopoStore) error {
	downstreams := store.GetDownstreams()
	if len(downstreams) == 0 {
		return nil
	}

	// Extract alive exits from direct downstreams
	var exits []ExitInfo
	for _, ds := range downstreams {
		if !ds.Self.Alive || ds.Self.Role != RoleExit {
			continue
		}
		ti := pickBestInbound(ds.Self)
		if ti != nil {
			exits = append(exits, ExitInfo{TransitInfo: *ti})
		}
	}
	sort.Slice(exits, func(i, j int) bool { return exits[i].Name < exits[j].Name })

	// Assign/persist per-exit inbound ports
	portsChanged := assignTransitExitPorts(meta, exits)

	ConfigMu.Lock()
	defer ConfigMu.Unlock()

	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return err
	}

	if !transitConfigNeedsUpdate(data, exits, meta) && !portsChanged {
		return nil
	}

	log.Printf("[reconcile] transit update: %d exits", len(exits))

	newData, err := rebuildTransitConfig(data, meta, exits)
	if err != nil {
		return err
	}

	// Persist port assignments
	if portsChanged {
		if err := meta.Save(); err != nil {
			log.Printf("[reconcile] save metadata error: %v", err)
		}
	}

	// Update metadata inbounds to include per-exit entries (for topology reporting)
	updateTransitInboundMeta(meta, exits)
	if err := meta.Save(); err != nil {
		log.Printf("[reconcile] save metadata error: %v", err)
	}

	// Open firewall for new per-exit ports
	for _, e := range exits {
		if port, ok := meta.ExitPorts[e.Name]; ok {
			FirewallAddPort(port, "tcp")
		}
	}
	FirewallReload()

	return validateAndApplyConfig(newData, "[reconcile-transit]")
}

func assignTransitExitPorts(meta *Metadata, exits []ExitInfo) bool {
	if meta.ExitPorts == nil {
		meta.ExitPorts = make(map[string]int)
	}
	if meta.ExitAuth == nil {
		meta.ExitAuth = make(map[string]string)
	}
	changed := false
	for _, e := range exits {
		if _, exists := meta.ExitPorts[e.Name]; !exists {
			meta.ExitPorts[e.Name] = AllocatePort(40000, 50000, meta.ExitPorts)
			changed = true
			log.Printf("[reconcile] assigned transit port %d for exit %s", meta.ExitPorts[e.Name], e.Name)
		}
		if _, exists := meta.ExitAuth[e.Name]; !exists {
			pw, err := GeneratePassword(16)
			if err != nil {
				pw = e.Name // fallback
			}
			meta.ExitAuth[e.Name] = pw
			changed = true
		}
	}
	activeExits := map[string]bool{}
	for _, e := range exits {
		activeExits[e.Name] = true
	}
	for name := range meta.ExitPorts {
		if !activeExits[name] {
			delete(meta.ExitPorts, name)
			delete(meta.ExitAuth, name)
			changed = true
		}
	}
	return changed
}

func transitConfigNeedsUpdate(configData []byte, exits []ExitInfo, meta *Metadata) bool {
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}

	// Semantic comparison: check if exit outbound tags match
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return true
	}
	currentExitTags := map[string]bool{}
	for _, ob := range outbounds {
		obMap, ok := ob.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := obMap["tag"].(string)
		if len(tag) > 5 && tag[:5] == "exit-" {
			currentExitTags[tag] = true
		}
	}

	desiredExitTags := map[string]bool{}
	for _, e := range exits {
		desiredExitTags["exit-"+e.Name] = true
	}

	if len(currentExitTags) != len(desiredExitTags) {
		return true
	}
	for tag := range desiredExitTags {
		if !currentExitTags[tag] {
			return true
		}
	}
	return false
}

func buildTransitOutbounds(exits []ExitInfo) []interface{} {
	outbounds := []interface{}{
		map[string]interface{}{"type": "direct", "tag": "direct"},
	}
	for _, e := range exits {
		outbounds = append(outbounds, BuildProxyOutbound("exit-"+e.Name, &e.TransitInfo))
	}
	return outbounds
}

func buildTransitPerExitInbounds(exits []ExitInfo, meta *Metadata) []map[string]interface{} {
	var result []map[string]interface{}
	for _, e := range exits {
		port, ok := meta.ExitPorts[e.Name]
		if !ok {
			continue
		}
		pw := meta.ExitAuth[e.Name]
		result = append(result, map[string]interface{}{
			"type":        "socks",
			"tag":         "for-exit-" + e.Name,
			"listen":      "::",
			"listen_port": port,
			"users": []map[string]interface{}{
				{"username": "sbm", "password": pw},
			},
		})
	}
	return result
}

func rebuildTransitConfig(currentData []byte, meta *Metadata, exits []ExitInfo) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(currentData, &config); err != nil {
		return nil, err
	}

	// Preserve main inbounds, remove old for-exit-* inbounds
	var mainInbounds []interface{}
	if existingIBs, ok := config["inbounds"].([]interface{}); ok {
		for _, ib := range existingIBs {
			ibMap, ok := ib.(map[string]interface{})
			if !ok {
				continue
			}
			tag, _ := ibMap["tag"].(string)
			if len(tag) > 9 && tag[:9] == "for-exit-" {
				continue
			}
			mainInbounds = append(mainInbounds, ib)
		}
	}
	for _, perExitIB := range buildTransitPerExitInbounds(exits, meta) {
		mainInbounds = append(mainInbounds, perExitIB)
	}
	config["inbounds"] = mainInbounds

	// Outbounds: direct + per-exit outbounds
	config["outbounds"] = buildTransitOutbounds(exits)

	// Route: sniff first (restore domain from TLS SNI for rule_set matching),
	// then per-exit inbound routing, then default direct.
	// Sniff is critical: upstream may use fake-ip, so Transit sees 198.18.x.x
	// instead of real domains. Sniff extracts domain from TLS ClientHello/HTTP Host.
	rules := []interface{}{
		map[string]interface{}{"action": "sniff"},
	}
	for _, e := range exits {
		rules = append(rules, map[string]interface{}{
			"inbound":  []string{"for-exit-" + e.Name},
			"action":   "route",
			"outbound": "exit-" + e.Name,
		})
	}
	config["route"] = map[string]interface{}{
		"rules":                rules,
		"final":                "direct",
		"auto_detect_interface": true,
	}

	return json.MarshalIndent(config, "", "  ")
}

// updateTransitInboundMeta updates metadata.Inbounds to include per-exit entries
// so they appear in topology reports to Entry.
func updateTransitInboundMeta(meta *Metadata, exits []ExitInfo) {
	// Keep only main inbounds (non-for-exit)
	var main []InboundMeta
	for _, inb := range meta.Inbounds {
		if inb.ForExit == "" {
			main = append(main, inb)
		}
	}
	// Add per-exit inbound metadata (with auth for topology reporting)
	for _, e := range exits {
		port, ok := meta.ExitPorts[e.Name]
		if !ok {
			continue
		}
		main = append(main, InboundMeta{
			Tag:       "for-exit-" + e.Name,
			Protocol:  "socks",
			Port:      port,
			Transport: "tcp",
			ForExit:   e.Name,
			Username:  "sbm",
			Password:  meta.ExitAuth[e.Name],
		})
	}
	meta.Inbounds = main
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
	for _, transit := range downstreams {
		if !transit.Self.Alive || transit.Self.Role != RoleTransit {
			continue
		}
		for _, exitDS := range transit.Downstreams {
			if !exitDS.Self.Alive || exitDS.Self.Role != RoleExit {
				continue
			}
			ti := pickBestInbound(exitDS.Self)
			if ti != nil {
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
			continue // skip per-exit channel inbounds
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

	// For VLESS Reality, SNI should be the handshake domain (e.g. www.walmart.com),
	// not the node's actual address. When node has no real domain, use default.
	sni := node.Domain
	if best.Protocol == "vless" && best.PublicKey != "" {
		sni = "www.walmart.com" // Reality SNI must match handshake server
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

// configNeedsUpdate is used by failover (which only changes outbound order).
func configNeedsUpdate(configData []byte, transits []TransitInfo, exits []ExitInfo) bool {
	var config map[string]interface{}
	if err := json.Unmarshal(configData, &config); err != nil {
		return true
	}
	currentOutbounds, ok := config["outbounds"]
	if !ok {
		return true
	}
	currentJSON, err := json.Marshal(currentOutbounds)
	if err != nil {
		return true
	}
	desiredJSON, _ := json.Marshal(BuildEntryOutbounds(transits, exits))
	return sha256.Sum256(currentJSON) != sha256.Sum256(desiredJSON)
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

// AllocatePort finds a random available port, for use with metadata persistence.
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

