#!/usr/bin/env bash
# 把仓库里的 App 图标（web/scripts/gen-icons.py 生成的 AppIcon.appiconset：Any / Dark / Tinted）装进 iOS 工程。
# gen/apple 不入库、由 tauri ios init 生成，模板里带的是 tauri 的默认图标，所以每次生成工程后都要换掉。
# 用法（在 web/ 下）：bash src-tauri/ci/ios-appicon.sh
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
src="$here/icons/ios/AppIcon.appiconset"
dst="$here/gen/apple/Assets.xcassets/AppIcon.appiconset"
[[ -f "$src/Contents.json" ]] || { echo "::error::缺少 $src（先跑 web/scripts/gen-icons.py）"; exit 1; }
[[ -d "$(dirname "$dst")" ]] || { echo "::error::没有 $(dirname "$dst")（先 tauri ios init）"; exit 1; }
rm -rf "$dst"
cp -R "$src" "$dst"
echo "已装入 App 图标：$(cd "$dst" && echo *)"
