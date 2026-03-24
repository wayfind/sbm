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
// Uses CF as primary topology source, falls back to local TopoStore.
func StartReconcileLoop(meta *Metadata, store *TopoStore, cfClient *CFTopoClient, interval time.Duration) {
	go func() {
		time.Sleep(10 * time.Second)

		for {
			downstreams := getTopoFromDualView(meta, store, cfClient)

			var err error
			switch meta.Role {
			case RoleEntry:
				err = ReconcileEntryConfig(meta, downstreams)
			case RoleTransit:
				err = ReconcileTransitConfig(meta, downstreams)
			}
			if err != nil {
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

	// Consistency check
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

// cfNodesToDownstreams converts flat CF node list into topology trees.
func cfNodesToDownstreams(nodes []NodeInfo, meta *Metadata) []TopologyTree {
	transitNodes := []NodeInfo{}
	exitNodes := []NodeInfo{}
	for _, n := range nodes {
		if n.NodeID == meta.NodeID {
			continue
		}
		switch n.Role {
		case RoleTransit:
			transitNodes = append(transitNodes, n)
		case RoleExit:
			exitNodes = append(exitNodes, n)
		}
	}

	switch meta.Role {
	case RoleEntry:
		var result []TopologyTree
		for _, t := range transitNodes {
			tree := TopologyTree{Self: t}
			for _, e := range exitNodes {
				tree.Downstreams = append(tree.Downstreams, TopologyTree{Self: e})
			}
			result = append(result, tree)
		}
		return result
	case RoleTransit:
		var result []TopologyTree
		for _, e := range exitNodes {
			result = append(result, TopologyTree{Self: e})
		}
		return result
	default:
		return nil
	}
}

// =============================================================================
// Entry reconcile
// =============================================================================

func ReconcileEntryConfig(meta *Metadata, downstreams []TopologyTree) error {
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
	ExitName       string
	TransitName    string
	TransitAddress string
	Port           int
	Username       string
	Password       string
}

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
	for _, ch := range channels {
		desiredTags["via-transit-exit-"+ch.ExitName] = true
	}

	if len(currentTags) != len(desiredTags) {
		return true
	}
	for tag := range desiredTags {
		if !currentTags[tag] {
			return true
		}
	}

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

func rebuildEntryConfig(currentData []byte, meta *Metadata, transits []TransitInfo, exits []ExitInfo, channels []PerExitChannel) ([]byte, error) {
	var config map[string]interface{}
	if err := json.Unmarshal(currentData, &config); err != nil {
		return nil, err
	}

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
	for _, socksIB := range buildPerExitSocksInbounds(channels, meta) {
		mainInbounds = append(mainInbounds, socksIB)
	}
	config["inbounds"] = mainInbounds

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
	outbounds = append(outbounds, map[string]interface{}{"type": "direct", "tag": "direct"})
	config["outbounds"] = outbounds

	socks5Rule := extractSocks5Rules(config)
	route := BuildEntryRoute(exits, 0, nil)
	rules := route["rules"].([]interface{})

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
// Transit reconcile
// =============================================================================

func ReconcileTransitConfig(meta *Metadata, downstreams []TopologyTree) error {
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

	if portsChanged {
		if err := meta.Save(); err != nil {
			log.Printf("[reconcile] save metadata error: %v", err)
		}
	}

	updateTransitInboundMeta(meta, exits)
	if err := meta.Save(); err != nil {
		log.Printf("[reconcile] save metadata error: %v", err)
	}

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
				pw = e.Name
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
	config["outbounds"] = buildTransitOutbounds(exits)

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

func updateTransitInboundMeta(meta *Metadata, exits []ExitInfo) {
	var main []InboundMeta
	for _, inb := range meta.Inbounds {
		if inb.ForExit == "" {
			main = append(main, inb)
		}
	}
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
		sni = "www.walmart.com"
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
