#!/usr/bin/env bash
# jq 程序里的 $w / $k 是 jq 变量（单引号是故意的）；谓词函数与 cleanup 经 until_ok / trap 间接调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# iOS 模拟器里原生底栏、原生页头与可替换图标的端到端检查（mobile.yml 的 ios-check，硬门禁）：真实网页、真实宿主权限、真实触摸。
# 与 ios-header-check.sh 的原生页头演示互补：演示绕过网页直接调插件，这里走生产路径的每一环——
#   - mock Web UI（VITE_MOCK=1 的构建）由本机 vite preview 提供在 http://localhost:<端口>；
#   - debug 包读 ORYN_E2E_URL（mobile.rs 的 e2e_open，仅 debug 构建），跳过配对，经与生产相同的 open_remote 打开它：
#     远程能力与 MOBILE_HOST_PERMISSIONS 照常注入，页面的 host_ready / host_tab_bar_* / host_header_* 都过权限校验；
#   - 页面认出 iOS 壳（壳注入的 window.__ORYN_HOST__，bridge ≥ 7）并进入 compact 下沉侧栏布局：原生底栏（系统 UITabBar，
#     垫在 WebView 之下，接管期间页面透明、遮挡由网页合成器决定）接管；线程页上原生页头接管，并跟着卡片走；
#   - 触摸由 XCUITest 合成（ios-e2e/，XcodeGen 生成工程）：经模拟器 HID 与 UIKit 命中测试，网页、底栏的触摸转交层与原生页头收到的都是真触摸。
# 三个 UI 测试先后各跑一次 xcodebuild（-only-testing），测试与本脚本经共享目录交接（见 E2ESupport.swift）。
# 底栏（TabBarE2ETests.swift，从首页 / 起步）。tabbar.reveal {x, covered} 是卡片左缘与遮挡：每次开合吸附（发终点）、拖动开始、
# 遮挡变化各一条，同值去重，拖动中不逐帧发；原生据此只决定底栏接不接触摸，卡片的过渡全由网页自己走。
#   loaded   页面就绪：接管时补发的 tabbar.reveal（x = 0），截关闭态基线，回传胶囊矩形（来自事件日志 tabbar.set）
#   open     点页头的侧栏按钮：等 tabbar.reveal 报 x = 侧栏宽，截图（无障碍树断言原生按钮在、网页胶囊隐藏、卡片 inert 在测试里）
#   closed   点露出的卡片条：等 tabbar.reveal 报 x = 0，截图
#   drag     从左缘拖到约一半并按住：从关闭位起手时拖动开始那条被去重，事件日志里没有信号；就绪看截图——
#            底栏横条连拍两张一致（卡片停住）且揭示边左侧已与关闭基线不同，期间没有新的 tabbar.reveal（没松手）
#   projects 点原生底栏的「项目」：等 tabbar.tap 与网页回发的 tabbar.set（selected = projects），截图
#   swipe    在原生底栏上按住「项目」横滑到第 1 项再松手（系统透镜换选中）：等 tabbar.tap 与 tabbar.set（selected = threads），截图
#   final    再点已选中的第 1 项收起侧栏：等 tabbar.reveal 报 x = 0，截图
# 页头（HeaderE2ETests.swift，直接打开 mock 的线程页 /t/<MAIN_THREAD>）：
#   h-loaded  header.set（key = 线程 id）与 header.show（alpha 1、dx 0）：原生页头接管，回传三件矩形与侧栏宽
#   h-native  原生三件接管、网页三件隐藏（无障碍树断言在测试里），截图
#   h-menu    点原生标题胶囊：等 header.event（menu = title），截图
#   h-sub     点「Orb」行进子页：此时不应有任何 select，截图
#   h-back    点返回行回主页，截图
#   h-dialog  点「改名」：等 header.event（select / title / rename），网页打开对话框、遮挡探测让原生让位（header.show alpha 0），截图
#   h-return  点遮罩关掉对话框：等 header.show（alpha 1），截图
#   h-sidebar 点原生侧栏钮：等 header.event（lead）与原生底栏的 tabbar.reveal（x = 侧栏宽）；原生页头不让位，跟着卡片吸附到右边
#             （header.show alpha 1、dx = 侧栏宽、passive，带 ms 与绝对起步时刻 at），期间没有 alpha 0，截图
#   h-closed  点卡片条关侧栏：等 tabbar.reveal（x = 0）、关闭吸附（dx 0、passive、at）与静止后的 header.show（dx 0、passive false），截图
# 可替换图标（AppIconE2ETests.swift，直接打开 mock 的 /settings/appearance）：点「品牌色」、点掉系统提示、
#   等 appicon.set（accent），再点「黑白」同样等 appicon.set（default）；测试自己完成，脚本只核对事件顺序。
# 像素断言（逐区域比较，同 ios-region-diff.swift）全部打印并写进 Step Summary，任一不符则退出码 1；
# 全部断言跑完且通过才写 <输出目录>/.complete。
# 事件日志里的 tabbar.reveal / header.show 只有 debug 包会写（mobile.rs）。就绪靠事件日志判断（拖动靠连拍截图），不靠固定 sleep。
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
# ci/ios-sink-spike 分支（不合入）：探针页随 mock Web UI 一起由 vite preview 提供在 /spike/
rm -rf "$dist/spike"
cp -R "$here/ios-e2e/spike" "$dist/spike"
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

