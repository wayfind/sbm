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

	rootDomain := ExtractRootDomain(p.Domain)

	// --- Inbounds ---
	inbounds := []interface{}{}

	// VLESS Reality inbound (client-facing)
	inbounds = append(inbounds, map[string]interface{}{
		"type": "vless", "tag": "vless-in",
		"listen": "::", "listen_port": p.VlessPort,
		"users": []map[string]interface{}{
			{"uuid": p.UUID, "flow": "xtls-rprx-vision"},
		},
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

	// Hysteria2 inbound (client-facing)
	ignoreBW := true
	inbounds = append(inbounds, map[string]interface{}{
		"type": "hysteria2", "tag": "hy2-in",
		"listen": "::", "listen_port": p.Hy2Port,
		"users":                    []map[string]interface{}{{"password": p.Hy2Pass}},
		"ignore_client_bandwidth":  ignoreBW,
		"obfs":                     map[string]interface{}{"type": "salamander", "password": p.ObfsPass},
		"tls": map[string]interface{}{
			"enabled": true,
			"acme":    map[string]interface{}{"domain": []string{p.Domain}, "email": "admin@" + rootDomain},
			"alpn":    []string{"h3"},
		},
	})

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

	// --- Outbounds ---
	outbounds := []interface{}{}
	transitTags := []string{}

	// Transit (GIA) outbounds
	for _, t := range p.Transits {
		tag := "transit-" + t.Name
		transitTags = append(transitTags, tag)
		outbounds = append(outbounds, BuildProxyOutbound(tag, &t))
	}

	// URLTest group for transit auto-selection
	outbounds = append(outbounds, map[string]interface{}{
		"type":      "urltest",
		"tag":       "gia-auto",
		"outbounds": transitTags,
		"url":       "https://www.gstatic.com/generate_204",
		"interval":  "5m",
		"tolerance": 100,
	})

	// Exit (residential) outbounds — chained through transit
	for _, e := range p.Exits {
		tag := "exit-" + e.Name
		outbounds = append(outbounds, buildExitOutbound(tag, &e))
	}

	// Residential chain selector (if we have exits)
	if len(p.Exits) > 0 {
		exitTags := make([]string, len(p.Exits))
		for i, e := range p.Exits {
			exitTags[i] = "exit-" + e.Name
		}
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "urltest",
			"tag":       "residential",
			"outbounds": exitTags,
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "5m",
			"tolerance": 200,
		})
	}

	// Direct + DNS outbounds
	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
		map[string]interface{}{"type": "dns", "tag": "dns-out"},
		map[string]interface{}{"type": "block", "tag": "block"},
	)

	// --- DNS ---
	dns := map[string]interface{}{
		"servers": []map[string]interface{}{
			{"tag": "remote", "type": "tls", "server": "8.8.8.8", "detour": "gia-auto"},
			{"tag": "local", "type": "https", "server": "223.5.5.5"},
		},
		"rules": []map[string]interface{}{
			{"rule_set": "geosite-cn", "server": "local"},
		},
	}

	// --- Route rules ---
	rules := []interface{}{
		map[string]interface{}{"protocol": "dns", "action": "route", "outbound": "dns-out"},
		map[string]interface{}{"ip_is_private": true, "action": "route", "outbound": "direct"},
		// CN traffic → direct
		map[string]interface{}{"rule_set": []string{"geoip-cn", "geosite-cn"}, "action": "route", "outbound": "direct"},
	}

	// AI services → residential (if exits configured)
	if len(p.Exits) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain": AIResidentialDomains,
			"action": "route",
			"outbound": "residential",
		})
	}

	// SOCKS5 H800 direct routing rules
	if p.Socks5Port > 0 && len(p.Socks5DirectIPs) > 0 {
		rules = append(rules, map[string]interface{}{
			"inbound":    []string{"socks-in"},
			"ip_cidr":    p.Socks5DirectIPs,
			"action":     "route",
			"outbound":   "direct",
		})
	}

	// Default overseas → gia-auto
	route := map[string]interface{}{
		"rules": rules,
		"rule_set": []map[string]interface{}{
			{"tag": "geosite-cn", "type": "remote", "format": "binary",
				"url": RuleSetGeositeCN, "download_detour": "gia-auto"},
			{"tag": "geoip-cn", "type": "remote", "format": "binary",
				"url": RuleSetGeoipCN, "download_detour": "gia-auto"},
		},
		"final":                "gia-auto",
		"auto_detect_interface": true,
	}

	// --- Experimental ---
	experimental := map[string]interface{}{
		"cache_file": map[string]interface{}{
			"enabled":    true,
			"store_rdrc": true,
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
	if e.ViaTransit != "" {
		out["detour"] = "transit-" + e.ViaTransit
	}
	return out
}
