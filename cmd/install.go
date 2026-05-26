package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sing-box-manager/internal"
	"strings"
	"time"
)

type InstallFlags struct {
	Role          string
	Domain        string
	PublicHost    string
	VlessPort     int
	Hy2Port       int
	SubPort       int
	HTTPSubPort   int
	SNI           string
	Force         bool
	UpstreamsFile string
	CertFile      string
	KeyFile       string
	Name          string
	CFWorkerURL   string
	CFAPIKey      string
	BWGVeid       string
	BWGAPIKey     string
}

// effectivePublicHost returns the client-facing host: PublicHost if set, else Domain.
// Used in sub_url display and install result output.
func effectivePublicHost(f *InstallFlags) string {
	if f.PublicHost != "" {
		return f.PublicHost
	}
	return f.Domain
}

// cryptoMaterials holds generated keys/passwords for a new installation.
type cryptoMaterials struct {
	PrivateKey string
	PublicKey  string
	UUID       string
	ShortID    string
	Hy2Pass    string
	ObfsPass   string
}

func RunInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var f InstallFlags
	fs.StringVar(&f.Role, "role", "transit", "节点角色: transit, exit, entry, client")
	fs.StringVar(&f.Domain, "domain", "", "域名 (必选)")
	fs.StringVar(&f.PublicHost, "public-host", "", "客户端连接用的主机/IP，订阅 URL 也用它；空则回退到 --domain")
	fs.IntVar(&f.VlessPort, "vless-port", 0, "VLESS 端口 (默认随机)")
	fs.IntVar(&f.Hy2Port, "hy2-port", 0, "Hysteria2 端口 (默认随机)")
	fs.IntVar(&f.SubPort, "sub-port", 20033, "订阅服务端口")
	fs.IntVar(&f.HTTPSubPort, "http-sub-port", 0, "明文 HTTP 订阅端口（可选，仅暴露 /sub/，规避 IP 证书警告）")
	fs.StringVar(&f.SNI, "sni", "", "VLESS Reality SNI (entry 默认 cloud.tencent.com, transit 默认 www.walmart.com)")
	fs.BoolVar(&f.Force, "force", false, "跳过替换确认")
	fs.StringVar(&f.UpstreamsFile, "upstreams", "", "上游节点配置文件 (entry 必选)")
	fs.StringVar(&f.CertFile, "cert", "", "TLS 证书文件路径 (替代 ACME)")
	fs.StringVar(&f.KeyFile, "key", "", "TLS 私钥文件路径 (替代 ACME)")
	fs.StringVar(&f.Name, "name", "", "节点名称 (拓扑显示用)")
	fs.StringVar(&f.CFWorkerURL, "cf-url", "", "Cloudflare Worker URL (拓扑上报)")
	fs.StringVar(&f.CFAPIKey, "cf-key", "", "Cloudflare API Key (拓扑认证)")
	fs.StringVar(&f.BWGVeid, "bwg-veid", "", "搬瓦工 VEID (带宽监控)")
	fs.StringVar(&f.BWGAPIKey, "bwg-api-key", "", "搬瓦工 API Key (带宽监控)")
	fs.Parse(args)

	role := internal.NodeRole(f.Role)
	if !role.Valid() {
		return fmt.Errorf("无效角色: %s (可选: transit, exit, entry, client)", f.Role)
	}
	if f.Domain == "" && role != internal.RoleExit {
		if internal.JSONMode {
			return fmt.Errorf("域名不能为空 (--domain required)")
		}
		f.Domain = promptString("请输入域名")
		if f.Domain == "" {
			return fmt.Errorf("域名不能为空")
		}
	}
	// Exit node: auto-detect public IP if no domain given
	if f.Domain == "" && role == internal.RoleExit {
		if ip := detectPublicIP(); ip != "" {
			f.Domain = ip
			humanPrint("自动检测公网 IP: %s\n", ip)
		}
	}
	// --json implies --force (AI mode = non-interactive)
	if internal.JSONMode {
		f.Force = true
	}

	switch role {
	case internal.RoleTransit, internal.RoleExit:
		return installServerNode(role, &f)
	case internal.RoleEntry:
		return installEntryNode(&f)
	case internal.RoleClient:
		return fmt.Errorf("client 配置生成请使用 sbm client gen")
	default:
		return fmt.Errorf("未知角色: %s", role)
	}
}

