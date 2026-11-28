#!/usr/bin/env bash
# 谓词函数经 until_ok 间接调用
# shellcheck disable=SC2329
# iPhone Duo 折叠往返检查（mobile.yml 的 ios-check，硬门禁）：同一次会话里「外屏 → 展开 → 合上」，跨 600px 断点来回一趟，
# 确认换布局不重挂载页面、原生根滚动跟随随之撤掉与重建。只在内屏、外屏各跑一遍抓不到这一类问题。
#   - 新建一台 iPhone Duo 模拟器（支持它的最新 iOS 运行时），debug 包经 ORYN_E2E_URL 打开 mock Web UI 的线程页
#     （与 ios-e2e-check.sh 同一条生产路径：open_remote、远程能力、宿主命令权限）；
#   - 合上时外屏不到 600pt 宽：compact 下沉侧栏，原生跟随根滚动（sink.set on）；展开后内屏 ≥ 600pt：列侧栏，
#     SinkLayout 退成普通外框并撤掉跟随（sink.set off）；
#   - simctl 没有铰链命令，折叠由 duoctl 驱动（github.com/skarol/duoctl，固定到 v0.2.1 的提交、从源码运行，不经 Homebrew tap）。
#     它走模拟器的私有 HID 接口，每次折叠后读回状态，没生效就报错：Xcode 更新后若失效，这一步红在 duoctl 的报错上而不是断言上。
#     不用 `simctl io … screenConfig` 关屏模拟折叠：会让 SpringBoard 反复崩溃；截图也用 duoctl（simctl 合上时截内屏是黑图）。
# 断言（网页探针 = ios-e2e-preview.mjs 的 main.mount / layout；壳的事件日志 = sink.set）：
#   folded    外屏：最后一条 layout 是 sink = true（宽 < 600），最后一条 sink.set 是 on
#   open      展开：layout 变成 sink = false（宽 ≥ 600），最后一条 sink.set 是 off
#   refolded  合上：layout 回到 sink = true，最后一条 sink.set 是 on
#   全程      main.mount 只有一条：应用壳的 <main> 只挂载过一次（没重挂载，也没整页重新加载）
# 三个姿态各截一张，duoctl state（铰链角度、当前屏、尺寸）写进 Step Summary；全部断言通过才写 <输出目录>/.complete。
# 用法：ios-duo-check.sh <Oryn.app> <mock Web UI 构建目录> <输出目录>
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
# 等待预算倍数，与 ios-e2e-check.sh 相同：只放宽等待，不改判定
wait_scale=${E2E_WAIT_SCALE:-4}
# 与 ios-e2e-check.sh 的 47613 错开，两步不会抢端口
port=47614
origin="http://localhost:$port"
# mock 的 MAIN_THREAD（web/src/mock/fixtures.ts）
thread=T-0193a1b2-0000-7000-8000-00000000a001
duoctl_rev=c6ff3e961a9858fb061907448e702d7d1617dc4a # v0.2.1
wlog="$out/duo-web.jsonl"
work=$(mktemp -d)
server=""
udid=""
log=""
cleanup() {
  if [[ -n $server ]]; then kill "$server" 2>/dev/null || true; fi
  if [[ -n $udid ]]; then
    xcrun simctl shutdown "$udid" 2>/dev/null || true
    xcrun simctl delete "$udid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

fail() {
  echo "::error::iPhone Duo 折叠往返（${1}）：${2}"
  echo "- ❌ ${1}：${2}" >>"$summary"
  if [[ -n $udid ]]; then "$work/duoctl/bin/duoctl" -d "$udid" screenshot "$out/duo-fail-$1.png" 2>/dev/null || true; fi
  echo "--- 事件日志"
  cat "$log" 2>/dev/null || true
  [[ -n $log ]] && cp "$log" "$out/duo-events.jsonl" 2>/dev/null || true
  echo "--- 网页探针（layout / main.mount）"
  jq -c 'select(.event == "layout" or .event == "main.mount")' "$wlog" 2>/dev/null || true
  exit 1
}

# until_ok <步骤> <秒> <说明> <命令…>：命令成功为止（等待预算 × wait_scale）
until_ok() {
  local step=$1 secs=$(($2 * wait_scale)) what=$3 end
  shift 3
  end=$((SECONDS + secs))
  while ((SECONDS < end)); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 0.5
  done
  fail "$step" "${secs} 秒内没有等到：${what}"
}

# duoctl：固定提交，从源码运行；首次运行用 Xcode 的 clang 编译模拟器里的小助手（缓存在 ~/.cache/duoctl）
git -C "$work" init -q duoctl
git -C "$work/duoctl" fetch -q --depth 1 https://github.com/skarol/duoctl.git "$duoctl_rev"
git -C "$work/duoctl" checkout -q FETCH_HEAD
duo() { "$work/duoctl/bin/duoctl" -d "$udid" "$@"; }
# pose <步骤> <open|close>：动画折叠（iOS 27.1 运行时上瞬间跳变会让模拟器之后切不了屏，duoctl 也总是扫过去），读回状态写进摘要
pose() {
  duo "$2" --duration 1 || fail "$1" "duoctl ${2} 失败（见上面 duoctl 的报错；Xcode 更新后私有接口可能变了）"
  echo "- ${1}：\`$(duo state | jq -c '{hingeAngle, activeScreen, orientation, sizePoints}')\`" | tee -a "$summary"
}
shot() { duo screenshot "$out/duo-$1.png" || fail "$1" "duoctl screenshot 失败"; }

# 支持 iPhone Duo 的最新 iOS 运行时（Xcode 27.1+ 自带）
pick=$(xcrun simctl list runtimes available -j | jq -c '
  [.runtimes[] | select(.platform == "iOS")
    | . as $r
    | (($r.supportedDeviceTypes // []) | map(select(.name == "iPhone Duo")) | first) as $t
    | select($t != null)
    | {runtime: $r.identifier, version: $r.version, type: $t.identifier}]
  | sort_by(.version | split(".") | map(tonumber)) | last // empty')
if [[ -z $pick ]]; then
  xcrun simctl list runtimes >&2 || true
  fail setup "没有支持 iPhone Duo 的 iOS 运行时（需要 Xcode 27.1+）"
fi
udid=$(xcrun simctl create "Oryn CI Duo" "$(jq -r .type <<<"$pick")" "$(jq -r .runtime <<<"$pick")")
echo "### iPhone Duo 折叠往返（跨 600px 断点）" >>"$summary"
echo "模拟器：iPhone Duo（iOS $(jq -r .version <<<"$pick")），duoctl ${duoctl_rev:0:7}" | tee -a "$summary"
echo >>"$summary"

# mock Web UI 由 Vite 预览，探针把 layout / main.mount 记到 duo-web.jsonl
(cd "$web" && exec bun "$here/ios-e2e-preview.mjs" "$dist" "$port" "$wlog") >"$out/preview.log" 2>&1 &
server=$!
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "$origin/" && break
  sleep 0.5
done
curl -fsS -o /dev/null "$origin/" || {
  cat "$out/preview.log"
  fail setup "mock Web UI 没起来（${origin}）"
}

xcrun simctl boot "$udid"
xcrun simctl bootstatus "$udid" -b
xcrun simctl install "$udid" "$app"
# 先合上：从外屏起步
pose folded close
data=$(xcrun simctl get_app_container "$udid" "$bundle" data)
log="$data/tmp/events.jsonl"
mkdir -p "$data/tmp"
rm -f "$log"
SIMCTL_CHILD_ORYN_E2E_URL="$origin/t/$thread" SIMCTL_CHILD_ORYN_DESKTOP_EVENT_LOG="$log" \
  xcrun simctl launch "$udid" "$bundle" >/dev/null

# 谓词
ev() { grep -F "\"event\":\"$1\"" "$log" 2>/dev/null || true; }
web_ready() { ev web.ready | jq -se --arg o "$origin" 'any(.origin == $o and .bridge >= 8)'; }
# 网页探针的某类事件，按页面（nav）与 seq 排成数组：beacon 到达顺序不保证
probe() { jq -sc --arg e "$1" 'map(select(.event == $e)) | sort_by(.nav, .seq)' "$wlog" 2>/dev/null || echo '[]'; }
# last_layout <true|false>：最后一条 layout 的下沉侧栏有无，且宽度落在对应的一侧（compact < 600 ≤ medium / expanded）
last_layout() { probe layout | jq -e --argjson s "$1" 'last | .info.sink == $s and ((.info.w < 600) == $s)'; }
# last_sink <true|false>：壳的最后一条 sink.set
last_sink() { ev sink.set | tail -1 | jq -e --argjson o "$1" '.on == $o'; }

until_ok folded 90 "web.ready（bridge ≥ 8）" web_ready
until_ok folded 60 "外屏 compact：探针 layout sink = true、宽 < 600" last_layout true
until_ok folded 30 "原生跟随根滚动：最后一条 sink.set 是 on" last_sink true
shot folded

pose open open
until_ok open 60 "展开后 layout sink = false、宽 ≥ 600" last_layout false
until_ok open 30 "原生撤掉根滚动跟随：最后一条 sink.set 是 off" last_sink false
shot open

pose refolded close
until_ok refolded 60 "合上后 layout 回到 sink = true" last_layout true
until_ok refolded 30 "原生重新跟随根滚动：最后一条 sink.set 是 on" last_sink true
shot refolded

mounts=$(probe main.mount | jq length)
if [[ $mounts != 1 ]]; then
  fail roundtrip "应用壳的 <main> 挂载了 ${mounts} 次（应为 1）：跨 600px 换布局时页面被重挂载或整页重新加载"
fi
echo "- 往返：<main> 只挂载 1 次，$(probe layout | jq length) 次布局切换" | tee -a "$summary"
cp "$log" "$out/duo-events.jsonl"
touch "$out/.complete"
