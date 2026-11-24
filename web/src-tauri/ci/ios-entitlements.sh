#!/usr/bin/env bash
# 把主 App target 需要的权限合并进 tauri ios init 生成的 `<app>_iOS.entitlements`（gen/apple 不入库，每次生成工程后都要跑一次）：
# - com.apple.developer.usernotifications.communication = true：与 web/src-tauri/Info.ios.plist 的 NSUserActivityTypes
#   (INSendMessageIntent) 配合启用通信通知，缺一不可；
# - aps-environment：APNs 推送，取环境变量 ORYN_APS_ENVIRONMENT（development | production，缺省 development；发版用 production）；
# - keychain-access-groups：依次为 $(AppIdentifierPrefix)com.lynricsy.oryn 与 $(AppIdentifierPrefix)com.lynricsy.oryn.push。
#   第一项是默认组（新钥匙串项写在这里），保持旧设备令牌所在的组不变；第二项与 Notification Service Extension 共享
#   推送接收端密钥（见 plugins/oryn-mobile/ios/Sources/Shared/PushKeyStore.swift）。
# 只处理文件名以 `_iOS.entitlements` 结尾的那一个：NSE 的 entitlements 由 XcodeGen 模板（web/src-tauri/ios/project.yml）直接生成。
# 仅在 Apple Developer Portal / 描述文件里勾选能力而不写入 target entitlements，codesign 不会把权限签入 IPA。
# 用法（在 web/ 下）：[ORYN_APS_ENVIRONMENT=production] bash src-tauri/ci/ios-entitlements.sh [gen/apple 目录]
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
apple_dir=${1:-"$here/gen/apple"}
aps=${ORYN_APS_ENVIRONMENT:-development}

[[ -d "$apple_dir" ]] || { echo "::error::没有 $apple_dir（先 tauri ios init）"; exit 1; }
[[ "$aps" == development || "$aps" == production ]] ||
  { echo "::error::ORYN_APS_ENVIRONMENT 只能是 development 或 production，当前 ${aps}"; exit 1; }

shopt -s nullglob
projects=("$apple_dir"/*.xcodeproj/project.pbxproj)
[[ ${#projects[@]} -eq 1 ]] || { echo "::error::$apple_dir 下应恰好有一个 .xcodeproj，找到 ${#projects[@]} 个"; exit 1; }
pbx=${projects[0]}

python3 - "$apple_dir" "$pbx" "$aps" <<'PY'
import os
import plistlib
import re
import sys

apple_dir = os.path.realpath(sys.argv[1])
pbx_path = sys.argv[2]
aps = sys.argv[3]

with open(pbx_path, "r", encoding="utf-8") as f:
    pbx = f.read()

# 匹配构建配置中的 CODE_SIGN_ENTITLEMENTS = path; 或 CODE_SIGN_ENTITLEMENTS = "path";
matches = re.findall(r'CODE_SIGN_ENTITLEMENTS\s*=\s*(?:"([^"]+)"|([^;\s]+))\s*;', pbx)
rel_paths = []
for quoted, bare in matches:
    p = (quoted or bare).strip()
    if p.endswith("_iOS.entitlements") and p not in rel_paths:
        rel_paths.append(p)

if len(rel_paths) != 1:
    print(f"::error::{pbx_path} 中应恰好有一个以 _iOS.entitlements 结尾的 CODE_SIGN_ENTITLEMENTS，找到 {rel_paths}，iOS 工程模板可能已变化", file=sys.stderr)
    sys.exit(1)

want = {
    "com.apple.developer.usernotifications.communication": True,
    "aps-environment": aps,
    "keychain-access-groups": [
        "$(AppIdentifierPrefix)com.lynricsy.oryn",
        "$(AppIdentifierPrefix)com.lynricsy.oryn.push",
    ],
}

rel = rel_paths[0]
for prefix in ("$(SRCROOT)/", "${SRCROOT}/"):
    if rel.startswith(prefix):
        rel = rel[len(prefix):]
if os.path.isabs(rel) or rel.startswith("$"):
    print(f"::error::不支持的 CODE_SIGN_ENTITLEMENTS 路径：{rel}", file=sys.stderr)
    sys.exit(1)
target = os.path.realpath(os.path.join(apple_dir, rel))
if os.path.commonpath([apple_dir, target]) != apple_dir:
    print(f"::error::CODE_SIGN_ENTITLEMENTS 越出 {apple_dir}：{rel}", file=sys.stderr)
    sys.exit(1)

os.makedirs(os.path.dirname(target), exist_ok=True)
if os.path.exists(target) and os.path.getsize(target) > 0:
    with open(target, "rb") as f:
        data = plistlib.load(f)
    if not isinstance(data, dict):
        print(f"::error::{target} 顶层不是 plist dict", file=sys.stderr)
        sys.exit(1)
else:
    data = {}

data.update(want)
with open(target, "wb") as f:
    plistlib.dump(data, f, fmt=plistlib.FMT_XML, sort_keys=True)

# 回读确认写入生效
with open(target, "rb") as f:
    verify = plistlib.load(f)
for key, value in want.items():
    if verify.get(key) != value:
        print(f"::error::{target} 回读校验失败：{key} = {verify.get(key)!r}，应为 {value!r}", file=sys.stderr)
        sys.exit(1)

print(f"已写入 communication=true、aps-environment={aps}、keychain-access-groups（2 项）-> {rel}（共 {len(verify)} 项权限）")
PY
