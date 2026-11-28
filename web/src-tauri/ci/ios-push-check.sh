#!/usr/bin/env bash
# 单引号里的 '$(' 是字面量（找没展开的构建变量）；cleanup 经 trap 调用
# shellcheck disable=SC2016,SC2329 source-path=SCRIPTDIR
# iOS 推送探针（mobile.yml 的 ios-check「模拟器：推送探针」）：一次 CI 同时回答 M28 计划 Step 0 的三问并验收 Step 5。
#   P2 模板     包里有 PlugIns/NotificationService.appex，包名 com.lynricsy.oryn.NotificationService，两个版本号与主包相同
#               （自定义 XcodeGen 模板 web/src-tauri/ios/project.yml 生效、ci/ios-nse.sh 跑过）。
#   P1 钩子     debug 包带 ORYN_PUSH_PROBE=1 冷启动（PushE2ETests 点掉系统授权框），探针请求授权并 registerForRemoteNotifications；
#               系统日志出现 apns.registered 或 apns.register_failed 之一即证明 tao 的 AppDelegate 补上的回调被 UIKit 调到了
#               （不签名的包没有 aps-environment，失败回调同样算数）。
#   challenge   App 在前台时 simctl push ci/ios-push/challenge.apns：出现 apns.challenge foreground=true（willPresent 不展示、
#               转成 pushchallenge 事件）。
#   P3 NSE      simctl terminate 后 push ci/ios-push/notify.apns：nse.received → nse.keys found=true → nse.decrypted ok=true
#               → nse.content parsed=true，通知横幅截图存 notify-<轮>.png（标题应是解密出的「Push probe」）。
#   自检        probe.vector ok=true：App 进程里用 WebPushDecrypt 解 RFC 8291 附录 A 的报文，不依赖模拟器是否调起 NSE。
# notify.apns 的 e：接收端是 RFC 8291 附录 A 的私钥与 auth（探针写进共享钥匙串的同一把），发送端临时密钥与 salt 也取附录 A，
# 明文 {"v":1,"id":"ntf_probe","kind":"done","title":"Push probe","body":"Decrypted by the notification service extension",
# "url":"/t/T-0193a1b2-0000-7000-8000-00000000a001","tag":"oryn-00000000a001","avatar":{"kind":"thread","color":"blue"}}。
# 轮次（PUSH_ADHOC_SIGN）：
#   auto（缺省）第 1 轮装原样的不签名包；NSE 没通过时第 2 轮装临时签名（codesign -）的副本再跑一遍：签入
#               keychain-access-groups 与 aps-environment，让模拟器给 App 与扩展同一个钥匙串访问组（P3 的预案二）。
#   1           只跑临时签名一轮；0 只跑不签名一轮。
# 门禁：P2 与第 1 轮的 P1、challenge、自检必须通过；NSE 看 PUSH_NSE_GATE：hard（缺省）要求某一轮 nse.decrypted ok=true 与
# nse.content parsed=true，soft 只记录（模拟器不调起 NSE 时切换，NSE 留给真机验收）。
# 结果写 <输出目录>/summary.md（日志原文在其中），完整系统日志存 push.log；全部门禁通过才写 <输出目录>/.complete。
# 用法：ios-push-check.sh <Oryn.app> <输出目录>
set -euo pipefail
if (($# != 2)); then
  echo "用法：$0 <Oryn.app> <输出目录>" >&2
  exit 2
fi
built=$(cd "$1" && pwd)
mkdir -p "$2"
out=$(cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
bundle=com.lynricsy.oryn
nse_id=com.lynricsy.oryn.NotificationService
nse_gate=${PUSH_NSE_GATE:-hard}
adhoc=${PUSH_ADHOC_SIGN:-auto}
# 等待预算倍数，同 ios-e2e-check.sh（CI 的 macOS 机器拥挤时整机慢数倍）
wait_scale=${E2E_WAIT_SCALE:-4}
summary="$out/summary.md"
plog="$out/push-stream.log"
predicate='subsystem == "com.lynricsy.oryn.push"'
work=$(mktemp -d)
runner=""
streamer=""
cleanup() {
  if [[ -n $runner ]]; then kill "$runner" 2>/dev/null || true; fi
  if [[ -n $streamer ]]; then kill "$streamer" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT
failed=0

note() { echo "$*" | tee -a "$summary"; }
pass() { note "- ✅ $*"; }
bad() {
  echo "::error::推送探针：$*"
  echo "- ❌ $*" >>"$summary"
  failed=1
}
warn() {
  echo "::warning::推送探针：$*"
  echo "- ⚠️ $*" >>"$summary"
}
# 把标准输入作为日志原文放进 summary 的代码块
quote() {
  {
    echo '```'
    cat
    echo '```'
  } >>"$summary"
}
plist_get() { /usr/libexec/PlistBuddy -c "Print :$2" "$1" 2>/dev/null || true; }

: >"$summary"
note "### iOS 推送探针（AppDelegate 钩子、网关 challenge、Notification Service Extension）"
note ""

# ---------------------------------------------------------------- P2：appex 在包里

appex="$built/PlugIns/NotificationService.appex"
if [[ -d $appex ]]; then
  nse_bid=$(plist_get "$appex/Info.plist" CFBundleIdentifier)
  nse_ver=$(plist_get "$appex/Info.plist" CFBundleShortVersionString)
  nse_build=$(plist_get "$appex/Info.plist" CFBundleVersion)
  app_ver=$(plist_get "$built/Info.plist" CFBundleShortVersionString)
  app_build=$(plist_get "$built/Info.plist" CFBundleVersion)
  if [[ $nse_bid == "$nse_id" && $nse_ver == "$app_ver" && $nse_build == "$app_build" ]]; then
    pass "P2 模板：PlugIns/NotificationService.appex（${nse_bid}，${nse_ver} / ${nse_build}，与主包一致）"
  else
    bad "P2 模板：appex 包名 ${nse_bid:-（无）}、版本 ${nse_ver:-（无）} / ${nse_build:-（无）}；应为 ${nse_id}、${app_ver} / ${app_build}"
  fi
else
  bad "P2 模板：包里没有 PlugIns/NotificationService.appex（tauri.conf.json 的 bundle.iOS.template 没生效）"
fi
for f in "$built/SymbolsNerdFontMono-Regular.ttf" "$appex/SymbolsNerdFontMono-Regular.ttf"; do
  if [[ -f $f ]]; then
    pass "P2 字体：已打包 $(basename "$(dirname "$f")")/SymbolsNerdFontMono-Regular.ttf"
  else
    bad "P2 字体：缺少 $f（通知线程 Nerd Font 图标会退回品牌标）"
  fi
done
note "Info.plist 的 OrynAppIdentifierPrefix：主包「$(plist_get "$built/Info.plist" OrynAppIdentifierPrefix)」，扩展「$(plist_get "$appex/Info.plist" OrynAppIdentifierPrefix)」"
note ""

# ---------------------------------------------------------------- 模拟器、UI 测试与日志

# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"
pick=$(pick_iphone)
udid=$(jq -r .udid <<<"$pick")
note "模拟器：$(jq -r '"\(.name)（iOS \(.os)）"' <<<"$pick")"
note ""

command -v xcodegen >/dev/null || brew install xcodegen
cp -R "$here/ios-e2e" "$work/proj"
(cd "$work/proj" && xcodegen generate --quiet)
xcb=(-project "$work/proj/OrynE2E.xcodeproj" -scheme OrynE2E -destination "id=$udid" -derivedDataPath "$work/dd")
xcodebuild build-for-testing "${xcb[@]}" -quiet

xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b

# 子系统 com.lynricsy.oryn.push 的系统日志：等待时读流（每轮从记下的行号起），最后用 log show 取完整原文
start=$(date '+%Y-%m-%d %H:%M:%S')
xcrun simctl spawn "$udid" log stream --level default --style compact --predicate "$predicate" >"$plog" 2>&1 &
streamer=$!
mark=0
round_start=$start
push_lines() {
  if [[ -n $streamer ]] && kill -0 "$streamer" 2>/dev/null; then
    tail -n "+$((mark + 1))" "$plog"
  else
    xcrun simctl spawn "$udid" log show --start "$round_start" --style compact --predicate "$predicate" 2>/dev/null || true
  fi
}
# seen <正则>：输出本轮最后一条匹配的日志行；没有时退出码非 0
seen() { push_lines | grep -E "$1" | tail -1 | grep .; }
# show <正则>：把本轮最后一条匹配的日志行原文写进 summary；没有时写明
show() {
  local line
  line=$(seen "$1") || line="（没有匹配 $1 的日志）"
  printf '%s\n' "$line" | quote
}
# wait_log <秒> <正则>：等到本轮日志里出现匹配行（等待预算 × wait_scale）
wait_log() {
  local end=$((SECONDS + $1 * wait_scale))
  while ((SECONDS < end)); do
    if seen "$2" >/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}

# wait_req <步骤> <秒>：等测试走到这一步（它带来的 JSON 存进 $req）；测试先退出或超时返回 1
wait_req() {
  local end=$((SECONDS + $2 * wait_scale))
  while ((SECONDS < end)); do
    if [[ -f $sync/$1.req ]]; then
      req=$(cat "$sync/$1.req")
      return 0
    fi
    kill -0 "$runner" 2>/dev/null || return 1
    sleep 0.5
  done
  return 1
}
ok() {
  printf '{}' >"$sync/$1.tmp"
  mv "$sync/$1.tmp" "$sync/$1.ok"
}

# adhoc_copy：临时签名的副本路径（输出）。访问组前缀取 Info.plist 里展开后的 OrynAppIdentifierPrefix；
# 没展开时 PushKeyStore 不指定访问组、落在 entitlements 的第一个组里，所以主包只签 push 组，两个进程的默认组就是同一个。
adhoc_copy() {
  local prefix signed="$work/adhoc/Oryn.app"
  local -a groups
  prefix=$(plist_get "$built/Info.plist" OrynAppIdentifierPrefix)
  if [[ -n $prefix && $prefix != *'$('* ]]; then
    groups=("${prefix}com.lynricsy.oryn" "${prefix}com.lynricsy.oryn.push")
  else
    prefix=XDPWT9ABRD.
    groups=("${prefix}com.lynricsy.oryn.push")
  fi
  rm -rf "$work/adhoc"
  mkdir -p "$work/adhoc"
  cp -R "$built" "$signed"
  python3 - "$work/adhoc/app.entitlements" "$work/adhoc/nse.entitlements" "${prefix}com.lynricsy.oryn.push" "${groups[@]}" <<'PY'
import plistlib
import sys

app_path, nse_path, push_group, *app_groups = sys.argv[1:]
with open(app_path, "wb") as f:
    plistlib.dump({"aps-environment": "development", "keychain-access-groups": app_groups}, f)
with open(nse_path, "wb") as f:
    plistlib.dump({"keychain-access-groups": [push_group]}, f)
PY
  if [[ -d $signed/Frameworks ]]; then
    find "$signed/Frameworks" -maxdepth 1 \( -name '*.framework' -o -name '*.dylib' \) \
      -exec codesign --force --sign - --timestamp=none {} \; >&2
  fi
  codesign --force --sign - --timestamp=none --entitlements "$work/adhoc/nse.entitlements" \
    "$signed/PlugIns/NotificationService.appex" >&2
  codesign --force --sign - --timestamp=none --entitlements "$work/adhoc/app.entitlements" "$signed" >&2
  echo "临时签名：主包 keychain-access-groups ${groups[*]}，扩展 ${prefix}com.lynricsy.oryn.push" >&2
  echo "$signed"
}

# round <轮号> <说明> <Oryn.app>：装包、跑一遍 UI 测试与三项探测。结果在 round_core（P1 / 自检 / challenge 全过为 1）
# 与 round_nse（NSE 解开为 1、被调起没解开为 0、没被调起为 -1）。
round() {
  local n=$1 label=$2 app=$3 rc
  round_core=1
  round_nse=-1
  note ""
  note "#### 第 ${n} 轮：${label}"
  sync="$work/sync$n"
  mkdir -p "$sync"
  mark=$(wc -l <"$plog" | tr -d ' ')
  round_start=$(date '+%Y-%m-%d %H:%M:%S')

  app_stop
  xcrun simctl install "$udid" "$app"
  local data
  data=$(xcrun simctl get_app_container "$udid" "$bundle" data)
  mkdir -p "$data/tmp"
  rm -f "$data/tmp/events.jsonl"

  # UI 测试在后台跑；TEST_RUNNER_ 前缀的变量由 xcodebuild 去掉前缀后交给测试进程（见 E2ESupport.swift 的交接）
  local xlog="$out/xcodebuild-$n.log"
  rm -rf "$out/push-$n.xcresult"
  TEST_RUNNER_E2E_SYNC="$sync" TEST_RUNNER_E2E_LOG="$data/tmp/events.jsonl" TEST_RUNNER_E2E_WAIT_SCALE="$wait_scale" \
    xcodebuild test-without-building "${xcb[@]}" -only-testing:OrynE2E/PushE2ETests -parallel-testing-enabled NO \
    -resultBundlePath "$out/push-$n.xcresult" >"$xlog" 2>&1 &
  runner=$!

  # P1 钩子、自检与 challenge（App 在前台）
  req=""
  if wait_req p-launched 300; then
    if jq -e '.alert == true' <<<"$req" >/dev/null; then
      note "系统授权框：已点「允许」"
    else
      note "系统授权框：没有出现（这台模拟器之前已决定过通知权限）"
    fi
    if wait_log 60 'probe\.start'; then
      if wait_log 90 'apns\.(registered|register_failed)'; then
        note "- P1 钩子：AppDelegate 收到了远程通知注册回调"
      else
        note "- P1 钩子：$((90 * wait_scale)) 秒内既没有 apns.registered 也没有 apns.register_failed（UIKit 没调到补上的回调）"
        round_core=0
      fi
      show 'apns\.hook'
      show 'apns\.(registered|register_failed)'
      if seen 'probe\.vector ok=true' >/dev/null; then
        note "- 自检：App 进程里 WebPushDecrypt 解开了 RFC 8291 附录 A"
      else
        note "- 自检：没有 probe.vector ok=true（WebPushDecrypt 解不开 RFC 8291 附录 A）"
        round_core=0
      fi
      show 'probe\.vector'
      note "探针写入接收端密钥："
      show 'probe\.keys'
    else
      note "- 探针没有启动（没有 probe.start）：debug 构建的 #if DEBUG 或环境变量 ORYN_PUSH_PROBE 没生效"
      round_core=0
    fi

    xcrun simctl push "$udid" "$bundle" "$here/ios-push/challenge.apns"
    if wait_log 30 'apns\.challenge foreground=true'; then
      note "- challenge：前台收到网关 challenge，未展示并转成 pushchallenge 事件"
      show 'apns\.challenge'
    else
      note "- challenge：$((30 * wait_scale)) 秒内没有 apns.challenge foreground=true（willPresent 没收到 simctl push 的 challenge）"
      round_core=0
    fi
    ok p-launched
  else
    note "- UI 测试没有走到 p-launched（App 没启动或授权框没处理，见 $(basename "$xlog")）"
    round_core=0
    # 测试若还在等，让它带着原因失败退出，下面的 wait 不会挂到步骤超时
    printf '脚本没等到 p-launched' >"$sync/p-launched.fail"
  fi

  # P3 NSE（App 已退出）
  app_stop
  sleep 2
  xcrun simctl push "$udid" "$bundle" "$here/ios-push/notify.apns"
  if wait_log 60 'nse\.received'; then
    round_nse=0
    wait_log 30 'nse\.(content parsed|decrypted ok=false|keys found=false|payload ok=false|expired)' || true
    sleep 1
    sim_shot "$out/notify-$n.png" 30 || true
    if seen 'nse\.decrypted ok=true' >/dev/null && seen 'nse\.content parsed=true' >/dev/null; then
      round_nse=1
      note "- NSE：解开了 notify.apns 并换上真实标题与正文（横幅截图 notify-$n.png）"
    elif seen 'nse\.keys found=false' >/dev/null; then
      note "- NSE：被调起但读不到共享钥匙串里的密钥（nse.keys found=false）"
    else
      note "- NSE：被调起但没有解开 notify.apns"
    fi
    show 'nse\.received'
    show 'nse\.keys'
    show 'nse\.decrypted'
    show 'nse\.content'
  else
    note "- NSE：$((60 * wait_scale)) 秒内没有 nse.received（模拟器没有为 simctl push 调起 Notification Service Extension）"
  fi

  if [[ -f $sync/p-done.req ]] || wait_req p-done 60; then ok p-done; fi
  set +e
  wait "$runner"
  rc=$?
  set -e
  runner=""
  if ((rc != 0)); then
    note "- UI 测试失败（xcodebuild 退出码 ${rc}，见产物 push/$(basename "$xlog")）"
    tail -60 "$xlog" || true
    round_core=0
  fi
}

# ---------------------------------------------------------------- 轮次与门禁

nse_ok=0
nse_seen=0
case $adhoc in
  1)
    signed=$(adhoc_copy)
    round 1 "临时签名包（PUSH_ADHOC_SIGN=1）" "$signed"
    ;;
  *)
    round 1 "不签名包（与 ios-check 编出的包相同）" "$built"
    ;;
esac
if ((round_core == 1)); then
  pass "第 1 轮：P1 钩子、自检与 challenge 全部通过"
else
  bad "第 1 轮：P1 钩子、自检或 challenge 没有通过（详见上方第 1 轮的记录）"
fi
if ((round_nse == 1)); then nse_ok=1; fi
if ((round_nse >= 0)); then nse_seen=1; fi

if [[ $adhoc == auto ]] && ((nse_ok == 0)); then
  signed=$(adhoc_copy)
  round 2 "临时签名包（第 1 轮 NSE 没通过，自动重试）" "$signed"
  if ((round_nse == 1)); then nse_ok=2; fi
  if ((round_nse >= 0)); then nse_seen=1; fi
fi

note ""
if ((nse_ok == 1)); then
  pass "P3 NSE：第 1 轮解开了加密通知"
elif ((nse_ok == 2)); then
  pass "P3 NSE：不签名包没解开，临时签名后解开（模拟器需要签入 keychain-access-groups 才能共享钥匙串）"
else
  if ((nse_seen == 1)); then
    msg="P3 NSE：NSE 被调起但没有解开加密通知（各轮记录见上）"
  else
    msg="P3 NSE：模拟器没有为 simctl push 调起 Notification Service Extension"
  fi
  # 变量后紧跟全角字符时一律写 ${…}：macOS 的 bash 3.2 会把多字节字符的字节当成变量名的一部分（set -u 下 unbound variable）
  if [[ $nse_gate == hard ]]; then bad "$msg"; else warn "${msg}（PUSH_NSE_GATE=soft，只记录）"; fi
fi

# ---------------------------------------------------------------- 收尾

kill "$streamer" 2>/dev/null || true
wait "$streamer" 2>/dev/null || true
streamer=""
xcrun simctl spawn "$udid" log show --start "$start" --style compact --predicate "$predicate" >"$out/push.log" 2>/dev/null || true
note ""
note "子系统 com.lynricsy.oryn.push 的完整日志（push.log）："
lines=$(grep -E 'apns\.|probe\.|nse\.|push\.' "$out/push.log" || true)
printf '%s\n' "${lines:-（空）}" | quote

# 走到这里才写完成标记：工作流据此确认脚本跑完了全部断言，防止中途异常退出却返回 0
if ((failed == 0)); then : >"$out/.complete"; fi
exit "$failed"
