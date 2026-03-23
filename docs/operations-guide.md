# sbm 部署手册

## 架构

### 流量方向

```
┌─────────┐  Hy2/VLESS  ┌──────────┐  Hy2/VLESS  ┌──────────┐  Hy2/VLESS  ┌──────────┐
│  Client  │ ──────────► │  Entry   │ ──────────► │ Transit  │ ──────────► │   Exit   │ ──► Internet
│ (手机/PC) │            │ (国内云)  │            │ (CN2 GIA) │            │  (家宽)   │   住宅IP
└─────────┘             └──────────┘             └──────────┘            └──────────┘
```

**三条路径：**
- 一般海外：Client → Entry → Transit → Internet（Transit 直连出站）
- AI/住宅IP：Client → Entry → Transit → Exit → Internet（per-exit 通道链）
- 国内：Client → Entry → direct

### Per-Exit 通道链（核心机制）

每个 Exit 节点在整条链路上建立专属通道：

```
Exit 部署并上报 → Transit 自动创建:
  · 出站到 Exit (hy2/vless)
  · per-exit SOCKS 入站 (端口随机, 带认证, 对外开放)
  · 路由: 该入站 → 该出站
                    → Transit 上报到 Entry → Entry 自动创建:
                        · 出站到 Transit 的 per-exit 端口 (带认证)
                        · SOCKS5 入站 (127.0.0.1, 随机端口)
                        · 路由: 该入站 → 该出站
```

最终效果：
```
App → Entry:127.0.0.1:10801 (socks5) → Transit:45678 (socks+auth) → Exit:34521 (hy2) → Internet
```

### 拓扑上报（右→左）

```
Entry ◄── Transit ◄── Exit
```

每个节点每 5 分钟向 metadata.upstreams 的 `/report` 端点 POST 自己的拓扑。
Entry 和 Transit 每 5 分钟 reconcile：检测拓扑变化 → 更新 sing-box 配置 → reload。

---

## 部署顺序

**Transit → Exit → Entry → Client**

---

## 0. 构建

```bash
cd /mnt/d/prj/sbm
make build    # 产出 ./sbm (linux/amd64)
```

---

## 1. 前置条件（每台服务器）

```bash
# 安装 sing-box
bash <(curl -fsSL https://sing-box.app/deb-install.sh)
sing-box version

# 确保 firewalld 运行
systemctl enable --now firewalld

# 域名已解析到本机 IP（ACME 证书需要）
```

---

## 2. 部署 Transit 节点

**以 metask_gia (67.230.165.100, metaskgia.vspeak.top) 为例。**

### 2.1 上传并安装

```bash
scp sbm root@67.230.165.100:~/sing-box-manager/sbm
ssh root@67.230.165.100 -o ServerAliveInterval=60

~/sing-box-manager/sbm install --role transit --domain metaskgia.vspeak.top --force
```

### 2.2 记录入站信息

```bash
~/sing-box-manager/sbm metadata --json
# 记录: vless_port, hy2_port, uuid, public_key, short_id

~/sing-box-manager/sbm config --json | python3 -c "
import json,sys; c=json.load(sys.stdin)
for ib in c['inbounds']:
    print(ib['tag'], ib.get('listen_port'),
          ib.get('users',[{}])[0].get('password',''),
          ib.get('obfs',{}).get('password',''))
"
# 记录: hy2 password, obfs password
```

### 2.3 配置节点名称和上游

编辑 `/etc/sing-box/metadata.json`，添加：

```json
{
  "name": "metask",
  "api_token": "你的共享密钥（所有节点一致）"
}
```

> Transit 的 upstreams 暂不配（Entry 还没部署）。第 4 步 Entry 部署后回来补。

### 2.4 验证

```bash
~/sing-box-manager/sbm status
# sing-box: active, sub-server: active

~/sing-box-manager/sbm sub
# 应显示订阅地址和 vless/hy2 URI
```

