#!/usr/bin/env bash
# jq 程序里的 $w / $dx / $k 是 jq 变量（单引号是故意的）；谓词函数与 cleanup 经 until_ok / trap 间接调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# iOS 模拟器里原生底栏与原生页头的端到端检查（mobile.yml 的 ios-check，硬门禁）：真实网页、真实宿主权限、真实触摸。
# 与 ios-tabbar-check.sh / ios-header-check.sh 的原生演示互补：演示绕过网页直接调插件，这里走生产路径的每一环——
#   - mock Web UI（VITE_MOCK=1 的构建）由本机 vite preview 提供在 http://localhost:<端口>；
#   - debug 包读 ORYN_E2E_URL（mobile.rs 的 e2e_open，仅 debug 构建），跳过配对，经与生产相同的 open_remote 打开它：
#     远程能力与 MOBILE_HOST_PERMISSIONS 照常注入，页面的 host_ready / host_tab_bar_* / host_header_* 都过权限校验；
#   - 页面认出 iOS 壳（壳注入的 window.__ORYN_HOST__，bridge ≥ 5）并进入 compact 下沉侧栏布局，原生底栏接管；线程页上原生页头接管；
#   - 触摸由 XCUITest 合成（ios-e2e/，XcodeGen 生成工程）：经模拟器 HID 与 UIKit 命中测试，网页与原生叠层收到的都是真触摸。
# 两个 UI 测试先后各跑一次 xcodebuild（-only-testing），测试与本脚本经共享目录交接（见 E2ESupport.swift）。
# 底栏（TabBarE2ETests.swift，从首页 / 起步）：
#   loaded   页面就绪：截关闭态基线，回传胶囊矩形（来自事件日志 tabbar.set）
#   open     点页头的侧栏按钮：等揭示边到侧栏宽，截图（无障碍树断言原生可见、网页胶囊隐藏在测试里）
#   closed   点露出的卡片条：等揭示边归 0，截图
#   drag     从左缘拖到约一半并按住：等揭示边停稳，按住期间截图
#   projects 点原生底栏的「项目」：等 tabbar.tap 与网页回发的 tabbar.set（selected = projects），截图
#   final    再点「项目」收起侧栏：等揭示边归 0，截图
# 页头（HeaderE2ETests.swift，直接打开 mock 的线程页 /t/<MAIN_THREAD>）：
#   h-loaded  header.set（key = 线程 id）与 header.show（alpha 1）：原生页头接管，回传三件矩形
#   h-native  原生三件接管、网页三件隐藏（无障碍树断言在测试里），截图
#   h-menu    点原生标题胶囊：等 header.event（menu = title），截图
#   h-sub     点「Orb」行进子页：此时不应有任何 select，截图
#   h-back    点返回行回主页，截图
#   h-dialog  点「改名」：等 header.event（select / title / rename），网页打开对话框、遮挡探测让原生让位（header.show alpha 0），截图
#   h-return  点遮罩关掉对话框：等 header.show（alpha 1），截图
#   h-sidebar 点原生侧栏钮：等 header.event（lead）、header.show（alpha 0）与原生底栏揭示到侧栏宽，截图
#   h-closed  点卡片条关侧栏：等揭示边归 0 与 header.show（alpha 1），截图
# 像素断言（逐区域比较，同 ios-region-diff.swift）全部打印并写进 Step Summary，任一不符则退出码 1；
# 全部断言跑完且通过才写 <输出目录>/.complete。
# 事件日志里的 tabbar.reveal / header.show 只有 debug 包会写（mobile.rs）。就绪一律靠事件日志判断，不靠固定 sleep。
# 用法：ios-e2e-check.sh <Oryn.app> <mock Web UI 构建目录> <输出目录>
set -euo pipefail
if (($# != 3)); then
  echo "用法：$0 <Oryn.app> <mock Web UI 构建目录> <输出目录>" >&2
  exit 2
fi
app=$1
dist=$(cd "$2" && pwd)
mkdir -p "$3"
out=$(cd "$3" && pwd)
bundle=com.lynricsy.oryn
here=$(cd "$(dirname "$0")" && pwd)
web=$(cd "$here/../.." && pwd)
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
port=47613
# 用不带点的主机名：ATS 默认放行（iOS 17 起 IP 字面量不再默认放行）；localhost 也是安全上下文
origin="http://localhost:$port"
work=$(mktemp -d)
sync="$work/sync"
mkdir -p "$sync"
server=""
runner=""
cleanup() {
  if [[ -n $runner ]]; then kill "$runner" 2>/dev/null || true; fi
  if [[ -n $server ]]; then kill "$server" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

# 工具与 UI 测试先编译：工具链问题在启动模拟器之前就暴露
xcrun --sdk macosx swiftc -O "$here/ios-region-diff.swift" -o "$work/region-diff"
command -v xcodegen >/dev/null || brew install xcodegen

# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"
pick=$(pick_iphone)
udid=$(jq -r .udid <<<"$pick")
echo "### iOS 原生底栏端到端（真实网页 + 触摸）" >>"$summary"
echo "模拟器：$(jq -r '"\(.name)（iOS \(.os)）"' <<<"$pick")" | tee -a "$summary"
echo >>"$summary"

cp -R "$here/ios-e2e" "$work/proj"
(cd "$work/proj" && xcodegen generate --quiet)
xcb=(-project "$work/proj/OrynE2E.xcodeproj" -scheme OrynE2E -destination "id=$udid" -derivedDataPath "$work/dd")
xcodebuild build-for-testing "${xcb[@]}" -quiet

# mock Web UI 的静态服务（:: 同时收 IPv4 / IPv6，localhost 解析到哪个都能连上）
(cd "$web" && exec bunx vite preview --outDir "$dist" --host :: --port "$port" --strictPort) >"$out/preview.log" 2>&1 &
server=$!
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$origin/" && break
  sleep 0.5
done
curl -fsS -o /dev/null "$origin/" || {
  echo "::error::mock Web UI 没起来（${origin}）"
  cat "$out/preview.log"
  exit 1
}

xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b
xcrun simctl install "$udid" "$app"
data=$(xcrun simctl get_app_container "$udid" "$bundle" data)
log="$data/tmp/events.jsonl"
mkdir -p "$data/tmp"
rm -f "$log"
rm -rf "$out/e2e.xcresult" "$out/header.xcresult"

# UI 测试在后台跑；TEST_RUNNER_ 前缀的变量由 xcodebuild 去掉前缀后交给测试进程。关掉并行测试：并行会克隆模拟器，克隆上没装 App。
# 先只跑底栏测试；页头测试在底栏全部断言之后单独跑（见下方「原生页头」）
xlog="$out/xcodebuild.log"
events=events.jsonl
TEST_RUNNER_E2E_SYNC="$sync" TEST_RUNNER_E2E_URL="$origin/" TEST_RUNNER_E2E_LOG="$log" \
  xcodebuild test-without-building "${xcb[@]}" -only-testing:OrynE2E/TabBarE2ETests -parallel-testing-enabled NO \
  -resultBundlePath "$out/e2e.xcresult" >"$xlog" 2>&1 &
runner=$!

# ---------------------------------------------------------------- 事件日志与交接

# ev <事件名>：该事件的全部行
ev() { grep -F "\"event\":\"$1\"" "$log" 2>/dev/null || true; }
last() { ev "$1" | tail -1; }
count() { ev "$1" | wc -l | tr -d ' '; }

# fail <步骤> <原因>：告诉测试（它带着原因失败），打印现场后退出
fail() {
  printf '%s' "$2" >"$sync/$1.fail"
  echo "::error::端到端（${1}）：${2}"
  echo "- ❌ ${1}：${2}" >>"$summary"
  echo "--- 事件日志"
  cat "$log" 2>/dev/null || true
  echo "--- xcodebuild（末尾）"
  tail -80 "$xlog" || true
  # WebKit 的进程也带上：ATS 拦截、导航失败都记在那里
  echo "--- 系统日志（末尾）"
  xcrun simctl spawn "$udid" log show --last 5m --style compact \
    --predicate 'processImagePath CONTAINS "Oryn" OR processImagePath CONTAINS "WebKit"' 2>/dev/null | tail -200 || true
  cp "$log" "$out/$events" 2>/dev/null || true
  exit 1
}

# wait_req <步骤> <秒>：等测试走到这一步，它带来的 JSON 存进 $req
wait_req() {
  local end=$((SECONDS + $2))
  while ((SECONDS < end)); do
    if [[ -f $sync/$1.req ]]; then
      req=$(cat "$sync/$1.req")
      return 0
    fi
    kill -0 "$runner" 2>/dev/null || fail "$1" "UI 测试在走到这一步之前就退出了（见 $(basename "$xlog")）"
    sleep 0.25
  done
  fail "$1" "${2} 秒内测试没有走到这一步"
}

# until_ok <步骤> <秒> <说明> <命令…>：命令成功为止（按墙钟计时，命令本身可能要等）
until_ok() {
  local step=$1 secs=$2 what=$3 end
  shift 3
  end=$((SECONDS + secs))
  while ((SECONDS < end)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.25
  done
  fail "$step" "${secs} 秒内没有等到：${what}"
}

# 揭示边停稳：tabbar.reveal 的条数 0.75 秒内不再变（吸附由原生插值，网页只发端点；拖动时每帧一条）
reveal_quiet() {
  local a b
  a=$(count tabbar.reveal)
  sleep 0.75
  b=$(count tabbar.reveal)
  [[ $a == "$b" ]]
}

# 最后一条某事件满足 jq 条件（$w = 侧栏宽，$dx = 拖动的手指位移，$k = 页头测试打开的线程 id）
last_is() {
  last "$1" | jq -e --argjson w "${sink_w:-0}" --argjson dx "${dx:-0}" --arg k "${thread:-}" "$2"
}
reveal_is() { last_is tabbar.reveal "$1"; }
# 揭示边停稳且停在满足条件的位置（先等停稳再判条件：停稳前的中间值不算）
settled() { reveal_quiet && reveal_is "$1"; }
# web_ready <最低 bridge>：host_ready 过了远程能力的权限校验才会记 web.ready；bridge 4 = 页面认得原生底栏，5 = 原生页头
web_ready() { ev web.ready | jq -se --arg o "$origin" --argjson b "$1" 'any(.origin == $o and .bridge >= $b)'; }

shot() { xcrun simctl io "$udid" screenshot "$out/e2e-$1.png" >/dev/null 2>&1; }

# ok <步骤> [JSON]：放行测试的这一步
ok() {
  local body='{}'
  if (($# > 1)); then body=$2; fi
  printf '%s' "$body" >"$sync/$1.tmp"
  mv "$sync/$1.tmp" "$sync/$1.ok"
  echo "- ✅ $1" >>"$summary"
}

# finish_runner：等当前这次 xcodebuild 跑完，事件日志存进产物；UI 测试失败则退出
finish_runner() {
  local rc
  set +e
  wait "$runner"
  rc=$?
  set -e
  runner=""
  cp "$log" "$out/$events"
  if ((rc != 0)); then
    echo "::error::UI 测试失败（xcodebuild 退出码 ${rc}）"
    echo "- ❌ UI 测试失败（xcodebuild 退出码 ${rc}，见产物 e2e/$(basename "$xlog")）" >>"$summary"
    tail -120 "$xlog"
    exit 1
  fi
}

# ---------------------------------------------------------------- 步骤

req=""
wait_req loaded 420
until_ok loaded 30 "e2e.open" last_is e2e.open '.'
last_is e2e.open '.ok' >/dev/null || fail loaded "ORYN_E2E_URL 没有打开：$(last e2e.open)"
until_ok loaded 60 "web.ready（${origin}，bridge ≥ 4）" web_ready 4
until_ok loaded 60 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
frame=$(last tabbar.set | jq -c .frame)
read -r fx fy fw fh < <(jq -r '[.x, .y, .width, .height] | @tsv' <<<"$frame")
sink_w=$(awk -v x="$fx" -v w="$fw" 'BEGIN { print 2 * x + w }')
until_ok loaded 30 "关闭态的揭示边停在 x = 0" settled '.x == 0'
sleep 1
shot closed0
pw=$(sips -g pixelWidth "$out/e2e-closed0.png" | awk '/pixelWidth/ { print $2 }')
scale=$(jq -r --argjson pw "$pw" '$pw / .width' <<<"$req")
echo "胶囊：${frame}，侧栏宽 ${sink_w}，缩放 ${scale}"
ok loaded "{\"frame\":$frame}"

wait_req open 120
until_ok open 15 "打开：揭示边停在侧栏宽且不透明" settled '.x >= $w - 1 and .alpha == 1'
sleep 0.5
shot open
ok open

wait_req closed 120
until_ok closed 15 "关闭：揭示边停在 0" settled '.x == 0'
sleep 0.5
shot closed1
ok closed

# 按住共 8 秒：停稳判定与截图都要落在按住期间，截完再确认期间没有新的揭示边（松手会发吸附终点）。
# 测试先发 req 再起手，XCUITest 起手前可能还要等 App 空闲，所以第一段等待放宽
wait_req drag 120
dx=$(jq -r .dx <<<"$req")
until_ok drag 20 "拖动：揭示边停在手指位移附近（${dx}）" \
  settled '.ms == 0 and .x > $w * 0.3 and .x < $w * 0.8 and (.x - $dx | if . < 0 then -. else . end) <= 2'
n=$(count tabbar.reveal)
edge=$(last tabbar.reveal | jq -r .x)
shot drag
[[ $(count tabbar.reveal) == "$n" ]] || fail drag "截图时手指已经松开（揭示边又动了），按住期间没来得及截图"
echo "拖动揭示边：${edge}（手指位移 ${dx}）"
ok drag

# 原生点击 → Rust 记 tabbar.tap 并转给页面 → 页面切分区（路由）→ 页面按新路由回发 spec（selected = projects）
wait_req projects 120
until_ok projects 15 "原生点击上报 tabbar.tap（projects）" last_is tabbar.tap '.id == "projects"'
until_ok projects 15 "网页切到「项目」后回发 tabbar.set（selected = projects）" \
  last_is tabbar.set '.on and .selected == "projects"'
until_ok projects 15 "点「项目」后侧栏保持打开（揭示边停在侧栏宽且不透明）" settled '.x >= $w - 1 and .alpha == 1'
sleep 0.5
shot projects
ok projects

wait_req final 120
until_ok final 15 "再点「项目」收起：揭示边停在 0" settled '.x == 0'
sleep 0.5
shot final
ok final

finish_runner

# ---------------------------------------------------------------- 像素断言

px() { awk -v v="$1" -v s="$scale" 'BEGIN { printf "%d", v * s + 0.5 }'; }
calc() { awk "BEGIN { print $1 }"; }

failed=0
# check <说明> <截图 a> <截图 b> <区域 x（pt）> <区域宽（pt）> <shown|same> [b 的区域 x（pt）]
# 区域竖直范围是胶囊所在的横条；比值 = 不同像素的占比。shown：≥ 0.01；same：≤ 0.01。
# 给了 b 的 x 时（比较平移过的卡片）在 ±2 像素内取最小值，吸收换算到像素的舍入。
check() {
  local label=$1 a=$2 b=$3 x=$4 w=$5 want=$6 bx=${7:-$4} r best="" d ok
  for d in -2 -1 0 1 2; do
    if [[ -z ${7:-} && $d != 0 ]]; then continue; fi
    r=$("$work/region-diff" "$out/e2e-$a.png" "$out/e2e-$b.png" \
      "$(px "$x")" "$(px "$fy")" "$(px "$w")" "$(px "$fh")" "$(($(px "$bx") + d))")
    if [[ -z $best ]] || awk -v r="$r" -v b="$best" 'BEGIN { exit !(r < b) }'; then best=$r; fi
  done
  echo "${label}: ${best}（期望 ${want}）"
  echo "- ${label}: ${best}（期望 ${want}）" >>"$summary"
  if [[ $want == shown ]]; then
    ok=$(awk -v r="$best" 'BEGIN { print (r >= 0.01) }')
  else
    ok=$(awk -v r="$best" 'BEGIN { print (r <= 0.01) }')
  fi
  if [[ $ok != 1 ]]; then
    echo "::error::${label} 不符合期望（${want}），差异 ${best}"
    failed=1
  fi
}

# 打开：胶囊处不再是卡片（与关闭基线不同）；关闭：回到关闭基线（原生没有残留）
check "打开：胶囊处与关闭基线不同" open closed0 "$fx" "$fw" shown
check "关闭：胶囊处回到关闭基线" closed1 closed0 "$fx" "$fw" same
# 拖动：揭示边左侧与打开态一致（侧栏层不动，原生胶囊露出）；让出 72pt 避开卡片投影（--sh-xl）
check "拖动：揭示边左侧与打开态一致" drag open "$fx" "$(calc "$edge - 72 - $fx")" same
check "拖动：揭示边左侧与关闭基线不同" drag closed0 "$fx" "$(calc "$edge - 72 - $fx")" shown
# 拖动：揭示边右侧是整体右移 edge 的卡片，与关闭基线错位比较一致 = 原生底栏没有画到卡片上；让出 6pt 避开发丝线
check "拖动：揭示边右侧是平移的卡片（底栏没画到卡片上）" drag closed0 \
  "$(calc "$edge + 6")" "$(calc "$fx + $fw - $edge - 6")" same 6

# ---------------------------------------------------------------- 原生页头（第二次 xcodebuild，只跑 HeaderE2ETests）

echo >>"$summary"
echo "### iOS 原生页头端到端（真实网页 + 触摸）" >>"$summary"
# mock 的 MAIN_THREAD（web/src/mock/fixtures.ts）：有 orb（标题菜单里有「Orb」子页）、有视图钮
thread=T-0193a1b2-0000-7000-8000-00000000a001
events=header-events.jsonl
xlog="$out/xcodebuild-header.log"
# 底栏测试留下的 App 先停掉，事件日志从头记
xcrun simctl terminate "$udid" "$bundle" 2>/dev/null || true
rm -f "$log"
TEST_RUNNER_E2E_SYNC="$sync" TEST_RUNNER_E2E_URL="$origin/t/$thread" TEST_RUNNER_E2E_LOG="$log" \
  xcodebuild test-without-building "${xcb[@]}" -only-testing:OrynE2E/HeaderE2ETests -parallel-testing-enabled NO \
  -resultBundlePath "$out/header.xcresult" >"$xlog" 2>&1 &
runner=$!

# header.set 在 Swift 挂好之后才记；key 是线程 id
wait_req h-loaded 420
until_ok h-loaded 30 "e2e.open" last_is e2e.open '.'
last_is e2e.open '.ok' >/dev/null || fail h-loaded "ORYN_E2E_URL 没有打开：$(last e2e.open)"
until_ok h-loaded 60 "web.ready（${origin}，bridge ≥ 5）" web_ready 5
until_ok h-loaded 30 "header.set（原生页头接管 ${thread}）" last_is header.set '.on and .key == $k'
until_ok h-loaded 30 "header.show（alpha 1：原生页头显示）" last_is header.show '.alpha == 1'
hset=$(last header.set)
jq -e '.trail != null' <<<"$hset" >/dev/null || fail h-loaded "线程页没有视图钮（header.set 的 trail 为 null）：${hset}"
read -r lx _ _ _ < <(jq -r '.lead | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r tx ty tw th < <(jq -r '.title | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r rx _ rw _ < <(jq -r '.trail | [.x, .y, .width, .height] | @tsv' <<<"$hset")
# 原生底栏的揭示边要用侧栏宽（线程页同样在下沉侧栏布局里）
until_ok h-loaded 30 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
sink_w=$(last tabbar.set | jq -r '.frame | 2 * .x + .width')
until_ok h-loaded 30 "关闭态的揭示边停在 x = 0" settled '.x == 0'
echo "页头：$(jq -c '{lead, title, trail}' <<<"$hset")"
ok h-loaded "$(jq -c '{lead, title, trail}' <<<"$hset")"

# 原生三件接管、网页三件隐藏的无障碍树断言在测试里；这里确认原生页头此刻是显示的
wait_req h-native 120
last_is header.show '.alpha == 1' >/dev/null || fail h-native "原生页头不在显示状态：$(last header.show)"
sleep 0.5
shot h-native
ok h-native

# 原生点击胶囊 → 插件事件 → Rust 记 header.event 并转给页面
wait_req h-menu 60
until_ok h-menu 15 "header.event（menu = title）" last_is header.event '.op == "menu" and .menu == "title" and .key == $k'
sleep 0.7
shot h-menu
ok h-menu

# 子页是原生本地换页：不回网页，不该有 select
wait_req h-sub 60
selects=$(ev header.event | jq -s 'map(select(.op == "select")) | length')
((selects == 0)) || fail h-sub "进子页不该产生 select：$(ev header.event | tr '\n' ' ')"
sleep 0.7
shot h-sub
ok h-sub

wait_req h-back 60
sleep 0.7
shot h-back
ok h-back

# 选「改名」→ header.event select → 页面在最新菜单项里找回 rename 的回调、打开对话框 → 遮挡探测让原生让位
wait_req h-dialog 60
until_ok h-dialog 15 "header.event（select / title / rename）" \
  last_is header.event '.op == "select" and .menu == "title" and .id == "rename" and .key == $k'
until_ok h-dialog 15 "对话框盖住页头后原生让位（header.show alpha 0）" last_is header.show '.alpha == 0'
sleep 0.5
shot h-dialog
ok h-dialog

wait_req h-return 60
until_ok h-return 15 "对话框关掉后原生页头回来（header.show alpha 1）" last_is header.show '.alpha == 1'
sleep 0.7
shot h-return
ok h-return

# 原生侧栏钮 → header.event lead → 页面开侧栏 → 卡片一动原生页头就让位，原生底栏揭示到侧栏宽
wait_req h-sidebar 60
until_ok h-sidebar 15 "header.event（lead）" last_is header.event '.op == "lead" and .key == $k'
until_ok h-sidebar 15 "开侧栏时原生页头让位（header.show alpha 0）" last_is header.show '.alpha == 0'
until_ok h-sidebar 15 "开侧栏：揭示边停在侧栏宽且不透明" settled '.x >= $w - 1 and .alpha == 1'
sleep 0.5
shot h-sidebar
ok h-sidebar

wait_req h-closed 60
until_ok h-closed 15 "关侧栏：揭示边停在 0" settled '.x == 0'
until_ok h-closed 15 "关侧栏后原生页头回来（header.show alpha 1）" last_is header.show '.alpha == 1'
sleep 0.7
shot h-closed
ok h-closed

finish_runner

# hcheck <说明> <截图 a> <截图 b> <x> <y> <w> <h>（pt）<shown|same>：阈值同 check（shown ≥ 0.01，same ≤ 0.01）
hcheck() {
  local label=$1 a=$2 b=$3 want=$8 r ok
  r=$("$work/region-diff" "$out/e2e-$a.png" "$out/e2e-$b.png" "$(px "$4")" "$(px "$5")" "$(px "$6")" "$(px "$7")")
  echo "${label}: ${r}（期望 ${want}）"
  echo "- ${label}: ${r}（期望 ${want}）" >>"$summary"
  if [[ $want == shown ]]; then
    ok=$(awk -v r="$r" 'BEGIN { print (r >= 0.01) }')
  else
    ok=$(awk -v r="$r" 'BEGIN { print (r <= 0.01) }')
  fi
  if [[ $ok != 1 ]]; then
    echo "::error::${label} 不符合期望（${want}），差异 ${r}"
    failed=1
  fi
}

# 面板区：胶囊下方 14pt 起 200pt 高，左右各让出 8pt；页头横条：侧栏钮左缘到视图钮右缘、胶囊的高度
pnx=$(calc "$tx + 8")
pny=$(calc "$ty + $th + 14")
pnw=$(calc "$tw - 16")
rowx=$lx
roww=$(calc "$rx + $rw - $lx")
hcheck "页头：点胶囊后面板展开" h-menu h-native "$pnx" "$pny" "$pnw" 200 shown
hcheck "页头：子页与主页不同" h-sub h-menu "$pnx" "$pny" "$pnw" 200 shown
hcheck "页头：返回后回到主页" h-back h-menu "$pnx" "$pny" "$pnw" 200 same
hcheck "页头：对话框关掉后原生页头原样回来" h-return h-native "$rowx" "$ty" "$roww" "$th" same
hcheck "页头：关侧栏后原生页头原样回来" h-closed h-native "$rowx" "$ty" "$roww" "$th" same

# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
