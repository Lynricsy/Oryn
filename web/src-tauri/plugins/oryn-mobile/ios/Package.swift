// swift-tools-version:5.3
// Oryn 移动端原生能力的 iOS 部分。包名与静态库产物名必须等于 crate 名（CARGO_PKG_NAME）：
// tauri-plugin 构建期用它调 link_apple_library，经 swift-rs 编成静态库链进 Rust。
// platforms 保持 iOS 13：swift-rs 以 IPHONEOS_DEPLOYMENT_TARGET（缺省 13.0）为目标版本，iOS 26 的 API 一律 @available 标注。

import PackageDescription

let package = Package(
  name: "tauri-plugin-oryn-mobile",
  platforms: [
    .macOS(.v10_13),
    .iOS(.v13),
  ],
  products: [
    .library(
      name: "tauri-plugin-oryn-mobile",
      type: .static,
      targets: ["tauri-plugin-oryn-mobile"])
  ],
  dependencies: [
    .package(name: "Tauri", path: "../.tauri/tauri-api")
  ],
  targets: [
    .target(
      name: "tauri-plugin-oryn-mobile",
      dependencies: [
        .byName(name: "Tauri")
      ],
      path: "Sources")
  ]
)
