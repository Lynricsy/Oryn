#!/usr/bin/env bash
# 把 iOS Communication Notifications 所需权限（com.apple.developer.usernotifications.communication = true）
# 合并进 tauri ios init 生成的 target entitlements（gen/apple 不入库，每次生成工程后都要跑一次）。
# 与 web/src-tauri/Info.ios.plist 里的 NSUserActivityTypes (INSendMessageIntent) 配合，缺一不可：
# 仅在 Apple Developer Portal / 描述文件里勾选能力而不写入 target entitlements，codesign 不会把权限签入 IPA。
# 用法（在 web/ 下）：bash src-tauri/ci/ios-entitlements.sh [gen/apple 目录]
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
apple_dir=${1:-"$here/gen/apple"}

[[ -d "$apple_dir" ]] || { echo "::error::没有 $apple_dir（先 tauri ios init）"; exit 1; }

shopt -s nullglob
projects=("$apple_dir"/*.xcodeproj/project.pbxproj)
[[ ${#projects[@]} -eq 1 ]] || { echo "::error::$apple_dir 下应恰好有一个 .xcodeproj，找到 ${#projects[@]} 个"; exit 1; }
pbx=${projects[0]}

python3 - "$apple_dir" "$pbx" <<'PY'
import os
import plistlib
import re
import sys

apple_dir = os.path.realpath(sys.argv[1])
pbx_path = sys.argv[2]

with open(pbx_path, "r", encoding="utf-8") as f:
    pbx = f.read()

# 匹配构建配置中的 CODE_SIGN_ENTITLEMENTS = path; 或 CODE_SIGN_ENTITLEMENTS = "path";
matches = re.findall(r'CODE_SIGN_ENTITLEMENTS\s*=\s*(?:"([^"]+)"|([^;\s]+))\s*;', pbx)
rel_paths = []
for quoted, bare in matches:
    p = (quoted or bare).strip()
    if p and p not in rel_paths:
        rel_paths.append(p)

if not rel_paths:
    print(f"::error::{pbx_path} 中未找到 CODE_SIGN_ENTITLEMENTS，iOS 工程模板可能已变化", file=sys.stderr)
    sys.exit(1)

KEY = "com.apple.developer.usernotifications.communication"

for rel in rel_paths:
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

    data[KEY] = True
    with open(target, "wb") as f:
        plistlib.dump(data, f, fmt=plistlib.FMT_XML, sort_keys=True)

    # 回读确认写入生效
    with open(target, "rb") as f:
        verify = plistlib.load(f)
    if verify.get(KEY) is not True:
        print(f"::error::{target} 回读校验失败：{KEY} != True", file=sys.stderr)
        sys.exit(1)

    print(f"已写入 {KEY}=true -> {rel}（共 {len(verify)} 项权限）")
PY
