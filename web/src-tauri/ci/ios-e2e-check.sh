#!/usr/bin/env bash
# jq 程序里的 $w / $k / $s / $o 是 jq 变量（单引号是故意的）；谓词函数与 cleanup 经 until_ok / trap 间接调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# iOS 模拟器里原生底栏、原生页头与可替换图标的端到端检查（mobile.yml 的 ios-check，硬门禁）：真实网页、真实宿主权限、真实触摸。
# 与 ios-header-check.sh 的原生页头演示互补：演示绕过网页直接调插件，这里走生产路径的每一环——
#   - mock Web UI（VITE_MOCK=1 的构建）由本机 vite preview 提供在 http://localhost:<端口>；
#   - debug 包读 ORYN_E2E_URL（mobile.rs 的 e2e_open，仅 debug 构建），跳过配对，经与生产相同的 open_remote 打开它：
#     远程能力与 MOBILE_HOST_PERMISSIONS 照常注入，页面的 host_ready / host_sink_* / host_tab_bar_* / host_header_* 都过权限校验；
#   - 页面认出 iOS 壳（壳注入的 window.__ORYN_HOST__，bridge ≥ 8）并进入 compact 下沉侧栏布局：先 host_sink_set(true) 让原生跟随
#     WKWebView 的根滚动，之后原生底栏（系统 UITabBar，浮在 WebView 之上、右缘随根滚动裁到卡片左缘；被网页内容盖住时隐藏、
#     网页胶囊顶上）与原生页头（线程页三件；首页只有侧栏钮，key = home）才接管（壳在 sink 接管前拒绝非空的 set）；
#   - 卡片开合 = 根滚动视图的横向滚动（<html data-sink-native>，scrollX 最大 = 关闭、0 = 打开）：手指拖动与松手吸附是系统滚动，
#     按钮 / 卡片条 / 再点已选分区这类程序化开合经 host_sink_show 由原生逐帧推进；原生页头与底栏在同一进程里按 contentOffset
#     同帧跟随，网页不再发卡片位置，开合期间也不发 header.show；
#   - 触摸由 XCUITest 合成（ios-e2e/，XcodeGen 生成工程）：经模拟器 HID 与 UIKit 命中测试，网页、原生底栏与原生页头收到的都是真触摸。
# 三个 UI 测试先后各跑一次 xcodebuild（-only-testing），测试与本脚本经共享目录交接（见 E2ESupport.swift）。
# 开合到位由测试的无障碍断言先行（卡片条、页头侧栏按钮、原生侧栏钮的矩形，±1pt），脚本确认事件、连拍到停稳再比像素。
# 底栏（TabBarE2ETests.swift，从首页 / 起步）：
#   loaded   页面就绪：sink.set（on）早于第一条 tabbar.set；首页的原生侧栏钮接管（header.set key = home、header.show alpha 1），
#            截关闭态基线，回传胶囊矩形（来自事件日志 tabbar.set）
#   open     点侧栏钮（此时网页钮已对读屏隐藏，点到的是原生钮）：header.event（op = lead、key = home），sink.show（open）=
#            原生推进，到位后不回弹（no_bounce），截图（卡片条左缘到侧栏宽、原生按钮在、网页胶囊隐藏在测试里断言）
#   closed   点露出的卡片条：sink.show（关），到位后不回弹，截图
#   drag     卡片左缘内起手拖到 0.55 × 侧栏宽并按住：拖动是系统滚动，事件日志里没有信号（也不该有 sink.show）；就绪看截图——
#            胶囊横条连拍两张一致（卡片停住）、与关闭基线不同、又不是打开态（没松手）
#   projects 点原生底栏的「项目」：等 tabbar.tap 与网页回发的 tabbar.set（selected = projects），截图（侧栏保持打开在测试里断言）
#   swipe    在原生底栏上按住「项目」横滑到第 1 项再松手（系统透镜换选中）：等 tabbar.tap 与 tabbar.set（selected = threads），截图
#   final    再点已选中的第 1 项收起侧栏（侧栏是 drag 拖开的）：sink.show（关），到位后不回弹，截图
# 页头（HeaderE2ETests.swift，直接打开 mock 的线程页 /t/<MAIN_THREAD>）：
#   h-loaded  sink.set（on）早于第一条 tabbar.set 与 header.set；header.set（key = 线程 id）与 header.show（alpha 1）：
#             原生页头接管，回传三件矩形与侧栏宽
#   h-native  原生三件接管、网页三件隐藏（无障碍树断言在测试里），截图
#   h-menu    点原生标题胶囊：等 header.event（menu = title），截图
#   h-sub     点「Orb」行进子页：此时不应有任何 select，截图
#   h-back    点返回行回主页，截图
#   h-dialog  点「改名」：等 header.event（select / title / rename），网页打开对话框、遮挡探测让原生让位（header.show alpha 0），截图
#   h-return  点遮罩关掉对话框：等 header.show（alpha 1），截图；从这里起数 header.show
#   h-sidebar 点原生侧栏钮：等 header.event（lead）与 sink.show（open）；原生页头的位移由原生从根滚动算，没有新的 header.show，截图
#   h-closed  点卡片条关侧栏：sink.show（关），仍没有新的 header.show，截图
#   h-drag    卡片左缘内起手按住拖到 0.55 × 侧栏宽：页头行连拍两张一致、与关闭态不同、又不是打开态；像素断言原生页头行与
#             卡片内容停在同一位移（揭示边左侧是不动的侧栏）
#   h-end     松手吸附到打开、点卡片条关上之后：sink.show（关），自 h-return 起仍没有新的 header.show，截图
#   h-keyboard 点输入框、系统软键盘弹出后（测试确认键盘在无障碍树里）：网页探针的视口采样（sink-web.jsonl 的 viewport）里
#             最后一条是页面没被推走——视觉视口 offsetTop、scrollY 都回到 0，scrollX 与聚焦时一样（侧栏仍关着），
#             键盘占高 > 100pt，输入框底边在键盘上沿之上（lib/viewport.ts：--kb-inset 让位 + settleViewport 拉回），截图
# 可替换图标（AppIconE2ETests.swift，直接打开 mock 的 /settings/appearance）：点「品牌色」、点掉系统提示、
#   等 appicon.set（accent），再点「黑白」同样等 appicon.set（default）；测试自己完成，脚本只核对事件顺序。
# 像素断言（逐区域比较，同 ios-region-diff.swift）全部打印并写进 Step Summary，任一不符则退出码 1；
# 全部断言跑完且通过才写 <输出目录>/.complete。
# 事件日志里的 sink.show / tabbar.cover / header.show 只有 debug 包会写（mobile.rs）。就绪靠事件日志与测试的无障碍断言
# （按住拖动靠连拍截图），不靠固定 sleep。
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