// --- Shared install helpers ---

// prepareInstall checks prerequisites, handles existing deployment backup, and creates deploy dir.
// Returns: ok (false if cancelled), oldMeta (if existing deployment found), err.
func prepareInstall(f *InstallFlags) (bool, *internal.Metadata, error) {
	if _, err := exec.LookPath("sing-box"); err != nil {
		return false, nil, fmt.Errorf("sing-box not found，请先安装 sing-box")
	}

	var savedMeta *internal.Metadata

	if _, err := os.Stat(internal.DeployDir); err == nil {
		if !f.Force {
			fmt.Printf("检测到现有部署: %s\n", internal.DeployDir)
			if !promptConfirm("是否替换现有部署？") {
				fmt.Println("取消安装")
				return false, nil, nil
			}
		}
		// Save metadata and config before backup
		if oldMeta, err := internal.LoadMetadata(); err == nil {
			savedMeta = oldMeta
			humanPrint("清理旧防火墙规则...\n")
			for _, inb := range oldMeta.Inbounds {
				internal.FirewallRemovePort(inb.Port, inb.Transport)
			}
			oldSubPort := oldMeta.SubPort
			if oldSubPort == 0 {
				oldSubPort = 20033
			}
			internal.FirewallRemovePort(oldSubPort, "tcp")
			internal.FirewallReload()
		}
		// Read Reality private key from old config before backup
		if savedMeta != nil {
			if privKey := extractPrivateKeyFromConfig(); privKey != "" {
				savedMeta.PrivateKey = privKey
			}
		}
		backup := fmt.Sprintf("/etc/sing-box.backup-%s", time.Now().Format("20060102-150405"))
		humanPrint("备份到: %s\n", backup)
		internal.SystemctlOp("stop", "sing-box", "sbm-server")
		internal.SystemctlOp("disable", "sing-box", "sbm-server")
		os.Rename(internal.DeployDir, backup)
	}
	os.MkdirAll(internal.DeployDir, 0755)
	return true, savedMeta, nil
}

func generateCryptoMaterials() (*cryptoMaterials, error) {
	privKey, pubKey, err := internal.GenerateRealityKeypair()
	if err != nil {
		return nil, err
	}
	uuid, err := internal.GenerateUUID()
	if err != nil {
		return nil, err
	}
	shortID, err := internal.GenerateShortID()
	if err != nil {
		return nil, err
	}
	hy2Pass, err := internal.GeneratePassword(12)
	if err != nil {
		return nil, err
	}
	obfsPass, err := internal.GeneratePassword(18)
	if err != nil {
		return nil, err
	}
	return &cryptoMaterials{
		PrivateKey: privKey, PublicKey: pubKey,
		UUID: uuid, ShortID: shortID,
		Hy2Pass: hy2Pass, ObfsPass: obfsPass,
	}, nil
}

func assignPorts(f *InstallFlags, role internal.NodeRole) {
	// Entry inbound ports start above SubPort to avoid conflicts
	min, max := 10000, 60000
	if role == internal.RoleEntry {
		min, max = f.SubPort+2, 29999
	}
	if f.VlessPort == 0 {
		f.VlessPort = findAvailablePort(min, max)
	}
	if f.Hy2Port == 0 {
		f.Hy2Port = findAvailablePort(min, max, f.VlessPort)
	}
}

