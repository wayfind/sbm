package main

import (
	"fmt"
	"os"
	"sing-box-manager/cmd"
	"sing-box-manager/internal"

	"golang.org/x/term"
)

func main() {
	// Scan and consume --json flag before command dispatch
	internal.JSONMode, os.Args = extractFlag(os.Args, "--json")

	if len(os.Args) < 2 {
		if term.IsTerminal(int(os.Stdin.Fd())) {
			interactiveMenu()
		} else {
			usage()
		}
		return
	}

	var err error
	switch os.Args[1] {
	case "install":
		err = cmd.RunInstall(os.Args[2:])
	case "serve":
		err = cmd.RunServe(os.Args[2:])
	case "start":
		err = cmd.RunStart()
	case "stop":
		err = cmd.RunStop()
	case "restart":
		err = cmd.RunRestart()
	case "status":
		err = cmd.RunStatus()
	case "logs":
		err = cmd.RunLogs(os.Args[2:])
	case "config":
		err = cmd.RunConfig()
	case "sub":
		err = cmd.RunSub()
	case "cert":
		err = cmd.RunCert()
	case "renew":
		err = cmd.RunRenew()
	case "topology":
		err = cmd.RunTopology(os.Args[2:])
	case "bwg":
		err = cmd.RunBWG(os.Args[2:])
	case "client":
		err = cmd.RunClient(os.Args[2:])
	case "user":
		err = cmd.RunUser(os.Args[2:])
	case "metadata":
		err = cmd.RunMetadata()
	case "version", "-v", "--version":
		err = cmd.RunVersion()
	case "update":
		err = cmd.RunUpdate(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		if internal.JSONMode {
			internal.PrintErrorJSON(err.Error())
		} else {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		}
		os.Exit(1)
	}
}

func interactiveMenu() {
	options := []string{
		"安装/重装",
		"启动服务",
		"停止服务",
		"重启服务",
		"查看状态",
		"查看日志",
		"查看配置",
		"查看订阅",
		"查看证书",
		"查看拓扑",
		"证书续期",
		"版本信息",
	}

	for {
		fmt.Println()
		fmt.Println("=== sing-box 管理器 ===")
		choice := cmd.InteractiveChoice("请选择", options)
		if choice == 0 {
			break
		}

		var err error
		switch choice {
		case 1:
			err = cmd.RunInstall(nil)
		case 2:
			err = cmd.RunStart()
			if err == nil {
				fmt.Println("服务已启动")
			}
		case 3:
			err = cmd.RunStop()
			if err == nil {
				fmt.Println("服务已停止")
			}
		case 4:
			err = cmd.RunRestart()
			if err == nil {
				fmt.Println("服务已重启")
			}
		case 5:
			err = cmd.RunStatus()
		case 6:
			err = cmd.RunLogs(nil)
		case 7:
			err = cmd.RunConfig()
		case 8:
			err = cmd.RunSub()
		case 9:
			err = cmd.RunCert()
		case 10:
			err = cmd.RunTopology(nil)
		case 11:
			err = cmd.RunRenew()
		case 12:
			err = cmd.RunVersion()
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		}
	}
}

func usage() {
	fmt.Println(`sing-box-manager (sbm) - sing-box 部署管理工具

用法:
  sbm                                          交互式菜单
  sbm install --role <角色> --domain <域名>    安装 (角色: transit/exit/entry)
  sbm start                                    启动服务
  sbm stop                                     停止服务
  sbm restart                                  重启服务
  sbm status                                   查看状态
  sbm logs [-n 100]                            查看日志
  sbm config                                   查看配置 (脱敏)
  sbm sub                                      查看订阅
  sbm cert                                     查看证书
  sbm renew                                    证书续期
  sbm topology                                 查看拓扑
  sbm bwg status                               查看 GIA 带宽
  sbm bwg rotate                               手动流量均衡
  sbm client gen --upstream <URL> --platform <平台>  生成客户端配置
  sbm user add/del/list/set-quota/enable/disable    用户管理
  sbm metadata                                 查看元数据
  sbm serve                                    启动订阅 HTTPS 服务 (前台)
  sbm update                                   自更新 sbm 二进制
  sbm version                                  版本信息

全局选项:
  --json          所有输出为 JSON (AI/自动化模式，无交互)
  --force         跳过确认提示 (install 时自动包含于 --json)`)
}

// extractFlag scans args for a boolean flag, removes it, and returns (found, remaining).
func extractFlag(args []string, flag string) (bool, []string) {
	found := false
	result := make([]string, 0, len(args))
	for _, a := range args {
		if a == flag {
			found = true
		} else {
			result = append(result, a)
		}
	}
	return found, result
}
