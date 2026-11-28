#!/usr/bin/env bash
# jq 程序里的 $k 是 jq 变量（单引号是故意的）；就绪谓词经 "$ready" 间接调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# README 手机三联与 iPhone Duo 双拼的 iOS 原图（readme-ios-shots 工作流）：iOS 26+ 模拟器里的真实 App 打开带演示数据的
# mock Web UI，原生液态玻璃底栏与页头由 UIKit 绘制，明暗各一套：
#   ios-sidebar-<明暗>.png  线程页打开下沉侧栏（原生底栏跟着卡片揭开）
#   ios-thread-<明暗>.png   线程页（原生页头接管）
#   ios-home-<明暗>.png     新建线程页（原生侧栏钮）
#   duo-outer-<明暗>.png    iPhone Duo 合上：外屏线程页（竖栏：开关侧栏、视图钮与分区底栏竖排在系统给的一侧，页头由网页画）
#   duo-inner-<明暗>.png    同一会话里展开、内屏横放：列侧栏与对话并排，页头操作进竖栏
# Duo 的就绪看壳的 rail 事件与网页探针（ios-e2e-preview.mjs 记到 <输出目录>/web.jsonl）：竖栏在时原生页头刻意不接管，
# 等 header.* 会超时；普通 iPhone 仍看原生页头 / 底栏 / 根滚动的事件。
# Duo 的折叠、转屏与截图用 duoctl（见 ios-sim.sh 的 duo_setup）。
# 演示数据与 web/scripts/readme-shots.ts 同一份：`--emit-ios-init` 生成注入脚本，插进 mock 构建的 index.html 最前面。
# debug 包读 ORYN_E2E_URL（mobile.rs 的 e2e_open）跳过配对直接打开；就绪靠事件日志（web.ready / header.* / tabbar.set / sink.* / rail）
# 与网页探针判断。
# 合成到 docs/assets/{mobile,duo}-views-*.png 在本机做：ORYN_IOS_SHOTS=<产物目录> bun run shots:readme --only mobile-views,duo-views。
# 用法：ios-readme-shots.sh <Oryn.app> <mock Web UI 构建目录> <输出目录>
# ORYN_SHOTS_ONLY=duo 只截 Duo 的四张：不选、不启动普通 iPhone，也不往它上面装 App（普通 iPhone 那段卡住时，
# Duo 的原图不必跟着拿不到）。默认（空或 all）全截。
set -euo pipefail
if (($# != 3)); then
  echo "用法：$0 <Oryn.app> <mock Web UI 构建目录> <输出目录>" >&2
  exit 2
fi
only=${ORYN_SHOTS_ONLY:-all}
if [[ $only != all && $only != duo ]]; then
  echo "ORYN_SHOTS_ONLY 只能是 all 或 duo，收到 ${only}" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
web=$(cd "$here/../.." && pwd)
dist=$(cd "$2" && pwd)
port=47614
origin="http://localhost:$port"

# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"
if [[ $only == duo ]]; then
  sim_prepare "$1" "$3" "README 截图（iOS 模拟器，只截 Duo）"
else
  sim_setup "$1" "$3" "README 截图（iOS 模拟器）"
fi
out=$(cd "$out" && pwd)
server=""
duo_udid=""
# 收尾：sim_drop（ios-sim.sh）限时关掉并删除 Duo 模拟器，前后打标记，清理卡住也不拖住整步
trap 'if [[ -n $server ]]; then kill "$server" 2>/dev/null || true; fi
if [[ -n $duo_udid ]]; then sim_drop "$duo_udid"; fi
rm -rf "$work"' EXIT

# 演示数据脚本在截图前一刻生成（线程的「x 分钟前」从这时算起），插在所有脚本之前
site="$work/site"
cp -R "$dist" "$site"
(cd "$web" && bun scripts/readme-shots.ts --emit-ios-init "$site/readme-demo.js")
perl -0pi -e 's#<head>#<head><script src="/readme-demo.js"></script>#' "$site/index.html"
grep -qF 'readme-demo.js' "$site/index.html" || {
  echo "::error::没能把演示数据脚本插进 index.html"
  exit 1
}

# 预览服务器带网页探针（与 ios-duo-check.sh 同一个）：Duo 的就绪要看网页竖栏
wlog="$out/web.jsonl"
(cd "$web" && exec bun "$here/ios-e2e-preview.mjs" "$site" "$port" "$wlog") >"$out/preview.log" 2>&1 &
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

# 状态栏固定成发布图的样子（discharging + 100%：满电且不带充电闪电）
pin_status_bar() {
  sim_run 60 xcrun simctl status_bar "$udid" override --time 9:41 --dataNetwork wifi --wifiMode active --wifiBars 3 \
    --cellularMode active --cellularBars 4 --batteryState discharging --batteryLevel 100
}
if [[ $only == all ]]; then pin_status_bar; fi

# 演示数据的主线程（与 readme-shots.ts 的 T.fc 同一个夹具 ID）
thread=$(cd "$web" && bun -e 'import { MAIN_THREAD } from "./src/mock/fixtures"; console.log(MAIN_THREAD)')

# last_is <事件> <jq 条件>：事件日志里该事件的最后一条满足条件
last_is() { grep -F "\"event\":\"$1\"" "$log" 2>/dev/null | tail -1 | jq -e --arg k "$thread" "$2" >/dev/null 2>&1; }
ready_home() { last_is header.set '.on and .key == "home"' && last_is header.show '.alpha == 1'; }
ready_thread() { last_is header.set '.on and .key == $k' && last_is header.show '.alpha == 1'; }
# 网页侧栏钮的 click() 走 toggleSidebar → host_sink_show（open）由原生推进根滚动；之后 shot_settled 等卡片停稳
ready_sidebar() { last_is tabbar.set '.on' && last_is sink.show '.open == true'; }
# 展开到内屏后换成列侧栏布局：SinkLayout 退成普通外框，壳撤掉根滚动跟随
ready_inner() { last_is sink.set '.on == false'; }
# web_rail：网页探针最新一页的最后一条 rail：竖排着、竖栏由原生画（网页只留占位）、页头里的侧栏开关全藏了
web_rail() {
  jq -se 'map(select(.event == "rail")) | sort_by(.nav, .seq) | last | .info
    | .side != null and .native and .headerToggles == 0' "$wlog" >/dev/null 2>&1
}
# native_rail <最少操作数>：壳最后一条 rail.set 是原生竖栏的内容，没被遮挡，页头操作至少这么多个
native_rail() { last_is rail.set ".key != null and .covered == false and (.actions | length) >= $1"; }
# Duo 外屏：竖栏接管侧栏开关，原生页头只剩标题胶囊（lead 为 null）且已淡入；壳报告竖栏、原生竖栏带着视图钮就位
ready_duo_outer() {
  last_is header.set '.on and .key == $k and .lead == null and .title != null' && last_is header.show '.alpha == 1' &&
    last_is rail '.side != null' && web_rail && native_rail 1
}
# Duo 内屏横放：列侧栏布局，且竖栏带着页头操作（会话树、更多，非双段时还有信息面板）
ready_duo_inner() { ready_inner && last_is rail '.side != null' && web_rail && native_rail 2; }

# capture_when <文件名> <就绪谓词>：就绪后再等过渡走完，截到相邻两张一致为止
capture_when() {
  local name=$1 ready=$2
  for _ in $(seq 1 120); do
    if "$ready"; then
      sleep 3
      shot_settled "$out/$name.png"
      cp "$log" "$out/$name.jsonl"
      echo "- ✅ $name" >>"$summary"
      return 0
    fi
    sleep 1
  done
  echo "::error::120 秒内没有就绪（${name}：${ready}）"
  echo "- ❌ $name" >>"$summary"
  cat "$log" 2>/dev/null || true
  sim_shot "$out/$name-fail.png" 30 || true
  app_sample "$out/$name-sample.txt"
  return 1
}

# shoot <文件名> <路径> <就绪谓词>：冷启动 App 打开 <路径>，就绪后截图
shoot() {
  app_stop || return 1
  rm -f "$log"
  SIMCTL_CHILD_ORYN_DESKTOP_EVENT_LOG="$log" SIMCTL_CHILD_ORYN_E2E_URL="$origin$2" \
    sim_run 300 xcrun simctl launch "$udid" "$bundle" >/dev/null
  capture_when "$1" "$3"
}

if [[ $only == all ]]; then
  for scheme in light dark; do
    sim_run 60 xcrun simctl ui "$udid" appearance "$scheme"
    shoot "ios-sidebar-$scheme" "/t/$thread?readme=sidebar" ready_sidebar
    shoot "ios-thread-$scheme" "/t/$thread" ready_thread
    shoot "ios-home-$scheme" "/" ready_home
  done
fi

# ---------------------------------------------------------------- iPhone Duo
# 换一台 Duo：iPhone 关掉腾出资源；同一会话里先在外屏打开线程页，再展开、内屏横放
if [[ $only == all ]]; then pg_timeout 60 xcrun simctl shutdown "$udid" >/dev/null 2>&1 || true; fi
duo_setup
duo_udid=$udid
echo "Duo 模拟器：iPhone Duo（iOS ${duo_os}），duoctl ${duoctl_rev:0:7}" | tee -a "$summary"
sim_run 300 xcrun simctl boot "$udid"
sim_run 300 xcrun simctl bootstatus "$udid" -b
sim_run 300 xcrun simctl install "$udid" "$app"
# 与 boot / install 同一上限：install 返回后 LaunchServices 还在登记，get_app_container 实测卡过 74 秒以上
data=$(sim_run 300 xcrun simctl get_app_container "$udid" "$bundle" data)
log="$data/tmp/events.jsonl"
mkdir -p "$data/tmp"
pin_status_bar
for scheme in light dark; do
  sim_run 60 xcrun simctl ui "$udid" appearance "$scheme"
  # 动画折叠：iOS 27.1 运行时上瞬间跳变会让模拟器之后切不了屏
  duo close --duration 1
  shoot "duo-outer-$scheme" "/t/$thread" ready_duo_outer
  duo open --duration 1
  duo rotate landscape
  capture_when "duo-inner-$scheme" ready_duo_inner
done