# ci/ios-sink-spike 分支（不合入）：只跑根滚动探针（SinkSpikeTests），底栏 / 页头 / 图标三段不跑
xlog="$out/xcodebuild.log"
events=events.jsonl
TEST_RUNNER_E2E_SYNC="$sync" TEST_RUNNER_E2E_URL="$origin/spike/sink-spike.html" TEST_RUNNER_E2E_LOG="$log" \
  xcodebuild test-without-building "${xcb[@]}" -only-testing:OrynE2E/SinkSpikeTests -parallel-testing-enabled NO \
  -resultBundlePath "$out/e2e.xcresult" >"$xlog" 2>&1 &
runner=$!

# ---------------------------------------------------------------- 事件日志与交接

# ev <事件名>：该事件的全部行
ev() { grep -F "\"event\":\"$1\"" "$log" 2>/dev/null || true; }
last() { ev "$1" | tail -1; }
count() { ev "$1" | wc -l | tr -d ' '; }

# fail <步骤> <原因>：告诉测试（它带着原因失败），打印现场后退出
fail() {
  # 现场截图（限时，截不到不影响报错）：判断是页面没打开、白屏还是原生没接管
  sim_shot "$out/e2e-fail-$1.png" 20 || true
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

# 最后一条某事件满足 jq 条件（$w = 侧栏宽，$k = 页头测试打开的线程 id）
last_is() {
  last "$1" | jq -e --argjson w "${sink_w:-0}" --arg k "${thread:-}" "$2"
}
reveal_is() { last_is tabbar.reveal "$1"; }
# 卡片左缘停在打开位 / 关闭位、底栏没被网页内容压住（tabbar.reveal 只在吸附开始时发终点，过渡由网页自己走）
opened='.x >= $w - 1 and .covered == false'
closed='.x == 0 and .covered == false'
# web_ready <最低 bridge>：host_ready 过了远程能力的权限校验才会记 web.ready；bridge 7 = 原生底栏垫底、原生页头跟随卡片
web_ready() { ev web.ready | jq -se --arg o "$origin" --argjson b "$1" 'any(.origin == $o and .bridge >= $b)'; }

# pt → 截图像素（scale 在 loaded 步骤由基线截图算出）
px() { awk -v v="$1" -v s="$scale" 'BEGIN { printf "%d", v * s + 0.5 }'; }
calc() { awk "BEGIN { print $1 }"; }
# strip_diff <图 a> <图 b> <x（pt）> <宽（pt）>：胶囊所在横条里两张图（完整路径）同位置区域的不同像素占比
strip_diff() { "$work/region-diff" "$1" "$2" "$(px "$3")" "$(px "$fy")" "$(px "$4")" "$(px "$fh")"; }
# at_most <比值> <阈值>
at_most() { awk -v r="$1" -v t="$2" 'BEGIN { exit !(r <= t) }'; }

# 限时截图（见 ios-sim.sh 的 sim_shot）；普通步骤截不到图直接判失败
shot() { sim_shot "$out/e2e-$1.png" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"; }
# steady_shot <步骤>：卡片过渡由网页合成器走，没有「停稳」事件；连拍到胶囊横条前后两张一致（≤ 0.01）才收下后一张
steady_shot() {
  local prev="$work/steady-prev.png" next="$work/steady-next.png" end=$((SECONDS + 120))
  sim_shot "$prev" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"
  while ((SECONDS < end)); do
    sim_shot "$next" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"
    if at_most "$(strip_diff "$next" "$prev" "$fx" "$fw")" 0.01; then
      mv "$next" "$out/e2e-$1.png"
      return 0
    fi
    mv "$next" "$prev"
  done
  fail "$1" "120 秒内底栏横条没有停稳（连拍两张总是不同）"
}

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

# ---------------------------------------------------------------- 根滚动探针（ci/ios-sink-spike 分支专用，不合入）
# plan-m20 步骤 1.1。测试按顺序发 p01、p02…，载荷里的键决定这里做什么：
#   width → 截未设底栏的关闭态（bare）并算缩放；tabbar → 等 tabbar.set 后截设了底栏的关闭态（closed）；
#   shot → 停稳后截图；hold → S5 按住拖动（连拍截图 + 事件日志读 sx）；probe → 记一条探针结果；end → 收尾。
# 每条探针附上自上一步以来的卡片左缘轨迹（探针页每帧经 host_tab_bar_reveal 发 max − sx，debug 包记为 tabbar.reveal）。
# 任一探针不过不中断，全部跑完才写 .complete，退出码 = 有无 FAIL。
echo >>"$summary"
echo "### 根滚动探针（plan-m20 S1–S9）" >>"$summary"
spike_failed=0
# probe <探针> <PASS|FAIL|INFO> <读数>
probe() {
  echo "$1 $2：$3"
  echo "- **$1 $2**：$3" >>"$summary"
  if [[ $2 == FAIL ]]; then spike_failed=1; fi
}
# rd <图 a> <图 b> <x> <宽> [b 的 x]（pt，竖直范围是底栏横条）：给了 b 的 x 时在 ±2pt 内取最小差异
rd() {
  local bx=${5:-$3} reach=0 d r best=""
  if [[ -n ${5:-} ]]; then reach=$(px 2); fi
  for ((d = -reach; d <= reach; d++)); do
    r=$("$work/region-diff" "$1" "$2" "$(px "$3")" "$(px "$fy")" "$(px "$4")" "$(px "$fh")" "$(($(px "$bx") + d))")
    if [[ -z $best ]] || awk -v r="$r" -v b="$best" 'BEGIN { exit !(r < b) }'; then best=$r; fi
  done
  echo "$best"
}
edge_now() { last tabbar.reveal | jq -r '.x // empty'; }
mid() { awk -v x="$1" -v w="$sink_w" 'BEGIN { exit !(x > 5 && x < w - 5) }'; }
# 自上一步以来的卡片左缘轨迹（取整、去掉相邻重复）
mark=0
trace() {
  ev tabbar.reveal | tail -n "+$((mark + 1))" | jq -r '.x | floor' |
    awk 'NR == 1 || $0 != prev { printf "%s ", $0 } { prev = $0 }' | cut -c1-300
}
has() { jq -e "$1" >/dev/null <<<"$req"; }

# hold_s5：一次按住拖动；判定后设 reply（没赶上且还能重拖时回 retry）
hold_s5() {
  local dx attempt prev="" moved="" state=timeout end c0 c1 x0 x1 edge left right err pass
  dx=$(jq -r .hold <<<"$req")
  attempt=$(jq -r .attempt <<<"$req")
  end=$((SECONDS + 180))
  while ((SECONDS < end)); do
    c0=$(count tabbar.reveal)
    x0=$(edge_now)
    if ! sim_shot "$work/s5-next.png" 40; then
      state=missed
      break
    fi
    c1=$(count tabbar.reveal)
    x1=$(edge_now)
    if mid "$x1"; then
      moved=1
    elif [[ -n $moved ]]; then
      state=missed # 拖开后又回到 0 或侧栏宽：已松手吸附
      break
    fi
    if [[ -n $prev && $c0 == "$c1" ]] && mid "$x0" &&
      at_most "$(strip_diff "$work/s5-next.png" "$prev" "$fx" "$fw")" 0.01; then
      mv "$work/s5-next.png" "$out/e2e-drag.png"
      state=held
      break
    fi
    mv "$work/s5-next.png" "$work/s5-prev.png"
    prev="$work/s5-prev.png"
  done
  reply='{}'
  if [[ $state != held ]]; then
    if ((attempt < 3)); then
      echo "::warning::S5 第 ${attempt} 次没赶在按住期间截到停稳的图（${state}），重拖"
      reply='{"retry":true}'
    else
      probe S5 FAIL "连续 3 次没能在按住期间截到停稳的图（最后一次 ${state}，最后的左缘 $(edge_now)）"
    fi
    return
  fi
  edge=$x0
  if [[ ! -f $out/e2e-open.png ]]; then
    probe S5 FAIL "没有打开态截图可比（左缘 ${edge}）"
    return
  fi
  left=$(rd "$out/e2e-drag.png" "$out/e2e-open.png" "$fx" "$(calc "$edge - 4 - $fx")")
  right=$(rd "$out/e2e-drag.png" "$out/e2e-closed.png" "$(calc "$edge + 6")" "$(calc "$fx + $fw - $edge - 6")" 6)
  err=$(calc "$edge - $dx")
  # 第 2 轮：像素两侧都是 0（页面的 sx 与画面上的卡片左缘在 ±2pt 内一致，A1 / A6 成立），手指与左缘差 −10：
  # 这是 UIScrollView 起拖的滞后（拖过滞后距离才开始跟手，之后 1:1），所以按像素判定，手指差只要求 ≤ 15pt 并记录
  pass=FAIL
  if at_most "$left" 0.01 && at_most "$right" 0.01 && awk -v e="$err" 'BEGIN { exit !(e <= 15 && e >= -15) }'; then
    pass=PASS
  fi
  probe S5 "$pass" "手指位移 ${dx}，按住时卡片左缘（max − sx）${edge}（与手指差 ${err}，UIScrollView 起拖滞后）；揭示边左侧 vs 打开态 ${left}（期望 ≤ 0.01），右侧 vs 按 sx 平移的关闭态 ${right}（期望 ≤ 0.01，±2pt）；第 ${attempt} 次"
}

req=""
seq=0
while :; do
  seq=$((seq + 1))
  step=$(printf 'p%02d' "$seq")
  wait_req "$step" 420
  if ((seq == 1)); then
    until_ok "$step" 30 "e2e.open" last_is e2e.open '.'
    last_is e2e.open '.ok' >/dev/null || fail "$step" "ORYN_E2E_URL 没有打开：$(last e2e.open)"
  fi
  reply='{}'
  if has .width; then
    sleep 1
    shot bare
    pw=$(sips -g pixelWidth "$out/e2e-bare.png" | awk '/pixelWidth/ { print $2 }')
    scale=$(jq -r --argjson pw "$pw" '$pw / .width' <<<"$req")
  fi
  if has .tabbar; then
    until_ok "$step" 30 "tabbar.set（探针页设了原生底栏）" last_is tabbar.set '.on and .frame != null'
    frame=$(last tabbar.set | jq -c .frame)
    read -r fx fy fw fh < <(jq -r '[.x, .y, .width, .height] | @tsv' <<<"$frame")
    sink_w=$(awk -v x="$fx" -v w="$fw" 'BEGIN { print 2 * x + w }')
    echo "底栏：${frame}，侧栏宽 ${sink_w}，缩放 ${scale}"
    sleep 1
    steady_shot closed
  fi
  if has '.shot'; then steady_shot "$(jq -r .shot <<<"$req")"; fi
  if has '.hold'; then hold_s5; fi
  if has '.probe'; then
    t=$(trace)
    probe "$(jq -r .probe <<<"$req")" \
      "$(jq -r 'if .pass == null then "INFO" elif .pass then "PASS" else "FAIL" end' <<<"$req")" \
      "$(jq -r .detail <<<"$req")${t:+ ｜ 左缘轨迹：$t}"
  fi
  mark=$(count tabbar.reveal)
  if has '.end'; then
    ok "$step"
    break
  fi
  ok "$step" "$reply"
done

# S9：设了底栏后，打开态横条与关闭态不同（底栏从透明侧栏透出）；关闭态与未设底栏时一致（卡片盖住）
if [[ -f $out/e2e-open.png && -f $out/e2e-closed.png && -f $out/e2e-bare.png ]]; then
  open_vs_closed=$(rd "$out/e2e-open.png" "$out/e2e-closed.png" "$fx" "$fw")
  closed_vs_bare=$(rd "$out/e2e-closed.png" "$out/e2e-bare.png" "$fx" "$fw")
  pass=FAIL
  if ! at_most "$open_vs_closed" 0.01 && at_most "$closed_vs_bare" 0.01; then pass=PASS; fi
  probe S9 "$pass" "打开态 vs 关闭态 ${open_vs_closed}（期望 ≥ 0.01）；关闭态 vs 未设底栏 ${closed_vs_bare}（期望 ≤ 0.01）"
else
  probe S9 FAIL "缺截图（open / closed / bare）"
fi

finish_runner
: >"$out/.complete"
exit "$spike_failed"

req=""
wait_req loaded 420
until_ok loaded 30 "e2e.open" last_is e2e.open '.'
last_is e2e.open '.ok' >/dev/null || fail loaded "ORYN_E2E_URL 没有打开：$(last e2e.open)"
until_ok loaded 60 "web.ready（${origin}，bridge ≥ 7）" web_ready 7
until_ok loaded 60 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
frame=$(last tabbar.set | jq -c .frame)
read -r fx fy fw fh < <(jq -r '[.x, .y, .width, .height] | @tsv' <<<"$frame")
sink_w=$(awk -v x="$fx" -v w="$fw" 'BEGIN { print 2 * x + w }')
until_ok loaded 30 "接管时补发当前开合：tabbar.reveal 停在 x = 0" reveal_is "$closed"
sleep 1
shot closed0
pw=$(sips -g pixelWidth "$out/e2e-closed0.png" | awk '/pixelWidth/ { print $2 }')
scale=$(jq -r --argjson pw "$pw" '$pw / .width' <<<"$req")
echo "胶囊：${frame}，侧栏宽 ${sink_w}，缩放 ${scale}"
ok loaded "{\"frame\":$frame}"

wait_req open 120
until_ok open 15 "打开：tabbar.reveal 报侧栏宽" reveal_is "$opened"
steady_shot open
ok open

wait_req closed 120
until_ok closed 15 "关闭：tabbar.reveal 报 0" reveal_is "$closed"
steady_shot closed1
ok closed

# 拖动：从关闭位起手，tabbar.reveal 本来就是 x = 0，拖动开始那条被控制器去重，按住期间事件日志里没有信号；
# 就绪改看截图：胶囊横条连拍两张一致（卡片停住了），且揭示边左侧与关闭基线不同（确实拖开了）。
# 松手会发吸附终点（tabbar.reveal x = 侧栏宽），所以 req 之后 tabbar.reveal 的条数一变就是已经松手。
# 卡片的平移就是手指位移（sinkProgress：0 + dx / 侧栏宽，拖到约一半不会夹到 1），揭示边取 dx。
# 按住 40 秒：判定用的两张截图都要落在按住期间，截完再确认没有新的 tabbar.reveal。
# CI 的 macOS 机器会整机卡顿几十秒到几分钟（截图曾卡 3 分钟），没赶上就让测试收起侧栏重拖，最多 3 次；
# 每次的步骤名带序号（drag1 / drag2 / drag3），交接文件不会与上一次混淆。
# 测试先发 req 再起手，XCUITest 起手前可能还要等 App 空闲，所以等 req 放宽
for attempt in 1 2 3; do
  step="drag$attempt"
  wait_req "$step" 300
  edge=$(jq -r .dx <<<"$req")
  n=$(count tabbar.reveal)
  prev=""
  state=timeout
  end=$((SECONDS + 180))
  while ((SECONDS < end)); do
    # 截图限时 40 秒（与按住时长相同）：超时、截之前或截完发现已松手都算没赶上，重拖
    if [[ $(count tabbar.reveal) != "$n" ]] || ! sim_shot "$work/drag-next.png" 40 ||
      [[ $(count tabbar.reveal) != "$n" ]]; then
      state=missed
      break
    fi
    if [[ -n $prev ]] &&
      at_most "$(strip_diff "$work/drag-next.png" "$prev" "$fx" "$fw")" 0.01 &&
      ! at_most "$(strip_diff "$work/drag-next.png" "$out/e2e-closed0.png" "$fx" "$(calc "$edge - 72 - $fx")")" 0.01; then
      mv "$work/drag-next.png" "$out/e2e-drag.png"
      state=held
      break
    fi
    mv "$work/drag-next.png" "$work/drag-prev.png"
    prev="$work/drag-prev.png"
  done
  case $state in
  held)
    echo "拖动揭示边：${edge}（= 手指位移，第 ${attempt} 次）"
    ok "$step"
    break
    ;;
  timeout) fail "$step" "180 秒内胶囊横条没有在拖开后停稳（手指位移 ${edge}），也没有松手吸附" ;;
  esac
  ((attempt < 3)) || fail "$step" "连续 3 次都没能在按住期间连拍到停稳的两张图"
  echo "::warning::第 ${attempt} 次拖动没赶在按住期间截图，收起后重拖"
  ok "$step" '{"retry":true}'
