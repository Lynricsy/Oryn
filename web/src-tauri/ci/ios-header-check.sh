#!/usr/bin/env bash
# iOS 模拟器里的原生页头检查（mobile.yml 的 ios-check，硬门禁）。
# debug 包里的 Rust 调试钩子（mobile.rs 的 header_demo）读环境变量 ORYN_HEADER_DEMO=<模式>，在连接页上按夹具摆出页头、
# 按模式打开菜单，并向事件日志写 header.demo（三件控件的矩形、scale、触摸探测 headerProbe 的结果）。
# 本脚本先不带演示启动一次截基线 h-base.png，再对每个模式冷启动一次并截图：
#   hidden     摆上但不显示：三件的位置与基线无差别，任何触摸都不截
#   bar        三件都看得到；页头下方没有多出东西；菜单没开时屏幕中心的触摸不被原生层截住、三件上的被截住
#   title      标题菜单：胶囊下方出现面板；此时整屏的触摸都归原生层
#   title:env  运行环境子页：面板内容与主页不同
# 视图菜单是系统菜单（没有程序化打开），在 ios-e2e-check.sh 里由 XCUITest 真实点开。
# 触摸归属看探测结果（确定性的），外观逐区域比像素（同 ios-region-diff.swift）。全部结果打印并写进 Step Summary，任一不符则退出码 1；
# 跑完全部断言且通过时才写 <输出目录>/.complete（工作流据此判定）。就绪一律靠事件日志判断，不靠固定 sleep。
# 用法：ios-header-check.sh <Oryn.app> <输出目录>
set -euo pipefail
if (($# != 2)); then
  echo "用法：$0 <Oryn.app> <输出目录>" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=ios-sim.sh source-path=SCRIPTDIR
source "$here/ios-sim.sh"
sim_setup "$1" "$2" "iOS 原生页头（模拟器）"

demo_line() { grep -F '"event":"header.demo"' "$out/$1.jsonl" | tail -1; }

run h-base launcher.ready
for mode in hidden bar title title:env; do
  name="h-${mode/:/-}"
  run "$name" header.demo "SIMCTL_CHILD_ORYN_HEADER_DEMO=${mode}"
  line=$(demo_line "$name")
  if [[ $(jq -r .ok <<<"$line") != true ]]; then
    echo "::error::原生页头演示失败（${mode}）：${line}"
    echo "原生页头演示失败（${mode}）：\`${line}\`" >>"$summary"
    exit 1
  fi
done

# 几何取自演示事件（pt）；截图是像素，乘 scale
line=$(demo_line h-bar)
scale=$(jq -r .scale <<<"$line")
rect() { jq -r ".$1 | [.x, .y, .width, .height] | @tsv" <<<"$line"; }
read -r lx ly lw lh < <(rect lead)
read -r tx ty tw th < <(rect title)
read -r rx ry rw rh < <(rect trail)
px() { awk -v v="$1" -v s="$scale" 'BEGIN { printf "%d", v * s }'; }
calc() { awk "BEGIN { print $1 }"; }
failed=0

# diff_ratio <截图 a> <截图 b> <x> <y> <w> <h>（pt；截图名不带扩展名）：区域内与 a 不同的像素占比
diff_ratio() {
  "$work/region-diff" "$out/$1.png" "$out/$2.png" "$(px "$3")" "$(px "$4")" "$(px "$5")" "$(px "$6")"
}

# expect <说明> <比值> <shown|hidden> [shown 的下限，缺省 0.01]：hidden 的上限 0.003
expect() {
  local label=$1 r=$2 want=$3 min=${4:-0.01} ok
  echo "${label}: ${r}（期望 ${want}）"
  echo "- ${label}: ${r}（期望 ${want}）" >>"$summary"
  if [[ $want == shown ]]; then
    ok=$(awk -v r="$r" -v m="$min" 'BEGIN { print (r >= m) }')
    [[ $ok == 1 ]] || echo "::error::${label} 应看得到，差异只有 ${r}（下限 ${min}）"
  else
    ok=$(awk -v r="$r" 'BEGIN { print (r <= 0.003) }')
    [[ $ok == 1 ]] || echo "::error::${label} 不应看到变化，差异 ${r}"
  fi
  [[ $ok == 1 ]] || failed=1
}

# probe <说明> <截图名> <jq 条件>：触摸归属（headerProbe 的 {center, lead, title, trail} 是否被原生层截住）
probe() {
  local label=$1 name=$2 filter=$3 got
  got=$(jq -c .probe <<<"$(demo_line "$name")")
  if jq -e "$filter" <<<"$got" >/dev/null; then
    echo "- ${label}: 通过" | tee -a "$summary"
  else
    echo "::error::${label}：${got}"
    echo "- ${label}: 不符（${got}）" >>"$summary"
    failed=1
  fi
}

probe "bar：只有三件控件接触摸" h-bar '.center == false and .lead and .title and .trail'
probe "hidden：不接任何触摸" h-hidden '(.center or .lead or .title or .trail) | not'
probe "title：菜单开着时整屏接管" h-title '.center and .title'

# 基线兜底：卡顿的 CI 机器上 launcher.ready 之后连接页可能还要好几秒才画出来，基线会截成稳定的白屏。
# hidden 模式什么都不画，页头下方那块与基线必须一致；不一致就是基线没画完，重截（最多两次）
for _ in 1 2; do
  base_diff=$(diff_ratio h-base h-hidden "$tx" "$(calc "$ty + $th + 40")" "$tw" 120)
  awk -v r="$base_diff" 'BEGIN { exit !(r > 0.003) }' || break
  echo "::warning::基线与 hidden 的页头下方不一致（${base_diff}），基线可能没画完，重截"
  run h-base launcher.ready
done

# 面板顶边：胶囊下方 14pt（面板与胶囊的间距之下），左右各让出 8pt 避开圆角
py=$(calc "$ty + $th + 14")
expect "hidden：侧栏钮" "$(diff_ratio h-base h-hidden "$lx" "$ly" "$lw" "$lh")" hidden
expect "hidden：标题胶囊" "$(diff_ratio h-base h-hidden "$tx" "$ty" "$tw" "$th")" hidden
expect "hidden：视图钮" "$(diff_ratio h-base h-hidden "$rx" "$ry" "$rw" "$rh")" hidden
expect "bar：侧栏钮" "$(diff_ratio h-base h-bar "$lx" "$ly" "$lw" "$lh")" shown
expect "bar：标题胶囊" "$(diff_ratio h-base h-bar "$tx" "$ty" "$tw" "$th")" shown
expect "bar：视图钮" "$(diff_ratio h-base h-bar "$rx" "$ry" "$rw" "$rh")" shown
expect "bar：页头下方没有东西" "$(diff_ratio h-base h-bar "$tx" "$(calc "$ty + $th + 40")" "$tw" 120)" hidden
expect "title：面板" "$(diff_ratio h-base h-title "$(calc "$tx + 8")" "$py" "$(calc "$tw - 16")" 200)" shown 0.05
expect "title:env：子页与主页不同" "$(diff_ratio h-title h-title-env "$(calc "$tx + 8")" "$py" "$(calc "$tw - 16")" 200)" shown
# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