// finishInstall installs systemd units, cron, firewall rules, starts services.
// For transit/entry: opens Hy2 port, ACME port 80, waits for cert.
// For exit: only opens VLESS port (no ACME needed).
func finishInstall(f *InstallFlags, role internal.NodeRole, extraTCPPorts ...int) error {
	sbmPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	sbmPath, err = filepath.Abs(sbmPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}

	if err := internal.InstallSystemdUnits(sbmPath); err != nil {
		return fmt.Errorf("install systemd units: %w", err)
	}
	if err := installRenewCron(sbmPath); err != nil {
		return fmt.Errorf("install renew cron: %w", err)
	}

	internal.FirewallAddPort(f.VlessPort, "tcp")
	if role != internal.RoleExit {
		internal.FirewallAddPort(f.Hy2Port, "udp")
	}
	if role == internal.RoleEntry {
		internal.FirewallAddPort(f.SubPort, "tcp")
		if f.HTTPSubPort > 0 {
			internal.FirewallAddPort(f.HTTPSubPort, "tcp")
		}
	}
	for _, p := range extraTCPPorts {
		internal.FirewallAddPort(p, "tcp")
	}

	if role != internal.RoleExit {
		// ACME needs port 80 temporarily
		internal.FirewallTempAddPort(80, "tcp")
	}
	internal.FirewallReload()

	internal.SystemctlOp("enable", "--now", "sing-box", "sbm-server")

	if role != internal.RoleExit {
		humanPrint("等待 ACME 证书签发...\n")
		time.Sleep(10 * time.Second)
		internal.FirewallTempRemovePort(80, "tcp")
	}
	return nil
}

func buildBaseInboundMetas(f *InstallFlags, cm *cryptoMaterials) []internal.InboundMeta {
	return []internal.InboundMeta{
		{
			Tag: "vless-in", Protocol: "vless", Port: f.VlessPort, Transport: "tcp",
			PublicKey: cm.PublicKey, ShortID: cm.ShortID, UUID: cm.UUID, SNI: f.SNI,
		},
		{
			Tag: "hy2-in", Protocol: "hysteria2", Port: f.Hy2Port, Transport: "udp",
			Password: cm.Hy2Pass, ObfsPass: cm.ObfsPass,
		},
	}
}

func buildInstallParams(f *InstallFlags, cm *cryptoMaterials) *internal.InstallParams {
	return &internal.InstallParams{
		Domain: f.Domain, VlessPort: f.VlessPort, Hy2Port: f.Hy2Port,
		SubPort: f.SubPort, UUID: cm.UUID, PrivateKey: cm.PrivateKey, PublicKey: cm.PublicKey,
		ShortID: cm.ShortID, Hy2Pass: cm.Hy2Pass, ObfsPass: cm.ObfsPass,
		CertFile: f.CertFile, KeyFile: f.KeyFile,
	}
}

// --- Transit / Exit installation ---

