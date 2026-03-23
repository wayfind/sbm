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
	Domain    string
	VlessPort int
	Hy2Port   int
	SubPort   int
	Force     bool
}

func RunInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var f InstallFlags
	fs.StringVar(&f.Domain, "domain", "", "域名 (必选)")
	fs.IntVar(&f.VlessPort, "vless-port", 0, "VLESS 端口 (默认随机)")
	fs.IntVar(&f.Hy2Port, "hy2-port", 0, "Hysteria2 端口 (默认随机)")
	fs.IntVar(&f.SubPort, "sub-port", 20032, "订阅服务端口")
	fs.BoolVar(&f.Force, "force", false, "跳过替换确认")
	fs.Parse(args)

	if f.Domain == "" {
		f.Domain = promptString("请输入域名")
		if f.Domain == "" {
			return fmt.Errorf("域名不能为空")
		}
	}

	fmt.Println("=== sing-box 一键部署 ===")

	// 检查 sing-box
	if _, err := exec.LookPath("sing-box"); err != nil {
		fmt.Println("sing-box 未安装，请先安装 sing-box")
		return fmt.Errorf("sing-box not found")
	}

	// 检测现有部署
	if _, err := os.Stat(internal.DeployDir); err == nil {
		if !f.Force {
			fmt.Printf("检测到现有部署: %s\n", internal.DeployDir)
			if !promptConfirm("是否替换现有部署？") {
				fmt.Println("取消安装")
				return nil
			}
		}

		// 清理旧防火墙规则
		if oldMeta, err := internal.LoadMetadata(); err == nil {
			fmt.Println("清理旧防火墙规则...")
			internal.FirewallRemovePort(oldMeta.VlessPort, "tcp")
			internal.FirewallRemovePort(oldMeta.Hy2Port, "udp")
			oldSubPort := oldMeta.SubPort
			if oldSubPort == 0 {
				oldSubPort = 20032
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

	fmt.Printf("域名: %s\n", f.Domain)

	// 生成密钥
	fmt.Println("生成密钥...")
	privKey, pubKey, err := internal.GenerateRealityKeypair()
	if err != nil {
		return err
	}
	uuid, err := internal.GenerateUUID()
	if err != nil {
		return err
	}
	shortID, err := internal.GenerateShortID()
	if err != nil {
		return err
	}
	hy2Pass, err := internal.GeneratePassword(12)
	if err != nil {
		return err
	}
	obfsPass, err := internal.GeneratePassword(18)
	if err != nil {
		return err
	}

	// 端口
	if f.VlessPort == 0 {
		f.VlessPort = findAvailablePort(10000, 60000)
	}
	if f.Hy2Port == 0 {
		f.Hy2Port = findAvailablePort(10000, 60000, f.VlessPort)
	}
	fmt.Printf("VLESS Reality 端口: %d\n", f.VlessPort)
	fmt.Printf("Hysteria2 端口: %d\n", f.Hy2Port)

	// 生成配置
	params := &internal.InstallParams{
		Domain: f.Domain, VlessPort: f.VlessPort, Hy2Port: f.Hy2Port,
		SubPort: f.SubPort, UUID: uuid, PrivateKey: privKey, PublicKey: pubKey,
		ShortID: shortID, Hy2Pass: hy2Pass, ObfsPass: obfsPass,
	}
	cfg := internal.GenerateConfig(params)
	if err := internal.SaveConfig(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	meta := &internal.Metadata{
		Domain: f.Domain, VlessPort: f.VlessPort, Hy2Port: f.Hy2Port,
		PublicKey: pubKey, ShortID: shortID, UUID: uuid, SubPort: f.SubPort,
	}
	if err := meta.Save(); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	// 自身路径
	sbmPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	sbmPath, err = filepath.Abs(sbmPath)
	if err != nil {
		return fmt.Errorf("resolve absolute path: %w", err)
	}

	// 安装 systemd 服务
	if err := internal.InstallSystemdUnits(sbmPath); err != nil {
		return fmt.Errorf("install systemd units: %w", err)
	}

	// 安装证书续期 cron
	if err := installRenewCron(sbmPath); err != nil {
		return fmt.Errorf("install renew cron: %w", err)
	}

	// 防火墙
	internal.FirewallAddPort(f.VlessPort, "tcp")
	internal.FirewallAddPort(f.Hy2Port, "udp")
	internal.FirewallAddPort(f.SubPort, "tcp")
	internal.FirewallTempAddPort(80, "tcp") // ACME 首次签发
	internal.FirewallReload()

	// 启动
	internal.SystemctlOp("enable", "--now", "sing-box", "sub-server")
	fmt.Println("等待 ACME 证书签发...")
	time.Sleep(10 * time.Second)

	// 关闭 80
	internal.FirewallTempRemovePort(80, "tcp")

	// 输出结果
	fmt.Println()
	fmt.Println("=== 部署完成 ===")
	fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
	fmt.Printf("sub-server: %s\n", activeStr("sub-server"))
	fmt.Printf("VLESS:      %d/tcp\n", f.VlessPort)
	fmt.Printf("Hysteria2:  %d/udp\n", f.Hy2Port)
	fmt.Printf("订阅地址:   https://%s:%d/sub/hz-office\n", f.Domain, f.SubPort)

	// JSON 输出便于 AI 解析
	result, _ := json.MarshalIndent(map[string]interface{}{
		"status":     "ok",
		"domain":     f.Domain,
		"vless_port": f.VlessPort,
		"hy2_port":   f.Hy2Port,
		"sub_url":    fmt.Sprintf("https://%s:%d/sub/hz-office", f.Domain, f.SubPort),
	}, "", "  ")
	fmt.Println(string(result))
	return nil
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

func installRenewCron(sbmPath string) error {
	content := fmt.Sprintf("17 3 * * 1 root %s renew\n", sbmPath)
	return os.WriteFile("/etc/cron.d/renew-cert", []byte(content), 0644)
}
