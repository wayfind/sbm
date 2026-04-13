---
name: sbm-upgrade
description: "sbm 全量升级：build + 部署到所有节点。当用户说 upgrade、升级、全量部署、deploy all、更新所有节点 时触发。"
---

# sbm 全量升级

将本地代码编译并部署到所有 sbm 节点。节点列表从 dashboard topology API 动态获取。

## 前置检查

1. `go build ./...` 编译通过
2. 确认 `internal/version.go` 版本号已 bump（和上次发布不同）
3. 有未提交的改动则先 commit + push

## 部署

```bash
bash scripts/deploy-all.sh
```

选项：
- `--only <name>` — 只部署指定节点
- `--skip-build` — 跳过编译（已有最新 binary 时）
- `--dry-run` — 只打印命令不执行

脚本自动完成：
1. `make build` 交叉编译
2. 从 topology API 获取节点列表 + 当前版本
3. 并行 scp 到所有节点
4. 逐节点 stop → replace → start → verify
5. 输出汇总表

## 验证

脚本会逐节点验证 `sbm version` 输出。额外可检查：

```bash
# topology API 全局版本确认（等 5 分钟 topo 上报后）
curl -sk -H "Authorization: Bearer sbm-topo-k8xPmQ2vNfW7rYzJ" \
  "https://entry.m3task.cn:20033/topology" | python3 -c "
import sys, json
data = json.load(sys.stdin)
nodes = {}
def c(n):
    s = n.get('self', {})
    nid = s.get('node_id','')
    if nid and nid not in nodes: nodes[nid] = s
    for d in n.get('downstreams', []): c(d)
c(data)
for n in sorted(nodes.values(), key=lambda x: x.get('name','')):
    print(f\"  {n['name']:12s} v{n.get('sbm_version','?')} [{'alive' if n.get('alive') else 'DEAD'}]\")
"
```

## 添加新节点

编辑 `scripts/deploy-all.sh` 的 `NODE_SSH` 映射表：

```bash
[新节点名]="user@host|/path/to/sbm|restart_method"
# restart_method: systemd / nohup / systemd-sudo / nohup-sudo
```

节点名必须和 `sbm install --name` 一致（topology API 用此名上报）。

## 与 sbm-publish 的区别

| | sbm-publish | sbm-upgrade |
|---|---|---|
| 用途 | 正式发布 | 快速升级/hotfix |
| bump 版本 | 必须 | 必须 |
| commit + push | 是 | 是 |
| 发布到 R2 | 是 | 否 |
| deploy CF Worker | 视情况 | 否 |
| 部署节点 | 部分（metask + picaospark） | 全量 |

## 约束

- **metask_gia 是生产节点**：绝不中断 sing-box，只重启 sbm serve
- 部署前必须 bump 版本号，避免"编译了但没上线"的问题重现
