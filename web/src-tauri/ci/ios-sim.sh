# shellcheck shell=bash
# iOS 模拟器 CI 脚本的公共部分：ios-header-check.sh（原生页头演示）、ios-e2e-check.sh（真实网页 + 真实触摸）、
# ios-duo-check.sh（iPhone Duo 折叠往返）与 ios-readme-shots.sh（README 截图）都 source 它。
# 下面函数设置的全局变量（app / out / bundle / summary / work / udid / data / log）由 source 它的脚本使用。
# shellcheck disable=SC2034

sim_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# 最新的 iOS 26+ 运行时里第一台非 SE 的 iPhone（SE 没有 Home 指示条，底栏贴底的几何与演示假设不同），
# 输出 {udid, name, os}；没有时报错并返回 1。原生底栏只在 iOS 26+ 启用，旧运行时上 set 会以 unsupported 拒绝。
pick_iphone() {
  local pick
  pick=$(xcrun simctl list devices available -j | jq -c '
    [.devices | to_entries[]
      | (.key | capture("SimRuntime\\.iOS-(?<major>[0-9]+)-(?<minor>[0-9]+)")) as $v
      | ($v.major | tonumber) as $major
      | select($major >= 26)
      | .value[]
      | select((.name | startswith("iPhone")) and (.name | contains("SE") | not))
      | {udid, name, os: "\($v.major).\($v.minor)", order: ($major * 100 + ($v.minor | tonumber))}]
    | sort_by(-.order) | .[0] // empty')
  if [[ -z $pick ]]; then
    echo "::error::没有可用的 iOS 26+ iPhone 模拟器" >&2
    xcrun simctl list runtimes >&2 || true
    return 1
  fi
  echo "$pick"
}

# sim_setup <Oryn.app> <输出目录> <Step Summary 小标题>：原生演示检查的准备（同一台模拟器已启动、App 已安装时幂等）。
# 编译逐区域比较工具到 $work/region-diff（工具链问题在启动模拟器之前就暴露）、选模拟器并写进 Step Summary、
# 启动并安装 App，算出事件日志路径 ${log}（App 数据容器里的 tmp/events.jsonl）。
sim_setup() {
  app=$1
  out=$2
  bundle=com.lynricsy.oryn
  summary=${GITHUB_STEP_SUMMARY:-/dev/null}
  mkdir -p "$out"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT

  xcrun --sdk macosx swiftc -O "$sim_dir/ios-region-diff.swift" -o "$work/region-diff"

  local pick
  pick=$(pick_iphone)
  udid=$(jq -r .udid <<<"$pick")
  echo "### $3" >>"$summary"
  echo "模拟器：$(jq -r '"\(.name)（iOS \(.os)）"' <<<"$pick")" | tee -a "$summary"
  echo >>"$summary"

  xcrun simctl boot "$udid" 2>/dev/null || true
  xcrun simctl bootstatus "$udid" -b
  xcrun simctl install "$udid" "$app"
  data=$(xcrun simctl get_app_container "$udid" "$bundle" data)
  log="$data/tmp/events.jsonl"
  mkdir -p "$data/tmp"
}

# iPhone Duo：simctl 没有铰链命令，折叠、转屏与截图由 duoctl 驱动（github.com/skarol/duoctl，固定到 v0.2.1 的提交、
# 从源码运行，不经 Homebrew tap）。它走模拟器的私有 HID 接口，每次折叠后读回状态、没生效就报错；Xcode 更新后可能失效。
# 不用 `simctl io … screenConfig` 关屏模拟折叠：会让 SpringBoard 反复崩溃。
duoctl_rev=c6ff3e961a9858fb061907448e702d7d1617dc4a # v0.2.1
# duo_setup 之后是 duoctl 的路径；非空时 sim_shot 改用它截图（simctl 合上时截的是内屏，一片黑）
duoctl=""

# duo_setup：在 $work 下取出 duoctl，用支持 iPhone Duo 的最新 iOS 运行时新建一台模拟器（不启动），
# 设置全局变量 udid、duo_os、duoctl；没有这样的运行时（需要 Xcode 27.1+）时报错并返回 1。调用方负责删除这台模拟器。
duo_setup() {
  git -C "$work" init -q duoctl
  git -C "$work/duoctl" fetch -q --depth 1 https://github.com/skarol/duoctl.git "$duoctl_rev"
  git -C "$work/duoctl" checkout -q FETCH_HEAD
  local pick
  pick=$(xcrun simctl list runtimes available -j | jq -c '
    [.runtimes[] | select(.platform == "iOS")
      | . as $r
      | (($r.supportedDeviceTypes // []) | map(select(.name == "iPhone Duo")) | first) as $t
      | select($t != null)
      | {runtime: $r.identifier, version: $r.version, type: $t.identifier}]
    | sort_by(.version | split(".") | map(tonumber)) | last // empty')
  if [[ -z $pick ]]; then
    echo "::error::没有支持 iPhone Duo 的 iOS 运行时（需要 Xcode 27.1+）" >&2
    xcrun simctl list runtimes >&2 || true
    return 1
  fi
  udid=$(xcrun simctl create "Oryn CI Duo" "$(jq -r .type <<<"$pick")" "$(jq -r .runtime <<<"$pick")")
  duo_os=$(jq -r .version <<<"$pick")
  duoctl="$work/duoctl/bin/duoctl"
}

# duo <duoctl 子命令…>：对当前这台 Duo 执行 duoctl
duo() { "$duoctl" -d "$udid" "$@"; }

# run <名字> <等待的事件> [要传给 App 的 SIMCTL_CHILD_* 赋值…]
# 冷启动 App，等事件日志里出现该事件后截图到 <输出目录>/<名字>.png，日志存为 <名字>.jsonl。
run() {
  local name=$1 event=$2
  shift 2
  xcrun simctl terminate "$udid" "$bundle" 2>/dev/null || true
  rm -f "$log"
  env SIMCTL_CHILD_ORYN_DESKTOP_EVENT_LOG="$log" "$@" xcrun simctl launch "$udid" "$bundle"
  for _ in $(seq 1 90); do
    if grep -qF "\"event\":\"$event\"" "$log" 2>/dev/null; then
      # 让页面与原生叠层画完再截：冷启动在卡顿的 CI 机器上 2 秒可能还没画完壁纸（曾截到全白的基线）
      sleep 2
      shot_settled "$out/$name.png" || return 1
      cp "$log" "$out/$name.jsonl"
      return 0
    fi
    sleep 1
  done
  echo "::error::90 秒内没有 ${event}（${name}）"
  cat "$log" 2>/dev/null || true
  xcrun simctl spawn "$udid" log show --last 3m --style compact --predicate 'processImagePath CONTAINS "Oryn"' 2>/dev/null |
    tail -200 || true
  return 1
}

# sim_shot <文件> <秒>：限时截图。CI 机器卡顿时 simctl 截图曾阻塞 3 分钟以上；超时就终止并回收子进程、
# 丢掉没写完的图片，返回 1。截图先写到旁边的临时文件，成功才改名，失败时不会留下半张图或旧图冒充。
sim_shot() {
  local f=$1 secs=$2 tmp="${1%.png}.part.png" pid i
  rm -f "$tmp"
  if [[ -n $duoctl ]]; then
    "$duoctl" -d "$udid" screenshot "$tmp" >/dev/null 2>&1 &
  else
    xcrun simctl io "$udid" screenshot "$tmp" >/dev/null 2>&1 &
  fi
  pid=$!
  for ((i = 0; i < secs * 4; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      if wait "$pid"; then
        mv "$tmp" "$f"
        return 0
      fi
      rm -f "$tmp"
      return 1
    fi
    sleep 0.25
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  rm -f "$tmp"
  return 1
}

# shot_settled <文件>：每秒截一张，直到相邻两张在顶部与底部横条里基本一致（差异 ≤ 0.003，与「隐藏」判定同一阈值；
# 最多 10 轮，每张限时 30 秒）。允许一点差异：页头里运行中的 orb 小球在呼吸（约 0.06%）；没画完的壁纸差几十个百分点。
# 中间一段不比：连接页品牌标的插入符在闪；顶部有状态栏时钟，跨分钟时多截一轮即可。
shot_settled() {
  local f=$1 prev="$work/settle-prev.png" w h top=- bottom=-
  rm -f "$prev"
  for _ in $(seq 1 10); do
    if ! sim_shot "$f" 30; then
      rm -f "$prev"
      continue
    fi
    if [[ -f $prev ]]; then
      w=$(sips -g pixelWidth "$f" | awk '/pixelWidth/ { print $2 }')
      h=$(sips -g pixelHeight "$f" | awk '/pixelHeight/ { print $2 }')
      top=$("$work/region-diff" "$prev" "$f" 0 0 "$w" $((h * 15 / 100)))
      bottom=$("$work/region-diff" "$prev" "$f" 0 $((h * 55 / 100)) "$w" $((h * 45 / 100)))
      if awk -v a="$top" -v b="$bottom" 'BEGIN { exit !(a <= 0.003 && b <= 0.003) }'; then return 0; fi
    fi
    cp "$f" "$prev"
    sleep 1
  done
  rm -f "$f"
  echo "::error::$(basename "$f") 截图 10 轮内没有稳定下来（顶部差异 ${top}，底部差异 ${bottom}）"
  return 1
}
