# shellcheck shell=bash
# iOS 模拟器 CI 脚本的公共部分：ios-tabbar-check.sh / ios-header-check.sh（原生演示）与 ios-e2e-check.sh（真实网页 + 真实触摸）
# 都 source 它，几步用同一台模拟器。
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
      # 让页面与原生叠层画完再截
      sleep 2
      xcrun simctl io "$udid" screenshot "$out/$name.png"
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
