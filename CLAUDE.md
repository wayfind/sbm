# sbm — sing-box 部署管理工具

Go 单二进制，管理链式 sing-box 代理网络：接入层→传输层→出口层。

## 用法

```bash
sbm                                              # 交互式菜单
sbm install --role <角色> --domain <域名>        # 部署节点
sbm start / stop / restart                        # 服务管理
sbm status                                        # 状态 + 端口
sbm logs [-n 100]                                 # sing-box 日志
sbm config                                        # 查看配置（脱敏）
sbm sub                                           # 查看订阅
sbm cert                                          # 查看证书
sbm renew                                         # 证书续期
sbm topology                                      # 查看链路拓扑
sbm bwg status                                    # 查看 GIA 带宽
sbm bwg rotate                                    # 手动流量均衡
sbm client gen --upstream <URL> --platform <平台>  # 生成客户端配置
sbm serve                                         # 启动订阅 HTTPS 服务（前台）
```

## 构建与部署

```bash
make build    # 交叉编译 linux/amd64
make deploy   # scp 到目标服务器
```

---

## 相关服务器

### 服务器列表

| 名称 | 用户 | IP | 端口 | 提供商 | 备注 |
|------|------|-----|------|--------|------|
| shuzuan_gia | root | 89.208.252.173 | 22 | 搬瓦工 | s-ui 面板，sing-box 代理节点 |
| yl_gia | root | 89.208.248.209 | 22 | 搬瓦工 | |
| metask_gia | root | 67.230.165.100 | 22 | 搬瓦工 | sbm 管理，AlmaLinux 9.7 |
| lisa家宽 | david | 154.37.209.17 | 22 | - | |
| yl_lisa家宽 | root | 104.247.120.170 | 14303 | - | |
| picaospark网关 | david | 175.27.169.180 | 22 | 腾讯云 | s-ui + H800 中转 |
| ai网关 | ubuntu | napi.origintask.cn | 22 | 腾讯云 | |

> SSH 保活: `-o ServerAliveInterval=60 -o ServerAliveCountMax=5`

### 搬瓦工 API

| 服务器 | VEID | API Key |
|--------|------|---------|
| shuzuan_gia | 2058893 | `private_MRJQxzmDBj5KEnabUAr7qW76` |
| yl_gia | 2059033 | `private_pGhH68wwKUTvfsyKGzJCZMQ3` |
| metask_gia | 2089272 | `private_rZNv77fMDaGxVUWrrzjjcUzU` |

> API 文档: https://api.64clouds.com/v1/

---

### shuzuan_gia (si.vspeak.top)

s-ui 管理面板，sing-box 内核，多用户代理节点。

| 服务 | 目录 | 管理方式 |
|------|------|----------|
| s-ui | `/usr/local/s-ui/` | systemd (`s-ui.service`) / 命令 `s-ui` |

**入站端口**:
- VLESS Reality: 19728/tcp (SNI: www.walmart.com)
- Hysteria2: 13642/udp (salamander obfs, 证书文件)
- 其他 hy2 用户: 13643-13652/udp

**订阅服务**: `https://si.vspeak.top:20032/sb/<name>`

**证书**: `/root/cert/si.vspeak.top/` (fullchain.pem + privkey.pem)

---

### metask_gia (metaskgia.vspeak.top)

sbm 管理的独立节点，AlmaLinux 9.7。

**接入信息**:
- IP: 67.230.165.100
- 域名: `metaskgia.vspeak.top`
- 系统: AlmaLinux 9.7, 3核/2GB/39GB

**安全加固** (2026-03-23):
- SSH: 禁用密码，root 仅密钥，MaxAuthTries=3
- 防火墙: firewalld
- fail2ban: sshd jail
- 自动更新: dnf-automatic

**sing-box**: 由 sbm 管理
- 配置: `/etc/sing-box/config.json`
- 元数据: `/etc/sing-box/metadata.json`
- 订阅: `https://metaskgia.vspeak.top:20033/sub/hz-office`
- 管理: `~/sing-box-manager/sbm`

**端口**: 通过 `sbm install` 随机生成，当前值见 `sbm status`

**证书续期**: cron 每周一 03:17, `sbm renew`

---

### picaospark网关 (175.27.169.180)

腾讯云，s-ui + H800 流量中转。

| 服务 | 管理方式 |
|------|----------|
| s-ui | systemd (`s-ui.service`) / `s-ui.sh` |
| sing-box 中转 | s-ui 内置 |

