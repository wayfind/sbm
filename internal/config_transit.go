package internal

// GenerateTransitConfig generates a sing-box config for a transit node.
// Transit nodes accept inbound connections (VLESS Reality + Hy2) and route to direct outbound.
func GenerateTransitConfig(p *InstallParams) *SBConfig {
	rootDomain := ExtractRootDomain(p.Domain)
	ignoreBW := true
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
				TLS: &SBTLS{
					Enabled: true,
					ACME:    &SBACME{Domain: []string{p.Domain}, Email: "admin@" + rootDomain},
					ALPN:    []string{"h3"},
				},
			},
		},
		Outbounds: []SBOutbound{{Type: "direct", Tag: "direct"}},
	}
}
