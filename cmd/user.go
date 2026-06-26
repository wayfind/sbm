package cmd

import (
	"database/sql"
	"fmt"
	"sing-box-manager/internal"
	"strconv"
	"strings"
)

func RunUser(args []string) error {
	if len(args) == 0 {
		return userUsage()
	}

	db, err := internal.OpenUsersDB()
	if err != nil {
		return err
	}
	defer db.Close()

	switch args[0] {
	case "add":
		return userAdd(db, args[1:])
	case "del":
		return userDel(db, args[1:])
	case "list":
		return userList(db)
	case "set-quota":
		return userSetQuota(db, args[1:])
	case "enable":
		return userEnable(db, args[1:], true)
	case "disable":
		return userEnable(db, args[1:], false)
	case "reset":
		return userReset(db, args[1:])
	default:
		return userUsage()
	}
}

func userUsage() error {
	fmt.Println(`用法:
  sbm user add <name> [--quota <N>GB]    添加用户
  sbm user del <name>                    删除用户
  sbm user list [--json]                 列出用户及用量
  sbm user set-quota <name> <N>GB        设置月限额
  sbm user enable <name>                 启用用户
  sbm user disable <name>                禁用用户
  sbm user reset <name>                  重置当月流量`)
	return nil
}

func userAdd(db *sql.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: sbm user add <name> [--quota <N>GB]")
	}
	name := args[0]
	quotaGB := 0.0

	for i := 1; i < len(args); i++ {
		if args[i] == "--quota" && i+1 < len(args) {
			q, err := parseGB(args[i+1])
			if err != nil {
				return err
			}
			quotaGB = q
			i++
		}
	}

	user, err := internal.AddUser(db, name, quotaGB)
	if err != nil {
		return err
	}

	if internal.JSONMode {
		internal.PrintJSON(user)
	} else {
		fmt.Printf("用户已添加: %s\n", user.Name)
		fmt.Printf("UUID:     %s\n", user.UUID)
		fmt.Printf("密码:     %s\n", user.Password)
		if quotaGB > 0 {
			fmt.Printf("月限额:   %.0f GB\n", quotaGB)
		} else {
			fmt.Printf("月限额:   无限\n")
		}
		fmt.Println("\n变更将在 1 分钟内自动生效")
	}
	return nil
}

func userDel(db *sql.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: sbm user del <name>")
	}
	if err := internal.DelUser(db, args[0]); err != nil {
		return err
	}
	if internal.JSONMode {
		internal.PrintJSON(map[string]string{"status": "deleted", "name": args[0]})
	} else {
		fmt.Printf("用户已删除: %s\n", args[0])
		fmt.Println("变更将在 1 分钟内自动生效")
	}
	return nil
}

func userList(db *sql.DB) error {
	users, err := internal.ListUsers(db)
	if err != nil {
		return err
	}

	if internal.JSONMode {
		internal.PrintJSON(users)
		return nil
	}

	if len(users) == 0 {
		fmt.Println("暂无用户")
		return nil
	}

	fmt.Printf("%-12s %-8s %-15s %-10s %s\n", "用户", "状态", "本月用量", "限额", "UUID")
	fmt.Println(strings.Repeat("-", 70))
	for _, u := range users {
		status := "启用"
		if !u.Enabled {
			status = "禁用"
		}
		usage := internal.FormatBytes(u.BytesTotal)
		quota := "无限"
		if u.QuotaGB > 0 {
			quota = fmt.Sprintf("%.0f GB", u.QuotaGB)
		}
		fmt.Printf("%-12s %-8s %-15s %-10s %s\n", u.Name, status, usage, quota, u.UUID[:8]+"...")
	}
	return nil
}

func userSetQuota(db *sql.DB, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("用法: sbm user set-quota <name> <N>GB")
	}
	quota, err := parseGB(args[1])
	if err != nil {
		return err
	}
	if err := internal.SetUserQuota(db, args[0], quota); err != nil {
		return err
	}
	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{"name": args[0], "quota_gb": quota})
	} else {
		fmt.Printf("已设置 %s 月限额: %.0f GB\n", args[0], quota)
	}
	return nil
}

func userEnable(db *sql.DB, args []string, enabled bool) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: sbm user enable/disable <name>")
	}
	if err := internal.SetUserEnabled(db, args[0], enabled); err != nil {
		return err
	}
	action := "启用"
	if !enabled {
		action = "禁用"
	}
	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{"name": args[0], "enabled": enabled})
	} else {
		fmt.Printf("用户 %s 已%s\n", args[0], action)
		fmt.Println("变更将在 1 分钟内自动生效")
	}
	return nil
}

func userReset(db *sql.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: sbm user reset <name>")
	}
	if err := internal.ResetUserTraffic(db, args[0]); err != nil {
		return err
	}
	// Drop in-memory baseline and clear BOTH disable flags (manual + quota),
	// matching the HTTP reset endpoint so reset behaves identically either way.
	internal.ResetTrackerUser(args[0])
	internal.SetUserEnabled(db, args[0], true)
	internal.SetQuotaDisabled(db, args[0], false)
	if internal.JSONMode {
		internal.PrintJSON(map[string]string{"name": args[0], "status": "reset"})
	} else {
		fmt.Printf("用户 %s 当月流量已重置并启用\n", args[0])
	}
	return nil
}

// parseGB parses "100GB" or "100" into float64 GB.
func parseGB(s string) (float64, error) {
	s = strings.TrimSuffix(strings.ToUpper(s), "GB")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("无效的容量格式: %s (示例: 100GB)", s)
	}
	return v, nil
}
