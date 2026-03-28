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
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}

	// Node with own BWG config: show self bandwidth
	if meta.BWG != nil && meta.Role != internal.RoleEntry {
		return bwgStatusSelf(meta)
	}

	// Entry node with CF: show all transit BWG from topology
	if meta.Role == internal.RoleEntry && meta.CFWorkerURL != "" && meta.CFAPIKey != "" {
		if err := bwgStatusFromTopology(meta); err == nil {
			return nil
		}
		// Fall through to failover.json if topology fails
	}

	// Fallback: failover.json
	return bwgStatusFromFailover()
}

// bwgStatusSelf shows the transit node's own bandwidth (reads own meta.BWG).
func bwgStatusSelf(meta *internal.Metadata) error {
	info, err := internal.GetBWGServiceInfo(meta.BWG.VEID, meta.BWG.APIKey)
	if err != nil {
		return fmt.Errorf("BWG API: %w", err)
	}

	monthlyBW := int64(float64(info.PlanMonthlyData) * info.MonthlyDataMultiplier)
	if monthlyBW == 0 {
		monthlyBW = info.PlanMonthlyData
	}
	remaining := float64(0)
	if monthlyBW > 0 {
		remaining = float64(monthlyBW-info.DataCounter) / float64(monthlyBW) * 100
	}

	if internal.JSONMode {
		internal.PrintJSON(map[string]interface{}{
			"name":          meta.Name,
			"used_bytes":    info.DataCounter,
			"monthly_bytes": monthlyBW,
			"remaining_pct": remaining,
			"next_reset":    time.Unix(info.DataNextReset, 0).Format(time.RFC3339),
		})
		return nil
	}

	fmt.Printf("=== %s 带宽使用 ===\n\n", meta.Name)
	fmt.Printf("已用:   %s / %s\n", internal.FormatBytes(info.DataCounter), internal.FormatBytes(monthlyBW))
	fmt.Printf("剩余:   %.1f%%\n", remaining)
	if info.DataNextReset > 0 {
		reset := time.Unix(info.DataNextReset, 0)
		fmt.Printf("重置:   %s (%.0f天后)\n", reset.Format("2006-01-02"), time.Until(reset).Hours()/24)
	}
	return nil
}

// bwgStatusFromTopology shows all transit BWG status from CF topology.
func bwgStatusFromTopology(meta *internal.Metadata) error {
	cfClient := internal.NewCFTopoClient(meta.CFWorkerURL, meta.CFAPIKey)
	nodes, err := cfClient.GetAllNodes()
	if err != nil {
		return err
	}

	type transitStatus struct {
		name  string
		bwg   *internal.BWGStatus
		alive bool
	}
	var transits []transitStatus
	for _, n := range nodes {
		if n.Role != internal.RoleTransit {
			continue
		}
		transits = append(transits, transitStatus{
			name:  n.Name,
			bwg:   n.BWGStatus,
			alive: time.Since(n.ReportedAt) <= internal.StaleThreshold,
		})
	}

	if internal.JSONMode {
		results := make([]map[string]interface{}, len(transits))
		for i, t := range transits {
			r := map[string]interface{}{
				"name":      t.name,
				"alive":     t.alive,
				"available": !t.bwg.IsExhausted(),
			}
			if t.bwg.HasData() {
				r["used_bytes"] = t.bwg.DataCounter
				r["monthly_bytes"] = t.bwg.PlanMonthlyData
				r["remaining_pct"] = t.bwg.RemainingPct()
			}
			results[i] = r
		}
		internal.PrintJSON(map[string]interface{}{"transits": results, "source": "topology"})
		return nil
	}

	fmt.Println("=== GIA 带宽使用情况 (拓扑) ===")
	fmt.Println()
	for _, t := range transits {
		aliveStr := ""
		if !t.alive {
			aliveStr = " [DEAD]"
		}
		if t.bwg.HasData() {
			status := "OK"
			if t.bwg.IsExhausted() {
				status = "EXHAUSTED"
			}
			fmt.Printf("%-15s  %s / %s  剩余 %.1f%%  %s%s\n",
				t.name,
				internal.FormatBytes(t.bwg.DataCounter),
				internal.FormatBytes(t.bwg.PlanMonthlyData),
				t.bwg.RemainingPct(), status, aliveStr,
			)
		} else {
			fmt.Printf("%-15s  无 BWG 监控%s\n", t.name, aliveStr)
		}
	}
	return nil
}

// bwgStatusFromFailover shows BWG status from failover.json (legacy path).
func bwgStatusFromFailover() error {
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
			"source":   "failover.json",
		})
		return nil
	}

	fmt.Println("=== GIA 带宽使用情况 (failover.json) ===")
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
	meta, err := internal.LoadMetadata()
	if err != nil {
		return err
	}

	// Primary: topology-driven
	if meta.Role == internal.RoleEntry && meta.CFWorkerURL != "" && meta.CFAPIKey != "" {
		cfClient := internal.NewCFTopoClient(meta.CFWorkerURL, meta.CFAPIKey)
		if !internal.JSONMode {
			fmt.Println("执行流量均衡 (拓扑驱动)...")
		}
		if err := internal.RebalanceFromTopology(cfClient); err != nil {
			// Fall through to failover.json
			if !internal.JSONMode {
				fmt.Printf("拓扑均衡失败: %v，尝试 failover.json...\n", err)
			}
		} else {
			if internal.JSONMode {
				internal.PrintJSON(map[string]string{"status": "ok", "source": "topology"})
			} else {
				fmt.Println("完成")
			}
			return nil
		}
	}

	// Fallback: failover.json
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
		fmt.Println("执行流量均衡 (failover.json)...")
	}
	if err := internal.RebalanceEntryConfig(failoverCfg); err != nil {
		return fmt.Errorf("rebalance: %w", err)
	}
	if internal.JSONMode {
		internal.PrintJSON(map[string]string{"status": "ok", "source": "failover.json"})
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