**对每台 GIA 服务器重复 2.1-2.4。**

---

## 3. 部署 Exit 节点

**以 lisa家宽 (154.37.209.17, lisa.vspeak.top) 为例。**

### 3.1 安装

```bash
scp sbm david@154.37.209.17:~/sbm
ssh david@154.37.209.17
sudo ~/sbm install --role exit --domain lisa.vspeak.top --force
```

### 3.2 配置名称、上游、token

编辑 `/etc/sing-box/metadata.json`：

```json
{
  "name": "lisa",
  "api_token": "共享密钥",
  "upstreams": [
    {
      "name": "metask-gia",
      "address": "metaskgia.vspeak.top",
      "sub_url": "https://metaskgia.vspeak.top:20033/sub/hz-office",
      "priority": 0
    }
  ]
}
```

> `api_token` 必须和 Transit 上配的一致。

### 3.3 重启使上游生效

```bash
sudo systemctl restart sub-server
```

### 3.4 验证上报

等 ~30 秒后，在 **Transit** 节点上：

```bash
~/sing-box-manager/sbm topology
# 应看到 lisa 出现在 downstream
```

等 ~5 分钟（Transit reconcile），在 **Transit** 上：

```bash
~/sing-box-manager/sbm config --json | python3 -c "
import json,sys; c=json.load(sys.stdin)
for ib in c['inbounds']:
    if ib['tag'].startswith('for-exit-'):
        print('入站:', ib['tag'], 'port:', ib['listen_port'])
for ob in c['outbounds']:
    if ob['tag'].startswith('exit-'):
        print('出站:', ob['tag'])
"
# 应看到 for-exit-lisa 入站 + exit-lisa 出站
```

```bash
~/sing-box-manager/sbm metadata --json | python3 -c "
import json,sys; m=json.load(sys.stdin)
print('exit_ports:', m.get('exit_ports'))
print('exit_auth:', m.get('exit_auth'))
"
# 应看到 lisa 的端口和认证密码已持久化
```

**对每台家宽重复 3.1-3.4。**

---

## 4. 部署 Entry 节点

**以 picaospark (175.27.169.180) 为例。**

### 4.1 准备 upstreams.json

用第 2 步记录的 Transit 入站信息填写：

```bash
cat > /tmp/upstreams.json << 'JSONEOF'
{
  "transits": [
    {
      "name": "metask",
      "address": "metaskgia.vspeak.top",
      "protocol": "hysteria2",
      "port": 48732,
      "password": "从 transit config 获取的 hy2 password",
      "obfs_pass": "从 transit config 获取的 obfs password",
      "sni": "metaskgia.vspeak.top"
    }
  ],
  "exits": [],
  "socks5": {
    "port": 17890,
    "user": "shuzuan",
    "pass": "antigravity_h800",
    "direct_ips": ["111.6.70.75/32", "111.6.70.85/32"]
  }
}
JSONEOF
```

> **exits 留空**——Exit 信息会通过拓扑自动发现，不需要手动填。
> socks5 是 H800 专线中转，不需要可删掉整个 socks5 块。

### 4.2 上传并安装

```bash
scp /tmp/upstreams.json david@175.27.169.180:~/upstreams.json
scp sbm david@175.27.169.180:~/sbm
ssh david@175.27.169.180

sudo ~/sbm install --role entry --domain entry.vspeak.top --upstreams ~/upstreams.json --force
```

### 4.3 回去给 Transit 配上游指向 Entry

在**每台 Transit** 上编辑 `/etc/sing-box/metadata.json`，添加 upstreams：

```json
{
  "upstreams": [
    {
      "name": "entry-picaospark",
      "address": "entry.vspeak.top",
      "sub_url": "https://entry.vspeak.top:20033/sub/hz-office",
      "priority": 0
    }
  ]
}
```

```bash
sudo systemctl restart sub-server
```

### 4.4 可选：配置 BWG 流量均衡

