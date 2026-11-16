#!/usr/bin/env bash
# 把 Icon Composer 包（web/src-tauri/icons/macos/AppIcon.icon，由 web/scripts/gen-icons.py 生成）编成 Assets.car。
# tauri 的 bundle.icon 只认 png / icns；macOS 26 的浅色 / 深色 / 着色图标要靠 Assets.car + Info.plist 的 CFBundleIconName（见 src-tauri/Info.plist），
# icns 仍作 macOS 26 之前与 tauri dev 的回退。需要 Xcode 26+（actool 才认 .icon）。
# 用法：bash macos-appicon.sh <输出目录>，产物 <输出目录>/Assets.car
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
out=$1
min=$(jq -r '.bundle.macOS.minimumSystemVersion' "$here/tauri.conf.json")
rm -rf "$out" && mkdir -p "$out"
xcrun actool "$here/icons/macos/AppIcon.icon" \
  --compile "$out" \
  --platform macosx \
  --target-device mac \
  --minimum-deployment-target "$min" \
  --app-icon AppIcon \
  --output-partial-info-plist "$out/partial-info.plist" \
  --output-format human-readable-text \
  --errors --warnings --notices
[[ -f "$out/Assets.car" ]] || { echo "::error::actool 没有产出 Assets.car"; exit 1; }
