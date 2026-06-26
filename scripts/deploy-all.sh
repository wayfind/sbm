#!/bin/bash
set -euo pipefail

# sbm deploy-all — build + upload + restart all nodes
#
# Usage:
#   scripts/deploy-all.sh                    # full deploy
#   scripts/deploy-all.sh --skip-build       # skip make build
#   scripts/deploy-all.sh --only metask      # single node
#   scripts/deploy-all.sh --dry-run          # print commands only
#   scripts/deploy-all.sh --check            # only check path consistency, no deploy

cd "$(dirname "$0")/.."

# --- Colors ---
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${GREEN}[✓]${NC} $*"; }
warn()  { echo -e "${YELLOW}[!]${NC} $*"; }
err()   { echo -e "${RED}[✗]${NC} $*"; }
step()  { echo -e "\n${CYAN}==>${NC} $*"; }

# --- SSH connection mapping ---
# name → "user@host|port|binary_path|restart_method"
# restart_method: systemd / nohup / systemd-sudo / nohup-sudo
#
# IMPORTANT: binary_path must match the actual ExecStart in systemd service
# or the path used by nohup. Run --check to verify consistency.
declare -A NODE_SSH=(
  [metask]="root@67.230.165.100|22|/root/sing-box-manager/sbm|systemd"
  [shuzuan]="root@89.208.252.173|22|/usr/local/bin/sbm|systemd"
  [yl]="root@89.208.248.209|22|/usr/local/bin/sbm|systemd"
  [picaospark]="david@175.27.169.180|22|/home/david/sbm|systemd-sudo"
  [lisa]="david@154.37.209.17|22|/usr/local/bin/sbm|systemd-sudo"
  [zgo]="root@23.185.200.18|22|/usr/local/bin/sbm|systemd"
  [ai-entry]="ubuntu@entry.origintask.cn|22|/usr/local/bin/sbm|systemd-sudo"
  [kunshan]="shuzuan@58.211.6.133|104|/usr/local/bin/sbm|systemd-sudo"
)

# ConnectTimeout caps TCP connect; ServerAlive caps a HANG AFTER connect (a stuck
# remote sudo/systemctl) at ~15s (3×5s) instead of the default 180s that used to
# freeze whole-fleet pre-flight when one node's remote command stalled.
SSH_OPTS="-o ConnectTimeout=10 -o ServerAliveInterval=5 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=no -o BatchMode=yes"

# --- Parse args ---
SKIP_BUILD=0
DRY_RUN=0
CHECK_ONLY=0
ONLY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --skip-build) SKIP_BUILD=1; shift ;;
    --dry-run)    DRY_RUN=1; shift ;;
    --check)      CHECK_ONLY=1; shift ;;
    --only)       ONLY="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: scripts/deploy-all.sh [--skip-build] [--only <name>] [--dry-run] [--check]"
      echo ""
      echo "Options:"
      echo "  --skip-build  Skip make build (use existing binary)"
      echo "  --only NAME   Deploy only the named node"
      echo "  --dry-run     Print commands without executing"
      echo "  --check       Verify binary path consistency only, no deploy"
      echo ""
      echo "Nodes:"
      for name in $(echo "${!NODE_SSH[@]}" | tr ' ' '\n' | sort); do
        IFS='|' read -r target port bin_path method <<< "${NODE_SSH[$name]}"
        printf "  %-12s %-30s port=%-5s path=%s (%s)\n" "$name" "$target" "$port" "$bin_path" "$method"
      done
      exit 0
      ;;
    *) echo "Unknown: $1"; exit 1 ;;
  esac
done

# TIMEOUT: hard wall-clock cap on each remote command, belt-and-suspenders on top
# of ServerAlive (which can fail to fire on some NATs). Prefer GNU timeout if present.
if command -v timeout >/dev/null 2>&1; then TIMEOUT="timeout 25";
elif command -v gtimeout >/dev/null 2>&1; then TIMEOUT="gtimeout 25";
else TIMEOUT=""; fi

do_ssh() {
  local target="$1" port="$2"; shift 2
  $TIMEOUT ssh $SSH_OPTS -p "$port" $target "$@"
}

# --- Build deploy list ---
deploy_names=()
for name in $(echo "${!NODE_SSH[@]}" | tr ' ' '\n' | sort); do
  if [ -n "$ONLY" ] && [ "$name" != "$ONLY" ]; then
    continue
  fi
  deploy_names+=("$name")
done

