#!/usr/bin/env bash
# Oryn docker provider 的 agent 沙箱网络（M30，docs/deploy.md「docker 沙箱」）。需要 root；幂等，可重复执行。
#
# 用法：oryn-sbx-net.sh up|down
#
#   up    建 docker 网络 ORYN_SBX_NETWORK（网桥 ORYN_SBX_BRIDGE，关闭容器间互通），
#         并装两组 iptables 规则：
#           INPUT       -i <网桥> -j DROP               沙箱到宿主一律丢弃（link 走挂进容器的 unix socket，不需要放行任何端口）
#           DOCKER-USER -i <网桥> -d <私网段> -j DROP   沙箱到内网（10/8、172.16/12、192.168/16、100.64/10、169.254/16）一律丢弃
#   down  删掉上述规则与网络（网络上还有容器时 docker 会拒绝删除）。
#
# 环境变量（与 provider.yaml 的 docker.sandbox.network 保持一致）：
#   ORYN_SBX_NETWORK  docker 网络名，默认 oryn-sbx
#   ORYN_SBX_BRIDGE   网桥接口名，默认 oryn-sbx0
#   ORYN_SBX_SUBNET   网段，默认 172.29.0.0/24
#
# iptables 规则不随重启持久化（与 orb 网桥的做法一致），请按自己的方式持久化或在开机时重跑 up。
# none 模式的沙箱没有网卡，不受这些规则影响。
set -euo pipefail

network="${ORYN_SBX_NETWORK:-oryn-sbx}"
bridge="${ORYN_SBX_BRIDGE:-oryn-sbx0}"
subnet="${ORYN_SBX_SUBNET:-172.29.0.0/24}"

usage() {
	sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

private_ranges=(10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16)

# rules 逐行打印「链 规则」，up / down 共用。
rules() {
	echo "INPUT -i $bridge -j DROP"
	local r
	for r in "${private_ranges[@]}"; do
		echo "DOCKER-USER -i $bridge -d $r -j DROP"
	done
}

up() {
	if ! docker network inspect "$network" >/dev/null 2>&1; then
		docker network create --driver bridge --subnet "$subnet" \
			-o com.docker.network.bridge.name="$bridge" \
			-o com.docker.network.bridge.enable_icc=false \
			"$network" >/dev/null
	fi
	# DOCKER-USER 由 dockerd 建立；极老的版本没有时补一条空链并挂进 FORWARD
	if ! iptables -n -L DOCKER-USER >/dev/null 2>&1; then
		iptables -N DOCKER-USER
		iptables -I FORWARD -j DOCKER-USER
	fi
	local chain rule
	while read -r chain rule; do
		# shellcheck disable=SC2086
		iptables -C "$chain" $rule 2>/dev/null || iptables -I "$chain" $rule
	done < <(rules)
	echo "oryn 沙箱网络已就绪：$network（网桥 $bridge，网段 $subnet）"
}

down() {
	local chain rule
	while read -r chain rule; do
		# shellcheck disable=SC2086
		while iptables -C "$chain" $rule 2>/dev/null; do iptables -D "$chain" $rule; done
	done < <(rules)
	if docker network inspect "$network" >/dev/null 2>&1; then
		docker network rm "$network" >/dev/null
	fi
	echo "oryn 沙箱网络已拆除"
}

case "${1:-}" in
up) up ;;
down) down ;;
*) usage ;;
esac
