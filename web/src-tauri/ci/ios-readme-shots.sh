#!/usr/bin/env bash
# jq 程序里的 $k 是 jq 变量（单引号是故意的）；就绪谓词经 "$ready" 间接调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# README 手机三联的 iOS 原图（readme-ios-shots 工作流）：iOS 26+ 模拟器里的真实 App 打开带演示数据的 mock Web UI，
# 原生液态玻璃底栏与页头由 UIKit 绘制，明暗各截三张：
#   ios-sidebar-<明暗>.png  线程页打开下沉侧栏（原生底栏跟着卡片揭开）
#   ios-thread-<明暗>.png   线程页（原生页头接管）
#   ios-home-<明暗>.png     新建线程页（原生侧栏钮）
# 演示数据与 web/scripts/readme-shots.ts 同一份：`--emit-ios-init` 生成注入脚本，插进 mock 构建的 index.html 最前面。
# debug 包读 ORYN_E2E_URL（mobile.rs 的 e2e_open）跳过配对直接打开；就绪靠事件日志（web.ready / header.* / tabbar.reveal）判断。
# 合成到 docs/assets/mobile-views-*.png 在本机做：ORYN_IOS_SHOTS=<产物目录> bun run shots:readme --only mobile-views。
# 用法：ios-readme-shots.sh <Oryn.app> <mock Web UI 构建目录> <输出目录>
set -euo pipefail
if (($# != 3)); then
  echo "用法：$0 <Oryn.app> <mock Web UI 构建目录> <输出目录>" >&2
  exit 2
fi
here=$(cd "$(dirname "$0")" && pwd)
web=$(cd "$here/../.." && pwd)
dist=$(cd "$2" && pwd)
port=47614
origin="http://localhost:$port"

# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"
sim_setup "$1" "$3" "README 截图（iOS 模拟器）"
out=$(cd "$out" && pwd)
server=""
trap 'if [[ -n $server ]]; then kill "$server" 2>/dev/null || true; fi; rm -rf "$work"' EXIT

# 演示数据脚本在截图前一刻生成（线程的「x 分钟前」从这时算起），插在所有脚本之前
site="$work/site"
cp -R "$dist" "$site"
(cd "$web" && bun scripts/readme-shots.ts --emit-ios-init "$site/readme-demo.js")
perl -0pi -e 's#<head>#<head><script src="/readme-demo.js"></script>#' "$site/index.html"
grep -qF 'readme-demo.js' "$site/index.html" || {
  echo "::error::没能把演示数据脚本插进 index.html"
  exit 1
}

(cd "$web" && exec bunx vite preview --outDir "$site" --host :: --port "$port" --strictPort) >"$out/preview.log" 2>&1 &
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

# 状态栏固定成发布图的样子
xcrun simctl status_bar "$udid" override --time 9:41 --dataNetwork wifi --wifiMode active --wifiBars 3 \
  --cellularMode active --cellularBars 4 --batteryState charged --batteryLevel 100

# 演示数据的主线程（与 readme-shots.ts 的 T.fc 同一个夹具 ID）
thread=$(cd "$web" && bun -e 'import { MAIN_THREAD } from "./src/mock/fixtures"; console.log(MAIN_THREAD)')

# last_is <事件> <jq 条件>：事件日志里该事件的最后一条满足条件
last_is() { grep -F "\"event\":\"$1\"" "$log" 2>/dev/null | tail -1 | jq -e --arg k "$thread" "$2" >/dev/null 2>&1; }
ready_home() { last_is header.set '.on and .key == "home"' && last_is header.show '.alpha == 1'; }
ready_thread() { last_is header.set '.on and .key == $k' && last_is header.show '.alpha == 1 and .dx == 0'; }
ready_sidebar() { last_is tabbar.reveal '.x > 0 and .covered == false and .ms > 0'; }

# shoot <文件名> <路径> <就绪谓词>：冷启动 App 打开 <路径>，就绪后再等过渡走完，截到相邻两张一致为止
shoot() {
  local name=$1 path=$2 ready=$3
  xcrun simctl terminate "$udid" "$bundle" 2>/dev/null || true
  rm -f "$log"
  SIMCTL_CHILD_ORYN_DESKTOP_EVENT_LOG="$log" SIMCTL_CHILD_ORYN_E2E_URL="$origin$path" \
    xcrun simctl launch "$udid" "$bundle" >/dev/null
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
  return 1
}

for scheme in light dark; do
  xcrun simctl ui "$udid" appearance "$scheme"
  shoot "ios-sidebar-$scheme" "/t/$thread?readme=sidebar" ready_sidebar
  shoot "ios-thread-$scheme" "/t/$thread" ready_thread
  shoot "ios-home-$scheme" "/" ready_home
done
