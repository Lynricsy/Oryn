#!/usr/bin/env bash
# 断言当前 Xcode 自带的 iOS SDK 不低于给定版本，版本写进 Step Summary。mobile.yml（ios-check / ios-release）与
# readme-ios-shots.yml 共用。按 SDK 版本判断而不是 Xcode 主版本：决定行为的是编进包里的 SDK——
#   - 27：App Store 自 2027-04 起只收 iOS 27 SDK 构建的包（ITMS-90725）；
#   - 27.1：iPhone Duo 上 App 铺满整块屏（27.0 SDK 构建的包在内屏右侧给状态栏留一条空白），系统工具栏 / 标签栏可竖排到右缘。
# 用法：require-ios-sdk.sh <最低 iOS SDK 版本>
set -euo pipefail
min=${1:?用法：require-ios-sdk.sh <最低 iOS SDK 版本>}
xcode=$(xcodebuild -version | awk 'NR==1 { print $2 }')
sdk=$(xcrun --sdk iphoneos --show-sdk-version)
echo "Xcode ${xcode}，iOS SDK ${sdk}" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
if [[ $(printf '%s\n%s\n' "$min" "$sdk" | sort -V | head -1) != "$min" ]]; then
  echo "::error::需要 iOS ${min}+ SDK（Xcode ${min}+），当前 Xcode ${xcode} 带的是 iOS ${sdk} SDK"
  exit 1
fi
