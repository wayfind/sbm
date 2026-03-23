package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DeployDir = "/etc/sing-box"
	CertBase  = "/.local/share/certmagic/certificates/acme-v02.api.letsencrypt.org-directory"
)

func ConfigPath() string   { return filepath.Join(DeployDir, "config.json") }
func MetadataPath() string { return filepath.Join(DeployDir, "metadata.json") }

type Metadata struct {
	Domain    string `json:"domain"`
	VlessPort int    `json:"vless_port"`
	Hy2Port   int    `json:"hy2_port"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
	UUID      string `json:"uuid"`
	SubPort   int    `json:"sub_port"`
}

func LoadMetadata() (*Metadata, error) {
	data, err := os.ReadFile(MetadataPath())
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}
	return &m, nil
}

func (m *Metadata) Save() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetadataPath(), data, 0600)
}

func LoadSingBoxConfig() (*sbConfig, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg sbConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

type InstallParams struct {
	Domain     string
	VlessPort  int
	Hy2Port    int
	SubPort    int
	UUID       string
	PrivateKey string
	PublicKey  string
	ShortID    string
	Hy2Pass    string
	ObfsPass   string
}

// sing-box config structs for type-safe JSON generation

type sbLog struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type sbUser struct {
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
}

type sbHandshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type sbReality struct {
	Enabled    bool        `json:"enabled"`
	Handshake  sbHandshake `json:"handshake"`
	PrivateKey string      `json:"private_key"`
	ShortID    []string    `json:"short_id"`
}

type sbACME struct {
	Domain []string `json:"domain"`
	Email  string   `json:"email"`
}

type sbTLS struct {
	Enabled    bool       `json:"enabled"`
	ServerName string     `json:"server_name,omitempty"`
	Reality    *sbReality `json:"reality,omitempty"`
	ACME       *sbACME    `json:"acme,omitempty"`
	ALPN       []string   `json:"alpn,omitempty"`
}

type sbObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type sbInbound struct {
	Type                   string   `json:"type"`
	Tag                    string   `json:"tag"`
	Listen                 string   `json:"listen"`
	ListenPort             int      `json:"listen_port"`
	Users                  []sbUser `json:"users"`
	TLS                    sbTLS    `json:"tls"`
	IgnoreClientBandwidth  *bool    `json:"ignore_client_bandwidth,omitempty"`
	Obfs                   *sbObfs  `json:"obfs,omitempty"`
}

type sbOutbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type sbConfig struct {
	Log       sbLog        `json:"log"`
	Inbounds  []sbInbound  `json:"inbounds"`
	Outbounds []sbOutbound `json:"outbounds"`
}

func GenerateConfig(p *InstallParams) *sbConfig {
	rootDomain := extractRootDomain(p.Domain)
	ignoreBW := true
	return &sbConfig{
		Log: sbLog{Level: "warn", Timestamp: true},
		Inbounds: []sbInbound{
			{
				Type: "vless", Tag: "vless-in",
				Listen: "::", ListenPort: p.VlessPort,
				Users: []sbUser{{UUID: p.UUID, Flow: "xtls-rprx-vision"}},
				TLS: sbTLS{
					Enabled:    true,
					ServerName: "www.walmart.com",
					Reality: &sbReality{
						Enabled:    true,
						Handshake:  sbHandshake{Server: "www.walmart.com", ServerPort: 443},
						PrivateKey: p.PrivateKey,
						ShortID:    []string{p.ShortID},
					},
				},
			},
			{
				Type: "hysteria2", Tag: "hy2-in",
				Listen: "::", ListenPort: p.Hy2Port,
				Users:                 []sbUser{{Password: p.Hy2Pass}},
				IgnoreClientBandwidth: &ignoreBW,
				Obfs:                  &sbObfs{Type: "salamander", Password: p.ObfsPass},
				TLS: sbTLS{
					Enabled: true,
					ACME:    &sbACME{Domain: []string{p.Domain}, Email: "admin@" + rootDomain},
					ALPN:    []string{"h3"},
				},
			},
		},
		Outbounds: []sbOutbound{{Type: "direct", Tag: "direct"}},
	}
}

func SaveConfig(cfg *sbConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0600)
}

func extractRootDomain(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "." + parts[len(parts)-1]
	}
	return domain
}

func CertPath(domain string) string {
	return filepath.Join(CertBase, domain, domain+".crt")
}

func CertKeyPath(domain string) string {
	return filepath.Join(CertBase, domain, domain+".key")
}
