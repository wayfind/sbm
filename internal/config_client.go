package internal

// ClientParams holds parameters for generating client sing-box configuration.
type ClientParams struct {
	Platform string // "linux", "macos", "windows", "openwrt"

	// Proxy outbounds (fetched from upstream subscription or manually specified)
	Proxies []ClientProxy
}

// ClientProxy is an alias for TransitInfo in client context.
type ClientProxy = TransitInfo

// GenerateClientConfig generates a complete sing-box client configuration
// with TUN inbound, fake-ip DNS, and CN direct routing.
func GenerateClientConfig(p *ClientParams) map[string]interface{} {
	// --- Inbounds ---
	tun := map[string]interface{}{
		"type": "tun",
		"tag":  "tun-in",
		"address": []string{
			"172.19.0.1/30",
			"fdfe:dcba:9876::1/126",
		},
		"auto_route":   true,
		"strict_route":  true,
		"sniff":         true,
		"sniff_override_destination": true,
	}

	// Platform-specific TUN adjustments
	switch p.Platform {
	case "windows":
		tun["stack"] = "gvisor"
	case "openwrt":
		tun["auto_route"] = false
		tun["interface_name"] = "sbm0"
		tun["stack"] = "system"
	default: // linux, macos
		tun["stack"] = "mixed"
	}

	inbounds := []interface{}{tun}

	// --- Outbounds ---
	outbounds := []interface{}{}
	proxyTags := []string{}

	for i := range p.Proxies {
		tag := "proxy-" + p.Proxies[i].Name
		proxyTags = append(proxyTags, tag)
		outbounds = append(outbounds, BuildProxyOutbound(tag, &p.Proxies[i]))
	}

	// URLTest auto selector
	if len(proxyTags) > 0 {
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "urltest",
			"tag":       "proxy",
			"outbounds": proxyTags,
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "5m",
			"tolerance": 100,
		})
	}

	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
		map[string]interface{}{"type": "dns", "tag": "dns-out"},
		map[string]interface{}{"type": "block", "tag": "block"},
	)

	// --- DNS ---
	proxyDetour := "proxy"
	if len(proxyTags) == 0 {
		proxyDetour = "direct"
	}

	dns := map[string]interface{}{
		"servers": []map[string]interface{}{
			{"tag": "google", "type": "tls", "server": "8.8.8.8", "detour": proxyDetour},
			{"tag": "local", "type": "udp", "server": "223.5.5.5", "detour": "direct"},
			{"tag": "remote", "type": "fakeip",
				"inet4_range": "198.18.0.0/15",
				"inet6_range": "fc00::/18",
			},
		},
		"rules": []map[string]interface{}{
			{"rule_set": "geosite-cn", "server": "local"},
			{"query_type": []string{"A", "AAAA"}, "server": "remote"},
		},
		"independent_cache": true,
	}

	// --- Route ---
	defaultOut := "proxy"
	if len(proxyTags) == 0 {
		defaultOut = "direct"
	}

	route := map[string]interface{}{
		"rules": []interface{}{
			map[string]interface{}{"action": "sniff"},
			map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
			map[string]interface{}{"ip_is_private": true, "action": "route", "outbound": "direct"},
			map[string]interface{}{"rule_set": []string{"geoip-cn", "geosite-cn"}, "action": "route", "outbound": "direct"},
		},
		"rule_set": []map[string]interface{}{
			{"tag": "geosite-cn", "type": "remote", "format": "binary",
				"url": RuleSetGeositeCN, "download_detour": defaultOut},
			{"tag": "geoip-cn", "type": "remote", "format": "binary",
				"url": RuleSetGeoipCN, "download_detour": defaultOut},
		},
		"final":                  defaultOut,
		"auto_detect_interface":  true,
		"default_domain_resolver": "local",
	}

	// --- Experimental ---
	experimental := map[string]interface{}{
		"cache_file": map[string]interface{}{
			"enabled":    true,
			"store_rdrc": true,
			"store_fakeip": true,
		},
	}

	return map[string]interface{}{
		"log":          map[string]interface{}{"level": "info", "timestamp": true},
		"dns":          dns,
		"inbounds":     inbounds,
		"outbounds":    outbounds,
		"route":        route,
		"experimental": experimental,
	}
}

