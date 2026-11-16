# shellcheck shell=bash
# iOS 模拟器 CI 脚本的公共部分：ios-tabbar-check.sh（原生演示）与 ios-e2e-check.sh（真实网页 + 真实触摸）都 source 它，
# 两步用同一台模拟器。

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
