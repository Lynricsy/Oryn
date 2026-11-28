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

# sim_prepare <Oryn.app> <输出目录> <Step Summary 小标题>：不碰模拟器的准备。设置 app / out / bundle / summary / work，
# 编译逐区域比较工具到 $work/region-diff（工具链问题在启动模拟器之前就暴露），写 Step Summary 小标题。
sim_prepare() {
  app=$1
  out=$2
  bundle=com.lynricsy.oryn
  summary=${GITHUB_STEP_SUMMARY:-/dev/null}
  mkdir -p "$out"
  work=$(mktemp -d)
  trap 'rm -rf "$work"' EXIT

  xcrun --sdk macosx swiftc -O "$sim_dir/ios-region-diff.swift" -o "$work/region-diff"
  echo "### $3" >>"$summary"
}

# sim_setup <Oryn.app> <输出目录> <Step Summary 小标题>：原生演示检查的准备（同一台模拟器已启动、App 已安装时幂等）。
# sim_prepare 之后选模拟器并写进 Step Summary、启动并安装 App，算出事件日志路径 ${log}（App 数据容器里的 tmp/events.jsonl）。
sim_setup() {
  sim_prepare "$@"
  local pick
  pick=$(pick_iphone)
  udid=$(jq -r .udid <<<"$pick")
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

# pg_timeout <秒> <命令…>：在独立进程组里跑命令，超时先 TERM 再 KILL 整个进程组，返回 124；否则返回命令的退出码。
# 只杀父进程不够：duoctl 是 Python，阻塞点常在它 subprocess.run 起的 xcrun simctl spawn，子进程留下来会继续卡住、
# 或攥着继承来的输出管道让调用方的 $(…) / 管道一直等。
pg_timeout() {
  perl -e '
    my $secs = shift;
    my $pid = fork() // die "fork: $!";
    if ($pid == 0) { setpgrp(0, 0); exec @ARGV or die "exec: $!"; }
    setpgrp($pid, $pid);
    $SIG{ALRM} = sub {
      kill "TERM", -$pid; sleep 2; kill "KILL", -$pid;
      waitpid($pid, 0);
      exit 124;
    };
    alarm $secs;
    waitpid($pid, 0);
    alarm 0;
    exit(($? & 127) ? 128 + ($? & 127) : $? >> 8);
  ' "$@"
}

# duo <duoctl 子命令…>：对当前这台 Duo 执行 duoctl，最多 DUO_TIMEOUT 秒（默认 180，首次调用含编译辅助程序），
# 超时连同它派生的 simctl 子进程一起结束，调用方按失败处理（以前没有限时，卡住时整步挂到 runner 被回收也没有日志）
duo() { pg_timeout "${DUO_TIMEOUT:-180}" "$duoctl" -d "$udid" "$@"; }

# sim_drop <udid>：收尾时关掉并删除模拟器，每条 simctl 限时 60 秒（pg_timeout，超时返回 124），前后打 [cleanup] 标记：
# 清理卡住时 EXIT trap 不会拖住整步（Actions 才能封存日志、上传产物），日志里也看得到停在哪一条、结果如何
sim_drop() {
  local u=$1 c rc
  for c in shutdown delete; do
    echo "[cleanup] xcrun simctl ${c} ${u}" >&2
    rc=0
    pg_timeout 60 xcrun simctl "$c" "$u" >/dev/null 2>&1 || rc=$?
    echo "[cleanup] xcrun simctl ${c} ${u} -> ${rc}" >&2
  done
}

# sim_run <秒> <命令…>：限时跑一条模拟器命令（pg_timeout），前后在 stderr 打 [sim] 标记、耗时与退出码，stdout 照常
# （可用在 $(…) 里）。Duo 步骤曾整段没有输出地挂到被回收，只剩 cleanup 时被终止的孤儿 simctl：靠这些标记定位卡在哪一条
sim_run() {
  local secs=$1 t=$SECONDS rc=0
  shift
  echo "[sim] $*" >&2
  pg_timeout "$secs" "$@" || rc=$?
  echo "[sim] $* -> ${rc}（$((SECONDS - t)) 秒）" >&2
  return "$rc"
}

# app_sample <文件>：对本台模拟器（$udid）里正在跑的 Oryn 进程采样 3 秒（macOS sample，模拟器里的 App 是宿主进程），
# 存进产物并打印主线程栈的开头；用于 App 卡住（没有事件、网页没加载、截图超时）时看主线程停在哪。
# 进程按可执行文件路径里的设备目录（CoreSimulator/Devices/<UDID>/…/Oryn.app/Oryn）认，同一台机器上别的模拟器
# （上一步没关掉的、并行的）里的 Oryn 不会被采到；同一台里有多个时取最新的（pgrep -n）
app_sample() {
  local pid
  pid=$(pgrep -n -f "/Devices/${udid}/.*/Oryn\.app/Oryn( |$)") || true
  if [[ -z $pid ]]; then
    echo "--- 模拟器 ${udid} 里没有在跑的 Oryn 进程" >&2
    return 0
  fi
  echo "--- 采样 pid ${pid}：$(ps -o command= -p "$pid" 2>/dev/null | cut -c1-200)"
  rm -f "$1"
  pg_timeout 60 sample "$pid" 3 -file "$1" >/dev/null 2>&1 || true
  if [[ ! -s $1 ]]; then
    echo "--- Oryn（pid ${pid}）采样失败：没有生成 $(basename "$1")" >&2
    return 0
  fi
  echo "--- Oryn（pid ${pid}）主线程采样，全文在 $(basename "$1")"
  grep -A40 -m1 "com.apple.main-thread" "$1" 2>/dev/null | head -40 || true
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
  pg_timeout 60 xcrun simctl spawn "$udid" log show --last 3m --style compact \
    --predicate 'processImagePath CONTAINS "Oryn"' 2>/dev/null | tail -200 || true
  app_sample "$out/$name-sample.txt"
  return 1
}

# sim_shot <文件> <秒>：限时截图。CI 机器卡顿时 simctl 截图曾阻塞 3 分钟以上；超时就按进程组终止（pg_timeout）、
# 丢掉没写完的图片，返回 1。截图先写到旁边的临时文件，成功才改名，失败时不会留下半张图或旧图冒充。
# 经 sim_run 跑：失败时把它的 [sim] 标记（真实退出码，124 才是超时，与耗时）和截图命令的 stderr 末尾打到 stderr；
# 成功时不打，免得每轮截图都刷屏
sim_shot() {
  local f=$1 secs=$2 tmp="${1%.png}.part.png" err="${1%.png}.err" ok
  rm -f "$tmp"
  if [[ -n $duoctl ]]; then
    sim_run "$secs" "$duoctl" -d "$udid" screenshot "$tmp" >/dev/null 2>"$err" && ok=1
  else
    sim_run "$secs" xcrun simctl io "$udid" screenshot "$tmp" >/dev/null 2>"$err" && ok=1
  fi
  if [[ -n ${ok:-} ]]; then
    rm -f "$err"
    mv "$tmp" "$f"
    return 0
  fi
  tail -8 "$err" >&2 || true
  rm -f "$err"
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
  # 截图一直超时（差异是 -）时分清是 App 主线程卡住，还是模拟器 / 宿主机整体卡住：采 App 主线程，再看宿主机最忙的进程
  app_sample "${f%.png}-sample.txt"
  echo "--- 宿主机最忙的进程"
  pg_timeout 30 top -l 1 -o cpu -n 12 -stats pid,command,cpu,mem 2>/dev/null | tail -14 || true
  return 1
}