```bash
sudo cat > /etc/sing-box/failover.json << 'EOF'
{
  "transits": [
    {"name": "metask", "bwg": null},
    {"name": "shuzuan", "bwg": {"veid": "2058893", "api_key": "private_MRJQxzmDBj5KEnabUAr7qW76"}},
    {"name": "yl", "bwg": {"veid": "2059033", "api_key": "private_pGhH68wwKUTvfsyKGzJCZMQ3"}}
  ]
}
EOF
sudo systemctl restart sub-server
```

### 4.5 验证自动配置

等待 ~10 分钟（拓扑上报 5min + reconcile 5min）。

```bash
# 完整拓扑
~/sbm topology
# [entry] picaospark (entry.vspeak.top) alive
# ├── [transit] metask (metaskgia.vspeak.top) alive
# │     hy2 udp/48732
# │     for-exit-lisa socks tcp/45678    ← per-exit 通道
# │   └── [exit] lisa (lisa.vspeak.top) alive

# 确认 per-exit SOCKS5 入站已创建
~/sbm config --json | python3 -c "
import json,sys; c=json.load(sys.stdin)
for ib in c['inbounds']:
    if ib['tag'].startswith('socks-exit-'):
        print(ib['tag'], '127.0.0.1:' + str(ib['listen_port']))
"
# 应看到: socks-exit-lisa 127.0.0.1:10801

# 确认 per-exit 出站已创建
~/sbm config --json | python3 -c "
import json,sys; c=json.load(sys.stdin)
for ob in c['outbounds']:
    if ob['tag'].startswith('via-transit-exit-'):
        print(ob['tag'], ob['server'] + ':' + str(ob['server_port']))
"
# 应看到: via-transit-exit-lisa metaskgia.vspeak.top:45678

# 确认 per-exit 端口持久化
~/sbm metadata --json | python3 -c "
import json,sys; m=json.load(sys.stdin)
print('exit_socks_ports:', m.get('exit_socks_ports'))
"

# 功能验证：通过特定 exit 出站
curl --socks5 127.0.0.1:10801 https://api.ipify.org
# 应返回 lisa 家宽的公网 IP
```

---

## 5. 生成客户端配置

```bash
# Linux
./sbm client gen --upstream "https://entry.vspeak.top:20033/sub/hz-office" --platform linux --output config.json

# Windows
./sbm client gen --upstream "..." --platform windows --output config.json

# OpenWrt
./sbm client gen --upstream "..." --platform openwrt --output config.json

# 使用
sing-box run -c config.json
```

---

## 6. 测试自动配置

### 新增 Exit

```bash
# 在新家宽上安装
sudo ~/sbm install --role exit --domain newexit.vspeak.top --force
# 编辑 metadata 加 name + upstreams(指向transit) + api_token
sudo systemctl restart sub-server
```

**等 ~10 分钟。** 在 Entry 上验证：
```bash
~/sbm topology                                    # newexit 出现
~/sbm config --json | grep socks-exit-newexit     # per-exit SOCKS5 入站出现
curl --socks5 127.0.0.1:<分配的端口> https://api.ipify.org  # 出口 IP 验证
```

### 下线 Exit

```bash
# 在 exit 上停服务
sudo ~/sbm stop
```

**等 ~25 分钟**（20min dead + 5min reconcile）。
```bash
~/sbm topology    # exit 标记 dead 或消失
~/sbm config --json | grep socks-exit-xxx  # 从配置中移除
```

### 新增 Transit

```bash
# 在新 GIA 上安装
~/sbm install --role transit --domain yl.vspeak.top --force
# 编辑 metadata 加 name + upstreams(指向entry) + api_token
sudo systemctl restart sub-server
```

**等 ~10 分钟。** Entry 上 `sbm config` 确认 `transit-yl` 出现在 outbounds。

---

## 7. 日常运维速查

