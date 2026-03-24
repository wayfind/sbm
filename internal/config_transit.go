package internal

// GenerateTransitConfig generates a sing-box config for a transit node.
// Transit nodes accept inbound connections (VLESS Reality + Hy2) and route to direct outbound.
// No sniff needed: transit is a pure relay (final→direct), no routing decisions are made.
func GenerateTransitConfig(p *InstallParams) *SBConfig {
	ignoreBW := true

	// Hy2 TLS: prefer certbot certificate over ACME
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
	} else {
		rootDomain := ExtractRootDomain(p.Domain)
		hy2TLS.ACME = &SBACME{Domain: []string{p.Domain}, Email: "admin@" + rootDomain}
	}

	return &SBConfig{
		Log: SBLog{Level: "warn", Timestamp: true},
		Inbounds: []SBInbound{
			{
				Type: "vless", Tag: "vless-in",
				Listen: "::", ListenPort: p.VlessPort,
				Users: []SBUser{{UUID: p.UUID, Flow: "xtls-rprx-vision"}},
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
				Users:                 []SBUser{{Password: p.Hy2Pass}},
				IgnoreClientBandwidth: &ignoreBW,
				Obfs:                  &SBObfs{Type: "salamander", Password: p.ObfsPass},
				TLS:                   hy2TLS,
			},
		},
		Outbounds: []SBOutbound{{Type: "direct", Tag: "direct"}},
	}
}
