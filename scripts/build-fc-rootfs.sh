#!/usr/bin/env bash
# 把 orb 镜像转成 Firecracker 的只读根盘 build/fc/rootfs.ext4（不需要宿主 root）：
# docker create → docker export 管道进一个 debian 容器，容器里（root）解包、写入 /etc/oryn/image-env.json、
# 按已用空间 ×1.2 + 256MiB 建稀疏文件并 mke2fs -d 填充。
#
# 用法：scripts/build-fc-rootfs.sh [镜像]   （默认 oryn-orb:dev，先 make image）
# 依赖：docker、jq。构建容器镜像取 images/fc/artifacts.lock.json 的 rootfs.builderImage。
#
# image-env.json = {"env": Config.Env, "workingDir": Config.WorkingDir, "user": Config.User}：
# microVM 里没有镜像配置，PATH、HOME 等只能这样带进去，guest-init 按它启动 orbd。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

for c in docker jq; do
	command -v "$c" >/dev/null || { echo "缺少命令 $c" >&2; exit 1; }
done

image="${1:-oryn-orb:dev}"
builder="$(jq -r .rootfs.builderImage images/fc/artifacts.lock.json)"
out_dir="$repo_root/build/fc"
mkdir -p "$out_dir"

if ! docker image inspect "$image" >/dev/null 2>&1; then
	echo "本机没有镜像 $image（先 make image）" >&2
	exit 1
fi
image_env="$(docker image inspect "$image" | jq -c '.[0].Config | {env: .Env, workingDir: .WorkingDir, user: .User}')"

cid="$(docker create "$image")"
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT

rm -f "$out_dir/rootfs.ext4.tmp"
docker export "$cid" | docker run --rm -i \
	-e IMAGE_ENV="$image_env" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	-v "$out_dir:/out" "$builder" bash -euo pipefail -c '
		# 构建镜像不带 mke2fs 时现装；apt 不能读 stdin（stdin 是 docker export 的 tar 流）。
		if ! command -v mke2fs >/dev/null; then
			export DEBIAN_FRONTEND=noninteractive
			apt-get update -qq </dev/null >/dev/null
			apt-get install -y -qq e2fsprogs </dev/null >/dev/null
		fi
		mkdir /r
		tar -xf - --numeric-owner --xattrs --xattrs-include="*" -C /r
		mkdir -p /r/etc/oryn
		printf "%s\n" "$IMAGE_ENV" > /r/etc/oryn/image-env.json
		chmod 0644 /r/etc/oryn/image-env.json
		used=$(du -s --block-size=1 /r | cut -f1)
		size=$(( (used * 12 / 10 + (256 << 20) + (1 << 20) - 1) / (1 << 20) ))
		truncate -s "${size}M" /out/rootfs.ext4.tmp
		mke2fs -q -t ext4 -F -L oryn-root -d /r /out/rootfs.ext4.tmp
		chown "$HOST_UID:$HOST_GID" /out/rootfs.ext4.tmp
		chmod 0644 /out/rootfs.ext4.tmp
		echo "已用 $((used >> 20)) MiB，根盘 ${size} MiB"
	'
mv -f "$out_dir/rootfs.ext4.tmp" "$out_dir/rootfs.ext4"
echo "已构建根盘 $image → build/fc/rootfs.ext4（sha256 $(sha256sum "$out_dir/rootfs.ext4" | cut -c1-16)…）"
