package internal

// GenerateExitConfig generates a sing-box config for an exit node (家宽).
// Exit only needs VLESS Reality inbound — no domain, no ACME, no certificate.
// Transit connects to Exit via VLESS Reality using IP address directly.
// Optionally adds a SOCKS5 inbound for direct outbound access.
func GenerateExitConfig(p *InstallParams) *SBConfig {
	inbounds := []SBInbound{
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
	}

	if p.Socks5Port > 0 {
		inbounds = append(inbounds, SBInbound{
			Type: "socks", Tag: "socks-in",
			Listen: "::", ListenPort: p.Socks5Port,
			Users: []SBUser{{Username: p.Socks5User, Password: p.Socks5Pass}},
		})
	}

	return &SBConfig{
		Log: SBLog{Level: "warn", Timestamp: true},
		DNS: &SBDNS{
			Servers: []SBDNSServer{
				{Type: "tls", Tag: "remote", Server: "8.8.8.8"},
			},
		},
		Inbounds:  inbounds,
		Outbounds: []SBOutbound{{Type: "direct", Tag: "direct"}},
	}
}
