#!/usr/bin/env bash
# iOS 模拟器里的原生底栏检查（mobile.yml 的 ios-check，硬门禁）。
# debug 包里的 Rust 调试钩子（mobile.rs 的 tab_bar_demo）读环境变量 ORYN_TABBAR_DEMO=<进度>，在连接页上按夹具摆出底栏、
# 把揭示边放到「进度 × 侧栏宽」，并向事件日志写 tabbar.demo（含 frame / sinkW / scale）。本脚本：
#   1. 选一台 iOS 26+ 的 iPhone 模拟器（原生底栏只在 iOS 26+ 启用，旧运行时上 set 会以 unsupported 拒绝）；
#   2. 不带演示启动一次，等 launcher.ready 截基线 base.png；再以进度 0 / 0.5 / 1 各启动一次，等 tabbar.demo 截图；
#   3. 在底栏所在的横条里与基线逐区域比像素：进度 0 整条看不到、进度 1 整条看得到、进度 0.5 揭示边左侧看得到右侧看不到。
# 就绪一律靠事件日志判断，不靠固定 sleep；不用 simctl openurl（自定义协议首次打开会弹系统确认框）。
# 四个比值总会全部打印并写进 Step Summary，任一不符合期望则退出码 1。
# 用法：ios-tabbar-check.sh <Oryn.app> <输出目录>
set -euo pipefail
if (($# != 2)); then
  echo "用法：$0 <Oryn.app> <输出目录>" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
# 模拟器准备（编译比较工具、选机、启动、安装）与 run 都在公共部分
# shellcheck source=ios-sim.sh source-path=SCRIPTDIR
source "$here/ios-sim.sh"
sim_setup "$1" "$2" "iOS 原生底栏（模拟器）"

run base launcher.ready
for p in 0 0.5 1; do
  run "p$p" tabbar.demo "SIMCTL_CHILD_ORYN_TABBAR_DEMO=$p"
  line=$(grep -F '"event":"tabbar.demo"' "$out/p$p.jsonl" | tail -1)
  if [[ $(jq -r .ok <<<"$line") != true ]]; then
    echo "::error::原生底栏演示失败（进度 ${p}）：${line}"
    echo "原生底栏演示失败（进度 ${p}）：\`${line}\`" >>"$summary"
    exit 1
  fi
done

# 几何取自演示事件（pt）；截图是像素，乘 scale
line=$(grep -F '"event":"tabbar.demo"' "$out/p1.jsonl" | tail -1)
read -r fx fy fw fh sw scale < <(jq -r '[.frame.x, .frame.y, .frame.width, .frame.height, .sinkW, .scale] | @tsv' <<<"$line")
px() { awk -v v="$1" -v s="$scale" 'BEGIN { printf "%d", v * s }'; }
calc() { awk "BEGIN { print $1 }"; }

failed=0
# check <说明> <截图名> <区域 x（pt）> <区域宽（pt）> <shown|hidden>
# 区域竖直范围就是胶囊所在的横条；比值 = 与基线不同的像素占比。
check() {
  local label=$1 shot=$2 x=$3 w=$4 want=$5 r ok
  r=$("$work/region-diff" "$out/base.png" "$out/$shot.png" "$(px "$x")" "$(px "$fy")" "$(px "$w")" "$(px "$fh")")
  echo "${label}: ${r}（期望 ${want}）"
  echo "- ${label}: ${r}（期望 ${want}）" >>"$summary"
  if [[ $want == shown ]]; then
    ok=$(awk -v r="$r" 'BEGIN { print (r >= 0.01) }')
    [[ $ok == 1 ]] || echo "::error::$label 应看得到底栏，差异只有 $r"
  else
    ok=$(awk -v r="$r" 'BEGIN { print (r <= 0.003) }')
    [[ $ok == 1 ]] || echo "::error::$label 不应看到底栏，差异 $r"
  fi
  [[ $ok == 1 ]] || failed=1
}

# 进度 0.5 的揭示边在 sinkW / 2；两侧各让出 12 / 4pt，避开裁切边上的抗锯齿与玻璃边缘
half=$(calc "$sw / 2")
check "进度 0：整条隐藏" p0 "$fx" "$fw" hidden
check "进度 1：整条可见" p1 "$fx" "$fw" shown
check "进度 0.5：揭示边左侧可见" p0.5 "$fx" "$(calc "$half - 12 - $fx")" shown
check "进度 0.5：揭示边右侧隐藏" p0.5 "$(calc "$half + 4")" "$(calc "$fx + $fw - $half - 4")" hidden
# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