```bash
sbm status [--json]          # 服务状态 + 端口
sbm topology [--json]        # 拓扑树
sbm bwg status [--json]      # GIA 带宽
sbm bwg rotate               # 手动流量均衡
sbm cert [--json]            # 证书有效期
sbm renew                    # 手动证书续期
sbm logs [-n 200] [--json]   # 日志
sbm config [--json]          # sing-box 配置
sbm metadata [--json]        # 元数据
sbm sub [--json]             # 订阅信息
sbm start / stop / restart   # 服务管理
```

---

## 8. 文件位置

| 文件 | 说明 |
|------|------|
| `/etc/sing-box/config.json` | sing-box 配置（reconcile 自动管理） |
| `/etc/sing-box/metadata.json` | sbm 元数据（角色、入站、上游、端口映射） |
| `/etc/sing-box/failover.json` | BWG 均衡配置（entry 可选） |
| `/etc/systemd/system/sing-box.service` | sing-box unit |
| `/etc/systemd/system/sub-server.service` | 订阅+拓扑+reconcile 服务 unit |
| `/etc/cron.d/renew-cert` | 证书续期 cron |

---

## 9. 关键时间线

| 事件 | 时间 |
|------|------|
| 拓扑上报周期 | 每 5 分钟 |
| Reconcile 周期 | 每 5 分钟 |
| BWG 均衡周期 | 每 1 小时 |
| 节点标记 dead | 上报中断 20 分钟后 |
| 节点条目清理 | 上报中断 1 小时后 |
| 新节点上线到 Entry 自动配置 | ~10 分钟 |
| 节点下线到 Entry 自动移除 | ~25 分钟 |

---

## 10. 故障排查

```bash
# 查看 reconcile/topology 日志
sbm logs -n 200 | grep -E '\[reconcile\]|\[topology\]|\[failover\]'

# 拓扑不更新
# → 检查 api_token 两端是否一致
# → 检查 upstreams 配置是否正确
# → 检查 sub-server 是否运行: sbm status

# per-exit 通道不出现
# → Transit 上: sbm topology --json | python3 -c "import json,sys; print(json.dumps(json.load(sys.stdin),indent=2))"
# → 确认 exit 在 transit 的 downstream 中
# → 确认 transit metadata 有 exit_ports 和 exit_auth

# ACME 证书签发失败
# → 检查 80 端口: curl http://域名 (应可达)
# → 检查域名解析: dig 域名

# sing-box 启动失败
sing-box check -c /etc/sing-box/config.json

# 验证 per-exit 通道端到端
curl --socks5 127.0.0.1:<entry端口> https://api.ipify.org  # 应返回 exit 的 IP
```

---

## Checklist

```
构建:
□ make build
□ scp sbm 到所有服务器

Transit × N:
□ sbm install --role transit --domain <域名> --force
□ 记录入站信息 (metadata --json / config --json)
□ 编辑 metadata: name + api_token
□ sbm status 确认服务运行

Exit × N:
□ sbm install --role exit --domain <域名> --force
□ 编辑 metadata: name + api_token + upstreams(→transit)
□ systemctl restart sub-server
□ Transit 上 sbm topology 确认 exit 出现
□ 等 5 分钟, Transit 上确认 per-exit 入站+出站生成

Entry:
□ 准备 upstreams.json (transit 信息, exits 留空)
□ sbm install --role entry --domain <域名> --upstreams xxx.json --force
□ 回每台 Transit 编辑 metadata 加 upstreams(→entry)
□ 每台 Transit: systemctl restart sub-server
□ 可选: 创建 failover.json
□ 等 10 分钟, sbm topology 确认完整链路
□ 确认 per-exit SOCKS5 入站自动创建
□ curl --socks5 验证端到端

Client:
□ sbm client gen --upstream <URL> --platform <平台>
□ sing-box run -c config.json
□ 验证: 国内直连 / 海外走代理 / 特定 exit 走 SOCKS5
```
