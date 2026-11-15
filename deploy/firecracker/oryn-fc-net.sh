#!/usr/bin/env bash
# Oryn Firecracker provider 的一次性宿主网络（docs/deploy.md「Firecracker」）。需要 root；幂等，可重复执行。
#
# 用法：oryn-fc-net.sh up|down
#
#   up    建 dummy 接口 oryn-fc（地址 ORYN_FC_HOST_ADDR/32）、打开 ip_forward、装 nft 表 inet oryn_fc 与 ip oryn_fc_nat；
#         有 Docker 的 DOCKER-USER 链时追加两条放行（否则 Docker 的 FORWARD 默认丢弃会拦掉 guest 出网）。
#   down  删掉上述 nft 表、DOCKER-USER 规则与 oryn-fc 接口（ip_forward 不动）。
#
# 环境变量（与 provider.yaml 的 firecracker.network 保持一致）：
#   ORYN_FC_CIDR        guest 网段，默认 172.31.0.0/16
#   ORYN_FC_HOST_ADDR   orynd 在 guest 侧监听的地址，默认 172.31.255.1
#   ORYN_FC_LINK_PORTS  guest 可以连到 ORYN_FC_HOST_ADDR 的 TCP 端口，逗号或空格分隔，默认 "7643"
#                       （orynd 的 orb_tls.listen，Firecracker orb 的 link 默认走 TLS：orb_url = https://172.31.255.1:7643）。
#                       没开 orb_tls 的部署要显式写成 "7600"（orynd 的明文 listen），或者两个都写 "7600,7643"。
#                       provider 预检会核对 orb_url 的端口在这里面。
#
# 规则全部按接口名通配（fc-*），驱动增删 VM 不需要改 nft。每台 VM 的 tap 与 /30 由驱动建（TapUp）。
set -euo pipefail

cidr="${ORYN_FC_CIDR:-172.31.0.0/16}"
host_addr="${ORYN_FC_HOST_ADDR:-172.31.255.1}"
link_ports="${ORYN_FC_LINK_PORTS:-7643}"
iface="oryn-fc"

usage() {
	sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//' >&2
	exit 2
}

# nft_ports 把 "7600, 7643" / "7600 7643" 规范成 "7600, 7643"，并拒绝非端口值。
nft_ports() {
	local out="" p
	for p in ${link_ports//,/ }; do
		if ! [[ "$p" =~ ^[0-9]+$ ]] || ((p < 1 || p > 65535)); then
			echo "ORYN_FC_LINK_PORTS 里的 \"$p\" 不是端口号" >&2
			exit 2
		fi
		out+="${out:+, }$p"
	done
	if [[ -z "$out" ]]; then
		echo "ORYN_FC_LINK_PORTS 为空" >&2
		exit 2
	fi
	printf '%s' "$out"
}

have_docker_user() { command -v iptables >/dev/null && iptables -n -L DOCKER-USER >/dev/null 2>&1; }

docker_user_rules=(
	"-i fc-+ -j ACCEPT"
	"-o fc-+ -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT"
)

up() {
	local ports
	ports="$(nft_ports)"
	if ! ip link show "$iface" >/dev/null 2>&1; then
		ip link add "$iface" type dummy
	fi
	ip addr replace "$host_addr/32" dev "$iface"
	ip link set "$iface" up
	sysctl -qw net.ipv4.ip_forward=1

	# 先建空表再删，保证首次执行时 delete 不报错；整个文件原子生效。
	# inet oryn_fc 的链优先级比 Docker 的 filter 链靠前，只负责丢弃（放行的包还要经过后面的链）。
	nft -f - <<EOF
table inet oryn_fc
delete table inet oryn_fc
table inet oryn_fc {
	chain input {
		type filter hook input priority filter - 10; policy accept;
		iifname "fc-*" ct state established,related accept
		iifname "fc-*" ip daddr $host_addr tcp dport { $ports } accept
		iifname "fc-*" drop
	}
	chain forward {
		type filter hook forward priority filter - 10; policy accept;
		iifname "fc-*" oifname "fc-*" drop
		iifname "fc-*" ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16, 127.0.0.0/8 } drop
	}
}
table ip oryn_fc_nat
delete table ip oryn_fc_nat
table ip oryn_fc_nat {
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr $cidr oifname != "fc-*" masquerade
	}
}
EOF

	if have_docker_user; then
		local r
		for r in "${docker_user_rules[@]}"; do
			# shellcheck disable=SC2086
			iptables -C DOCKER-USER $r 2>/dev/null || iptables -I DOCKER-USER $r
		done
	fi
	echo "oryn-fc 网络已就绪：$iface $host_addr/32，guest 网段 $cidr，放行端口 { $ports }"
}

down() {
	nft delete table inet oryn_fc 2>/dev/null || true
	nft delete table ip oryn_fc_nat 2>/dev/null || true
	if have_docker_user; then
		local r
		for r in "${docker_user_rules[@]}"; do
			# shellcheck disable=SC2086
			while iptables -C DOCKER-USER $r 2>/dev/null; do iptables -D DOCKER-USER $r; done
		done
	fi
	if ip link show "$iface" >/dev/null 2>&1; then
		ip link del "$iface"
	fi
	echo "oryn-fc 网络已拆除"
}

case "${1:-}" in
up) up ;;
down) down ;;
*) usage ;;
esac