if [ ${#deploy_names[@]} -eq 0 ]; then
  err "No matching node for --only '$ONLY'"
  exit 1
fi

# =============================================================================
# Pre-flight check: verify binary paths and restart method consistency.
# Catches two deployment footguns:
#   1. Deploying to the wrong binary path
#   2. Using nohup when systemd is managing the process (causes duplicates)
# =============================================================================
preflight_check() {
  step "Pre-flight: verifying node configuration"
  local bad=0
  for name in "${deploy_names[@]}"; do
    IFS='|' read -r ssh_target port bin_path method <<< "${NODE_SSH[$name]}"
    local sudo_prefix=""
    case "$method" in *-sudo) sudo_prefix="sudo" ;; esac

    # Check 1: if configured as nohup, detect systemd conflict
    case "$method" in
      nohup*)
        local has_systemd
        has_systemd=$(do_ssh "$ssh_target" "$port" "$sudo_prefix systemctl is-enabled sbm-server 2>/dev/null" 2>/dev/null || echo "")
        if [ "$has_systemd" = "enabled" ]; then
          err "$name: configured as 'nohup' but has systemd sbm-server.service (enabled) — change to 'systemd' in NODE_SSH"
          bad=1
          continue
        fi
        ;;
    esac

    # Check 2: verify binary path matches actual running binary
    local actual
    case "$method" in
      systemd*)
        actual=$(do_ssh "$ssh_target" "$port" "$sudo_prefix cat /etc/systemd/system/sbm-server.service 2>/dev/null" 2>/dev/null \
          | grep '^ExecStart=' | sed 's/ExecStart=//' | awk '{print $1}' || echo "")
        ;;
      nohup*)
        actual=$(do_ssh "$ssh_target" "$port" "readlink -f /proc/\$(pgrep -x sbm | head -1)/exe 2>/dev/null" 2>/dev/null || echo "")
        ;;
    esac

    if [ -z "$actual" ]; then
      warn "$name: cannot determine running binary (node down or no sbm process?)"
    else
      local expected_real
      expected_real=$(do_ssh "$ssh_target" "$port" "readlink -f '$bin_path' 2>/dev/null || echo '$bin_path'" 2>/dev/null)
      actual_real=$(do_ssh "$ssh_target" "$port" "readlink -f '$actual' 2>/dev/null || echo '$actual'" 2>/dev/null)
      if [ "$actual_real" = "$expected_real" ]; then
        info "$name: OK ($bin_path, $method)"
      else
        err "$name: PATH MISMATCH — script='$bin_path' node='$actual'"
        bad=1
      fi
    fi
  done

  if [ "$bad" -eq 1 ]; then
    echo ""
    err "Fix NODE_SSH paths in scripts/deploy-all.sh before deploying!"
    return 1
  fi
  return 0
}

# --check mode: only run path verification
if [ "$CHECK_ONLY" -eq 1 ]; then
  preflight_check
  exit $?
fi

# --- Version ---
VERSION=$(grep 'Version =' internal/version.go | sed 's/.*"\(.*\)".*/\1/')
BINARY="./sbm"

# --- Build ---
if [ "$SKIP_BUILD" -eq 0 ]; then
  step "Building sbm v${VERSION}"
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "  make build"
  else
    make build
    info "Build OK"
  fi
else
  step "Skipping build (--skip-build)"
fi

[ -f "$BINARY" ] || { err "Binary not found: $BINARY"; exit 1; }

# --- Pre-flight path check (skip in dry-run) ---
if [ "$DRY_RUN" -eq 0 ]; then
  if ! preflight_check; then
    exit 1
  fi
fi

step "Deploying v${VERSION} to ${#deploy_names[@]} node(s): ${deploy_names[*]}"

# --- Pre-deploy: show current versions ---
if [ "$DRY_RUN" -eq 0 ]; then
  step "Current versions"
  for name in "${deploy_names[@]}"; do
    IFS='|' read -r ssh_target port bin_path _ <<< "${NODE_SSH[$name]}"
    cur=$(do_ssh "$ssh_target" "$port" "${bin_path} version 2>&1 | head -1" 2>/dev/null || echo "unreachable")
    printf "  %-12s %s\n" "$name" "$cur"
  done
fi

# --- Parallel upload ---
step "Uploading binary"
declare -A upload_ok=()
upload_pids=()

for name in "${deploy_names[@]}"; do
  IFS='|' read -r ssh_target port _ _ <<< "${NODE_SSH[$name]}"
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "  scp -P $port $BINARY → ${ssh_target}:/tmp/sbm.new"
    upload_ok[$name]=1
  else
    scp $SSH_OPTS -P "$port" "$BINARY" "${ssh_target}:/tmp/sbm.new" &
    upload_pids+=("$!:$name")
  fi
done

