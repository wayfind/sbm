---
name: sbm-deploy
description: |
  sbm 节点部署：向 sbm 代理网络添加新节点或替换现有节点。当用户说"部署节点"、"安装 transit/exit/entry"、"添加新服务器"、"替换 s-ui"时触发。
  覆盖：前置检查、处理现有服务、安装配置、BWG 带宽监控、拓扑验证、全链路检查。
---

# sbm 节点部署

## 部署顺序

多节点操作：**exit → transit → entry**。exit 无依赖，transit 被 entry 依赖，entry 是用户入口最后动。

## 流程

### 1. 前置检查

```bash
systemctl list-units --type=service | grep -E 'sing-box|s-ui|sbm'
ss -tlnp | grep -E '<端口>'
ls /etc/sing-box/ /usr/local/s-ui/ 2>&1
ls /root/cert/<domain>/ /etc/letsencrypt/live/<domain>/ 2>&1
sing-box version
grep ExecStart /etc/systemd/system/sbm-server.service
```

### 2. 处理现有服务

**s-ui → sbm 替换**（用户确认后）：
```bash
systemctl stop s-ui && systemctl disable s-ui
rm -f /etc/systemd/system/s-ui.service && systemctl daemon-reload
rm -rf /usr/local/s-ui/ /usr/local/bin/s-ui
```

**旧 sbm 升级**：`--force` 自动备份并复用密钥/端口。

### 3. 上传二进制

R2 安装脚本（优先）：
```bash
curl -fsSL https://tun.vspeak.top/install | bash -s -- --download-only
```

国内慢时直接 SCP：
```bash
scp /mnt/d/prj/sbm/sbm <user>@<host>:/usr/local/bin/sbm
scp /mnt/d/prj/sbm/sing-box-linux-amd64 <user>@<host>:/usr/local/bin/sing-box
```

### 4. 安装

**Transit**：
```bash
sbm install --role transit --domain <域名> --name <名称> \
  --vless-port <端口> --hy2-port <端口> \
  --cert <证书路径> --key <私钥路径> \
  --cf-url https://tun.vspeak.top --cf-key <cf_api_key> \
  --bwg-veid <VEID> --bwg-api-key <BWG_API_KEY> \
  --force
```

**Exit**：
```bash
sbm install --role exit --domain <域名或IP> --name <名称> \
  --cf-url https://tun.vspeak.top --cf-key <cf_api_key> \
  --force
```

**Entry**：
```bash
sbm install --role entry --domain <域名> --name <名称> \
  --upstreams <upstreams.json> \
  --cf-url https://tun.vspeak.top --cf-key <cf_api_key> \
  --force
```

参数获取：
- `--cf-key`：`cat /etc/sing-box/metadata.json | jq -r .cf_api_key`（任意已有节点）
- `--bwg-*`：见 CLAUDE.md 搬瓦工 API 表（仅搬瓦工服务器）
- `--cert/--key`：已有证书路径；不指定则 ACME 自动签发
- `--vless-port/--hy2-port`：指定可保留原端口，不指定则随机

### 5. 验证

```bash
sbm status                    # 两个 service 均 active
sbm bwg status                # BWG 带宽（有 BWG 配置时）
```

拓扑验证：
```bash
curl -s 'https://tun.vspeak.top/nodes' -H 'X-API-Key: <key>'
```

确认 entry 日志 reconcile 发现新节点：`journalctl -u sbm-server -n 20`

## 自动发现

部署后无需手动配置，以下自动发生：

| 事件 | 行为 | 延迟 |
|------|------|------|
| 新 transit | entry 加入 urltest | ~1min |
| 新 exit | entry + transit 创建 per-exit 通道 | ~1min |
| transit 有 BWG | entry 按带宽均衡排序 | ~5min |

## 约束

- **metask_gia 是生产节点**：绝不中断 sing-box，只重启 sbm-server
- sbm-server ExecStart 路径因节点而异（`/usr/local/bin/sbm`、`/home/david/sbm`、`~/sing-box-manager/sbm`），部署时确认并同步
- 替换 s-ui 前需用户确认（订阅用户会失效）
