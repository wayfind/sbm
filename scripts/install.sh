#!/bin/bash
set -euo pipefail

# sbm installer — downloads sbm + sing-box and runs sbm install
#
# Usage:
#   curl -fsSL https://tun.vspeak.top/install | bash -s -- --role transit --domain node.example.com
#   curl -fsSL https://tun.vspeak.top/install | bash -s -- --role exit
#   curl -fsSL https://tun.vspeak.top/install | bash -s -- --role entry --domain gw.example.com --upstreams /path/to/upstreams.json
#   curl -fsSL https://tun.vspeak.top/install | bash              # interactive mode

BASE_URL="https://tun.vspeak.top"
INSTALL_DIR="/usr/local/bin"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# --- Parse arguments ---
ROLE=""
DOMAIN=""
UPSTREAMS=""
SKIP_INSTALL=""
NAME=""
CF_URL=""
CF_KEY=""
BWG_VEID=""
BWG_API_KEY=""
EXTRA_ARGS=()

while [ $# -gt 0 ]; do
    case "$1" in
        --role)    ROLE="$2"; shift 2 ;;
        --domain)  DOMAIN="$2"; shift 2 ;;
        --upstreams) UPSTREAMS="$2"; shift 2 ;;
        --download-only) SKIP_INSTALL=1; shift ;;
        --name)    NAME="$2"; shift 2 ;;
        --cf-url)  CF_URL="$2"; shift 2 ;;
        --cf-key)  CF_KEY="$2"; shift 2 ;;
        --bwg-veid) BWG_VEID="$2"; shift 2 ;;
        --bwg-api-key) BWG_API_KEY="$2"; shift 2 ;;
        --force)   EXTRA_ARGS+=(--force); shift ;;
        --cert)    EXTRA_ARGS+=(--cert "$2"); shift 2 ;;
        --key)     EXTRA_ARGS+=(--key "$2"); shift 2 ;;
        --sni)     EXTRA_ARGS+=(--sni "$2"); shift 2 ;;
        -h|--help)
            echo "Usage: curl -fsSL $BASE_URL/install | bash -s -- [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --role <transit|exit|entry>  节点类型"
            echo "  --domain <域名>             域名 (exit 不需要，自动探测 IP)"
            echo "  --name <名称>               节点名称 (拓扑显示用)"
            echo "  --cf-url <URL>              Cloudflare Worker URL (拓扑上报)"
            echo "  --cf-key <KEY>              Cloudflare API Key (拓扑认证)"
            echo "  --bwg-veid <VEID>           搬瓦工 VEID (带宽监控)"
            echo "  --bwg-api-key <KEY>         搬瓦工 API Key (带宽监控)"
            echo "  --upstreams <文件>           上游配置文件 (entry 必需)"
            echo "  --force                      跳过替换确认"
            echo "  --cert <路径>                TLS 证书文件"
            echo "  --key <路径>                 TLS 私钥文件"
            echo "  --sni <SNI>                  VLESS Reality SNI"
            echo "  --download-only             仅下载二进制，不配置节点"
            echo ""
            echo "Examples:"
            echo "  bash -s -- --role transit --domain node.example.com --name my-transit"
            echo "  bash -s -- --role exit --name my-exit --cf-url https://tun.vspeak.top --cf-key xxx"
            echo "  bash -s -- --role entry --domain gw.example.com --upstreams /tmp/upstreams.json"
            exit 0
            ;;
        *) error "未知参数: $1 (用 --help 查看用法)" ;;
    esac
done

# --- Root check ---
[ "$(id -u)" -eq 0 ] || error "请用 root 运行: curl -fsSL $BASE_URL/install | sudo bash -s -- ..."

# --- OS detection ---
detect_os() {
    if [ -f /etc/os-release ]; then
        . /etc/os-release
        echo "$ID"
    else
        echo "unknown"
    fi
}

OS=$(detect_os)
info "系统: $OS"

# --- Install dependencies ---
install_deps() {
    case "$OS" in
        almalinux|centos|rhel|rocky|fedora)
            command -v curl >/dev/null || yum install -y curl
            command -v jq >/dev/null || yum install -y jq
            ;;
        ubuntu|debian)
            command -v curl >/dev/null || { apt-get update && apt-get install -y curl; }
            command -v jq >/dev/null || apt-get install -y jq
            ;;
        *)
            command -v curl >/dev/null || error "请先安装 curl"
            command -v jq >/dev/null || error "请先安装 jq"
            ;;
    esac
}

install_deps

# --- Fetch version info ---
info "获取版本信息..."
LATEST=$(curl -fsSL "$BASE_URL/releases/latest.json") || error "无法获取版本信息"

SBM_VER=$(echo "$LATEST" | jq -r '.sbm')
SB_VER=$(echo "$LATEST" | jq -r '.sing_box')

