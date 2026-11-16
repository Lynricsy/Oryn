#!/usr/bin/env bash
# iOS 模拟器里的可替换图标检查（mobile.yml 的 ios-check，硬门禁）。
# debug 包的 Rust 调试钩子（mobile.rs 的 app_icon_demo）读环境变量 ORYN_APPICON_DEMO，走与页面相同的
# host_app_icon_get / host_app_icon_set（Rust → Swift → UIApplication.setAlternateIconName），依次 默认 → 品牌色 → 默认，
# 每次换完回读 alternateIconName，把整串结果写进事件日志 appicon.demo。本脚本只认这条事件：ok 为 true 且每步回读与目标一致。
# Assets.car 里编没编进图标由「断言产物」的 check-appicon.sh 负责；这里证明的是 App 内真的能换过去再换回来。
# 用法：ios-appicon-check.sh <Oryn.app> <输出目录>
set -euo pipefail
if (($# != 2)); then
  echo "用法：$0 <Oryn.app> <输出目录>" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=ios-sim.sh source-path=SCRIPTDIR
source "$here/ios-sim.sh"
sim_setup "$1" "$2" "iOS 可替换图标（模拟器）"

run appicon appicon.demo "SIMCTL_CHILD_ORYN_APPICON_DEMO=1"
line=$(grep -F '"event":"appicon.demo"' "$out/appicon.jsonl" | tail -1)
jq -r '.steps[] | "- \(.op) \(.id // "")：\(.current // .result)\(if .set then "（set 失败：\(.set)）" else "" end)"' <<<"$line" | tee -a "$summary"
if [[ $(jq -r .ok <<<"$line") != true ]]; then
  echo "::error::可替换图标没能在 App 内切换：${line}"
  exit 1
fi
: >"$out/.complete"
