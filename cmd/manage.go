package cmd

import (
	"flag"
	"fmt"
	"os/exec"
	"sing-box-manager/internal"
	"strings"
)

func RunStart() error {
	return internal.SystemctlOp("start", "sing-box", "sub-server")
}

func RunStop() error {
	return internal.SystemctlOp("stop", "sing-box", "sub-server")
}

func RunRestart() error {
	return internal.SystemctlOp("restart", "sing-box", "sub-server")
}

func RunStatus() error {
	singboxActive := internal.IsActive("sing-box")
	subServerActive := internal.IsActive("sub-server")

	listeningPorts := collectListeningPorts()
	firewallPorts := internal.FirewallListPorts()

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"services": map[string]string{
				"sing-box":   boolToStatus(singboxActive),
				"sub-server": boolToStatus(subServerActive),
			},
			"listening_ports": listeningPorts,
			"firewall_ports":  firewallPorts,
		})
		return nil
	}

	fmt.Println("=== 服务状态 ===")
	fmt.Printf("sing-box:   %s\n", boolToStatus(singboxActive))
	fmt.Printf("sub-server: %s\n", boolToStatus(subServerActive))
	fmt.Println()

	fmt.Println("=== 监听端口 ===")
	for _, p := range listeningPorts {
		fmt.Println(p)
	}
	fmt.Println()

	fmt.Println("=== 防火墙端口 ===")
	fmt.Println(strings.Join(firewallPorts, "  "))
	return nil
}

func RunLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	n := fs.Int("n", 50, "日志行数")
	fs.Parse(args)

	if internal.JSONMode {
		return internal.RunCmd("journalctl", "-u", "sing-box", "-n", fmt.Sprintf("%d", *n), "--no-pager", "-o", "json")
	}
	return internal.RunCmd("journalctl", "-u", "sing-box", "-n", fmt.Sprintf("%d", *n), "--no-pager")
}

func boolToStatus(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

func collectListeningPorts() []string {
	var ports []string
	for _, proto := range []string{"-tlnp", "-ulnp"} {
		out, _ := exec.Command("ss", proto).Output()
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "sing-box") || strings.Contains(line, "sbm") {
				ports = append(ports, strings.TrimSpace(line))
			}
		}
	}
	return ports
}