func installServerNode(role internal.NodeRole, f *InstallFlags) error {
	roleLabel := map[internal.NodeRole]string{
		internal.RoleTransit: "传输节点",
		internal.RoleExit:    "出口节点",
	}[role]
	humanPrint("=== sing-box %s部署 ===\n", roleLabel)

	ok, oldMeta, err := prepareInstall(f)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if f.Domain != "" {
		humanPrint("域名: %s\n", f.Domain)
	}

	// Recover crypto from old deployment or generate new
	cm, err := resolveOrGenerateCrypto(f, oldMeta)
	if err != nil {
		return err
	}
	assignPorts(f, role)
	detectCertbot(f)
	humanPrint("VLESS Reality 端口: %d\n", f.VlessPort)

	params := buildInstallParams(f, cm)
	var cfg *internal.SBConfig
	var socks5Port int
	switch role {
	case internal.RoleTransit:
		humanPrint("Hysteria2 端口: %d\n", f.Hy2Port)
		cfg = internal.GenerateTransitConfig(params)
	case internal.RoleExit:
		// Generate socks5 credentials for exit node
		socks5Port = findAvailablePort(10000, 60000, f.VlessPort)
		socks5User, err := internal.GenerateHexString(8)
		if err != nil {
			return fmt.Errorf("generate socks5 username: %w", err)
		}
		socks5Pass, err := internal.GeneratePassword(12)
		if err != nil {
			return fmt.Errorf("generate socks5 password: %w", err)
		}
		params.Socks5Port = socks5Port
		params.Socks5User = socks5User
		params.Socks5Pass = socks5Pass
		humanPrint("SOCKS5 端口: %d\n", socks5Port)
		cfg = internal.GenerateExitConfig(params)
	}
	if err := internal.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	nodeID, err := internal.GenerateNodeID()
	if err != nil {
		return err
	}

	// Exit only has VLESS inbound + optional SOCKS5 (no Hy2, no ACME)
	var inboundMetas []internal.InboundMeta
	if role == internal.RoleExit {
		inboundMetas = []internal.InboundMeta{{
			Tag: "vless-in", Protocol: "vless", Port: f.VlessPort, Transport: "tcp",
			PublicKey: cm.PublicKey, ShortID: cm.ShortID, UUID: cm.UUID,
		}}
		if socks5Port > 0 {
			inboundMetas = append(inboundMetas, internal.InboundMeta{
				Tag: "socks-in", Protocol: "socks", Port: socks5Port, Transport: "tcp",
				Username: params.Socks5User, Password: params.Socks5Pass,
			})
		}
	} else {
		inboundMetas = buildBaseInboundMetas(f, cm)
	}

	meta := &internal.Metadata{
		Version: 2, Role: role, NodeID: nodeID,
		Name: f.Name, Domain: f.Domain, PublicHost: f.PublicHost, SubPort: f.SubPort, HTTPSubPort: f.HTTPSubPort,
		Inbounds:    inboundMetas,
		CFWorkerURL: f.CFWorkerURL,
		CFAPIKey:    f.CFAPIKey,
		APIToken:    f.CFAPIKey, // use same key for API auth
	}
	if f.BWGVeid != "" && f.BWGAPIKey != "" {
		meta.BWG = &internal.BWGConfig{VEID: f.BWGVeid, APIKey: f.BWGAPIKey}
	}
	if oldMeta != nil {
		restoreMetaFields(meta, oldMeta)
	}
	if err := meta.Save(); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	var extraPorts []int
	if socks5Port > 0 {
		extraPorts = append(extraPorts, socks5Port)
	}
	if err := finishInstall(f, role, extraPorts...); err != nil {
		return err
	}

	resultData := map[string]interface{}{
		"status": "ok", "role": string(role),
		"vless_port": f.VlessPort,
	}
	if f.Domain != "" {
		resultData["domain"] = f.Domain
		resultData["sub_url"] = fmt.Sprintf("https://%s:%d/sub/hz-office", effectivePublicHost(f), f.SubPort)
	}
	if role == internal.RoleTransit {
		resultData["hy2_port"] = f.Hy2Port
	}
	if socks5Port > 0 {
		resultData["socks5_port"] = socks5Port
		resultData["socks5_user"] = params.Socks5User
		resultData["socks5_pass"] = params.Socks5Pass
	}

	if internal.JSONMode {
		internal.PrintJSON(resultData)
	} else {
		fmt.Printf("\n=== %s部署完成 ===\n", roleLabel)
		fmt.Printf("角色:       %s\n", role)
		fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
		fmt.Printf("sbm-server: %s\n", activeStr("sbm-server"))
		fmt.Printf("VLESS:      %d/tcp\n", f.VlessPort)
		if role == internal.RoleTransit {
			fmt.Printf("Hysteria2:  %d/udp\n", f.Hy2Port)
		}
		if socks5Port > 0 {
			fmt.Printf("SOCKS5:     %d/tcp (user: %s)\n", socks5Port, params.Socks5User)
		}
		if f.Domain != "" {
			fmt.Printf("订阅地址:   https://%s:%d/sub/hz-office\n", effectivePublicHost(f), f.SubPort)
		}
		result, _ := json.MarshalIndent(resultData, "", "  ")
		fmt.Println(string(result))
	}
	return nil
}

// --- Entry node installation ---

type UpstreamsConfig struct {
	Transits []internal.TransitInfo `json:"transits"`
	Exits    []internal.ExitInfo    `json:"exits,omitempty"`
	Socks5   *Socks5Config          `json:"socks5,omitempty"`
}

type Socks5Config struct {
	Port      int      `json:"port"`
	User      string   `json:"user"`
	Pass      string   `json:"pass"`
	DirectIPs []string `json:"direct_ips,omitempty"`
}

