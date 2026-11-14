fn main() {
    // 应用自定义命令：AppManifest::commands 为每个命令生成 `allow-<命令名，下划线换连字符>` 权限。
    // 连接页（本地）只拿 launcher_*（capabilities/launcher.json；移动端另有 launcher-mobile.json）；远程页面的 host_* 在进入应用时于运行期按 origin 添加。
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        tauri_build::AppManifest::new().commands(&[
            "launcher_state",
            "launcher_connect",
            "launcher_cancel",
            "launcher_retry",
            "launcher_open_browser",
            "launcher_change_server",
            "host_ready",
            "host_notify",
            "host_set_status",
            "host_set_language",
            "host_reauth",
            "host_info",
            "host_set_settings",
            "host_check_update",
            "host_install_update",
            "host_change_server",
            // 桌面自绘标题栏（无系统标题栏）：连接页（capabilities/launcher-desktop.json）与远程页面都可用
            "host_window",
            // 移动端（M7）：只在移动端注册，远程能力按平台给权限（webview::HOST_PERMISSIONS）
            "host_mobile_info",
            "host_push_enable",
            "host_push_disable",
            "host_open_external",
            "host_share_ack",
            "host_set_chrome",
            "host_open_notification_settings",
        ]),
    ))
    .unwrap();
}
