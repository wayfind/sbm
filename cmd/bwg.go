package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sing-box-manager/internal"
	"time"
)

func RunBWG(args []string) error {
	if len(args) == 0 {
		return bwgUsage()
	}
	switch args[0] {
	case "status":
		return bwgStatus()
	case "rotate":
		return bwgRotate()
	default:
		return bwgUsage()
	}
}

func bwgUsage() error {
	fmt.Println(`用法:
  sbm bwg status    查看各 GIA 带宽使用情况
  sbm bwg rotate    手动触发流量均衡`)
	return nil
}

func bwgStatus() error {
	failoverCfg, err := loadFailoverConfig()
	if err != nil {
		return err
	}
	if len(failoverCfg.Transits) == 0 {
		if internal.JSONMode {
			internal.PrintJSON(map[string]interface{}{"transits": []interface{}{}, "order": []string{}})
			return nil
		}
		fmt.Println("未配置 BWG 带宽监控")
		return nil
	}

	statuses := internal.CheckAllTransitBandwidth(failoverCfg.Transits)
	ordered := internal.BalancedTransitOrder(statuses)

	if internal.JSONMode {
		transitResults := make([]map[string]interface{}, len(statuses))
		for i, s := range statuses {
			t := map[string]interface{}{
				"name":          s.Name,
				"used_bytes":    s.UsedBW,
				"monthly_bytes": s.MonthlyBW,
				"remaining_pct": s.RemainingPct,
				"available":     s.Available,
			}
			if !s.NextReset.IsZero() {
				t["next_reset"] = s.NextReset.Format(time.RFC3339)
			}
			transitResults[i] = t
		}
		internal.PrintJSON(map[string]interface{}{
			"transits": transitResults,
			"order":    ordered,
		})
		return nil
	}

	fmt.Println("=== GIA 带宽使用情况 ===")
	fmt.Println()
	for _, s := range statuses {
		status := "OK"
		if !s.Available {
			status = "EXHAUSTED"
		}
		fmt.Printf("%-15s  %s / %s  剩余 %.1f%%  %s\n",
			s.Name,
			internal.FormatBytes(s.UsedBW),
			internal.FormatBytes(s.MonthlyBW),
			s.RemainingPct,
			status,
		)
		if !s.NextReset.IsZero() {
			fmt.Printf("%-15s  下次重置: %s (%.0f天后)\n",
				"",
				s.NextReset.Format("2006-01-02"),
				time.Until(s.NextReset).Hours()/24,
			)
		}
	}
	fmt.Println()
	fmt.Printf("均衡优先级: %v\n", ordered)
	return nil
}

func bwgRotate() error {
	failoverCfg, err := loadFailoverConfig()
	if err != nil {
		return err
	}
	if len(failoverCfg.Transits) == 0 {
		if internal.JSONMode {
			internal.PrintJSON(map[string]string{"status": "no_transits"})
			return nil
		}
		fmt.Println("未配置 BWG 带宽监控")
		return nil
	}

	if !internal.JSONMode {
		fmt.Println("执行流量均衡...")
	}
	if err := internal.RebalanceEntryConfig(failoverCfg); err != nil {
		return fmt.Errorf("rebalance: %w", err)
	}
	if internal.JSONMode {
		internal.PrintJSON(map[string]string{"status": "ok"})
	} else {
		fmt.Println("完成")
	}
	return nil
}

func loadFailoverConfig() (*internal.FailoverConfig, error) {
	failoverPath := internal.DeployDir + "/failover.json"
	data, err := os.ReadFile(failoverPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &internal.FailoverConfig{}, nil
		}
		return nil, fmt.Errorf("read failover config: %w", err)
	}
	var cfg internal.FailoverConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse failover config: %w", err)
	}
	return &cfg, nil
}
