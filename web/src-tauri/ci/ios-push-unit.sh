#!/usr/bin/env bash
# shellcheck source-path=SCRIPTDIR
# 推送共享代码的单元测试（mobile.yml 的 ios-check「模拟器：推送共享代码单元测试」，硬门禁）。
# 把 plugins/oryn-mobile/ios/Sources/Shared/*.swift（NSE 与插件共用的那一份）与 ci/ios-push/notify.apns 拷进
# ci/ios-push-unit 的 XcodeGen 工程，在模拟器上跑 PushSharedTests：附录 A 向量、notify.apns 解密 → 解析 → 构造通知、
# 篡改与错 auth 失败、钥匙串读写。模拟器不为 simctl push 调起 NSE（M28 P3），NSE 的处理链在这里把关。
# 结果写 <输出目录>/summary.md，xcresult 存 <输出目录>/unit.xcresult；全部通过才写 <输出目录>/.complete。
# 用法：ios-push-unit.sh <输出目录>
set -euo pipefail
if (($# != 1)); then
  echo "用法：$0 <输出目录>" >&2
  exit 2
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
here=$(cd "$(dirname "$0")" && pwd)
shared="$here/../plugins/oryn-mobile/ios/Sources/Shared"
summary="$out/summary.md"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
rm -f "$out/.complete"

command -v xcodegen >/dev/null || brew install xcodegen
# shellcheck source=ios-sim.sh
source "$here/ios-sim.sh"
pick=$(pick_iphone)
udid=$(jq -r .udid <<<"$pick")

cp -R "$here/ios-push-unit" "$work/proj"
mkdir -p "$work/proj/Shared" "$work/proj/Fixtures"
cp "$shared"/*.swift "$work/proj/Shared/"
cp "$here/ios-push/notify.apns" "$work/proj/Fixtures/"
(cd "$work/proj" && xcodegen generate --quiet)

{
  echo "### iOS 推送共享代码单元测试（NSE 的解密与内容构造）"
  echo ""
  echo "模拟器：$(jq -r '"\(.name)（iOS \(.os)）"' <<<"$pick")"
  echo "源码：$(cd "$shared" && printf '%s ' ./*.swift)"
  echo ""
} >"$summary"

rm -rf "$out/unit.xcresult"
set +e
xcodebuild test -project "$work/proj/OrynPushUnit.xcodeproj" -scheme PushUnit -destination "id=$udid" \
  -derivedDataPath "$work/dd" -resultBundlePath "$out/unit.xcresult" >"$out/xcodebuild.log" 2>&1
rc=$?
set -e

{
  echo '```'
  grep -E "Test Case '.*' (passed|failed)|error: |Executed [0-9]+ test" "$out/xcodebuild.log" || echo "（没有测试输出，见 xcodebuild.log）"
  echo '```'
} >>"$summary"
if ((rc != 0)); then
  echo "- ❌ 单元测试失败（xcodebuild 退出码 ${rc}）" >>"$summary"
  tail -60 "$out/xcodebuild.log"
  exit 1
fi
echo "- ✅ 全部通过" >>"$summary"
: >"$out/.complete"
