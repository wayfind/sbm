package internal

// GenerateExitConfig generates a sing-box config for an exit node (家宽).
// Exit only needs VLESS Reality inbound — no domain, no ACME, no certificate.
// Transit connects to Exit via VLESS Reality using IP address directly.
func GenerateExitConfig(p *InstallParams) *SBConfig {
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
				Users: []SBUser{{Name: "relay", UUID: p.UUID}},
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
		},
		Outbounds: []SBOutbound{{Type: "direct", Tag: "direct"}},
	}
}