func installEntryNode(f *InstallFlags) error {
	humanPrint("=== sing-box 接入节点部署 ===\n")

	if f.UpstreamsFile == "" {
		return fmt.Errorf("entry 安装需要 --upstreams 参数，指定上游节点配置文件")
	}

	upData, err := os.ReadFile(f.UpstreamsFile)
	if err != nil {
		return fmt.Errorf("read upstreams file: %w", err)
	}
	var upCfg UpstreamsConfig
	if err := json.Unmarshal(upData, &upCfg); err != nil {
		return fmt.Errorf("parse upstreams file: %w", err)
	}
	if len(upCfg.Transits) == 0 {
		return fmt.Errorf("upstreams 配置必须包含至少一个 transit 节点")
	}

	ok, oldMeta, err := prepareInstall(f)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	humanPrint("域名: %s\n", f.Domain)

	// Recover crypto from old deployment or generate new
	cm, err := resolveOrGenerateCrypto(f, oldMeta)
	if err != nil {
		return err
	}
	assignPorts(f, internal.RoleEntry)
	humanPrint("VLESS Reality 端口: %d\n", f.VlessPort)
	humanPrint("Hysteria2 端口: %d\n", f.Hy2Port)
	humanPrint("Transit 上游: %d 个\n", len(upCfg.Transits))
	humanPrint("Exit 上游: %d 个\n", len(upCfg.Exits))

	detectCertbot(f)

	// Seed default user in SQLite (uses install-time credentials)
	usersDB, dbErr := internal.OpenUsersDB()
	if dbErr != nil {
		return fmt.Errorf("open users db: %w", dbErr)
	}
	defer usersDB.Close()
	if _, err := usersDB.Exec(
		"INSERT OR IGNORE INTO users (name, uuid, password, quota_gb) VALUES (?, ?, ?, 0)",
		"hz-office", cm.UUID, cm.Hy2Pass,
	); err != nil {
		log.Printf("[install] seed user: %v", err)
	}
	entryUsers, err := internal.GetEnabledUsers(usersDB)
	if err != nil {
		return fmt.Errorf("load users: %w", err)
	}

	// Entry config
	entryParams := &internal.EntryParams{
		Domain: f.Domain, VlessPort: f.VlessPort, Hy2Port: f.Hy2Port,
		SubPort: f.SubPort, UUID: cm.UUID, PrivateKey: cm.PrivateKey, PublicKey: cm.PublicKey,
		ShortID: cm.ShortID, Hy2Pass: cm.Hy2Pass, ObfsPass: cm.ObfsPass,
		CertFile: f.CertFile, KeyFile: f.KeyFile, RealitySNI: f.SNI,
		Transits: upCfg.Transits, Exits: upCfg.Exits,
		Users: entryUsers,
	}
	if upCfg.Socks5 != nil {
		entryParams.Socks5Port = upCfg.Socks5.Port
		entryParams.Socks5User = upCfg.Socks5.User
		entryParams.Socks5Pass = upCfg.Socks5.Pass
		entryParams.Socks5DirectIPs = upCfg.Socks5.DirectIPs
	}

	entryCfg, err := internal.GenerateEntryConfig(entryParams)
	if err != nil {
		return fmt.Errorf("generate entry config: %w", err)
	}
	if err := internal.SaveRawConfig(entryCfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Write default routing policy (if not already present)
	if !internal.FileExists(internal.RoutingPolicyPath()) {
		if err := internal.SaveRoutingPolicy(internal.DefaultRoutingPolicy()); err != nil {
			return fmt.Errorf("save routing policy: %w", err)
		}
	}

	// Metadata
	nodeID, err := internal.GenerateNodeID()
	if err != nil {
		return err
	}
	inboundMetas := buildBaseInboundMetas(f, cm)
	if upCfg.Socks5 != nil && upCfg.Socks5.Port > 0 {
		inboundMetas = append(inboundMetas, internal.InboundMeta{
			Tag: "socks-in", Protocol: "socks", Port: upCfg.Socks5.Port, Transport: "tcp",
		})
	}

	upstreamMetas := make([]internal.UpstreamMeta, len(upCfg.Transits))
	for i, t := range upCfg.Transits {
		upstreamMetas[i] = internal.UpstreamMeta{Name: t.Name, Address: t.Address, Priority: i}
	}

	// Capture per-transit brutal CC settings into metadata so reconcile preserves them.
	var hy2Brutal map[string]internal.Hy2BrutalConfig
	for _, t := range upCfg.Transits {
		if t.UpMbps > 0 && t.DownMbps > 0 {
			if hy2Brutal == nil {
				hy2Brutal = map[string]internal.Hy2BrutalConfig{}
			}
			hy2Brutal[t.Name] = internal.Hy2BrutalConfig{UpMbps: t.UpMbps, DownMbps: t.DownMbps}
		}
	}

	meta := &internal.Metadata{
		Version: 2, Role: internal.RoleEntry, NodeID: nodeID,
		Name: f.Name, Domain: f.Domain, PublicHost: f.PublicHost, SubPort: f.SubPort, HTTPSubPort: f.HTTPSubPort,
		Inbounds: inboundMetas, Upstreams: upstreamMetas,
		CFWorkerURL: f.CFWorkerURL,
		CFAPIKey:    f.CFAPIKey,
		APIToken:    f.CFAPIKey,
		Hy2Brutal:   hy2Brutal,
	}
	if f.BWGVeid != "" && f.BWGAPIKey != "" {
		meta.BWG = &internal.BWGConfig{VEID: f.BWGVeid, APIKey: f.BWGAPIKey}
	}
	if oldMeta != nil {
		restoreMetaFields(meta, oldMeta)
	}
	if err := meta.Save(); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	var extraPorts []int
	if upCfg.Socks5 != nil && upCfg.Socks5.Port > 0 {
		extraPorts = append(extraPorts, upCfg.Socks5.Port)
	}
	if err := finishInstall(f, internal.RoleEntry, extraPorts...); err != nil {
		return err
	}

	resultData := map[string]interface{}{
		"status": "ok", "role": "entry", "domain": f.Domain,
		"vless_port": f.VlessPort, "hy2_port": f.Hy2Port,
		"transits": len(upCfg.Transits), "exits": len(upCfg.Exits),
		"sub_url": fmt.Sprintf("https://%s:%d/sub/hz-office", effectivePublicHost(f), f.SubPort),
	}

	if internal.JSONMode {
		internal.PrintJSON(resultData)
	} else {
		fmt.Printf("\n=== 接入节点部署完成 ===\n")
		fmt.Printf("角色:       entry\n")
		fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
		fmt.Printf("sbm-server: %s\n", activeStr("sbm-server"))
		fmt.Printf("VLESS:      %d/tcp\n", f.VlessPort)
		fmt.Printf("Hysteria2:  %d/udp\n", f.Hy2Port)
		if upCfg.Socks5 != nil && upCfg.Socks5.Port > 0 {
			fmt.Printf("SOCKS5:     %d/tcp\n", upCfg.Socks5.Port)
		}
		fmt.Printf("订阅地址:   https://%s:%d/sub/hz-office\n", effectivePublicHost(f), f.SubPort)
		result, _ := json.MarshalIndent(resultData, "", "  ")
		fmt.Println(string(result))
	}
	return nil
}

// --- Helpers ---

// extractPrivateKeyFromConfig reads the Reality private_key from the existing sing-box config.
// This is needed because metadata only stores the public key, not the private key.
func extractPrivateKeyFromConfig() string {
	data, err := os.ReadFile(internal.DeployDir + "/config.json")
	if err != nil {
		return ""
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	inbounds, _ := raw["inbounds"].([]interface{})
	for _, ib := range inbounds {
		ibMap, _ := ib.(map[string]interface{})
		tls, _ := ibMap["tls"].(map[string]interface{})
		reality, _ := tls["reality"].(map[string]interface{})
		if pk, ok := reality["private_key"].(string); ok && pk != "" {
			return pk
		}
	}
	return ""
}

// resolveOrGenerateCrypto recovers crypto from old metadata or generates new materials.
// Also restores old ports into InstallFlags if not explicitly specified.
func resolveOrGenerateCrypto(f *InstallFlags, oldMeta *internal.Metadata) (*cryptoMaterials, error) {
	if oldMeta != nil {
		if cm := recoverCrypto(oldMeta); cm != nil {
			humanPrint("复用现有密钥\n")
			// Reuse old ports if not explicitly specified
			for _, inb := range oldMeta.Inbounds {
				if inb.Protocol == "vless" && f.VlessPort == 0 {
					f.VlessPort = inb.Port
				}
				if inb.Protocol == "hysteria2" && f.Hy2Port == 0 {
					f.Hy2Port = inb.Port
				}
			}
			return cm, nil
		}
	}
	humanPrint("生成密钥...\n")
	return generateCryptoMaterials()
}

// recoverCrypto extracts cryptographic materials from old metadata + private key.
// Returns nil if recovery is not possible (missing critical fields).
func recoverCrypto(oldMeta *internal.Metadata) *cryptoMaterials {
	var cm cryptoMaterials
	for _, inb := range oldMeta.Inbounds {
		switch inb.Protocol {
		case "vless":
			cm.PublicKey = inb.PublicKey
			cm.ShortID = inb.ShortID
			cm.UUID = inb.UUID
		case "hysteria2":
			cm.Hy2Pass = inb.Password
			cm.ObfsPass = inb.ObfsPass
		}
	}
	cm.PrivateKey = oldMeta.PrivateKey

	// Validate: must have at least UUID and PrivateKey
	if cm.UUID == "" || cm.PrivateKey == "" {
		return nil
	}
	return &cm
}

// restoreMetaFields copies operational fields from old metadata to new metadata.
func restoreMetaFields(meta *internal.Metadata, old *internal.Metadata) {
	if old.NodeID != "" {
		meta.NodeID = old.NodeID
	}
	meta.Name = old.Name
	meta.APIToken = old.APIToken
	meta.CFWorkerURL = old.CFWorkerURL
	meta.CFAPIKey = old.CFAPIKey
	meta.BWG = old.BWG
	if old.ExitPorts != nil {
		meta.ExitPorts = old.ExitPorts
	}
	if old.ExitAuth != nil {
		meta.ExitAuth = old.ExitAuth
	}
	if old.ExitSocksPorts != nil {
		meta.ExitSocksPorts = old.ExitSocksPorts
	}
}

// detectCertbot auto-detects certbot certificates for the domain.
func detectCertbot(f *InstallFlags) {
	if f.CertFile == "" && f.Domain != "" {
		cb := "/etc/letsencrypt/live/" + f.Domain
		if _, err := os.Stat(cb + "/fullchain.pem"); err == nil {
			f.CertFile = cb + "/fullchain.pem"
			f.KeyFile = cb + "/privkey.pem"
			humanPrint("检测到 certbot 证书: %s\n", cb)
		}
	}
}

func findAvailablePort(min, max int, exclude ...int) int {
	for i := 0; i < 100; i++ {
		port := internal.GenerateRandomPort(min, max, exclude...)
		if internal.CheckPortAvailable(port) {
			return port
		}
	}
	return internal.GenerateRandomPort(min, max, exclude...)
}

func activeStr(unit string) string {
	if internal.IsActive(unit) {
		return "active"
	}
	return "inactive"
}

// humanPrint prints only in human mode (non-JSON).
func humanPrint(format string, args ...interface{}) {
	if !internal.JSONMode {
		fmt.Printf(format, args...)
	}
}

func installRenewCron(sbmPath string) error {
	content := fmt.Sprintf("17 3 * * 1 root %s renew\n", sbmPath)
	return os.WriteFile("/etc/cron.d/renew-cert", []byte(content), 0644)
}

// detectPublicIP queries external services to find this machine's public IP.
func detectPublicIP() string {
	for _, url := range []string{"https://api.ipify.org", "https://ifconfig.me", "https://icanhazip.com"} {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		ip := strings.TrimSpace(string(body))
		// Validate: must look like an IP (digits and dots/colons, no HTML)
		if ip != "" && !strings.Contains(ip, "<") && !strings.Contains(ip, " ") {
			return ip
		}
	}
	return ""
}
