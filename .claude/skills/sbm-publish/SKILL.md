---
name: sbm-publish
description: "sbm 项目发布流程：bump 版本、commit、publish 到 CF R2、deploy CF Worker、push git、更新远程节点。当用户说 publish、发布、上线、deploy sbm 时触发。"
---

# sbm 发布

## 前置检查

1. `go build ./...` 编译通过
2. `git diff --stat` 确认改动范围

## 步骤

### 1. Bump 版本

编辑 `cmd/info.go` 的 `sbmVersion`。规则：patch = bug fix，minor = 功能/行为变更。
已 bump 过则跳过。

### 2. Commit + Push

按项目风格 commit，然后 `git push origin master`。

### 3. Publish 到 R2

```bash
make publish   # timeout 120s, 成功输出 "Published sbm vX.Y.Z"
```

### 4. Deploy Worker（仅 cf-worker/ 有改动时）

```bash
make deploy-worker   # 成功输出 "Deployed sbm-topo triggers"
```

### 5. 更新节点

全量部署到所有节点：

```bash
bash scripts/deploy-all.sh --skip-build   # binary 已在 step 3 编译
```

单节点部署（按需）：

```bash
bash scripts/deploy-all.sh --skip-build --only metask
```

**关键约束：** metask_gia 是生产节点，sing-box 服务绝不能中断。只重启 sbm serve，不碰 sing-box。

### 6. 验证

确认所有节点 `sbm version` 输出新版本号（脚本会自动验证并输出汇总表）。
