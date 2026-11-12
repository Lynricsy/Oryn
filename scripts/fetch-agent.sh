#!/usr/bin/env bash
# 按 agent/agent-source.json 下载 orb 内使用的 omp 二进制到 build/agent/<arch>/omp，并校验 sha256。
#
# 用法：scripts/fetch-agent.sh [x64|arm64]   （默认宿主架构）
# 依赖：gh（已登录且能读取 agent 仓库）、jq、sha256sum。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

host_arch() {
	case "$(uname -m)" in
	x86_64 | amd64) echo x64 ;;
	aarch64 | arm64) echo arm64 ;;
	*) echo "不支持的宿主架构 $(uname -m)" >&2; exit 1 ;;
	esac
}

arch="${1:-$(host_arch)}"
case "$arch" in
x64 | arm64) ;;
*) echo "用法：$0 [x64|arm64]" >&2; exit 2 ;;
esac

source_json="agent/agent-source.json"
repo="$(jq -r .repo "$source_json")"
tag="$(jq -r .tag "$source_json")"
want="$(jq -r --arg k "linux-${arch}" '.sha256[$k] // empty' "$source_json")"
if [[ -z "$want" ]]; then
	echo "$source_json 中没有 linux-${arch} 的 sha256" >&2
	exit 1
fi

dest_dir="build/agent/${arch}"
dest="${dest_dir}/omp"
asset="omp-linux-${arch}"

if [[ -f "$dest" ]] && [[ "$(sha256sum "$dest" | cut -d' ' -f1)" == "$want" ]]; then
	echo "已是最新：$dest（${tag}）"
	exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
gh release download "$tag" --repo "$repo" --pattern "$asset" --dir "$tmp"

got="$(sha256sum "${tmp}/${asset}" | cut -d' ' -f1)"
if [[ "$got" != "$want" ]]; then
	echo "sha256 不匹配：${asset} 期望 ${want}，实际 ${got}" >&2
	exit 1
fi

mkdir -p "$dest_dir"
install -m 0755 "${tmp}/${asset}" "$dest"
echo "已下载 ${repo}@${tag} ${asset} → ${dest}"
