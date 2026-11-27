#!/usr/bin/env bash
# 在 orb 镜像上换装微软官方 VS Code，构建只在本机使用的派生镜像（images/orb/Dockerfile.vscode）。
#
# 用法：scripts/build-orb-vscode-image.sh
# 环境变量：BASE（底镜像，默认 oryn-orb:dev；也可以是发布的 ghcr.io/lynricsy/oryn-orb:<版本>）、
#           IMAGE（产物名，默认取 BASE 去掉仓库前缀再加 -vscode，如 oryn-orb:0.2.0-vscode）
#
# 微软许可不允许分享或发布 VS Code：产物不要推送到任何公开仓库。构建上下文为空，只读 Dockerfile。
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

base="${BASE:-oryn-orb:dev}"
default_image="${base##*/}"
[[ "$default_image" == *:* ]] || default_image="${default_image}:latest"
image="${IMAGE:-${default_image}-vscode}"

case "$(uname -m)" in
x86_64 | amd64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) echo "不支持的宿主架构 $(uname -m)" >&2; exit 1 ;;
esac

docker build \
	--build-arg "BASE=${base}" \
	--build-arg "TARGETARCH=${goarch}" \
	-t "$image" \
	- < images/orb/Dockerfile.vscode

# 与公开镜像同一份冒烟（底镜像里的 /usr/local/lib/oryn/smoke-test.sh），再确认 code 已换成官方版
docker run --rm --network none --entrypoint bash "$image" /usr/local/lib/oryn/smoke-test.sh
docker run --rm --network none --entrypoint bash "$image" -c '[[ "$(readlink -f "$(command -v code)")" == /opt/vscode/* ]]'

docker image ls "$image"