info "sbm 版本: $SBM_VER"
info "sing-box 版本: $SB_VER"

# --- Download binaries ---
download() {
    local name=$1 url=$2 dest=$3
    local tmp="${dest}.new"
    if [ -f "$dest" ]; then
        info "$name 已存在，覆盖安装"
    fi
    info "下载 $name..."
    curl -fsSL -o "$tmp" "$url" || { rm -f "$tmp"; error "下载 $name 失败"; }
    chmod +x "$tmp"
    mv -f "$tmp" "$dest" || { rm -f "$tmp"; error "安装 $name 失败 (mv)"; }
    info "$name 安装到 $dest"
}

download "sing-box" "$BASE_URL/releases/sing-box/$SB_VER/sing-box-linux-amd64" "$INSTALL_DIR/sing-box"
download "sbm" "$BASE_URL/releases/sbm/v$SBM_VER/sbm-linux-amd64" "$INSTALL_DIR/sbm"

# --- Verify ---
info "验证安装..."
sing-box version || error "sing-box 启动失败"
sbm version || error "sbm 启动失败"

# --- Firewall setup (non-fatal, skip if not available) ---
setup_firewall() {
    case "$OS" in
        almalinux|centos|rhel|rocky|fedora)
            if systemctl is-active --quiet firewalld 2>/dev/null; then
                info "firewalld 已运行"
            fi
            ;;
        ubuntu|debian)
            if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "active"; then
                info "ufw 已运行"
            fi
            ;;
    esac
}

setup_firewall

# --- Download-only mode ---
if [ -n "$SKIP_INSTALL" ]; then
    info "下载完成 (--download-only)"
    exit 0
fi

# --- Interactive fallback: ask if --role not given ---
if [ -z "$ROLE" ]; then
    if [ ! -t 0 ]; then
        error "非交互模式下必须指定 --role 参数 (用 --help 查看用法)"
    fi
    echo ""
    echo "=== 节点配置 ==="
    echo "  1) transit  — 传输节点 (CN2 GIA)"
    echo "  2) exit     — 出口节点 (家宽IP)"
    echo "  3) entry    — 接入节点 (国内云)"
    echo "  4) 跳过     — 稍后手动运行 sbm install"
    echo ""
    read -p "选择节点类型 [1-4]: " choice
    case "$choice" in
        1) ROLE="transit" ;;
        2) ROLE="exit" ;;
        3) ROLE="entry" ;;
        4) info "安装完成！稍后运行: sbm install --role <类型> --domain <域名>"; exit 0 ;;
        *) error "无效选择" ;;
    esac
fi

# --- Validate role ---
case "$ROLE" in
    transit|exit|entry) ;;
    *) error "无效角色: $ROLE (可选: transit, exit, entry)" ;;
esac

# --- Interactive fallback: ask domain if not given (except exit) ---
if [ "$ROLE" != "exit" ] && [ -z "$DOMAIN" ]; then
    read -p "输入域名 (如 node.example.com): " DOMAIN
    [ -n "$DOMAIN" ] || error "域名不能为空"
fi

# --- Interactive fallback: ask upstreams if entry and not given ---
if [ "$ROLE" = "entry" ] && [ -z "$UPSTREAMS" ]; then
    read -p "上游节点配置文件路径 (upstreams.json): " UPSTREAMS
    [ -n "$UPSTREAMS" ] && [ -f "$UPSTREAMS" ] || error "文件不存在: $UPSTREAMS"
fi

# --- Validate entry upstreams ---
if [ "$ROLE" = "entry" ]; then
    [ -n "$UPSTREAMS" ] || error "entry 节点需要 --upstreams 参数"
    [ -f "$UPSTREAMS" ] || error "文件不存在: $UPSTREAMS"
fi

# --- Build and run sbm install ---
cmd=(sbm install --role "$ROLE" --force)
[ -n "$DOMAIN" ] && cmd+=(--domain "$DOMAIN")
[ -n "$UPSTREAMS" ] && cmd+=(--upstreams "$UPSTREAMS")
[ -n "$NAME" ] && cmd+=(--name "$NAME")
[ -n "$CF_URL" ] && cmd+=(--cf-url "$CF_URL")
[ -n "$CF_KEY" ] && cmd+=(--cf-key "$CF_KEY")
[ -n "$BWG_VEID" ] && cmd+=(--bwg-veid "$BWG_VEID")
[ -n "$BWG_API_KEY" ] && cmd+=(--bwg-api-key "$BWG_API_KEY")
cmd+=("${EXTRA_ARGS[@]}")

info "执行: ${cmd[*]}"
"${cmd[@]}"

info "安装完成！"
echo ""
echo "下一步:"
echo "  1. 运行 sbm status 检查服务状态"
echo "  2. 运行 sbm sub 查看订阅信息"
