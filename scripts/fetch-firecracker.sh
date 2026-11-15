#!/usr/bin/env bash
# 按 images/fc/artifacts.lock.json 下载 Firecracker 发布包，校验 sha256 后取出 firecracker 与 jailer 到 build/fc/。
#
# 用法：scripts/fetch-firecracker.sh
# 依赖：curl、jq、sha256sum、tar。只支持 x86_64 宿主。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

for c in curl jq sha256sum tar; do
	command -v "$c" >/dev/null || { echo "缺少命令 $c" >&2; exit 1; }
done
if [[ "$(uname -m)" != "x86_64" ]]; then
	echo "Firecracker provider 只支持 x86_64 宿主（当前 $(uname -m)）" >&2
	exit 1
fi

lock="images/fc/artifacts.lock.json"
version="$(jq -r .firecracker.version "$lock")"
url="$(jq -r .firecracker.releaseArchive "$lock")"
want_tgz="$(jq -r .firecracker.releaseArchiveSha256 "$lock")"
fc_path="$(jq -r .firecracker.firecrackerPath "$lock")"
fc_want="$(jq -r .firecracker.firecrackerSha256 "$lock")"
jailer_path="$(jq -r .firecracker.jailerPath "$lock")"
jailer_want="$(jq -r .firecracker.jailerSha256 "$lock")"

dest_dir="build/fc"
sum() { sha256sum "$1" | cut -d' ' -f1; }

if [[ -f "$dest_dir/firecracker" && -f "$dest_dir/jailer" ]] &&
	[[ "$(sum "$dest_dir/firecracker")" == "$fc_want" && "$(sum "$dest_dir/jailer")" == "$jailer_want" ]]; then
	echo "已是最新：$dest_dir/{firecracker,jailer}（${version}）"
	exit 0
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl --fail --location --silent --show-error --retry 3 "$url" --output "$tmp/release.tgz"
got="$(sum "$tmp/release.tgz")"
if [[ "$got" != "$want_tgz" ]]; then
	echo "sha256 不匹配：$url 期望 $want_tgz，实际 $got" >&2
	exit 1
fi
tar -xzf "$tmp/release.tgz" -C "$tmp" "$fc_path" "$jailer_path"
for pair in "$fc_path:$fc_want" "$jailer_path:$jailer_want"; do
	f="${pair%%:*}" w="${pair##*:}"
	g="$(sum "$tmp/$f")"
	if [[ "$g" != "$w" ]]; then
		echo "sha256 不匹配：$f 期望 $w，实际 $g" >&2
		exit 1
	fi
done

mkdir -p "$dest_dir"
install -m 0755 "$tmp/$fc_path" "$dest_dir/firecracker"
install -m 0755 "$tmp/$jailer_path" "$dest_dir/jailer"
echo "已下载 Firecracker ${version} → $dest_dir/{firecracker,jailer}"
