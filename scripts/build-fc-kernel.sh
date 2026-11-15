#!/usr/bin/env bash
# 构建 Firecracker guest 内核 build/fc/vmlinux：按 images/fc/artifacts.lock.json 下载并校验内核源码包与基础配置，
# 在 images/fc/kernel.Dockerfile 里编译（不需要宿主 root；需要 Docker BuildKit / buildx），核对最终配置后输出。
#
# 用法：scripts/build-fc-kernel.sh
# 依赖：curl、jq、sha256sum、od、docker buildx。下载缓存在 build/fc/downloads/（校验通过才复用）。
# 首次构建要拉 Arch 快照镜像并编译内核，通常需要十几分钟到半小时。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

for c in curl jq sha256sum od docker; do
	command -v "$c" >/dev/null || { echo "缺少命令 $c" >&2; exit 1; }
done
if [[ "$(uname -m)" != "x86_64" ]]; then
	echo "Firecracker guest 内核只在 x86_64 宿主上构建（当前 $(uname -m)）" >&2
	exit 1
fi

lock="images/fc/artifacts.lock.json"
fragment="images/fc/kernel.config.fragment"
k() { jq -r ".kernel.$1" "$lock"; }
kernel_url="$(k sourceArchive)"
kernel_sha="$(k sourceArchiveSha256)"
config_url="$(k configUrl)"
config_sha="$(k configSha256)"
arch_digest="$(k builder.archOciDigest)"
arch_snapshot="$(k builder.archSnapshot)"

downloads="build/fc/downloads"
mkdir -p "$downloads"

# fetch_verified <url> <sha256> <输出>：已有且校验通过则复用，否则下载到临时文件、校验后改名。
fetch_verified() {
	local url="$1" want="$2" out="$3"
	if [[ -f "$out" ]] && echo "$want  $out" | sha256sum --check --status; then
		return 0
	fi
	curl --fail --location --silent --show-error --retry 3 "$url" --output "$out.tmp"
	if ! echo "$want  $out.tmp" | sha256sum --check --status; then
		echo "sha256 不匹配：$url 期望 $want，实际 $(sha256sum "$out.tmp" | cut -d' ' -f1)" >&2
		rm -f "$out.tmp"
		exit 1
	fi
	mv "$out.tmp" "$out"
}

fetch_verified "$kernel_url" "$kernel_sha" "$downloads/kernel.tar.gz"
fetch_verified "$config_url" "$config_sha" "$downloads/kernel.config"

ctx="build/fc/kernel-context"
outdir="build/fc/kernel-output"
rm -rf "$ctx" "$outdir"
mkdir -p "$ctx"
# 硬链接进构建上下文（同一文件系统时不复制 200MB 的源码包）。
ln "$downloads/kernel.tar.gz" "$ctx/kernel.tar.gz" 2>/dev/null || cp "$downloads/kernel.tar.gz" "$ctx/kernel.tar.gz"
cp "$downloads/kernel.config" "$ctx/kernel.config"
cp "$fragment" "$ctx/kernel.config.fragment"

docker buildx build \
	--file images/fc/kernel.Dockerfile \
	--build-arg "ARCH_DIGEST=$arch_digest" \
	--build-arg "ARCH_SNAPSHOT=$arch_snapshot" \
	--build-arg "KERNEL_ARCHIVE_SHA256=$kernel_sha" \
	--build-arg "KERNEL_CONFIG_SHA256=$config_sha" \
	--output "type=local,dest=$outdir" \
	"$ctx"

magic="$(od -An -tx1 -N4 "$outdir/vmlinux" | tr -d '[:space:]')"
if [[ "$magic" != "7f454c46" ]]; then
	echo "$outdir/vmlinux 不是 ELF 镜像" >&2
	exit 1
fi

# 核对 fragment 生效：Oryn 依赖的选项缺失即失败，其余（随内核版本可能不存在的）只警告。
declare -A required=()
for opt in CONFIG_OVERLAY_FS CONFIG_EXT4_FS CONFIG_VIRTIO_BLK CONFIG_VIRTIO_NET CONFIG_VIRTIO_MMIO CONFIG_IP_PNP \
	CONFIG_VSOCKETS CONFIG_VIRTIO_VSOCKETS CONFIG_INOTIFY_USER CONFIG_CGROUPS CONFIG_MEMCG CONFIG_CGROUP_PIDS \
	CONFIG_TMPFS CONFIG_DEVTMPFS CONFIG_UNIX98_PTYS CONFIG_FHANDLE CONFIG_SERIO_I8042 CONFIG_KEYBOARD_ATKBD; do
	required[$opt]=1
done
final="$outdir/config"
fail=0
while IFS= read -r line; do
	case "$line" in
	CONFIG_*=y)
		opt="${line%%=*}"
		if ! grep -qx "$opt=y" "$final"; then
			if [[ -n "${required[$opt]:-}" ]]; then
				echo "最终内核配置缺少 $opt=y" >&2
				fail=1
			else
				echo "警告：最终内核配置没有 $opt=y（该版本内核可能没有这个选项）" >&2
			fi
		fi
		;;
	"# CONFIG_"*" is not set")
		opt="${line#\# }"
		opt="${opt%% *}"
		if grep -qx "$opt=y" "$final"; then
			echo "最终内核配置仍有 $opt=y（应关闭）" >&2
			fail=1
		fi
		;;
	esac
done <"$fragment"
if [[ "$fail" != 0 ]]; then
	exit 1
fi

install -m 0644 "$outdir/vmlinux" build/fc/vmlinux
install -m 0644 "$final" build/fc/vmlinux.config
echo "已构建 guest 内核 → build/fc/vmlinux（$(k sourceTag)；最终配置 build/fc/vmlinux.config）"
