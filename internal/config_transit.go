package internal

// GenerateTransitConfig generates a sing-box config for a transit node.
// Transit nodes accept inbound connections (VLESS Reality + Hy2) and route to direct outbound.
// No sniff needed: transit is a pure relay (final→direct), no routing decisions are made.
func GenerateTransitConfig(p *InstallParams) *SBConfig {
	ignoreBW := true

	// Hy2 TLS: default to the unified cert path; certbot file or ACME on opt-in.
	hy2TLS := &SBTLS{
		Enabled: true,
		ALPN:    []string{"h3"},
	}
	if p.CertFile != "" {
		hy2TLS.CertificatePath = p.CertFile
		hy2TLS.KeyPath = p.KeyFile
		if p.Domain != "" {
			hy2TLS.ServerName = p.Domain
		}
	} else if p.UseACME {
		rootDomain := ExtractRootDomain(p.Domain)
		hy2TLS.ACME = &SBACME{Domain: []string{p.Domain}, Email: "admin@" + rootDomain}
	} else {
		hy2TLS.CertificatePath = UnifiedCertPath
		hy2TLS.KeyPath = UnifiedCertKeyPath
		if p.Domain != "" {
			hy2TLS.ServerName = p.Domain
		}
	}

	return &SBConfig{
		Log: SBLog{Level: "warn", Timestamp: true},
		DNS: &SBDNS{
			Servers: []SBDNSServer{
				{Type: "tls", Tag: "remote", Server: "8.8.8.8"},
			},
		},
		Inbounds: []SBInbound{
			{
				Type: "vless", Tag: "vless-in",
				Listen: "::", ListenPort: p.VlessPort,
				Users: []SBUser{{Name: "relay", UUID: p.UUID, Flow: "xtls-rprx-vision"}},
				TLS: &SBTLS{
					Enabled:    true,
					ServerName: "www.walmart.com",
					Reality: &SBReality{
						Enabled:    true,
						Handshake:  SBHandshake{Server: "www.walmart.com", ServerPort: 443},
						PrivateKey: p.PrivateKey,
						ShortID:    []string{p.ShortID},
					},
				},
			},
			{
				Type: "hysteria2", Tag: "hy2-in",
				Listen: "::", ListenPort: p.Hy2Port,
				Users:                 []SBUser{{Name: "relay", Password: p.Hy2Pass}},
				IgnoreClientBandwidth: &ignoreBW,
				Obfs:                  &SBObfs{Type: "salamander", Password: p.ObfsPass},
				TLS:                   hy2TLS,
			},
		},
		Outbounds: []SBOutbound{{Type: "direct", Tag: "direct"}},
	}
}