done

# 原生点击 → Rust 记 tabbar.tap 并转给页面 → 页面切分区（路由）→ 页面按新路由回发 spec（selected = projects）
wait_req projects 120
until_ok projects 15 "原生点击上报 tabbar.tap（projects）" last_is tabbar.tap '.id == "projects"'
until_ok projects 15 "网页切到「项目」后回发 tabbar.set（selected = projects）" \
  last_is tabbar.set '.on and .selected == "projects"'
until_ok projects 15 "点「项目」后侧栏保持打开（tabbar.reveal 仍是侧栏宽）" reveal_is "$opened"
steady_shot projects
ok projects

# 系统透镜的横滑选中：按住已选中的「项目」横滑到第 1 项（threads）松手 → UITabBar 换选中 → 同一条 tabbar.tap 路径
wait_req swipe 120
until_ok swipe 15 "横滑松手后上报 tabbar.tap（threads）" last_is tabbar.tap '.id == "threads"'
until_ok swipe 15 "网页切到第 1 项后回发 tabbar.set（selected = threads）" \
  last_is tabbar.set '.on and .selected == "threads"'
until_ok swipe 15 "横滑换项后侧栏保持打开（tabbar.reveal 仍是侧栏宽）" reveal_is "$opened"
steady_shot swipe
ok swipe

