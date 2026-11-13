fn main() {
    // 应用自定义命令：AppManifest::commands 为每个命令生成 `allow-<命令名，下划线换连字符>` 权限。
    // 连接页（本地）只拿 launcher_*（capabilities/launcher.json）；远程页面的 host_* 在进入应用时于运行期按 origin 添加。
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
        ]),
    ))
    .unwrap();
}
