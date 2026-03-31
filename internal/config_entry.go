package internal

import (
	"fmt"
	"sort"
)

// EntryParams holds parameters for generating entry node configuration.
type EntryParams struct {
	// Node's own inbound config
	Domain    string
	VlessPort int
	Hy2Port   int
	SubPort   int

	// TLS certificate files (alternative to ACME, e.g. certbot-managed)
	CertFile string // path to fullchain.pem
	KeyFile  string // path to privkey.pem

	// Crypto materials for client-facing inbounds
	UUID       string
	PrivateKey string
	PublicKey  string
	ShortID    string
	Hy2Pass    string
	ObfsPass   string
	RealitySNI string // TLS SNI for Reality handshake (default: www.walmart.com)

	// SOCKS5 inbound for special routing (e.g., H800 relay)
	Socks5Port     int      // 0 = no SOCKS5 inbound
	Socks5User     string
	Socks5Pass     string
	Socks5DirectIPs []string // IPs to route directly through SOCKS5 inbound

	// Upstream transit nodes (GIA servers)
	Transits []TransitInfo

	// Upstream exit nodes (residential)
	Exits []ExitInfo

	// Users (for multi-user inbounds, from SQLite)
	Users []User
}

// TransitInfo describes a proxy node's connection parameters.
// Used for transit (GIA) nodes and embedded in ExitInfo.
type TransitInfo struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Protocol string `json:"protocol"` // "hysteria2" or "vless"
	Port     int    `json:"port"`

	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
	ObfsPass string `json:"obfs_pass,omitempty"`

	SNI       string `json:"sni,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	ShortID   string `json:"short_id,omitempty"`
	Insecure  bool   `json:"insecure,omitempty"`

	BWGStatus *BWGStatus `json:"-"` // from topology, not serialized
}

// ExitInfo describes an exit (家宽) node. Embeds TransitInfo for connection params.
type ExitInfo struct {
	TransitInfo
	// Which transit to chain through (must match a TransitInfo.Name)
	ViaTransit string `json:"via_transit,omitempty"`
}

// bwgEqual returns true if two BWGStatus pointers represent the same usage tier
// for sorting purposes: both nil, or same used/total ratio.
func bwgEqual(a, b *BWGStatus) bool {
	aHas, bHas := a.HasData(), b.HasData()
	if aHas != bHas {
		return false
	}
	if !aHas {
		return true // both nil/no-data
	}
	// Equal ratio: a.Used * b.Total == b.Used * a.Total
	return a.DataCounter*b.PlanMonthlyData == b.DataCounter*a.PlanMonthlyData
}

// GenerateEntryConfig generates a full sing-box config for the entry node.
// Returns a map suitable for JSON marshaling (entry configs are too complex for static structs).
func GenerateEntryConfig(p *EntryParams) (map[string]interface{}, error) {
	if len(p.Transits) == 0 {
		return nil, fmt.Errorf("entry node requires at least one transit upstream")
	}

	// --- Inbounds ---
	inbounds := []interface{}{}

	// Build users arrays for multi-user inbounds
	vlessUsers := []map[string]interface{}{}
	hy2Users := []map[string]interface{}{}
	userNames := []string{}

	if len(p.Users) == 0 {
		return nil, fmt.Errorf("entry node requires at least one user (run: sbm user add <name>)")
	}
	for _, u := range p.Users {
		vlessUsers = append(vlessUsers, map[string]interface{}{"name": u.Name, "uuid": u.UUID, "flow": "xtls-rprx-vision"})
		hy2Users = append(hy2Users, map[string]interface{}{"name": u.Name, "password": u.Password})
		userNames = append(userNames, u.Name)
	}

	// VLESS Reality inbound (always, no domain needed)
	realitySNI := p.RealitySNI
	if realitySNI == "" {
		realitySNI = "cloud.tencent.com" // entry is domestic, use CN-accessible SNI
	}
	inbounds = append(inbounds, map[string]interface{}{
		"type": "vless", "tag": "vless-in",
		"listen": "::", "listen_port": p.VlessPort,
		"sniff": true, "sniff_override_destination": true,
		"users": vlessUsers,
		"tls": map[string]interface{}{
			"enabled":     true,
			"server_name": realitySNI,
			"reality": map[string]interface{}{
				"enabled":     true,
				"handshake":   map[string]interface{}{"server": realitySNI, "server_port": 443},
				"private_key": p.PrivateKey,
				"short_id":    []string{p.ShortID},
			},
		},
	})

	// Hysteria2 inbound (needs TLS: cert file or ACME)
	if p.CertFile != "" || p.Domain != "" {
		ignoreBW := true
		hy2TLS := map[string]interface{}{
			"enabled": true,
			"alpn":    []string{"h3"},
		}
		if p.CertFile != "" {
			// Use external certificate (e.g. certbot)
			hy2TLS["certificate_path"] = p.CertFile
			hy2TLS["key_path"] = p.KeyFile
			if p.Domain != "" {
				hy2TLS["server_name"] = p.Domain
			}
		} else {
			// Use ACME (sing-box auto-manages cert)
			rootDomain := ExtractRootDomain(p.Domain)
			hy2TLS["acme"] = map[string]interface{}{"domain": []string{p.Domain}, "email": "admin@" + rootDomain}
		}
		inbounds = append(inbounds, map[string]interface{}{
			"type": "hysteria2", "tag": "hy2-in",
			"listen": "::", "listen_port": p.Hy2Port,
			"sniff": true, "sniff_override_destination": true,
			"users":                   hy2Users,
			"ignore_client_bandwidth": ignoreBW,
			"obfs":                    map[string]interface{}{"type": "salamander", "password": p.ObfsPass},
			"tls":                     hy2TLS,
		})
	}

	// SOCKS5 inbound for H800 relay (optional)
	if p.Socks5Port > 0 {
		socks := map[string]interface{}{
			"type": "socks", "tag": "socks-in",
			"listen": "::", "listen_port": p.Socks5Port,
		}
		if p.Socks5User != "" {
			socks["users"] = []map[string]interface{}{
				{"username": p.Socks5User, "password": p.Socks5Pass},
			}
		}
		inbounds = append(inbounds, socks)
	}

	outbounds := BuildEntryOutbounds(p.Transits, p.Exits)
	route := BuildEntryRoute(p.Exits, p.Socks5Port, p.Socks5DirectIPs)

	dns := map[string]interface{}{
		"servers": []map[string]interface{}{
			{"tag": "remote", "type": "tls", "server": "8.8.8.8", "detour": "gia-auto"},
			{"tag": "local", "type": "udp", "server": "223.5.5.5"},
			{"tag": "system", "type": "local"}, // system resolver for outbound domain resolution
		},
		"rules": []map[string]interface{}{
			{"rule_set": "geosite-cn", "server": "local"},
		},
	}

	experimental := map[string]interface{}{
		"cache_file": map[string]interface{}{
			"enabled":    true,
			"store_rdrc": true,
		},
		"v2ray_api": map[string]interface{}{
			"listen": V2RayStatsAddr,
			"stats": map[string]interface{}{
				"enabled": true,
				"users":   userNames,
			},
		},
	}

	config := map[string]interface{}{
		"log":          map[string]interface{}{"level": "warn", "timestamp": true},
		"dns":          dns,
		"inbounds":     inbounds,
		"outbounds":    outbounds,
		"route":        route,
		"experimental": experimental,
	}

	return config, nil
}

// BuildProxyOutbound creates a transit outbound (hy2 or vless) from TransitInfo.
func BuildProxyOutbound(tag string, t *TransitInfo) map[string]interface{} {
	switch t.Protocol {
	case "hysteria2":
		out := map[string]interface{}{
			"type":        "hysteria2",
			"tag":         tag,
			"server":      t.Address,
			"server_port": t.Port,
			"password":    t.Password,
			"tls": map[string]interface{}{
				"enabled":     true,
				"server_name": t.SNI,
				"alpn":        []string{"h3"},
			},
		}
		if t.ObfsPass != "" {
			out["obfs"] = map[string]interface{}{"type": "salamander", "password": t.ObfsPass}
		}
		if t.Insecure {
			out["tls"].(map[string]interface{})["insecure"] = true
		}
		return out

	case "vless":
		tls := map[string]interface{}{
			"enabled":     true,
			"server_name": t.SNI,
		}
		if t.PublicKey != "" {
			tls["reality"] = map[string]interface{}{
				"enabled":    true,
				"public_key": t.PublicKey,
				"short_id":   t.ShortID,
			}
			tls["utls"] = map[string]interface{}{
				"enabled":     true,
				"fingerprint": "chrome",
			}
		}
		if t.Insecure {
			tls["insecure"] = true
		}
		out := map[string]interface{}{
			"type":        "vless",
			"tag":         tag,
			"server":      t.Address,
			"server_port": t.Port,
			"uuid":        t.UUID,
			"tls":         tls,
		}
		if t.Flow != "" {
			out["flow"] = t.Flow
		}
		return out

	default:
		return map[string]interface{}{
			"type":        "direct",
			"tag":         tag,
			"override_address": t.Address,
			"override_port":    t.Port,
		}
	}
}

// buildExitOutbound creates an exit outbound that chains through a transit node.
func buildExitOutbound(tag string, e *ExitInfo) map[string]interface{} {
	out := BuildProxyOutbound(tag, &e.TransitInfo)
	// Chain through any available transit via urltest auto-selection
	if e.ViaTransit != "" {
		out["detour"] = "gia-auto"
	}
	return out
}

// BuildEntryOutbounds builds the outbounds section for an entry node config.
// Used by both GenerateEntryConfig (initial install) and reconcile (auto-update).
// Transit outbounds are sorted by: BWG availability → BWG usage (least used first) → hy2 preferred.
// Exhausted transits (BWG remaining < 5%) are excluded from the urltest group.
func BuildEntryOutbounds(transits []TransitInfo, exits []ExitInfo) []interface{} {
	outbounds := []interface{}{}

	// Sort transits: BWG-aware, hy2 preferred within same tier
	type transitWithTag struct {
		tag     string
		info    TransitInfo
		isHy2   bool
		exhaust bool // BWG remaining < 5%
	}
	sorted := make([]transitWithTag, 0, len(transits))
	for _, t := range transits {
		sorted = append(sorted, transitWithTag{
			tag:     "transit-" + t.Name,
			info:    t,
			isHy2:   t.Protocol == "hysteria2",
			exhaust: t.BWGStatus.IsExhausted(),
		})
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		// Exhausted always last
		if sorted[i].exhaust != sorted[j].exhaust {
			return !sorted[i].exhaust
		}
		// Less bandwidth used sorts first (integer cross-multiply, no float)
		iBWG, jBWG := sorted[i].info.BWGStatus, sorted[j].info.BWGStatus
		if !bwgEqual(iBWG, jBWG) {
			return iBWG.UsedLessThan(jBWG)
		}
		// hy2 preferred within same tier
		if sorted[i].isHy2 != sorted[j].isHy2 {
			return sorted[i].isHy2
		}
		return false
	})

	// Build all transit outbounds (even exhausted, for potential manual override)
	for _, s := range sorted {
		outbounds = append(outbounds, BuildProxyOutbound(s.tag, &s.info))
	}

	// urltest group only includes non-exhausted transits
	transitTags := make([]interface{}, 0, len(sorted))
	for _, s := range sorted {
		if !s.exhaust {
			transitTags = append(transitTags, s.tag)
		}
	}
	if len(transitTags) == 0 {
		// All exhausted — include all as fallback
		for _, s := range sorted {
			transitTags = append(transitTags, s.tag)
		}
	}

	outbounds = append(outbounds, map[string]interface{}{
		"type":      "urltest",
		"tag":       "gia-auto",
		"outbounds": transitTags,
		"url":       "https://www.gstatic.com/generate_204",
		"interval":  "5m",
		"tolerance": 500,
	})

	for _, e := range exits {
		tag := "exit-" + e.Name
		exitCopy := e
		outbounds = append(outbounds, buildExitOutbound(tag, &exitCopy))
	}

	if len(exits) > 0 {
		exitTags := make([]interface{}, len(exits))
		for i, e := range exits {
			exitTags[i] = "exit-" + e.Name
		}
		// Daily sticky: pick one exit per day, rotate daily
		// All AI traffic goes through the same exit IP for the entire day
		defaultExit := stickyFirstExit(exitTags)
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "residential",
			"outbounds": exitTags,
			"default":   defaultExit,
		})
	}

	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
	)

	return outbounds
}

// stickyFirstExit picks the first exit tag as the selector default.
// When an exit goes offline, reconcile removes it from the list,
// and the next available exit becomes the new default.
func stickyFirstExit(exitTags []interface{}) string {
	if len(exitTags) == 0 {
		return ""
	}
	if s, ok := exitTags[0].(string); ok {
		return s
	}
	return ""
}

// BuildEntryRoute builds the route section for an entry node config.
// Routing rules come from the routing policy file (or defaults if absent).
func BuildEntryRoute(exits []ExitInfo, socks5Port int, socks5DirectIPs []string) map[string]interface{} {
	policy := LoadRoutingPolicy()

	rules := []interface{}{
		map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
		map[string]interface{}{"ip_is_private": true, "action": "route", "outbound": "direct"},
	}

	rules = append(rules, policy.BuildPolicyRules(len(exits) > 0)...)

	if socks5Port > 0 && len(socks5DirectIPs) > 0 {
		rules = append(rules, map[string]interface{}{
			"inbound":  []string{"socks-in"},
			"ip_cidr":  socks5DirectIPs,
			"action":   "route",
			"outbound": "direct",
		})
	}

	return map[string]interface{}{
		"rules":                  rules,
		"rule_set":               policy.BuildRuleSetDefs(),
		"final":                  "gia-auto",
		"auto_detect_interface":  true,
		"default_domain_resolver": "system",
	}
}

// structuralRuleCount is the number of fixed rules at the start of the rules array
// (dns hijack + private IP). Per-exit rules are inserted after these.
const structuralRuleCount = 2

// SplicePerExitRules inserts per-exit inbound rules and socks5 rule into a route
// generated by BuildEntryRoute. Shared by reconcile and reload.
func SplicePerExitRules(route map[string]interface{}, perExitRules []interface{}, socks5Rule interface{}) {
	rules := route["rules"].([]interface{})
	if len(perExitRules) > 0 {
		newRules := make([]interface{}, 0, len(rules)+len(perExitRules))
		newRules = append(newRules, rules[:structuralRuleCount]...)
		newRules = append(newRules, perExitRules...)
		newRules = append(newRules, rules[structuralRuleCount:]...)
		route["rules"] = newRules
	}
	if socks5Rule != nil {
		r := route["rules"].([]interface{})
		route["rules"] = append(r, socks5Rule)
	}
}