# mock Web UI 仍由 Vite 预览；探针把网页滚动、焦点与触摸记到单独的制品，和 sink.trace 按 ts 对齐。
(cd "$web" && exec bun "$here/ios-e2e-preview.mjs" "$dist" "$port" "$out/sink-web.jsonl") >"$out/preview.log" 2>&1 &
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

# 页头测试的 h-keyboard 要系统软键盘真的弹出：模拟器连着 Mac 的硬件键盘时点输入框不出软键盘，开机前断开
defaults write com.apple.iphonesimulator ConnectHardwareKeyboard -bool false
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

# 最后一条某事件满足 jq 条件（$k = 页头测试打开的线程 id）
last_is() {
  last "$1" | jq -e --arg k "${thread:-}" "$2"
}
# web_ready <最低 bridge>：host_ready 过了远程能力的权限校验才会记 web.ready；bridge 8 = 卡片开合是根滚动、原生按 contentOffset 跟随
web_ready() { ev web.ready | jq -se --arg o "$origin" --argjson b "$1" 'any(.origin == $o and .bridge >= $b)'; }
# sink_first <事件>：第一条 sink.set（on）在事件日志里早于第一条该事件——原生底栏 / 页头只在根滚动跟随接管后才 set
# （旧网页连新壳时非空的 set 在这一步被拒，退回网页页头 / 底栏）
sink_first() {
  jq -se --arg e "$1" '
    (to_entries | map(select(.value.event == "sink.set" and .value.on == true)) | first | .key) as $s
    | (to_entries | map(select(.value.event == $e)) | first | .key) as $t
    | $s != null and $t != null and $s < $t' "$log"
}
# sink_shown <true|false>：自上一步放行（$mark = 那时 sink.show 的条数）起有程序化开合请求、最后一条的 open 是给定值。
# 按钮、卡片条、再点已选分区都经 host_sink_show 交给原生逐帧推进；手指拖动与松手吸附是系统滚动，不产生 sink.show
sink_shown() { ev sink.show | tail -n "+$((mark + 1))" | jq -se --argjson o "$1" 'length > 0 and (last | .open == $o)'; }
# 自上一步放行（$lines = 那时事件日志的行数）起的 sink.trace，单行 JSON 数组
step_traces() { tail -n "+$((lines + 1))" "$log" | jq -sc 'map(select(.event == "sink.trace"))'; }
# 到位：原生动画走完（done）、一步落位（jump，含网页截止兜底：CI 上网页命令到原生常晚几百毫秒，兜底会把还在走的动画
# 截住一步落完）、或本来就在终点（noop）
arrived='map(.op == "done" or .op == "jump" or .op == "noop") | index(true)'
trace_arrived() { step_traces | jq -e "($arrived) != null"; }
# 底栏可先于 WebKit 提交第一帧；启动基线要等原生实际观察到网页根滚动抵达关闭位。
initial_closed() {
  last sink.trace | jq -se --argjson x "$sink_w" '
    (last // {}) | (.op == "move" or .op == "jump" or .op == "done" or .op == "noop")
    and .to >= ($x - 0.5) and .to <= ($x + 0.5)'
}
# no_bounce <步骤>：程序化开合第一次到位后，等过网页的截止兜底窗口（请求后 SINK_REQUEST_MS 一步落位、
# 再一个 SINK_REQUEST_MS 宽限，CI 上定时器还会晚几百毫秒）。拒绝从别处发起的 jump / start，也拒绝从终点
# 离开超过 1pt 的 move：网页可直接用 scrollTo 拉回，不一定再发命令；起点允许 0.5pt 的浮点和像素舍入误差。
bounce_traces() {
  jq -c "
    def absdiff: if . < 0 then -. else . end;
    ($arrived) as \$d
    | .[\$d].to as \$target
    | .[\$d + 1:]
    | map(select(
        ((.op == \"jump\" or .op == \"start\") and ((.x - .to) | absdiff) > 0.5)
        or (.op == \"move\" and ((.x - \$target) | absdiff) <= 0.5
            and ((.to - \$target) | absdiff) > 1)))"
}
no_bounce() {
  local step=$1 bad
  until_ok "$step" 15 "程序化开合到位（sink.trace done / jump / noop）" trace_arrived
  sleep 4
  bad=$(step_traces | bounce_traces)
  [[ $bad == "[]" ]] ||
    fail "$step" "开合到位后卡片又离开终点（回弹）：${bad}；本步轨迹：$(step_traces)"
}

# pt → 截图像素（scale 在 loaded 步骤由基线截图算出）
px() { awk -v v="$1" -v s="$scale" 'BEGIN { printf "%d", v * s + 0.5 }'; }
calc() { awk "BEGIN { print $1 }"; }
# rdiff <图 a> <图 b> <x> <y> <宽> <高>（pt）[b 的 x（pt）]：两张图（完整路径）区域的不同像素占比
rdiff() { "$work/region-diff" "$1" "$2" "$(px "$3")" "$(px "$4")" "$(px "$5")" "$(px "$6")" ${7:+"$(px "$7")"}; }
# strip_diff <图 a> <图 b> <x（pt）> <宽（pt）>：胶囊所在横条里同位置区域的不同像素占比
strip_diff() { rdiff "$1" "$2" "$3" "$fy" "$4" "$fh"; }
# at_most <比值> <阈值>
at_most() { awk -v r="$1" -v t="$2" 'BEGIN { exit !(r <= t) }'; }

# 限时截图（见 ios-sim.sh 的 sim_shot）；普通步骤截不到图直接判失败
shot() { sim_shot "$out/e2e-$1.png" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"; }
# steady_shot <步骤>：程序化开合由原生逐帧推进、系统吸附也没有「停稳」事件；连拍到胶囊横条前后两张一致（≤ 0.01）才收下后一张。
# 没停稳时把最后比较的一对图存成 e2e-<步骤>-steady-{a,b}.png，报错带比较次数、最后的差异率与耗时（区分画面在动还是模拟器慢）
steady_shot() {
  local prev="$work/steady-prev.png" next="$work/steady-next.png" older="$work/steady-older.png"
  local start=$SECONDS end=$((SECONDS + 120)) n=0 ratio=""
  sim_shot "$prev" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"
  if [[ -z ${scale:-} ]]; then
    local pw
    pw=$(sips -g pixelWidth "$prev" | awk '/pixelWidth/ { print $2 }')
    scale=$(jq -r --argjson pw "$pw" '$pw / .width' <<<"$req")
  fi
  while ((SECONDS < end)); do
    sim_shot "$next" 60 || fail "$1" "截图 60 秒没有返回（模拟器卡住）"
    ratio=$(strip_diff "$next" "$prev" "$fx" "$fw") || fail "$1" "比较连拍截图失败（ios-region-diff 退出码非 0）"
    n=$((n + 1))
    if at_most "$ratio" 0.01; then
      mv "$next" "$out/e2e-$1.png"
      return 0
    fi
    mv "$prev" "$older"
    mv "$next" "$prev"
  done
  if ((n > 0)); then
    cp "$older" "$out/e2e-$1-steady-a.png"
    cp "$prev" "$out/e2e-$1-steady-b.png"
  fi
  fail "$1" "120 秒内底栏横条没有停稳（连拍两张总是不同）：比较 ${n} 次，最后一次差异率 ${ratio:-无}，耗时 $((SECONDS - start)) 秒"
}

# hold <步骤> <截图名> <x> <y> <宽> <高>（pt）<关闭基线> <打开基线>：测试正按住拖动，连拍到区域停在拖开处——前后两张一致
# （≤ 0.01，卡片停住了）、与关闭基线不同（确实拖开了）、又不是打开基线（打开 = 已松手吸附）——存成 e2e-<截图名>.png，返回 0。
# 截图 40 秒没返回（与按住时长相同）或已经松手吸附到打开：返回 1，让测试收起后重拖。
# 拖动与吸附都是系统滚动，按住期间出现 sink.show（网页的程序化开合）直接判失败。
hold() {
  local step=$1 name=$2 x=$3 y=$4 w=$5 h=$6 closed=$7 opened=$8 n
  local next="$work/hold-next.png" prev="" end=$((SECONDS + 180))
  n=$(count sink.show)
  while ((SECONDS < end)); do
    sim_shot "$next" 40 || return 1
    [[ $(count sink.show) == "$n" ]] ||
      fail "$step" "按住拖动期间出现了程序化开合（拖动应是系统滚动）：$(last sink.show)"
    if at_most "$(rdiff "$next" "$out/e2e-$opened.png" "$x" "$y" "$w" "$h")" 0.01; then return 1; fi
    if [[ -n $prev ]] &&
      at_most "$(rdiff "$next" "$prev" "$x" "$y" "$w" "$h")" 0.01 &&
      ! at_most "$(rdiff "$next" "$out/e2e-$closed.png" "$x" "$y" "$w" "$h")" 0.01; then
      mv "$next" "$out/e2e-$name.png"
      return 0
    fi
    mv "$next" "$work/hold-prev.png"
    prev="$work/hold-prev.png"
  done
  fail "$step" "180 秒内没有在拖开处停稳（一直停在关闭位 = 拖不动卡片），也没有松手吸附到打开"
}

# ok <步骤> [JSON]：放行测试的这一步；先记下此刻 sink.show 的条数（下一步的 sink_shown 从这里数起）
ok() {
  local body='{}'
  if (($# > 1)); then body=$2; fi
  mark=$(count sink.show)
  lines=$(cat "$log" 2>/dev/null | wc -l | tr -d ' ')
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
mark=0
lines=0
wait_req loaded 420
until_ok loaded 30 "e2e.open" last_is e2e.open '.'
last_is e2e.open '.ok' >/dev/null || fail loaded "ORYN_E2E_URL 没有打开：$(last e2e.open)"
until_ok loaded 60 "web.ready（${origin}，bridge ≥ 8）" web_ready 8
until_ok loaded 60 "sink.set（on：原生跟随根滚动）" last_is sink.set '.on'
until_ok loaded 60 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
sink_first tabbar.set >/dev/null ||
  fail loaded "sink.set（on）应早于第一条 tabbar.set：$(ev sink.set | head -1) / $(ev tabbar.set | head -1)"
frame=$(last tabbar.set | jq -c .frame)
read -r fx fy fw fh < <(jq -r '[.x, .y, .width, .height] | @tsv' <<<"$frame")
sink_w=$(awk -v x="$fx" -v w="$fw" 'BEGIN { print 2 * x + w }')
until_ok loaded 120 "根滚动初始落在关闭位（${sink_w}pt）" initial_closed
# 首页左上角的侧栏钮由原生页头接管（只有侧栏钮的 spec，key = home）；显示稳了网页钮才对读屏隐藏，测试点到的就是原生钮
until_ok loaded 30 "header.set（首页原生侧栏钮接管，key = home）" last_is header.set '.on and .key == "home"'
until_ok loaded 30 "header.show（alpha 1：首页原生侧栏钮显示）" last_is header.show '.alpha == 1'
steady_shot closed0
echo "胶囊：${frame}，侧栏宽 ${sink_w}，缩放 ${scale}"
ok loaded "{\"frame\":$frame}"

wait_req open 120
# 测试在第一下轻点没效果（卡片没动）时会再点一次并回传 retry：照常断言，但在日志与 Step Summary 里留痕
if jq -e '.retry == true' <<<"$req" >/dev/null; then
  echo "::warning::打开：第一下轻点没有效果，测试重点了一次（点击前按钮矩形 $(jq -c '[.first, .second]' <<<"$req")）"
  echo "- ⚠️ open：第一下轻点没有效果，重点一次" >>"$summary"
fi
# 网页回退也能打开侧栏，只凭「侧栏打开」不能说明原生接管：要看到原生侧栏钮自己上报的点击
until_ok open 15 "原生侧栏钮上报 header.event（op = lead、key = home）" \
  last_is header.event '.op == "lead" and .key == "home"'
until_ok open 15 "点侧栏按钮打开：sink.show（open）交给原生推进" sink_shown true
no_bounce open
steady_shot open
ok open

wait_req closed 120
until_ok closed 15 "点卡片条关闭：sink.show（关）交给原生推进" sink_shown false
no_bounce closed
steady_shot closed1
ok closed

# 拖动：手指拖动是根滚动视图自己的滚动，按住期间事件日志里没有信号；就绪看截图（见 hold）。
# 卡片跟手 1:1，但 UIScrollView 起拖有一段滞后（M20 探针：手指 176pt 时卡片左缘 166pt），所以揭示边不取手指位移本身：
# 像素断言在手指位移的 [−18, +2]pt 内搜卡片的实际平移，左侧区域按最靠左的可能位置再让出投影。
# 按住 40 秒：判定用的两张截图都要落在按住期间（松手后吸附到打开，截图与打开态一致即判没赶上）。
# CI 的 macOS 机器会整机卡顿几十秒到几分钟（截图曾卡 3 分钟），没赶上就让测试收起侧栏重拖，最多 3 次；
# 每次的步骤名带序号（drag1 / drag2 / drag3），交接文件不会与上一次混淆。
# 测试先发 req 再起手，XCUITest 起手前可能还要等 App 空闲，所以等 req 放宽
for attempt in 1 2 3; do
  step="drag$attempt"
  wait_req "$step" 300
  edge=$(jq -r .dx <<<"$req")
  if hold "$step" drag "$fx" "$fy" "$fw" "$fh" closed0 open; then
    echo "拖动：手指位移 ${edge}（第 ${attempt} 次）"
    ok "$step"
    break
  fi
  ((attempt < 3)) || fail "$step" "连续 3 次都没能在按住期间连拍到停稳的两张图"
  echo "::warning::第 ${attempt} 次拖动没赶在按住期间截图，收起后重拖"
  ok "$step" '{"retry":true}'
done

# 原生点击 → Rust 记 tabbar.tap 并转给页面 → 页面切分区（路由）→ 页面按新路由回发 spec（selected = projects）；
# 侧栏保持打开（卡片条仍在侧栏宽处）在测试里断言
wait_req projects 120
until_ok projects 15 "原生点击上报 tabbar.tap（projects）" last_is tabbar.tap '.id == "projects"'
until_ok projects 15 "网页切到「项目」后回发 tabbar.set（selected = projects）" \
  last_is tabbar.set '.on and .selected == "projects"'
steady_shot projects
ok projects

# 系统透镜的横滑选中：按住已选中的「项目」横滑到第 1 项（threads）松手 → UITabBar 换选中 → 同一条 tabbar.tap 路径
wait_req swipe 120
until_ok swipe 15 "横滑松手后上报 tabbar.tap（threads）" last_is tabbar.tap '.id == "threads"'
until_ok swipe 15 "网页切到第 1 项后回发 tabbar.set（selected = threads）" \
  last_is tabbar.set '.on and .selected == "threads"'
steady_shot swipe
ok swipe

wait_req final 120
until_ok final 15 "再点已选中的第 1 项收起：sink.show（关）交给原生推进" sink_shown false
no_bounce final
steady_shot final
ok final

finish_runner

# ---------------------------------------------------------------- 像素断言

failed=0
moved=0
# hcheck <说明> <截图 a> <截图 b> <x> <y> <宽> <高>（pt）<shown|same> [b 的区域 x（pt）] [搜索半径（pt，缺省 2）]
# 比值 = 不同像素的占比；shown：≥ 0.01，same：≤ 0.01。给了 b 的 x 时比较横向平移过的内容（卡片、随卡片走的原生页头）：
# 在 b 的 x ± 半径内逐像素取最小值（吸收卡片实际平移与给定值之差及换算到像素的舍入），取到最小值处 a 相对 b 的平移（pt）记进 $moved。
hcheck() {
  local label=$1 a=$2 b=$3 x=$4 y=$5 w=$6 h=$7 want=$8 bx=${9:-$4} r best="" at=0 d ok reach=0
  if [[ -n ${9:-} ]]; then reach=$(px "${10:-2}"); fi
  for ((d = -reach; d <= reach; d++)); do
    r=$("$work/region-diff" "$out/e2e-$a.png" "$out/e2e-$b.png" \
      "$(px "$x")" "$(px "$y")" "$(px "$w")" "$(px "$h")" "$(($(px "$bx") + d))")
    if [[ -z $best ]] || awk -v r="$r" -v b="$best" 'BEGIN { exit !(r < b) }'; then
      best=$r
      at=$d
    fi
  done
  moved=$(awk -v a="$(px "$x")" -v b="$(($(px "$bx") + at))" -v s="$scale" 'BEGIN { printf "%.1f", (a - b) / s }')
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
# check <说明> <截图 a> <截图 b> <区域 x（pt）> <区域宽（pt）> <shown|same> [b 的区域 x（pt）] [搜索半径（pt）]：
# 区域竖直范围是胶囊所在的横条，其余同 hcheck
check() { hcheck "$1" "$2" "$3" "$4" "$fy" "$5" "$fh" "${@:6}"; }

# 打开：胶囊处不再是卡片（与关闭基线不同）；关闭：回到关闭基线（卡片盖回来，原生底栏裁到 0）
check "打开：胶囊处与关闭基线不同" open closed0 "$fx" "$fw" shown
check "关闭：胶囊处回到关闭基线" closed1 closed0 "$fx" "$fw" same
# 拖动：卡片的实际左缘在手指位移的 [−18, +2]pt 内（起拖滞后，见上面拖动步骤）；左侧区域取最靠左的可能位置 lo 再让出 72pt
# 避开卡片投影（--sh-xl）：与打开态一致（侧栏层不动，原生底栏裁到卡片左缘为止）
lo=$(calc "$edge - 18")
check "拖动：揭示边左侧与打开态一致" drag open "$fx" "$(calc "$lo - 72 - $fx")" same
check "拖动：揭示边左侧与关闭基线不同" drag closed0 "$fx" "$(calc "$lo - 72 - $fx")" shown
# 拖动：揭示边右侧是整体右移的卡片，与关闭基线错位比较一致 = 原生底栏的裁切跟上了卡片、没有画到卡片上。区域从手指位移 + 8pt 起
# （离实际左缘至少 6pt，避开发丝线与卡片左下圆角），b 的 x 以平移 edge − 8 为中心、搜 ±10pt
check "拖动：揭示边右侧是平移的卡片（底栏没透到卡片上）" drag closed0 \
  "$(calc "$edge + 8")" "$(calc "$fx + $fw - $edge - 8")" same 16 10
echo "- 拖动：卡片平移 ${moved}pt（手指位移 ${edge}pt，差值是 UIScrollView 的起拖滞后）" | tee -a "$summary"

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

# sink.set 先于原生部件接管；header.set 在 Swift 挂好之后才记，key 是线程 id
wait_req h-loaded 420
until_ok h-loaded 30 "e2e.open" last_is e2e.open '.'
last_is e2e.open '.ok' >/dev/null || fail h-loaded "ORYN_E2E_URL 没有打开：$(last e2e.open)"
until_ok h-loaded 60 "web.ready（${origin}，bridge ≥ 8）" web_ready 8
until_ok h-loaded 30 "sink.set（on：原生跟随根滚动）" last_is sink.set '.on'
until_ok h-loaded 30 "header.set（原生页头接管 ${thread}）" last_is header.set '.on and .key == $k'
until_ok h-loaded 30 "header.show（alpha 1：原生页头显示）" last_is header.show '.alpha == 1'
hset=$(last header.set)
jq -e '.trail != null' <<<"$hset" >/dev/null || fail h-loaded "线程页没有视图钮（header.set 的 trail 为 null）：${hset}"
read -r lx _ _ _ < <(jq -r '.lead | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r tx ty tw th < <(jq -r '.title | [.x, .y, .width, .height] | @tsv' <<<"$hset")
read -r rx _ rw _ < <(jq -r '.trail | [.x, .y, .width, .height] | @tsv' <<<"$hset")
# 页头横条：侧栏钮左缘到视图钮右缘、胶囊的高度
rowx=$lx
roww=$(calc "$rx + $rw - $lx")
# 侧栏宽：根滚动的可滚范围 = 打开位的卡片平移（线程页同样在下沉侧栏布局里）
until_ok h-loaded 30 "tabbar.set（原生底栏接管）" last_is tabbar.set '.on and .frame != null'
sink_w=$(last tabbar.set | jq -r '.frame | 2 * .x + .width')
for e in tabbar.set header.set; do
  sink_first "$e" >/dev/null ||
    fail h-loaded "sink.set（on）应早于第一条 ${e}：$(ev sink.set | head -1) / $(ev "$e" | head -1)"
done
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
# 之后的开合（按钮、卡片条、按住拖动）期间原生页头的位移全由原生从根滚动算，网页不再发 header.show：从这里起数
shows=$(count header.show)
sleep 0.7
shot h-return
ok h-return

# fresh <步骤>：自 h-return 起没有新的 header.show（R2：开合期间网页不发页头的位移，也不让位）
fresh() {
  [[ $(count header.show) == "$shows" ]] ||
    fail "$1" "开合期间网页不该发 header.show：$(ev header.show | tail -n "+$((shows + 1))" | tr '\n' ' ')"
}

# 原生侧栏钮 → header.event lead → 页面开侧栏 → host_sink_show 交给原生推进；原生页头随根滚动右移侧栏宽、不让位
# （右移后的无障碍矩形在测试里断言）
wait_req h-sidebar 60
until_ok h-sidebar 15 "header.event（lead）" last_is header.event '.op == "lead" and .key == $k'
until_ok h-sidebar 15 "开侧栏：sink.show（open）交给原生推进" sink_shown true
fresh h-sidebar
sleep 0.5
shot h-sidebar
ok h-sidebar

# 点卡片条关侧栏 → host_sink_show（关）→ 原生页头随根滚动回到原位
wait_req h-closed 60
until_ok h-closed 15 "关侧栏：sink.show（关）交给原生推进" sink_shown false
fresh h-closed
sleep 0.7
shot h-closed
ok h-closed

# 按住拖动：同底栏的拖动步骤（hold、起拖滞后、最多重拖 3 次），停稳看页头横条，关闭 / 打开基线取 h-closed / h-sidebar
for attempt in 1 2 3; do
  step="h-drag$attempt"
  wait_req "$step" 300
  hedge=$(jq -r .dx <<<"$req")
  if hold "$step" h-drag "$rowx" "$ty" "$roww" "$th" h-closed h-sidebar; then
    fresh "$step"
    echo "页头拖动：手指位移 ${hedge}（第 ${attempt} 次）"
    ok "$step"
    break
  fi
  ((attempt < 3)) || fail "$step" "连续 3 次都没能在按住期间连拍到停稳的两张图"
  echo "::warning::第 ${attempt} 次页头拖动没赶在按住期间截图，收起后重拖"
  ok "$step" '{"retry":true}'
done

# 松手过半吸附到打开（系统滚动，无 sink.show）、点卡片条关上（sink.show 关）；整段没有新的 header.show
wait_req h-end 60
until_ok h-end 15 "拖动后点卡片条关侧栏：sink.show（关）交给原生推进" sink_shown false
fresh h-end
sleep 0.7
shot h-end
# 软键盘步骤只看这之后的网页探针采样
wlog="$out/sink-web.jsonl"
wmark=$(wc -l <"$wlog" | tr -d ' ')
ok h-end

# 自 h-end 放行起网页探针的视口采样（beacon 到达顺序不保证，按 seq 排）
kb_samples() {
  tail -n "+$((wmark + 1))" "$wlog" | jq -sc 'map(select(.event == "viewport")) | sort_by(.seq) | map(.info)'
}
# 聚焦输入框那一刻（键盘弹出之前）的 scrollX：侧栏关闭位
kb_base() { kb_samples | jq -e 'map(select(.why == "focusin" and .active == "textarea")) | first | .sx'; }
# 键盘弹出并稳定后：最后一条采样里页面没被推走、横向没动、输入框在键盘上沿之上
kb_settled() {
  kb_samples | jq -e --argjson x0 "$kb_x0" '
    def mag: if . < 0 then -. else . end;
    last | . != null and .active == "textarea" and .scale == 1 and (.ih - .h) > 100
      and (.top | mag) < 1 and (.sy | mag) < 1 and ((.sx - $x0) | mag) < 1 and .bottom <= .h + 1'
}
wait_req h-keyboard 120
until_ok h-keyboard 15 "聚焦输入框时的视口采样（focusin）" kb_base
kb_x0=$(kb_base)
kb_end=$((SECONDS + 20))
until kb_settled >/dev/null 2>&1; do
  ((SECONDS < kb_end)) || fail h-keyboard \
    "软键盘弹出后页面被推走或输入框被键盘挡住（期望 offsetTop、scrollY 为 0，scrollX = ${kb_x0}，输入框底边 ≤ 视觉视口高）：$(kb_samples)"
  sleep 0.25
done
echo "- 软键盘：$(kb_samples | jq -c 'last')" | tee -a "$summary"
shot h-keyboard
ok h-keyboard

finish_runner

# 面板区：胶囊下方 14pt 起 200pt 高，左右各让出 8pt
pnx=$(calc "$tx + 8")
pny=$(calc "$ty + $th + 14")
pnw=$(calc "$tw - 16")
hcheck "页头：点胶囊后面板展开" h-menu h-native "$pnx" "$pny" "$pnw" 200 shown
hcheck "页头：子页与主页不同" h-sub h-menu "$pnx" "$pny" "$pnw" 200 shown
hcheck "页头：返回后回到主页" h-back h-menu "$pnx" "$pny" "$pnw" 200 same
hcheck "页头：对话框关掉后原生页头原样回来" h-return h-native "$rowx" "$ty" "$roww" "$th" same
hcheck "页头：关侧栏后原生页头原样回来" h-closed h-native "$rowx" "$ty" "$roww" "$th" same
hcheck "页头：拖动开合后原生页头原样回来" h-end h-native "$rowx" "$ty" "$roww" "$th" same
# 按住拖动（R1）：揭示边左侧是不动的侧栏（与打开态一致，区域取法同底栏的拖动）；揭示边右侧的页头横条与卡片内容
# （面板区那一带的转录）都是平移过的关闭态（h-closed），各自搜出平移量——两者一致 = 原生页头与卡片停在同一位移
hlo=$(calc "$hedge - 18")
hcheck "页头拖动：揭示边左侧与打开态一致（侧栏不动）" h-drag h-sidebar "$rowx" "$ty" "$(calc "$hlo - 72 - $rowx")" "$th" same
hcheck "页头拖动：揭示边右侧的原生页头行是平移的关闭态" h-drag h-closed \
  "$(calc "$hedge + 8")" "$ty" "$(calc "$rowx + $roww - $hedge - 8")" "$th" same 16 10
head_dx=$moved
hcheck "页头拖动：揭示边右侧的卡片内容是平移的关闭态" h-drag h-closed \
  "$(calc "$hedge + 8")" "$pny" "$(calc "$rowx + $roww - $hedge - 8")" 200 same 16 10
card_dx=$moved
echo "- 页头拖动：原生页头平移 ${head_dx}pt、卡片平移 ${card_dx}pt（手指位移 ${hedge}pt，期望两者相差 ≤ 1pt）" | tee -a "$summary"
if ! awk -v a="$head_dx" -v b="$card_dx" 'BEGIN { d = a - b; exit !(d <= 1 && d >= -1) }'; then
  echo "::error::按住拖动时原生页头（${head_dx}pt）没有与卡片（${card_dx}pt）停在同一位移"
  failed=1
fi

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
else
  echo "- [OK] 可替换图标：品牌色 → 默认" >>"$summary"
fi

# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
