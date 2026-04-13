---
name: sbm-inspect
description: "sbm 项目全节点巡检：基础健康 + 告警计数 + 证书 + BWG 带宽 + 用户活跃 + CF 双控制面比对。当用户说巡检、健康检查、check 节点、看节点状态、health check 时触发。"
---

# sbm 全节点巡检

## 用法

直接运行脚本（cwd 在项目根时）：

```bash
bash .claude/skills/sbm-inspect/scripts/inspect.sh
```

可选参数：
- `--since N` — 告警计数的时间窗口，默认 15 分钟

```bash
bash .claude/skills/sbm-inspect/scripts/inspect.sh --since 30
```

脚本会自动从自身位置推算项目根，cwd 在哪都能跑。

## 输出

脚本输出 markdown 格式，包含三段：

1. **节点健康表** — 每节点 active/restarts/版本/证书/BWG/用户/告警计数
2. **CF 注册表** — 双控制面 alive 状态 + 上报新鲜度
3. **异常摘要** — 自动累积所有偏离基线的项

退出码：0 全绿 / 1 有异常。

整个脚本耗时约 5-15 秒（8 节点并行 ssh）。

## 工作机制

- **节点列表来源**：复用 `scripts/deploy-all.sh` 的 `NODE_SSH` map，自动跟踪 add/remove node，无需在巡检脚本里重复维护
- **采集方式**：每节点 1 次 ssh，远程串行跑 `sbm --json {version,metadata,cert,bwg status,user list}` + journalctl 告警计数 + systemd 状态，结果用 `KEY=base64(value)` 行回传，避免 quote/换行问题
- **8 节点并行**：bash 后台 + wait
- **CF 数据**：脚本最后单独 curl `https://tun.vspeak.top/nodes`，CF API key 从 `$SBM_CF_API_KEY` 读，默认值在脚本里

## 阈值规则

异常判定（写在 inspect.sh 里）：

| 项 | 阈值 |
|----|------|
| 服务 | sbm-server 或 sing-box ≠ active |
| sing-box 异常重启 | NRestarts > 0 |
| sbm-server 告警 | 时间窗口内 WARN/ALERT/ERR > 0（附带最近 5 行样本） |
| sing-box 致命 | 时间窗口内 FATAL/panic > 0 |
| 证书 | 剩余 < 14 天 |
| BWG 带宽 | 剩余 < 20% |
| CF 上报 | 单节点 reported_at > 6 分钟 |
| 版本 | 集群存在多个 sbm 版本 |

## 给读者（Claude）的解读指引

- **报告是给 david 自己看的**，不需要外发。直接把 markdown 完整贴出，再补一句话点评关键发现即可。不要复述表格内容
- **告警样本里的 ALERT 值得仔细看**：reconcile.go 的 `ALERT: upstream X in CF but not in chain` 是 v2.9.3 之后真实的数据面诊断信号，不是噪音；指向 CF 链路正常但 chain 链路断了
- **cert 列为空** 是预期的：transit/exit 节点用预置证书或 s-ui 管理，sbm cert 子命令找不到 ACME 证书，不算异常
- **BWG 列为空** 也是预期的：BWG 只在搬瓦工 transit（shuzuan/yl/metask）有意义
- **用户列为空** 也是预期的：user list 只在 entry 有 SQLite
- **如果"SSH/采集失败"覆盖多个节点** → 大概率是本机网络/SSH key 问题，不是节点问题
- **如果只一两个节点失败** → 该节点 down 或 SSH 阻塞，去单独排查
- **如果发现真实异常**（不是已知的 false positive），按 sbm 项目的"诊断标准开局"（见 `project_sbm_architecture.md` 模式库）去定位：取 ssh 状态 + CF /nodes + 不同 AS 探测三个证据源，不要先读代码推
