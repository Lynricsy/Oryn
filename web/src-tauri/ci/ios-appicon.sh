#!/usr/bin/env bash
# 把仓库里的 App 图标（web/scripts/gen-icons.py 生成：默认 AppIcon 与可替换图标，各带 Any / Dark / Tinted）装进 iOS 工程。
# gen/apple 不入库、由 tauri ios init 生成，模板里带的是 tauri 的默认图标，所以每次生成工程后都要换掉。
# 可替换图标要 actool 一并编进 Assets.car 并写进 Info.plist 的 CFBundleAlternateIcons：
# 给工程里每个设了 ASSETCATALOG_COMPILER_APPICON_NAME 的构建配置加上 ASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS = YES。
# 用法（在 web/ 下）：bash src-tauri/ci/ios-appicon.sh
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
src="$here/icons/ios"
assets="$here/gen/apple/Assets.xcassets"
[[ -f "$src/AppIcon.appiconset/Contents.json" ]] || { echo "::error::缺少 $src/AppIcon.appiconset（先跑 web/scripts/gen-icons.py）"; exit 1; }
[[ -d "$assets" ]] || { echo "::error::没有 $assets（先 tauri ios init）"; exit 1; }
for set in "$src"/*.appiconset; do
  name=$(basename "$set")
  rm -rf "${assets:?}/$name"
  cp -R "$set" "$assets/$name"
  echo "已装入 $name"
done

shopt -s nullglob
projects=("$here"/gen/apple/*.xcodeproj/project.pbxproj)
[[ ${#projects[@]} -eq 1 ]] || { echo "::error::gen/apple 下应恰好有一个 .xcodeproj，找到 ${#projects[@]} 个"; exit 1; }
pbx=${projects[0]}
if ! grep -q 'ASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS' "$pbx"; then
  n=$(grep -c 'ASSETCATALOG_COMPILER_APPICON_NAME = ' "$pbx" || true)
  [[ $n -gt 0 ]] || { echo "::error::$pbx 里没有 ASSETCATALOG_COMPILER_APPICON_NAME，工程模板变了"; exit 1; }
  # 同一缩进插在 APPICON_NAME 下一行（只动这一个键，其余工程设置原样保留）
  awk '{ print } /ASSETCATALOG_COMPILER_APPICON_NAME = / { match($0, /^[ \t]*/); printf "%sASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS = YES;\n", substr($0, 1, RLENGTH) }' "$pbx" > "$pbx.tmp"
  mv "$pbx.tmp" "$pbx"
  echo "已在 $n 个构建配置里打开 ASSETCATALOG_COMPILER_INCLUDE_ALL_APPICON_ASSETS"
fi
