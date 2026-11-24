#!/usr/bin/env bash
# Notification Service Extension 的版本号与发版签名（tauri ios init 之后、tauri ios build 之前跑）。
# - NSE 的 CFBundleShortVersionString 设为 <版本>；CFBundleVersion 有构建号时为 <版本>.<构建号>（与 tauri-cli
#   mobile/ios/build.rs 给主包的拼法一致），否则为 <版本>。App Store 要求扩展与主包的两个版本号相同；
#   tauri 只改主包的 Info.plist，NSE 的由 XcodeGen 在 init 时生成到 gen/apple/NotificationService/Info.plist。
# - 设了 IOS_NSE_MOBILE_PROVISION（com.lynricsy.oryn.NotificationService 的 App Store Connect 描述文件，base64）时另做三件事：
#   1. 解码描述文件，装到 ~/Library/MobileDevice/Provisioning Profiles/ 与 ~/Library/Developer/Xcode/UserData/Provisioning Profiles/；
#   2. 把 web/src-tauri/ios/NotificationService/Signing.xcconfig 写成手动签名：身份 Apple Distribution、团队 XDPWT9ABRD、
#      该描述文件的 UUID（主包的签名由 tauri 按 IOS_CERTIFICATE / IOS_MOBILE_PROVISION 同步，只认名字含 _iOS 的 target）；
#   3. 在 gen/apple/ExportOptions.plist 的 provisioningProfiles 下加 com.lynricsy.oryn.NotificationService → UUID
#      （tauri 2.12.6 导出时合并这张表，不会覆盖扩展的条目）。
# 只在 macOS 上运行（security cms）。secret 只经环境变量进来，不打印、不落到工作目录以外的持久位置。
# 用法（在 web/ 下）：[IOS_NSE_MOBILE_PROVISION=…] bash src-tauri/ci/ios-nse.sh <版本> [构建号]
set -euo pipefail

if (($# < 1 || $# > 2)); then
  echo "用法：$0 <版本> [构建号]" >&2
  exit 2
fi
version=$1
build=${2:-}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
apple_dir="$here/gen/apple"
plist="$apple_dir/NotificationService/Info.plist"
xcconfig="$here/ios/NotificationService/Signing.xcconfig"
export_options="$apple_dir/ExportOptions.plist"
nse_id=com.lynricsy.oryn.NotificationService
team=XDPWT9ABRD

[[ -f "$plist" ]] || {
  echo "::error::没有 $plist：自定义 XcodeGen 模板（tauri.conf.json 的 bundle.iOS.template）没有生效，或还没 tauri ios init"
  exit 1
}

bundle_version=$version${build:+.$build}
python3 - "$plist" "$version" "$bundle_version" <<'PY'
import plistlib
import sys

path, short, full = sys.argv[1:4]
with open(path, "rb") as f:
    info = plistlib.load(f)
info["CFBundleShortVersionString"] = short
info["CFBundleVersion"] = full
with open(path, "wb") as f:
    plistlib.dump(info, f, fmt=plistlib.FMT_XML)
PY
echo "NSE 版本：${version}（${bundle_version}）"

if [[ -z "${IOS_NSE_MOBILE_PROVISION:-}" ]]; then
  echo "没有 IOS_NSE_MOBILE_PROVISION：NSE 沿用 Signing.xcconfig 的缺省（自动签名 / 不签名）"
  exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
printf '%s' "$IOS_NSE_MOBILE_PROVISION" | base64 --decode >"$work/nse.mobileprovision"
security cms -D -i "$work/nse.mobileprovision" >"$work/nse.plist"
read -r uuid app_id < <(python3 - "$work/nse.plist" <<'PY'
import plistlib
import sys

with open(sys.argv[1], "rb") as f:
    profile = plistlib.load(f)
print(profile["UUID"], profile.get("Entitlements", {}).get("application-identifier", ""))
PY
)
if [[ "$app_id" != "$team.$nse_id" ]]; then
  echo "::error::IOS_NSE_MOBILE_PROVISION 的 application-identifier 是「${app_id}」，应为 ${team}.${nse_id}"
  exit 1
fi

for dir in "$HOME/Library/MobileDevice/Provisioning Profiles" "$HOME/Library/Developer/Xcode/UserData/Provisioning Profiles"; do
  mkdir -p "$dir"
  cp "$work/nse.mobileprovision" "$dir/$uuid.mobileprovision"
done

cat >"$xcconfig" <<EOF
// 由 ci/ios-nse.sh 生成（发版构建的手动签名）；不要提交。
CODE_SIGN_STYLE = Manual
CODE_SIGN_IDENTITY = Apple Distribution
CODE_SIGN_IDENTITY[sdk=iphoneos*] = Apple Distribution
DEVELOPMENT_TEAM = $team
PROVISIONING_PROFILE_SPECIFIER = $uuid
PROVISIONING_PROFILE_SPECIFIER[sdk=iphoneos*] = $uuid
EOF

python3 - "$export_options" "$nse_id" "$uuid" <<'PY'
import os
import plistlib
import sys

path, bundle_id, uuid = sys.argv[1:4]
options = {}
if os.path.exists(path):
    with open(path, "rb") as f:
        options = plistlib.load(f)
profiles = options.get("provisioningProfiles") or {}
profiles[bundle_id] = uuid
options["provisioningProfiles"] = profiles
with open(path, "wb") as f:
    plistlib.dump(options, f, fmt=plistlib.FMT_XML)
PY
echo "NSE 签名：手动（Apple Distribution，描述文件 ${uuid}），已写入 Signing.xcconfig 与 ExportOptions.plist"
