#!/usr/bin/env bash
# M28 本机验收用的隔离栈（m7-accept.ts 的 A23）：本机官方推送网关 + 一套独立数据目录的 orynd 与 process provider。
# 不碰 dev 部署的 .dev/data：0058 迁移会删除库里旧的 FCM 订阅，所以验收 orynd 用自己的 .dev/m28/data。
#
# 用法（仓库根目录，先 make web && make build）：
#   bash web/scripts/m28-gateway-stack.sh up     # 启动并打印 accept:m7 要用的环境变量（eval "$(… up)" 可直接导入）
#   bash web/scripts/m28-gateway-stack.sh down   # 停止全部进程（数据目录保留，便于事后查库；rm -rf .dev/m28 .dev/pushgw 清空）
#
# 端口：orynd 127.0.0.1:7600（App 与 adb reverse 用这个端口，所以 dev 的 make dev 必须先停）、网关 127.0.0.1:7700。
# 网关凭据：.dev/m7/fcm-service-account.json（Firebase 项目须与构建 gms 包的 google-services.json 相同）；不配 APNs。
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
M28="$ROOT/.dev/m28"
GW="$ROOT/.dev/pushgw"
BIN="$ROOT/bin"
SA="$ROOT/.dev/m7/fcm-service-account.json"

pidfile() { echo "$M28/$1.pid"; }

stop_one() {
	local f
	f=$(pidfile "$1")
	if [[ -f $f ]] && kill -0 "$(cat "$f")" 2>/dev/null; then
		kill "$(cat "$f")"
		for _ in $(seq 50); do kill -0 "$(cat "$f")" 2>/dev/null || break; sleep 0.1; done
	fi
	rm -f "$f"
}

start_bg() { # <名称> <日志> <命令…>
	local name=$1 log=$2
	shift 2
	nohup "$@" >>"$log" 2>&1 &
	echo $! >"$(pidfile "$name")"
}

wait_http() { # <url> <名称>
	for _ in $(seq 150); do
		curl -sf -o /dev/null "$1" && return 0
		sleep 0.2
	done
	echo "$2 没有就绪：$1" >&2
	exit 1
}

up() {
	for b in orynd oryn orbd fakeomp oryn-push-gateway; do
		[[ -x $BIN/$b ]] || { echo "缺少 $BIN/$b：先 make web && make build" >&2; exit 1; }
	done
	[[ -f $SA ]] || { echo "缺少 $SA（Firebase 服务账号 JSON）" >&2; exit 1; }
	if curl -sf -o /dev/null http://127.0.0.1:7600/healthz && [[ ! -f $(pidfile orynd) ]]; then
		echo "7600 已被其它 orynd 占用（make dev？）：先停掉它" >&2
		exit 1
	fi
	mkdir -p "$M28/data" "$M28/orbs" "$GW/data"

	cat >"$GW/gateway.yaml" <<EOF
listen: "127.0.0.1:7700"
public_origin: "http://localhost:7700"
data_dir: "$GW/data"
log_level: "debug"
fcm: { service_account_file: "$SA", app_id: "com.lynricsy.oryn" }
EOF
	cat >"$M28/orynd.yaml" <<EOF
listen: ["127.0.0.1:7600"]
public_url: "http://localhost:7600"
portal_domain: ""
data_dir: "$M28/data"
log_level: "debug"
idle: { agent_idle: "30m", user_idle: "30m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "local", type: "" }
push: { gateway: { url: "http://localhost:7700" } }
EOF
	cat >"$M28/provider.yaml" <<EOF
url: "http://127.0.0.1:7600"
token_file: "$M28/provider.token"
orb_url: "http://127.0.0.1:7600"
driver: process
EOF

	[[ -f $(pidfile gateway) ]] || start_bg gateway "$GW/gateway.log" "$BIN/oryn-push-gateway" -config "$GW/gateway.yaml"
	wait_http http://127.0.0.1:7700/v1/info 网关
	[[ -f $(pidfile orynd) ]] || start_bg orynd "$M28/orynd.log" "$BIN/orynd" --config "$M28/orynd.yaml"
	wait_http http://127.0.0.1:7600/healthz orynd

	local admin=("$BIN/orynd" --config "$M28/orynd.yaml" admin)
	if [[ ! -f $M28/credentials.json ]]; then
		local tok
		tok=$("${admin[@]}" create-device --name m28-accept --kind cli)
		printf '{"url":"http://localhost:7600","token":"%s"}\n' "$tok" >"$M28/credentials.json"
		chmod 0600 "$M28/credentials.json"
	fi
	if [[ ! -f $M28/provider.token ]]; then
		ORYN_CREDENTIALS="$M28/credentials.json" "$BIN/oryn" provider create local >"$M28/provider.token"
		chmod 0600 "$M28/provider.token"
	fi
	[[ -f $(pidfile provider) ]] || ORYN_ORBD_BIN="$BIN/orbd" ORYN_AGENT_BIN="$BIN/fakeomp" ORYN_PROCESS_ROOT="$M28/orbs" \
		start_bg provider "$M28/provider.log" "$BIN/oryn" provider run --config "$M28/provider.yaml"

	cat <<EOF
export ORYN_URL=http://localhost:7600
export ORYN_CREDENTIALS=$M28/credentials.json
export ORYN_DB=$M28/data/oryn.db
export ORYND_CONFIG=$M28/orynd.yaml
export M28_GATEWAY=http://localhost:7700
export M28_GATEWAY_DB=$GW/data/gateway.db
export M28_GATEWAY_LOG=$GW/gateway.log
EOF
}

down() {
	stop_one provider
	stop_one orynd
	stop_one gateway
	# process 驱动起的 orbd / fakeomp
	pkill -f "$M28/orbs" 2>/dev/null || true
}

case "${1:-}" in
up) up ;;
down) down ;;
*) echo "用法：$0 up|down" >&2; exit 2 ;;
esac
