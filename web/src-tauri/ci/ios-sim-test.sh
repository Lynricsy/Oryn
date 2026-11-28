#!/usr/bin/env bash
# ios-sim.sh 里不依赖模拟器的部分的自测：app_stop（结束本台模拟器里的 Oryn）与冷启动 run() 的失败分支。
# 用法：bash web/src-tauri/ci/ios-sim-test.sh
# check 的条件以单引号字符串交给 eval（到时才展开）；ios-sim.sh 的全局变量（udid / bundle / out / log）由这里设置、在那边用；
# kill 函数在第 3 组里覆盖 app_stop 调到的 kill
# shellcheck disable=SC2016,SC2034,SC2154,SC2329 source-path=SCRIPTDIR
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"

t=$(mktemp -d)
# 只用 Bash 3.2 也有的写法（macOS 的 /bin/bash）：不用负下标，空数组展开带 + 保护（3.2 的 set -u 会把空数组当未定义）
victims=()
trap 'for p in ${victims[@]+"${victims[@]}"}; do builtin kill -9 "$p" 2>/dev/null || true; done; rm -rf "$t"' EXIT
mkdir -p "$t/bin"
cat >"$t/bin/xcrun" <<EOF
#!/bin/sh
echo "\$*" >>"$t/calls"
exit 0
EOF
chmod +x "$t/bin/xcrun"
PATH="$t/bin:$PATH"
bundle=com.lynricsy.oryn
out=$t
log=$t/events.jsonl

failures=0
check() {
  if eval "$2"; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi
}
# fake_app <UDID>：起一个可执行文件路径像模拟器 App（…/Devices/<UDID>/…/Oryn.app/Oryn）的进程，pid 放进 last_pid
fake_app() {
  mkdir -p "$t/Devices/$1/data/Containers/Bundle/Application/X/Oryn.app"
  cp "$(type -P sleep)" "$t/Devices/$1/data/Containers/Bundle/Application/X/Oryn.app/Oryn"
  "$t/Devices/$1/data/Containers/Bundle/Application/X/Oryn.app/Oryn" 120 &
  last_pid=$!
  victims+=("$last_pid")
  sleep 0.3
}

# 1. 本台没有 Oryn：terminate 之后直接成功
udid=NONE
check "没有进程时 app_stop 返回 0" 'app_stop 2>/dev/null'

# 2. terminate 没杀掉：SIGKILL 后确认退出、返回 0；别的模拟器里的 Oryn 不受影响
fake_app OTHER
other=$last_pid
fake_app STUCK
stuck=$last_pid
udid=STUCK
check "terminate 后仍在时 SIGKILL 并返回 0" 'app_stop 2>/dev/null'
check "本台的 Oryn 已退出" '! builtin kill -0 "$stuck" 2>/dev/null'
check "别的模拟器里的 Oryn 还在" 'builtin kill -0 "$other" 2>/dev/null'

# 3. SIGKILL 也杀不掉（kill -9 换成空操作）：app_stop 返回 1，run() 不得冷启动
fake_app UNKILLABLE
udid=UNKILLABLE
kill() { if [[ $1 == -9 ]]; then return 0; fi; builtin kill "$@"; }
check "SIGKILL 后仍在时 app_stop 返回 1" '! app_stop 2>/dev/null'
rm -f "$t/calls"
check "run() 在 App 停不下来时返回 1" '! run name web.ready 2>/dev/null'
check "run() 先请求了 simctl terminate" 'grep -q "simctl terminate" "$t/calls"'
check "run() 没有调 simctl launch" '! grep -q "simctl launch" "$t/calls"'
unset -f kill

# 4. header_titled_in：淡入要晚于最近一次撤掉；矩形更新重发 set 不要求再淡入
h=$t/header.jsonl
on='{"event":"header.set","on":true,"key":"T-1","lead":null,"title":{"x":0}}'
off='{"event":"header.set","on":false,"key":null,"lead":null,"title":null}'
shown='{"event":"header.show","alpha":1,"ms":120}'
printf '%s\n' "$on" "$shown" >"$h"
check "挂上后淡入 → 就绪" 'header_titled_in "$h" T-1'
check "key 不是给定线程 → 未就绪" '! header_titled_in "$h" T-2'
printf '%s\n' "$on" "$shown" "$off" "$on" >"$h"
check "再次挂上、还没淡入：不吃上一次的 alpha 1" '! header_titled_in "$h"'
printf '%s\n' "$on" "$shown" "$off" "$on" "$shown" "$on" "not json" >"$h"
check "重挂后淡入，之后只是矩形更新 → 就绪（非 JSON 行忽略）" 'header_titled_in "$h"'
printf '%s\n' '{"event":"header.set","on":true,"key":"T-1","lead":{"x":0},"title":{"x":0}}' "$shown" >"$h"
check "带侧栏钮（没进竖栏）→ 未就绪" '! header_titled_in "$h"'

if ((failures > 0)); then
  echo "${failures} 项失败" >&2
  exit 1
fi
echo "全部通过"
