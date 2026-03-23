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
	fmt.Println("=== 服务状态 ===")
	fmt.Printf("sing-box:   %s\n", activeStr("sing-box"))
	fmt.Printf("sub-server: %s\n", activeStr("sub-server"))
	fmt.Println()

	fmt.Println("=== 监听端口 ===")
	out, _ := exec.Command("ss", "-tlnp").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "sing-box") || strings.Contains(line, "sbm") {
			fmt.Println(line)
		}
	}
	out, _ = exec.Command("ss", "-ulnp").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "sing-box") || strings.Contains(line, "sbm") {
			fmt.Println(line)
		}
	}
	fmt.Println()

	fmt.Println("=== 防火墙端口 ===")
	ports := internal.FirewallListPorts()
	fmt.Println(strings.Join(ports, "  "))
	return nil
}

func RunLogs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	n := fs.Int("n", 50, "日志行数")
	fs.Parse(args)
	return internal.RunCmd("journalctl", "-u", "sing-box", "-n", fmt.Sprintf("%d", *n), "--no-pager")
}