wait_req final 120
until_ok final 15 "再点已选中的第 1 项收起：tabbar.reveal 报 0" reveal_is "$closed"
steady_shot final
ok final

finish_runner

# ---------------------------------------------------------------- 像素断言

failed=0
# check <说明> <截图 a> <截图 b> <区域 x（pt）> <区域宽（pt）> <shown|same> [b 的区域 x（pt）]
# 区域竖直范围是胶囊所在的横条；比值 = 不同像素的占比。shown：≥ 0.01；same：≤ 0.01。
# 给了 b 的 x 时（比较平移过的卡片）在 ±2pt 内取最小值：揭示边取的是手指位移，吸收它与实际平移之差及换算到像素的舍入。
check() {
  local label=$1 a=$2 b=$3 x=$4 w=$5 want=$6 bx=${7:-$4} r best="" d ok reach=0
  if [[ -n ${7:-} ]]; then reach=$(px 2); fi
  for ((d = -reach; d <= reach; d++)); do
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

# 打开：胶囊处不再是卡片（与关闭基线不同）；关闭：回到关闭基线（卡片重新盖住垫底的原生底栏）
check "打开：胶囊处与关闭基线不同" open closed0 "$fx" "$fw" shown
check "关闭：胶囊处回到关闭基线" closed1 closed0 "$fx" "$fw" same
# 拖动：揭示边左侧与打开态一致（侧栏层不动，垫底的原生底栏从透明页面透出）；让出 72pt 避开卡片投影（--sh-xl）
check "拖动：揭示边左侧与打开态一致" drag open "$fx" "$(calc "$edge - 72 - $fx")" same
check "拖动：揭示边左侧与关闭基线不同" drag closed0 "$fx" "$(calc "$edge - 72 - $fx")" shown
# 拖动：揭示边右侧是整体右移 edge 的卡片，与关闭基线错位比较一致 = 卡片不透明、底栏没有透到卡片上；
# 让出 6pt 避开发丝线与卡片左下圆角
check "拖动：揭示边右侧是平移的卡片（底栏没透到卡片上）" drag closed0 \
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
until_ok h-loaded 60 "web.ready（${origin}，bridge ≥ 7）" web_ready 7
until_ok h-loaded 30 "header.set（原生页头接管 ${thread}）" last_is header.set '.on and .key == $k'
until_ok h-loaded 30 "header.show（alpha 1、dx 0：原生页头显示在关闭位）" last_is header.show '.alpha == 1 and .dx == 0'
hset=$(last header.set)
jq -e '.trail != null' <<<"$hset" >/dev/null || fail h-loaded "线程页没有视图钮（header.set 的 trail 为 null）：${hset}"
read -r lx _ _ _ < <(jq -r '.lead | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r tx ty tw th < <(jq -r '.title | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r rx _ rw _ < <(jq -r '.trail | [.x, .y, .width, .height] | @tsv' <<<"$hset")
# 侧栏宽：原生底栏与原生页头吸附到打开位的终点（线程页同样在下沉侧栏布局里）
until_ok h-loaded 30 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
sink_w=$(last tabbar.set | jq -r '.frame | 2 * .x + .width')
until_ok h-loaded 30 "接管时补发当前开合：tabbar.reveal 停在 x = 0" reveal_is "$closed"
echo "页头：$(jq -c '{lead, title, trail}' <<<"$hset")，侧栏宽 ${sink_w}"
ok h-loaded "$(jq -c --argjson w "$sink_w" '{lead, title, trail, sinkW: $w}' <<<"$hset")"

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
# 之后的开合期间原生页头一直显示：从这里起数 header.show
shows=$(count header.show)
sleep 0.7
shot h-return
ok h-return

# 原生侧栏钮 → header.event lead → 页面开侧栏 → 原生页头不让位：按网页给的终点、时长、缓动与绝对起步时刻
# （at = 卡片起步时刻，打开早 SYNC_BIAS_MS）跟着卡片吸附到右边，只显示不接触摸（passive）；原生底栏的 tabbar.reveal 到侧栏宽
wait_req h-sidebar 60
until_ok h-sidebar 15 "header.event（lead）" last_is header.event '.op == "lead" and .key == $k'
until_ok h-sidebar 15 "开侧栏：tabbar.reveal 报侧栏宽" reveal_is "$opened"
until_ok h-sidebar 15 "开侧栏：原生页头跟着卡片吸附到侧栏宽（header.show alpha 1、dx = 侧栏宽、passive、ms > 0、带 at）" \
  last_is header.show '.alpha == 1 and .passive == true and .ms > 0 and .at != null and (.dx - $w | if . < 0 then -. else . end) <= 1'
since=$(ev header.show | tail -n "+$((shows + 1))")
jq -se 'all(.alpha == 1)' <<<"$since" >/dev/null ||
  fail h-sidebar "开侧栏时原生页头不该让位（出现了 alpha 0）：$(tr '\n' ' ' <<<"$since")"
sleep 0.5
shot h-sidebar
ok h-sidebar

# 点卡片条关侧栏 → 原生页头跟着卡片吸附回 dx 0（关闭晚 SYNC_BIAS_MS 起步），静止 REST_MS 后再发一条 passive false 重新接触摸
wait_req h-closed 60
until_ok h-closed 15 "关侧栏：tabbar.reveal 报 0" reveal_is "$closed"
until_ok h-closed 15 "关侧栏后原生页头静止在关闭位、重新接触摸（header.show alpha 1、dx 0、passive false）" \
  last_is header.show '.alpha == 1 and .dx == 0 and .passive == false'
since=$(ev header.show | tail -n "+$((shows + 1))")
jq -se 'any(.dx == 0 and .passive == true and .ms > 0 and .at != null)' <<<"$since" >/dev/null ||
  fail h-closed "关侧栏时原生页头没有跟着卡片吸附（没有 dx 0、passive、带 at 的 header.show）：$(tr '\n' ' ' <<<"$since")"
jq -se 'all(.alpha == 1)' <<<"$since" >/dev/null ||
  fail h-closed "开合期间原生页头不该让位（出现了 alpha 0）：$(tr '\n' ' ' <<<"$since")"
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

# ---------------------------------------------------------------- 可替换图标（第三次 xcodebuild，只跑 AppIconE2ETests）
# 测试自己点选、点掉系统提示并等 appicon.set（壳只在 setAlternateIconName 的完成回调成功后才写）；
# 这里再核对先后顺序：先换成品牌色，再换回默认。

echo >>"$summary"
echo "### iOS 可替换图标端到端（设置页点选 + 系统提示）" >>"$summary"
events=appicon-events.jsonl
xlog="$out/xcodebuild-appicon.log"
xcrun simctl terminate "$udid" "$bundle" 2>/dev/null || true
rm -f "$log"
TEST_RUNNER_E2E_SYNC="$sync" TEST_RUNNER_E2E_URL="$origin/settings/appearance" TEST_RUNNER_E2E_LOG="$log" \
  xcodebuild test-without-building "${xcb[@]}" -only-testing:OrynE2E/AppIconE2ETests -parallel-testing-enabled NO \
  -resultBundlePath "$out/appicon.xcresult" >"$xlog" 2>&1 &
runner=$!
finish_runner
ids=$(jq -r 'select(.event == "appicon.set") | .id' "$out/$events" | paste -sd, -)
echo "- appicon.set 顺序：${ids:-（无）}（期望 accent,default）" | tee -a "$summary"
if [[ $ids != "accent,default" ]]; then
  echo "::error::可替换图标没有按 品牌色 → 默认 切换：${ids:-（无）}"
  failed=1
fi

# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