**s-ui 面板**:
- Web: http://175.27.169.180:2095
- Sub: http://175.27.169.180:2096

**H800 中转配置**:
- SOCKS5 入站: 端口 17890, 用户 `shuzuan` / 密码 `antigravity_h800`
- 路由: `111.6.70.75/32`, `111.6.70.85/32` → direct
- 链路: `客户端 → 腾讯云:17890 → H800`

---

### lisa家宽 (154.37.209.17)

| 服务 | 状态 |
|------|------|
| claude-relay-service | ⚠️ 已关闭 (2026-02-23) |

---

### ai网关 (napi.origintask.cn)

腾讯云，API 网关服务。

---

## 域名与证书

| 域名 | 指向 | 用途 |
|------|------|------|
| si.vspeak.top | shuzuan_gia (89.208.252.173) | s-ui 面板 + 订阅 + hy2 证书 |
| metaskgia.vspeak.top | metask_gia (67.230.165.100) | sbm 订阅 + hy2 ACME 证书 |
| yl.vspeak.top | yl_gia (89.208.248.209) | |
| lisa.vspeak.top | lisa家宽 (154.37.209.17) | |

## 项目结构

```
main.go                  # 入口，CLI + 交互式菜单
cmd/
  install.go             # 安装子命令 (--role transit/exit/entry)
  serve.go               # HTTPS 订阅服务 + 拓扑API + 故障切换
  manage.go              # start/stop/restart/status/logs
  info.go                # config/sub/cert/metadata/version
  renew.go               # 证书续期
  prompt.go              # 交互式输入
  topology.go            # sbm topology 子命令
  bwg.go                 # sbm bwg status/rotate 子命令
  client.go              # sbm client gen 子命令
internal/
  config.go              # Metadata v2 结构体、迁移、sing-box 类型定义
  config_gen.go          # 角色分发 GenerateConfig
  config_transit.go      # Transit 配置生成 (VLESS+Hy2入站, direct出站)
  config_exit.go         # Exit 配置生成 (家宽出口)
  config_entry.go        # Entry 配置生成 (路由分流+urltest+链式代理)
  config_client.go       # Client 配置生成 (TUN+fake-ip+分流规则)
  topology.go            # 拓扑数据结构、HTTP handler、逐级上报
  bwg.go                 # 搬瓦工 API 客户端
  failover.go            # 智能轮换逻辑 (BWG流量均衡)
  rules.go               # 路由规则定义 (AI住宅域名、rule_set URL)
  keygen.go              # 密钥生成
  systemd.go             # systemd 服务管理
  firewall.go            # firewall-cmd 操作
  subscription.go        # 订阅 URI 生成
Makefile                 # 构建与部署
```

## 节点角色

| 角色 | 说明 | 安装命令 |
|------|------|----------|
| transit | 传输节点 (CN2 GIA) | `sbm install --role transit --domain <域名>` |
| exit | 出口节点 (家宽IP) | `sbm install --role exit --domain <域名>` |
| entry | 接入节点 (国内云) | `sbm install --role entry --domain <域名> --upstreams <配置文件>` |
| client | 客户端 | `sbm client gen --upstream <订阅URL> --platform <平台>` |

## 配置文件

Entry 节点需要 `--upstreams` JSON 文件描述上游信息:
```json
{
  "transits": [
    {"name": "shuzuan", "address": "si.vspeak.top", "protocol": "hysteria2", "port": 13642, "password": "xxx", "obfs_pass": "xxx", "sni": "si.vspeak.top"},
    {"name": "metask", "address": "metaskgia.vspeak.top", "protocol": "hysteria2", "port": 12345, "password": "xxx", "sni": "metaskgia.vspeak.top"}
  ],
  "exits": [
    {"name": "lisa", "address": "lisa.vspeak.top", "protocol": "vless", "port": 19728, "uuid": "xxx", "sni": "www.walmart.com", "public_key": "xxx", "short_id": "xxx", "via_transit": "shuzuan"}
  ],
  "socks5": {"port": 17890, "user": "shuzuan", "pass": "antigravity_h800", "direct_ips": ["111.6.70.75/32", "111.6.70.85/32"]}
}
```

Entry 节点 BWG 流量均衡需要 `/etc/sing-box/failover.json`:
```json
{
  "transits": [
    {"name": "shuzuan", "bwg": {"veid": "2058893", "api_key": "private_xxx"}},
    {"name": "yl", "bwg": {"veid": "2059033", "api_key": "private_xxx"}},
    {"name": "metask", "bwg": null}
  ]
}
```
