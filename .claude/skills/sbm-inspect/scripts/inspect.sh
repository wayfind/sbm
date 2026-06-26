#!/bin/bash
# sbm 全节点巡检脚本
#
# Usage: scripts/inspect.sh [--since MINUTES]
#
# 输出：节点健康表 + CF 注册表 + 异常摘要（markdown，给 Claude 读）
#
# 采集维度：
#   1. 基础健康  systemd active + NRestarts + sbm/sing-box 版本
#   2. 告警计数  sbm-server WARN/ALERT/ERR + sing-box FATAL/panic（最近 N 分钟）
#   3. 证书      days_remaining（sbm --json cert）
#   4. BWG 带宽  transit 节点剩余 %（sbm --json bwg status）
#   5. 用户活跃  entry 节点 enabled 用户数（sbm --json user list）
#   6. CF 控制面 tun.vspeak.top/nodes 的 alive + reported_at 新鲜度

set -uo pipefail

# Resolve project root from this script's location:
#   <PROJECT_ROOT>/.claude/skills/sbm-inspect/scripts/inspect.sh
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
cd "$PROJECT_ROOT"

SINCE_MIN=15
while [ $# -gt 0 ]; do
  case "$1" in
    --since) SINCE_MIN="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

# --- 复用 deploy-all.sh 的 NODE_SSH 列表 ---
DEPLOY_SH="$PROJECT_ROOT/scripts/deploy-all.sh"
[ -f "$DEPLOY_SH" ] || { echo "deploy-all.sh not found at $DEPLOY_SH" >&2; exit 1; }

