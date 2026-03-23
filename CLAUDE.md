# sbm — sing-box 部署管理工具

Go 单二进制，集成 sing-box 安装、配置生成、HTTPS 订阅服务、证书续期和服务管理。

## 用法

```bash
sbm                          # 交互式菜单（人类）
sbm install --domain <域名>  # 一键安装（AI: 加 --force 跳过确认）
sbm start / stop / restart   # 服务管理
sbm status                   # 状态 + 端口
sbm logs [-n 100]            # sing-box 日志
sbm config                   # 查看配置（脱敏）
sbm sub                      # 查看订阅
sbm cert                     # 查看证书
sbm renew                    # 证书续期
sbm serve                    # 启动订阅 HTTPS 服务（前台，systemd 调用）
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
- 订阅: `https://metaskgia.vspeak.top:20032/sub/hz-office`
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
main.go              # 入口，CLI + 交互式菜单
cmd/
  install.go         # 安装子命令
  serve.go           # HTTPS 订阅服务
  manage.go          # start/stop/restart/status/logs
  info.go            # config/sub/cert/metadata/version
  renew.go           # 证书续期
  prompt.go          # 交互式输入
internal/
  config.go          # 配置结构体与读写
  keygen.go          # 密钥生成
  systemd.go         # systemd 服务管理
  firewall.go        # firewall-cmd 操作
  subscription.go    # 订阅 URI 生成
Makefile             # 构建与部署
```
