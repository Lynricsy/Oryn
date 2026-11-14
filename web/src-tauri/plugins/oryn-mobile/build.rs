fn main() {
    // 命令表必须为空：不生成任何权限，远程页面与 iframe 都调不到原生命令（只有 Rust 经 run_mobile_plugin 调用）。
    // 往这里加一个名字就等于给远程页面开了口子。
    tauri_plugin::Builder::new(&[])
        .android_path("android")
        .build();
}
