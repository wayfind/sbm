package internal

import (
	"fmt"
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
}

// ExitInfo describes an exit (家宽) node. Embeds TransitInfo for connection params.
type ExitInfo struct {
	TransitInfo
	// Which transit to chain through (must match a TransitInfo.Name)
	ViaTransit string `json:"via_transit,omitempty"`
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

	if len(p.Users) > 0 {
		for _, u := range p.Users {
			vlessUsers = append(vlessUsers, map[string]interface{}{"name": u.Name, "uuid": u.UUID, "flow": "xtls-rprx-vision"})
			hy2Users = append(hy2Users, map[string]interface{}{"name": u.Name, "password": u.Password})
			userNames = append(userNames, u.Name)
		}
	} else {
		// Fallback: single user from install params
		vlessUsers = append(vlessUsers, map[string]interface{}{"uuid": p.UUID, "flow": "xtls-rprx-vision"})
		hy2Users = append(hy2Users, map[string]interface{}{"password": p.Hy2Pass})
	}

	// VLESS Reality inbound (always, no domain needed)
	inbounds = append(inbounds, map[string]interface{}{
		"type": "vless", "tag": "vless-in",
		"listen": "::", "listen_port": p.VlessPort,
		"users": vlessUsers,
		"tls": map[string]interface{}{
			"enabled":     true,
			"server_name": "www.walmart.com",
			"reality": map[string]interface{}{
				"enabled":     true,
				"handshake":   map[string]interface{}{"server": "www.walmart.com", "server_port": 443},
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
		"clash_api": map[string]interface{}{
			"external_controller": "127.0.0.1:9090",
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
func BuildEntryOutbounds(transits []TransitInfo, exits []ExitInfo) []interface{} {
	outbounds := []interface{}{}
	transitTags := []string{}

	for _, t := range transits {
		tag := "transit-" + t.Name
		transitTags = append(transitTags, tag)
		outbounds = append(outbounds, BuildProxyOutbound(tag, &t))
	}

	outbounds = append(outbounds, map[string]interface{}{
		"type":      "urltest",
		"tag":       "gia-auto",
		"outbounds": transitTags,
		"url":       "https://www.gstatic.com/generate_204",
		"interval":  "5m",
		"tolerance": 100,
	})

	for _, e := range exits {
		tag := "exit-" + e.Name
		exitCopy := e
		outbounds = append(outbounds, buildExitOutbound(tag, &exitCopy))
	}

	if len(exits) > 0 {
		exitTags := make([]string, len(exits))
		for i, e := range exits {
			exitTags[i] = "exit-" + e.Name
		}
		// Daily sticky: pick one exit per day, rotate daily
		// All AI traffic goes through the same exit IP for the entire day
		todayExit := dailyStickyExit(exitTags)
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "residential",
			"outbounds": exitTags,
			"default":   todayExit,
		})
	}

	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
	)

	return outbounds
}

// dailyStickyExit picks the first exit tag as default.
// Sticky: stays on the same exit until it goes offline.
// When an exit goes offline, reconcile removes it from the list,
// and the next available exit becomes the new default.
func dailyStickyExit(exitTags []string) string {
	if len(exitTags) == 0 {
		return ""
	}
	return exitTags[0]
}

// BuildEntryRoute builds the route section for an entry node config.
// extraSocksRules is optional (for SOCKS5 H800 direct routing).
func BuildEntryRoute(exits []ExitInfo, socks5Port int, socks5DirectIPs []string) map[string]interface{} {
	rules := []interface{}{
		map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
		map[string]interface{}{"ip_is_private": true, "action": "route", "outbound": "direct"},
		map[string]interface{}{"rule_set": []string{"geoip-cn", "geosite-cn"}, "action": "route", "outbound": "direct"},
	}

	if len(exits) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain":   AIResidentialDomains,
			"action":   "route",
			"outbound": "residential",
		})
	}

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
		"rule_set":               BuildRuleSetDefs(),
		"final":                  "gia-auto",
		"auto_detect_interface":  true,
		"default_domain_resolver": "system", // resolve outbound server domains via system DNS, not via proxy
	}
}
