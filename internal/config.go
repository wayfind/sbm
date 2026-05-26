package internal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	DeployDir = "/etc/sing-box"
	CertBase  = "/.local/share/certmagic/certificates/acme-v02.api.letsencrypt.org-directory"
)

func ConfigPath() string   { return filepath.Join(DeployDir, "config.json") }
func MetadataPath() string { return filepath.Join(DeployDir, "metadata.json") }

// --- Node roles ---

type NodeRole string

const (
	RoleClient  NodeRole = "client"
	RoleEntry   NodeRole = "entry"
	RoleTransit NodeRole = "transit"
	RoleExit    NodeRole = "exit"
)

func (r NodeRole) Valid() bool {
	switch r {
	case RoleClient, RoleEntry, RoleTransit, RoleExit:
		return true
	}
	return false
}

// --- Metadata v2 ---

type InboundMeta struct {
	Tag       string `json:"tag"`
	Protocol  string `json:"protocol"`             // vless, hysteria2, shadowsocks, socks
	Port      int    `json:"port"`
	Transport string `json:"transport"`             // tcp, udp
	PublicKey string `json:"public_key,omitempty"`   // vless reality
	ShortID   string `json:"short_id,omitempty"`     // vless reality
	SNI       string `json:"sni,omitempty"`          // vless reality server_name
	UUID      string `json:"uuid,omitempty"`         // vless
	Username  string `json:"username,omitempty"`      // socks auth
	Password  string `json:"password,omitempty"`     // hy2, ss, socks auth
	ObfsPass  string `json:"obfs_password,omitempty"` // hy2 salamander
	ForExit   string `json:"for_exit,omitempty"`      // non-empty = per-exit channel inbound
}

type UpstreamMeta struct {
	Name     string `json:"name"`
	Address  string `json:"address"`           // domain or IP
	SubURL   string `json:"sub_url,omitempty"` // subscription URL for fetching inbound info
	Priority int    `json:"priority"`          // lower = preferred
}

type BWGConfig struct {
	VEID   string `json:"veid"`
	APIKey string `json:"api_key"`
}

type Metadata struct {
	Version int      `json:"version"`
	Role    NodeRole `json:"role"`
	NodeID  string   `json:"node_id"`
	Name    string   `json:"name,omitempty"`
	Domain  string   `json:"domain,omitempty"`

	// PublicHost overrides Domain in subscription URIs and sub_url display only.
	// TLS server_name, ACME, and CF topology still use Domain. Use this when the
	// client-facing address differs from the certificate domain (e.g. NAT'd node
	// reusing another node's cert).
	PublicHost string `json:"public_host,omitempty"`

	Inbounds  []InboundMeta  `json:"inbounds,omitempty"`
	Upstreams []UpstreamMeta `json:"upstreams,omitempty"`

	// UpstreamsMu protects concurrent read/write of Upstreams (reconcile vs topology reporter).
	UpstreamsMu sync.RWMutex `json:"-"`

	SubPort     int    `json:"sub_port,omitempty"`
	HTTPSubPort int    `json:"http_sub_port,omitempty"` // optional plaintext HTTP listener for /sub/ (avoids IP-based HTTPS browser warning)
	APIToken    string `json:"api_token,omitempty"`

	BWG *BWGConfig `json:"bwg,omitempty"`

	// Cloudflare Workers KV topology registry
	CFWorkerURL string `json:"cf_worker_url,omitempty"`
	CFAPIKey    string `json:"cf_api_key,omitempty"`

	// Per-exit channel state (persisted for stability across restarts)
	ExitPorts      map[string]int    `json:"exit_ports,omitempty"`       // transit: exit_name → per-exit inbound port
	ExitAuth       map[string]string `json:"exit_auth,omitempty"`        // transit: exit_name → per-exit socks auth password
	ExitSocksPorts map[string]int    `json:"exit_socks_ports,omitempty"` // entry: exit_name → localhost socks5 port
	ExitVlessPorts map[string]int    `json:"exit_vless_ports,omitempty"` // entry: exit_name → per-exit vless inbound port
	ExitHy2Ports   map[string]int    `json:"exit_hy2_ports,omitempty"`   // entry: exit_name → per-exit hy2 inbound port

	// hy2 brutal congestion control per transit (entry-only).
	// Key = transit name. When set, entry's hy2 outbound to that transit
	// declares up_mbps/down_mbps to claim bandwidth from QoS-throttled paths.
	Hy2Brutal map[string]Hy2BrutalConfig `json:"hy2_brutal,omitempty"`

	// SBM binary version that last generated/rebuilt the config.
	// Used by reconcile to detect schema changes after sbm upgrade.
	SBMVersion string `json:"sbm_version,omitempty"`

	// Transient field: Reality private key recovered from old config (not serialized)
	PrivateKey string `json:"-"`
}

// Hy2BrutalConfig is the per-upstream brutal congestion control settings.
type Hy2BrutalConfig struct {
	UpMbps   int `json:"up_mbps,omitempty"`
	DownMbps int `json:"down_mbps,omitempty"`
}

