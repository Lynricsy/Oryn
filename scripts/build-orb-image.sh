#!/usr/bin/env bash
# 构建 orb 镜像 oryn-orb:dev。
#
# 用法：scripts/build-orb-image.sh [x64|arm64]   （默认宿主架构）
# 环境变量：IMAGE（默认 oryn-orb:dev）、VERSION（默认 git describe）
#
# 前置：build/core/<arch>/omp 已就位（scripts/fetch-core.sh <arch>）。
# 构建上下文是 build/orb-context/<arch>/，只含 omp / orbd / oryn 三个文件，不把仓库与 node_modules 发给 docker。
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
x64) goarch=amd64 ;;
arm64) goarch=arm64 ;;
*) echo "用法：$0 [x64|arm64]" >&2; exit 2 ;;
esac

image="${IMAGE:-oryn-orb:dev}"
version="${VERSION:-$(git describe --tags --exclude 'desktop-v*' --exclude 'mobile-v*' --always --dirty 2>/dev/null || echo dev)}"
omp="build/core/${arch}/omp"
ctx="build/orb-context/${arch}"

if [[ ! -x "$omp" ]]; then
	echo "缺少 $omp，先运行 scripts/fetch-core.sh ${arch}" >&2
	exit 1
fi

rm -rf "$ctx"
mkdir -p "$ctx"
for b in orbd oryn; do
	CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath \
		-ldflags "-s -w -X github.com/Lynricsy/Oryn/internal/version.Version=${version}" \
		-o "${ctx}/${b}" "./cmd/${b}"
done
cp "$omp" "${ctx}/omp"

docker build \
	--platform "linux/${goarch}" \
	--build-arg "TARGETARCH=${goarch}" \
	-f images/orb/Dockerfile \
	-t "$image" \
	"$ctx"

docker image ls "$image"
