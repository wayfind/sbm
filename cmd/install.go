package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sing-box-manager/internal"
	"time"
)

type InstallFlags struct {
	Role          string
	Domain        string
	VlessPort     int
	Hy2Port       int
	SubPort       int
	Force         bool
	UpstreamsFile string
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
	fs.IntVar(&f.VlessPort, "vless-port", 0, "VLESS 端口 (默认随机)")
	fs.IntVar(&f.Hy2Port, "hy2-port", 0, "Hysteria2 端口 (默认随机)")
	fs.IntVar(&f.SubPort, "sub-port", 20033, "订阅服务端口")
	fs.BoolVar(&f.Force, "force", false, "跳过替换确认")
	fs.StringVar(&f.UpstreamsFile, "upstreams", "", "上游节点配置文件 (entry 必选)")
	fs.Parse(args)

	role := internal.NodeRole(f.Role)
	if !role.Valid() {
		return fmt.Errorf("无效角色: %s (可选: transit, exit, entry, client)", f.Role)
	}
	if f.Domain == "" {
		f.Domain = promptString("请输入域名")
		if f.Domain == "" {
			return fmt.Errorf("域名不能为空")
		}
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
// Returns false if user cancelled.
func prepareInstall(f *InstallFlags) (bool, error) {
	if _, err := exec.LookPath("sing-box"); err != nil {
		return false, fmt.Errorf("sing-box not found，请先安装 sing-box")
	}

	if _, err := os.Stat(internal.DeployDir); err == nil {
		if !f.Force {
			fmt.Printf("检测到现有部署: %s\n", internal.DeployDir)
			if !promptConfirm("是否替换现有部署？") {
				fmt.Println("取消安装")
				return false, nil
			}
		}
		if oldMeta, err := internal.LoadMetadata(); err == nil {
			fmt.Println("清理旧防火墙规则...")
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
		backup := fmt.Sprintf("/etc/sing-box.backup-%s", time.Now().Format("20060102-150405"))
		fmt.Printf("备份到: %s\n", backup)
		internal.SystemctlOp("stop", "sing-box", "sub-server")
		internal.SystemctlOp("disable", "sing-box", "sub-server")
		os.Rename(internal.DeployDir, backup)
	}
	os.MkdirAll(internal.DeployDir, 0755)
	return true, nil
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

func assignPorts(f *InstallFlags) {
	if f.VlessPort == 0 {
		f.VlessPort = findAvailablePort(10000, 60000)
	}
	if f.Hy2Port == 0 {
		f.Hy2Port = findAvailablePort(10000, 60000, f.VlessPort)
	}
}

// finishInstall installs systemd units, cron, firewall rules, starts services, waits for ACME.
func finishInstall(f *InstallFlags, extraTCPPorts ...int) error {
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
	internal.FirewallAddPort(f.Hy2Port, "udp")
	internal.FirewallAddPort(f.SubPort, "tcp")
	for _, p := range extraTCPPorts {
		internal.FirewallAddPort(p, "tcp")
	}
	internal.FirewallTempAddPort(80, "tcp")
	internal.FirewallReload()

	internal.SystemctlOp("enable", "--now", "sing-box", "sub-server")
	fmt.Println("等待 ACME 证书签发...")
	time.Sleep(10 * time.Second)
	internal.FirewallTempRemovePort(80, "tcp")
	return nil
}

func buildBaseInboundMetas(f *InstallFlags, cm *cryptoMaterials) []internal.InboundMeta {
	return []internal.InboundMeta{
		{
			Tag: "vless-in", Protocol: "vless", Port: f.VlessPort, Transport: "tcp",
			PublicKey: cm.PublicKey, ShortID: cm.ShortID, UUID: cm.UUID,
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
	}
}

// --- Transit / Exit installation ---

func installServerNode(role internal.NodeRole, f *InstallFlags) error {
	roleLabel := map[internal.NodeRole]string{
		internal.RoleTransit: "传输节点",
		internal.RoleExit:    "出口节点",
	}[role]
	fmt.Printf("=== sing-box %s部署 ===\n", roleLabel)

	ok, err := prepareInstall(f)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	fmt.Printf("域名: %s\n", f.Domain)
	fmt.Println("生成密钥...")
	cm, err := generateCryptoMaterials()
	if err != nil {
		return err
	}
	assignPorts(f)
	fmt.Printf("VLESS Reality 端口: %d\n", f.VlessPort)
	fmt.Printf("Hysteria2 端口: %d\n", f.Hy2Port)

	// Config
	params := buildInstallParams(f, cm)
	var cfg *internal.SBConfig
	switch role {
	case internal.RoleTransit:
		cfg = internal.GenerateTransitConfig(params)
	case internal.RoleExit:
		cfg = internal.GenerateExitConfig(params)
	}
	if err := internal.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	// Metadata
	nodeID, err := internal.GenerateNodeID()
	if err != nil {
		return err
	}
	meta := &internal.Metadata{
		Version: 2, Role: role, NodeID: nodeID,
		Domain: f.Domain, SubPort: f.SubPort,
		Inbounds: buildBaseInboundMetas(f, cm),
	}
	if err := meta.Save(); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	if err := finishInstall(f); err != nil {
		return err
	}

	fmt.Printf("\n=== %s部署完成 ===\n", roleLabel)
	fmt.Printf("角色:       %s\n", role)
	fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
	fmt.Printf("sub-server: %s\n", activeStr("sub-server"))
	fmt.Printf("VLESS:      %d/tcp\n", f.VlessPort)
	fmt.Printf("Hysteria2:  %d/udp\n", f.Hy2Port)
	fmt.Printf("订阅地址:   https://%s:%d/sub/hz-office\n", f.Domain, f.SubPort)

	result, _ := json.MarshalIndent(map[string]interface{}{
		"status": "ok", "role": string(role), "domain": f.Domain,
		"vless_port": f.VlessPort, "hy2_port": f.Hy2Port,
		"sub_url": fmt.Sprintf("https://%s:%d/sub/hz-office", f.Domain, f.SubPort),
	}, "", "  ")
	fmt.Println(string(result))
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
	fmt.Println("=== sing-box 接入节点部署 ===")

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

	ok, err := prepareInstall(f)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	fmt.Printf("域名: %s\n", f.Domain)
	fmt.Println("生成密钥...")
	cm, err := generateCryptoMaterials()
	if err != nil {
		return err
	}
	assignPorts(f)
	fmt.Printf("VLESS Reality 端口: %d\n", f.VlessPort)
	fmt.Printf("Hysteria2 端口: %d\n", f.Hy2Port)
	fmt.Printf("Transit 上游: %d 个\n", len(upCfg.Transits))
	fmt.Printf("Exit 上游: %d 个\n", len(upCfg.Exits))

	// Entry config
	entryParams := &internal.EntryParams{
		Domain: f.Domain, VlessPort: f.VlessPort, Hy2Port: f.Hy2Port,
		SubPort: f.SubPort, UUID: cm.UUID, PrivateKey: cm.PrivateKey, PublicKey: cm.PublicKey,
		ShortID: cm.ShortID, Hy2Pass: cm.Hy2Pass, ObfsPass: cm.ObfsPass,
		Transits: upCfg.Transits, Exits: upCfg.Exits,
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

	meta := &internal.Metadata{
		Version: 2, Role: internal.RoleEntry, NodeID: nodeID,
		Domain: f.Domain, SubPort: f.SubPort,
		Inbounds: inboundMetas, Upstreams: upstreamMetas,
	}
	if err := meta.Save(); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	var extraPorts []int
	if upCfg.Socks5 != nil && upCfg.Socks5.Port > 0 {
		extraPorts = append(extraPorts, upCfg.Socks5.Port)
	}
	if err := finishInstall(f, extraPorts...); err != nil {
		return err
	}

	fmt.Printf("\n=== 接入节点部署完成 ===\n")
	fmt.Printf("角色:       entry\n")
	fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
	fmt.Printf("sub-server: %s\n", activeStr("sub-server"))
	fmt.Printf("VLESS:      %d/tcp\n", f.VlessPort)
	fmt.Printf("Hysteria2:  %d/udp\n", f.Hy2Port)
	if upCfg.Socks5 != nil && upCfg.Socks5.Port > 0 {
		fmt.Printf("SOCKS5:     %d/tcp\n", upCfg.Socks5.Port)
	}
	fmt.Printf("订阅地址:   https://%s:%d/sub/hz-office\n", f.Domain, f.SubPort)

	result, _ := json.MarshalIndent(map[string]interface{}{
		"status": "ok", "role": "entry", "domain": f.Domain,
		"vless_port": f.VlessPort, "hy2_port": f.Hy2Port,
		"transits": len(upCfg.Transits), "exits": len(upCfg.Exits),
		"sub_url": fmt.Sprintf("https://%s:%d/sub/hz-office", f.Domain, f.SubPort),
	}, "", "  ")
	fmt.Println(string(result))
	return nil
}

// --- Helpers ---

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

func installRenewCron(sbmPath string) error {
	content := fmt.Sprintf("17 3 * * 1 root %s renew\n", sbmPath)
	return os.WriteFile("/etc/cron.d/renew-cert", []byte(content), 0644)
}
