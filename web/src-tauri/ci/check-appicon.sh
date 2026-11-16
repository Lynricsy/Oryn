#!/usr/bin/env bash
# 断言编好的 Assets.car 里有 AppIcon，且带深色外观（iOS 的 Dark 变体 / macOS Icon Composer 的 dark 特化）。
# 外观名随 Xcode 版本可能变化，只要求其中有一个含 dark（不区分大小写）；全部外观名写进 Step Summary 备查。
# 用法：bash check-appicon.sh <Assets.car> <标签>
set -euo pipefail
car=$1
label=$2
[[ -f "$car" ]] || { echo "::error::${label}：没有 $car（App 图标没编进包）"; exit 1; }
info=$(xcrun assetutil --info "$car")
names=$(jq -r '.[] | select(.Name? == "AppIcon") | .Appearance // "(默认)"' <<<"$info" | sort -u)
if [[ -z "$names" ]]; then echo "::error::${label}：Assets.car 里没有 AppIcon"; exit 1; fi
summary=$(paste -sd, - <<<"$names")
echo "- ${label} App 图标外观：${summary}" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
grep -qi dark <<<"$names" || { echo "::error::${label}：AppIcon 没有深色外观"; exit 1; }