// LoadMetadata loads metadata.json with automatic v0→v2 migration.
func LoadMetadata() (*Metadata, error) {
	data, err := os.ReadFile(MetadataPath())
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	// Probe version field
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	if probe.Version >= 2 {
		var m Metadata
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse metadata v2: %w", err)
		}
		return &m, nil
	}

	// v0 migration
	return migrateV0(data)
}

// metadataV0 is the legacy flat structure.
type metadataV0 struct {
	Domain    string `json:"domain"`
	VlessPort int    `json:"vless_port"`
	Hy2Port   int    `json:"hy2_port"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
	UUID      string `json:"uuid"`
	SubPort   int    `json:"sub_port"`
}

func migrateV0(data []byte) (*Metadata, error) {
	var v0 metadataV0
	if err := json.Unmarshal(data, &v0); err != nil {
		return nil, fmt.Errorf("parse metadata v0: %w", err)
	}

	nodeID, err := GenerateNodeID()
	if err != nil {
		return nil, fmt.Errorf("generate node_id: %w", err)
	}

	m := &Metadata{
		Version: 2,
		Role:    RoleTransit,
		NodeID:  nodeID,
		Domain:  v0.Domain,
		SubPort: v0.SubPort,
		Inbounds: []InboundMeta{
			{
				Tag:       "vless-in",
				Protocol:  "vless",
				Port:      v0.VlessPort,
				Transport: "tcp",
				PublicKey: v0.PublicKey,
				ShortID:   v0.ShortID,
				UUID:      v0.UUID,
			},
			{
				Tag:       "hy2-in",
				Protocol:  "hysteria2",
				Port:      v0.Hy2Port,
				Transport: "udp",
			},
		},
	}

	// Note: hy2 password and obfs_password are in sing-box config, not in v0 metadata.
	// They'll be populated from sing-box config if needed, or left empty for subscription
	// generation which reads from the config file directly.

	// Save migrated metadata
	if err := m.Save(); err != nil {
		return nil, fmt.Errorf("save migrated metadata: %w", err)
	}

	return m, nil
}

func GenerateNodeID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (m *Metadata) Save() error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(MetadataPath(), data, 0600)
}

// EffectiveHost returns the client-facing host: PublicHost if set, otherwise Domain.
// Use this in subscription URIs and sub_url display. Do not use for TLS / ACME / CF.
func (m *Metadata) EffectiveHost() string {
	if m.PublicHost != "" {
		return m.PublicHost
	}
	return m.Domain
}

// --- sing-box config read ---

func LoadSingBoxConfig() (*SBConfig, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg SBConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

func SaveConfig(cfg *SBConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0600)
}

// SaveRawConfig saves an arbitrary config (used by entry/client which need richer JSON).
func SaveRawConfig(cfg interface{}) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0600)
}

// --- sing-box config structs ---

type SBLog struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type SBUser struct {
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Password string `json:"password,omitempty"`
}

type SBHandshake struct {
	Server     string `json:"server"`
	ServerPort int    `json:"server_port"`
}

type SBReality struct {
	Enabled    bool        `json:"enabled"`
	Handshake  SBHandshake `json:"handshake"`
	PrivateKey string      `json:"private_key"`
	ShortID    []string    `json:"short_id"`
}

type SBACME struct {
	Domain []string `json:"domain"`
	Email  string   `json:"email"`
}

type SBTLS struct {
	Enabled         bool       `json:"enabled"`
	ServerName      string     `json:"server_name,omitempty"`
	CertificatePath string     `json:"certificate_path,omitempty"`
	KeyPath         string     `json:"key_path,omitempty"`
	Reality         *SBReality `json:"reality,omitempty"`
	ACME            *SBACME    `json:"acme,omitempty"`
	ALPN            []string   `json:"alpn,omitempty"`
}

type SBObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type SBInbound struct {
	Type                  string   `json:"type"`
	Tag                   string   `json:"tag"`
	Listen                string   `json:"listen"`
	ListenPort            int      `json:"listen_port"`
	Users                 []SBUser `json:"users,omitempty"`
	TLS                   *SBTLS   `json:"tls,omitempty"`
	IgnoreClientBandwidth *bool    `json:"ignore_client_bandwidth,omitempty"`
	Obfs                  *SBObfs  `json:"obfs,omitempty"`
}

type SBOutbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type SBDNSServer struct {
	Type   string `json:"type"`
	Tag    string `json:"tag"`
	Server string `json:"server,omitempty"`
}

type SBDNS struct {
	Servers []SBDNSServer `json:"servers"`
}

type SBConfig struct {
	Log       SBLog        `json:"log"`
	DNS       *SBDNS       `json:"dns,omitempty"`
	Inbounds  []SBInbound  `json:"inbounds"`
	Outbounds []SBOutbound `json:"outbounds"`
}

// --- Install params (used by transit/exit install flows) ---

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
	CertFile   string // certbot certificate path (alternative to ACME)
	KeyFile    string // certbot key path
	Socks5Port int
	Socks5User string
	Socks5Pass string
}

// --- Helpers ---

func ExtractRootDomain(domain string) string {
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