declare -A NODE_SSH
while IFS= read -r line; do
  if [[ $line =~ ^[[:space:]]*\[([a-z0-9-]+)\]=\"([^\"]+)\" ]]; then
    NODE_SSH[${BASH_REMATCH[1]}]="${BASH_REMATCH[2]}"
  fi
done < <(awk '/^declare -A NODE_SSH/,/^\)/' "$DEPLOY_SH")

[ ${#NODE_SSH[@]} -eq 0 ] && { echo "Failed to parse NODE_SSH from $DEPLOY_SH" >&2; exit 1; }

SSH_OPTS="-o ConnectTimeout=10 -o ServerAliveInterval=60 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=no -o BatchMode=yes"
TMPDIR=$(mktemp -d)
[ -n "${SBM_INSPECT_KEEP_TMP:-}" ] || trap 'rm -rf "$TMPDIR"' EXIT
[ -n "${SBM_INSPECT_KEEP_TMP:-}" ] && echo "DEBUG: TMPDIR=$TMPDIR" >&2

# --- 远程脚本：每节点单独 ssh，远程跑命令并打印 KEY=base64(value) 行 ---
# base64 包裹值是为了避免任何 quote/换行 问题
build_remote_script() {
  local bin_path="$1"
  local sudo_prefix="$2"
  local since_min="$3"
  cat <<REMOTE
set +e
SBM='${bin_path}'
SINCE='${since_min} min ago'
b64() { base64 -w0; }
emit() { echo "\$1=\$(b64)"; }

${sudo_prefix}systemctl is-active sbm-server sing-box 2>/dev/null | tr '\n' ' ' | emit ACTIVE
${sudo_prefix}systemctl show sing-box -p NRestarts 2>/dev/null | cut -d= -f2 | tr -d '\n' | emit NRESTARTS
\$SBM --json version 2>/dev/null | emit VERSION
\$SBM --json metadata 2>/dev/null | emit METADATA
\$SBM --json cert 2>/dev/null | emit CERT
\$SBM --json bwg status 2>/dev/null | emit BWG
\$SBM --json user list 2>/dev/null | emit USER
${sudo_prefix}journalctl -u sbm-server --since "\$SINCE" --no-pager 2>/dev/null | grep -cE 'WARN|ALERT|ERR' | tr -d '\n' | emit SBMWARN
${sudo_prefix}journalctl -u sing-box --since "\$SINCE" --no-pager 2>/dev/null | grep -cE 'FATAL|panic' | tr -d '\n' | emit SBFATAL
${sudo_prefix}journalctl -u sbm-server --since "\$SINCE" --no-pager 2>/dev/null | grep -E 'WARN|ALERT|ERR' | tail -5 | emit WARNSAMPLE
# 系统健康维度
df -h / 2>/dev/null | tail -1 | awk '{print \$5}' | tr -d '%' | tr -d '\n' | emit DISKPCT
${sudo_prefix}journalctl --disk-usage 2>/dev/null | grep -oP '\d+\.\d+[KMG]' | head -1 | tr -d '\n' | emit JOURNALSIZE
${sudo_prefix}systemctl --failed --no-legend 2>/dev/null | wc -l | tr -d '\n' | emit FAILEDUNITS
${sudo_prefix}systemctl --failed --no-legend 2>/dev/null | head -5 | emit FAILEDSAMPLE
# SSH 暴力破解检测（最近 15 分钟）
tail -500 /var/log/auth.log 2>/dev/null | grep "Failed password" | grep -c '' | tr -d '\n' | emit SSHFAIL
# sing-box 错误计数（1h）+ 样本
${sudo_prefix}journalctl -u sing-box --since '1 hour ago' --no-pager 2>/dev/null | grep -ciE 'error|warn|fail|refus|timeout' | tr -d '\n' | emit SBERR1H
${sudo_prefix}journalctl -u sing-box --since '1 hour ago' --no-pager 2>/dev/null | grep -iE 'error|warn|fail|refus|timeout' | tail -5 | emit SBERRSAMPLE
# 24h 重启次数
${sudo_prefix}journalctl -u sing-box --since '24 hours ago' --no-pager 2>/dev/null | grep -c 'Started sing-box' | tr -d '\n' | emit SBRESTARTS24H
${sudo_prefix}journalctl -u sbm-server --since '24 hours ago' --no-pager 2>/dev/null | grep -c 'Started sbm' | tr -d '\n' | emit SBMRESTARTS24H
# 24h 异常退出次数(区分 deploy 重启 vs crash-loop): systemd 非零退出/被信号杀/panic。
# 干净的 deploy stop/start 不会留这些痕迹，只有真崩溃才会。
${sudo_prefix}journalctl -u sbm-server --since '24 hours ago' --no-pager 2>/dev/null | grep -cE 'code=exited, status=[1-9]|code=killed|panic:|signal: ' | tr -d '\n' | emit SBMABNORMAL24H
${sudo_prefix}journalctl -u sing-box --since '24 hours ago' --no-pager 2>/dev/null | grep -cE 'code=exited, status=[1-9]|code=killed|panic:|signal: ' | tr -d '\n' | emit SBABNORMAL24H
# 内存压力
free -m 2>/dev/null | awk '/Mem:/ {print int(\$3/\$2*100)}' | tr -d '\n' | emit MEMPCT
free -m 2>/dev/null | awk '/Swap:/ {print \$3}' | tr -d '\n' | emit SWAPUSED_MB
# 配置文件新鲜度（最后修改时间戳）
stat -c %Y /etc/sing-box/config.json 2>/dev/null | tr -d '\n' | emit CONFIG_MTIME
echo DONE
REMOTE
}

inspect_one() {
  local name="$1"
  IFS='|' read -r ssh_target port bin_path method <<< "${NODE_SSH[$name]}"
  local sudo_prefix=""
  case "$method" in *-sudo) sudo_prefix="sudo " ;; esac
  local script
  script=$(build_remote_script "$bin_path" "$sudo_prefix" "$SINCE_MIN")
  ssh $SSH_OPTS -p "$port" "$ssh_target" "$script" > "$TMPDIR/$name.out" 2> "$TMPDIR/$name.err"
}

# --- 并行采集 ---
echo "Collecting from ${#NODE_SSH[@]} nodes (since ${SINCE_MIN}min)..." >&2
pids=()
for name in "${!NODE_SSH[@]}"; do
  inspect_one "$name" &
  pids+=("$!:$name")
done
for p in "${pids[@]}"; do
  IFS=':' read -r pid n <<< "$p"
  wait "$pid" 2>/dev/null
done

# --- CF /nodes ---
CF_KEY="${SBM_CF_API_KEY:-sbm-topo-k8xPmQ2vNfW7rYzJ}"
curl -sk --max-time 10 -H "X-API-Key: $CF_KEY" https://tun.vspeak.top/nodes > "$TMPDIR/cf-nodes.json" 2>/dev/null

# --- Python 渲染 ---
NODE_NAMES_CSV=$(IFS=,; echo "${!NODE_SSH[*]}")
TMPDIR="$TMPDIR" SINCE_MIN="$SINCE_MIN" NODE_NAMES="$NODE_NAMES_CSV" python3 <<'PYEOF'
import json, os, datetime, sys, base64

tmp = os.environ['TMPDIR']
since_min = int(os.environ['SINCE_MIN'])
node_names = os.environ['NODE_NAMES'].split(',')

def parse_kv_file(path):
    """Parse KEY=base64value lines into dict of decoded strings."""
    if not os.path.exists(path) or os.path.getsize(path) == 0:
        return None
    out = {}
    with open(path, 'r', errors='replace') as f:
        for line in f:
            line = line.rstrip('\n')
            if line == 'DONE':
                out['_done'] = True
                continue
            if '=' not in line:
                continue
            k, v = line.split('=', 1)
            try:
                out[k] = base64.b64decode(v).decode('utf-8', errors='replace')
            except Exception:
                out[k] = ''
    return out

rows = []
issues = []

for name in sorted(node_names):
    out_path = f"{tmp}/{name}.out"
    err_path = f"{tmp}/{name}.err"
    row = {'name': name, 'reachable': False}
    kv = parse_kv_file(out_path)

    if not kv or not kv.get('_done'):
        err_msg = ''
        if os.path.exists(err_path):
            with open(err_path) as f:
                err_msg = f.read().strip().split('\n')[-1][:80]
        row['error'] = err_msg or 'no DONE marker'
        rows.append(row)
        issues.append(f"**{name}**: SSH/采集失败 — `{err_msg or 'no output'}`")
        continue

    row['reachable'] = True
    row['active'] = kv.get('ACTIVE', '?').strip()
    row['nrestarts'] = kv.get('NRESTARTS', '?').strip()

    try:
        vj = json.loads(kv.get('VERSION', '{}') or '{}')
        row['sbm_version'] = vj.get('sbm_version', '?')
        row['singbox_version'] = vj.get('singbox_version', '?').replace('sing-box version ', '')
    except Exception:
        row['sbm_version'] = '?'; row['singbox_version'] = '?'

    try:
        mj = json.loads(kv.get('METADATA', '{}') or '{}')
        row['role'] = mj.get('role', '?')
    except Exception:
        row['role'] = '?'

    try:
        cj = json.loads(kv.get('CERT', '{}') or '{}')
        row['cert_days'] = cj.get('days_remaining', '')
    except Exception:
        row['cert_days'] = ''

    row['bwg_remaining'] = ''
    try:
        bj = json.loads(kv.get('BWG', '{}') or '{}')
        if 'remaining_pct' in bj:
            row['bwg_remaining'] = f"{bj['remaining_pct']:.1f}%"
    except Exception:
        pass

    row['users'] = ''
    try:
        uj = json.loads(kv.get('USER', '[]') or '[]')
        if isinstance(uj, list):
            enabled = sum(1 for u in uj if u.get('enabled'))
            row['users'] = f"{enabled}/{len(uj)}"
    except Exception:
        pass

    try: row['sbm_warn'] = int((kv.get('SBMWARN', '0') or '0').strip())
    except: row['sbm_warn'] = 0
    try: row['singbox_fatal'] = int((kv.get('SBFATAL', '0') or '0').strip())
    except: row['singbox_fatal'] = 0
    row['warn_sample'] = kv.get('WARNSAMPLE', '')

    # 系统健康维度
    try: row['disk_pct'] = int((kv.get('DISKPCT', '0') or '0').strip())
    except: row['disk_pct'] = 0
    row['journal_size'] = kv.get('JOURNALSIZE', '').strip()
    try: row['failed_units'] = int((kv.get('FAILEDUNITS', '0') or '0').strip())
    except: row['failed_units'] = 0
    row['failed_sample'] = kv.get('FAILEDSAMPLE', '')
    try: row['ssh_fail'] = int((kv.get('SSHFAIL', '0') or '0').strip())
    except: row['ssh_fail'] = 0

    # sing-box 错误计数 (1h)
    try: row['sb_err_1h'] = int((kv.get('SBERR1H', '0') or '0').strip())
    except: row['sb_err_1h'] = 0
    row['sb_err_sample'] = kv.get('SBERRSAMPLE', '')

    # 24h 重启次数
    try: row['sb_restarts_24h'] = int((kv.get('SBRESTARTS24H', '0') or '0').strip())
    except: row['sb_restarts_24h'] = 0
    try: row['sbm_restarts_24h'] = int((kv.get('SBMRESTARTS24H', '0') or '0').strip())
    except: row['sbm_restarts_24h'] = 0
    # 异常退出计数(crash 信号),用于区分 deploy 重启 vs crash-loop
    try: row['sbm_abnormal_24h'] = int((kv.get('SBMABNORMAL24H', '0') or '0').strip())
    except: row['sbm_abnormal_24h'] = 0
    try: row['sb_abnormal_24h'] = int((kv.get('SBABNORMAL24H', '0') or '0').strip())
    except: row['sb_abnormal_24h'] = 0

    # 内存压力
    try: row['mem_pct'] = int((kv.get('MEMPCT', '0') or '0').strip())
    except: row['mem_pct'] = 0
    try: row['swap_used_mb'] = int((kv.get('SWAPUSED_MB', '0') or '0').strip())
    except: row['swap_used_mb'] = 0

    # 配置新鲜度
    try: row['config_mtime'] = int(kv.get('CONFIG_MTIME', '0').strip())
    except: row['config_mtime'] = 0

    rows.append(row)

    # 异常累积
    parts = row['active'].split()
    if not parts or parts[0] != 'active' or (len(parts) > 1 and parts[1] != 'active'):
        issues.append(f"**{name}**: 服务异常 active=`{row['active']}`")
    try:
        if int(row['nrestarts']) > 0:
            issues.append(f"**{name}**: sing-box NRestarts=`{row['nrestarts']}`（异常重启）")
    except: pass
    if row['sbm_warn'] > 0:
        sample = row['warn_sample'].strip()
        sample_lines = sample.split('\n')[:5]
        sample_block = '\n'.join('  ' + l for l in sample_lines)
        issues.append(f"**{name}**: sbm-server 最近 {since_min}min 有 {row['sbm_warn']} 条 WARN/ALERT/ERR\n  ```\n{sample_block}\n  ```")
    if row['singbox_fatal'] > 0:
        issues.append(f"**{name}**: sing-box 最近 {since_min}min 有 {row['singbox_fatal']} 条 FATAL/panic")
    if isinstance(row['cert_days'], int) and row['cert_days'] < 14:
        issues.append(f"**{name}**: 证书剩余 {row['cert_days']} 天（< 14）")
    if row['bwg_remaining']:
        try:
            pct = float(row['bwg_remaining'].rstrip('%'))
            if pct < 20:
                issues.append(f"**{name}**: BWG 剩余带宽 {row['bwg_remaining']}（< 20%）")
        except: pass
    # 系统健康异常
    if row['disk_pct'] >= 85:
        issues.append(f"**{name}**: 磁盘使用 {row['disk_pct']}%（≥ 85%）")
    if row['failed_units'] > 0:
        sample = row['failed_sample'].strip().split('\n')[:3]
        sample_block = '\n'.join('  ' + l for l in sample)
        issues.append(f"**{name}**: {row['failed_units']} 个 systemd failed units\n  ```\n{sample_block}\n  ```")
    if row['ssh_fail'] > 20:
        issues.append(f"**{name}**: 最近 15min SSH 失败登录 {row['ssh_fail']} 次（疑似暴力破解）")
    # sing-box 错误异常（排除 mux EOF / connection closed 等客户端断连噪音）
    if row['sb_err_1h'] > 100:
        sample = row['sb_err_sample'].strip().split('\n')[:3]
        sample_block = '\n'.join('  ' + l for l in sample)
        issues.append(f"**{name}**: sing-box 最近 1h 有 {row['sb_err_1h']} 条 ERROR/WARN\n  ```\n{sample_block}\n  ```")
    # 重启次数异常：只在伴随异常退出(crash)时告警。干净的 deploy stop/start 也会
    # 抬高重启计数，但不留 crash 痕迹——那种不告警，否则每次部署后都狼来了，真 crash-loop 反被忽略。
    if row['sb_restarts_24h'] > 5:
        if row.get('sb_abnormal_24h', 0) > 0:
            issues.append(f"**{name}**: sing-box 24h 重启 {row['sb_restarts_24h']} 次且有 {row['sb_abnormal_24h']} 次异常退出（疑似 crash-loop）")
        else:
            issues.append(f"**{name}**: sing-box 24h 重启 {row['sb_restarts_24h']} 次但无异常退出（疑为部署触发，非故障）")
    if row['sbm_restarts_24h'] > 5:
        if row.get('sbm_abnormal_24h', 0) > 0:
            issues.append(f"**{name}**: sbm-server 24h 重启 {row['sbm_restarts_24h']} 次且有 {row['sbm_abnormal_24h']} 次异常退出（疑似 crash-loop）")
        else:
            issues.append(f"**{name}**: sbm-server 24h 重启 {row['sbm_restarts_24h']} 次但无异常退出（疑为部署触发，非故障）")
    # 内存压力
    if row['mem_pct'] >= 90:
        issues.append(f"**{name}**: 内存使用 {row['mem_pct']}%（≥ 90%）")
    if row['swap_used_mb'] > 500:
        issues.append(f"**{name}**: Swap 使用 {row['swap_used_mb']}MB（> 500MB，内存压力）")
    # 注意: 不用"日志时间差"判 sbm-server 卡死 —— sbm-server 正常运行时静默不打日志,
    # 10min 无日志是常态,会让所有健康节点全部误报。reporter goroutine 卡死的正确判据是
    # CF 上报新鲜度(下方 reported_at > 6min / alive=false),那才是 reporter 停了的真实信号。

# --- 节点健康表 ---
print("# sbm 全节点巡检报告\n")
print(f"时间窗口: 最近 {since_min} 分钟  |  生成时间: {datetime.datetime.now().strftime('%Y-%m-%d %H:%M:%S %Z')}\n")
print("## 节点健康\n")
print("| 节点 | 角色 | sbm | sing-box | restarts | sbm-ver | sb-ver | 证书 | BWG | 用户 | WARN | FATAL | 磁盘 | 内存 | SB-ERR(1h) | 重启(24h) | Failed |")
print("|------|------|-----|----------|----------|---------|--------|------|-----|------|------|-------|------|------|-----------|----------|--------|")
for r in rows:
    if not r['reachable']:
        print(f"| {r['name']} | ? | ❌ | — | — | — | — | — | — | — | — | — | — | — | — | — | — |")
        continue
    parts = r['active'].split()
    sbm_ok = '✅' if (parts and parts[0] == 'active') else '❌'
    sb_ok = '✅' if (len(parts) > 1 and parts[1] == 'active') else '❌'
    cert = f"{r['cert_days']}d" if isinstance(r['cert_days'], int) else ''
    warn_cell = '0' if r['sbm_warn'] == 0 else f"⚠️ {r['sbm_warn']}"
    fatal_cell = '0' if r['singbox_fatal'] == 0 else f"🔴 {r['singbox_fatal']}"
    disk_cell = f"{r['disk_pct']}%" if r['disk_pct'] else ''
    if r['disk_pct'] >= 85: disk_cell = f"🔴 {disk_cell}"
    mem_cell = f"{r['mem_pct']}%" if r['mem_pct'] else ''
    if r['mem_pct'] >= 90: mem_cell = f"🔴 {mem_cell}"
    sb_err_cell = '0' if r.get('sb_err_1h', 0) <= 5 else f"⚠️ {r['sb_err_1h']}"
    if r.get('sb_err_1h', 0) > 100: sb_err_cell = f"🔴 {r['sb_err_1h']}"
    total_restarts = r.get('sb_restarts_24h', 0) + r.get('sbm_restarts_24h', 0)
    restart_cell = '0' if total_restarts == 0 else f"⚠️ {total_restarts}"
    failed_cell = '0' if r.get('failed_units', 0) == 0 else f"⚠️ {r['failed_units']}"
    print(f"| {r['name']} | {r['role']} | {sbm_ok} | {sb_ok} | {r['nrestarts']} | {r['sbm_version']} | {r['singbox_version']} | {cert} | {r['bwg_remaining']} | {r['users']} | {warn_cell} | {fatal_cell} | {disk_cell} | {mem_cell} | {sb_err_cell} | {restart_cell} | {failed_cell} |")

# --- CF /nodes ---
print("\n## CF 注册表（双控制面）\n")
try:
    with open(f"{tmp}/cf-nodes.json") as f:
        cf = json.load(f)
    if isinstance(cf, dict) and 'error' in cf:
        print(f"❌ CF 拉取失败: `{cf}`")
        issues.append(f"**CF**: /nodes 鉴权失败 `{cf}`")
    else:
        now = datetime.datetime.now(datetime.timezone.utc)
        print("| 节点 | 角色 | 版本 | alive | 上报间隔 |")
        print("|------|------|------|-------|----------|")
        cf_stale = 0; cf_dead = 0
        for n in sorted(cf, key=lambda x: x['name']):
            try:
                t = datetime.datetime.fromisoformat(n['reported_at'].replace('Z', '+00:00'))
                age = int((now - t).total_seconds())
                if age < 360:
                    fresh = f"{age}s"
                else:
                    fresh = f"{age//60}min ⚠️"; cf_stale += 1
            except Exception:
                fresh = '?'
            alive = '✅' if n.get('alive') else '❌'
            if not n.get('alive'): cf_dead += 1
            print(f"| {n['name']} | {n.get('role','?')} | v{n.get('sbm_version','?')} | {alive} | {fresh} |")
        if cf_stale:
            issues.append(f"**CF**: {cf_stale} 个节点上报陈旧（> 6min）")
        if cf_dead:
            issues.append(f"**CF**: {cf_dead} 个节点 alive=false")
except FileNotFoundError:
    print("❌ CF 注册表未抓取")
    issues.append("**CF**: /nodes 接口请求失败")
except Exception as e:
    print(f"❌ CF 解析失败: {e}")
    issues.append(f"**CF**: 解析异常 — {e}")

# --- 版本一致性 ---
versions = set()
for r in rows:
    if r['reachable'] and r.get('sbm_version', '?') != '?':
        versions.add(r['sbm_version'])
if len(versions) > 1:
    issues.append(f"**版本漂移**: 集群存在多版本 {sorted(versions)}")

# --- 异常摘要 ---
print("\n## 异常摘要\n")
if not issues:
    print("✅ **全绿** — 无异常\n")
else:
    print(f"⚠️ **发现 {len(issues)} 项异常**:\n")
    for i, msg in enumerate(issues, 1):
        print(f"{i}. {msg}")
    print()

sys.exit(0 if not issues else 1)
PYEOF