if [ "$DRY_RUN" -eq 0 ]; then
  for pid_name in "${upload_pids[@]}"; do
    IFS=':' read -r pid name <<< "$pid_name"
    if wait "$pid" 2>/dev/null; then
      info "Upload $name"
      upload_ok[$name]=1
    else
      err "Upload $name FAILED — will skip deploy"
    fi
  done
fi

# --- Deploy each node ---
results=()

for name in "${deploy_names[@]}"; do
  # Skip nodes with failed upload
  if [ -z "${upload_ok[$name]+x}" ]; then
    results+=("$name|SKIP|upload failed")
    continue
  fi

  IFS='|' read -r ssh_target port bin_path method <<< "${NODE_SSH[$name]}"
  step "Deploying $name"

  sudo_prefix=""
  case "$method" in *-sudo) sudo_prefix="sudo" ;; esac

  case "$method" in
    systemd*)
      stop_cmd="$sudo_prefix systemctl stop sbm-server"
      start_cmd="$sudo_prefix systemctl start sbm-server"
      ;;
    nohup*)
      # kill and wait for exit (up to 5s), not blind sleep
      stop_cmd="$sudo_prefix kill \$(pgrep -x sbm) 2>/dev/null || true; for i in 1 2 3 4 5; do pgrep -x sbm >/dev/null 2>&1 || break; sleep 1; done"
      start_cmd="$sudo_prefix nohup ${bin_path} serve >> /tmp/sbm.log 2>&1 &"
      ;;
  esac

  replace_cmd="$sudo_prefix cp /tmp/sbm.new ${bin_path} && $sudo_prefix chmod +x ${bin_path}"
  verify_cmd="${bin_path} version 2>&1 | head -1"

  if [ "$DRY_RUN" -eq 1 ]; then
    echo "  stop:    $stop_cmd"
    echo "  replace: $replace_cmd"
    echo "  start:   $start_cmd"
    echo "  verify:  $verify_cmd"
    results+=("$name|DRY-RUN|?")
    continue
  fi

  # Stop → replace → start → verify
  do_ssh "$ssh_target" "$port" "$stop_cmd" 2>/dev/null || true

  if ! do_ssh "$ssh_target" "$port" "$replace_cmd" 2>/dev/null; then
    err "$name: replace failed"
    results+=("$name|FAILED|replace")
    continue
  fi

  do_ssh "$ssh_target" "$port" "$start_cmd" 2>/dev/null || true
  # Wait for process to be ready (systemd start is sync; nohup needs a moment)
  sleep 2

  # --- Post-deploy verification ---
  # 1. Version check
  got_version=$(do_ssh "$ssh_target" "$port" "$verify_cmd" 2>/dev/null || echo "UNREACHABLE")
  if ! echo "$got_version" | grep -q "v${VERSION}"; then
    err "$name: expected v${VERSION}, got: $got_version"
    results+=("$name|MISMATCH|$got_version")
    continue
  fi

  # 2. Process check: is sbm serve actually running?
  if ! do_ssh "$ssh_target" "$port" "pgrep -x sbm >/dev/null 2>&1" 2>/dev/null; then
    err "$name: v${VERSION} installed but process not running!"
    results+=("$name|NOT-RUNNING|v${VERSION}")
    continue
  fi

  # 3. Process binary check: is the running process using the correct binary?
  running_path=$(do_ssh "$ssh_target" "$port" "readlink -f /proc/\$(pgrep -x sbm | head -1)/exe 2>/dev/null" 2>/dev/null || echo "")
  expected_real=$(do_ssh "$ssh_target" "$port" "readlink -f ${bin_path} 2>/dev/null" 2>/dev/null || echo "$bin_path")
  if [ -n "$running_path" ] && [ "$running_path" != "$expected_real" ]; then
    warn "$name: running from $running_path instead of $expected_real"
  fi

  info "$name → v${VERSION}"
  results+=("$name|OK|v${VERSION}")
done

# --- Summary ---
step "Deploy summary"
printf "  %-12s %-12s %s\n" "NODE" "STATUS" "VERSION"
printf "  %-12s %-12s %s\n" "----" "------" "-------"
failed=0
for r in "${results[@]}"; do
  IFS='|' read -r name status ver <<< "$r"
  case "$status" in
    OK)       color="$GREEN" ;;
    DRY-RUN)  color="$CYAN" ;;
    *)        color="$RED"; failed=1 ;;
  esac
  printf "  ${color}%-12s %-12s %s${NC}\n" "$name" "$status" "$ver"
done
echo ""

[ "$failed" -eq 0 ] || { err "Some nodes failed — check above"; exit 1; }
info "All nodes deployed to v${VERSION}"
