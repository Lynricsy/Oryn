//! Oryn 桌面壳：加载所配对服务器自己的 Web UI，壳内只内置连接页。
//!
//! 模块分工见 `docs/desktop.md` 与 M6 计划：配对 / 会话换取（`session`）、宿主命令（`host`）、
//! 主窗口与远程能力（`webview`）、托盘 / 快捷键 / 更新 / 下载（仅桌面）。

mod api;
#[cfg(desktop)]
mod chrome;
mod config;
mod deeplink;
mod eventlog;
mod host;
mod i18n;
#[cfg(mobile)]
mod mobile;
mod mobile_logic;
mod notify;
mod secrets;
mod session;
mod webview;

#[cfg(desktop)]
mod download;
#[cfg(desktop)]
mod shortcut;
#[cfg(desktop)]
mod tray;
#[cfg(desktop)]
mod updater;

#[cfg(desktop)]
use std::path::PathBuf;
use std::sync::Arc;
#[cfg(desktop)]
use std::time::Duration;

use serde_json::json;
use tauri::{App, AppHandle, Manager, RunEvent};

use crate::api::HttpApi;
use crate::config::Config;
use crate::host::Desktop;
use crate::secrets::Vault;
use crate::session::{Core, SharedNav};
use crate::webview::{RemoteOrigin, TauriShell};

/// 冒烟模式的超时。
#[cfg(desktop)]
const SMOKE_TIMEOUT: Duration = Duration::from_secs(30);

/// 命令行参数。深链接 URL 由 deep-link 插件自己从 argv 取。
///
/// argv 里只要出现 `oryn:` 链接，就只当作「打开这个链接」处理，其它参数一律忽略：Windows 上协议注册为
/// `"exe" "%1"`，转发了原始引号的调用方能借链接往 argv 里塞 `--server=…`、`--quit` 之类的标志。
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct Args {
    /// `--server <url>`：预填并直接连接。
    pub server: Option<String>,
    /// `--quit`：让已运行的实例正常退出；没有实例时直接退出 0。
    pub quit: bool,
    /// `--smoke`：临时配置目录、不注册单实例、不碰凭据库与网络，连接页就绪即退出 0。
    pub smoke: bool,
    /// 因 argv 里有深链接而被忽略的其它参数（记 `deeplink.args_ignored`）。
    pub ignored: Vec<String>,
}

/// 是不是 `oryn:` 链接（scheme 不分大小写）。
fn is_deep_link(arg: &str) -> bool {
    arg.trim()
        .get(..5)
        .is_some_and(|p| p.eq_ignore_ascii_case("oryn:"))
}

impl Args {
    /// 解析 argv（不含程序路径）。
    pub fn parse<I: IntoIterator<Item = String>>(args: I) -> Self {
        let args: Vec<String> = args.into_iter().collect();
        if args.iter().any(|a| is_deep_link(a)) {
            return Args {
                ignored: args.into_iter().filter(|a| !is_deep_link(a)).collect(),
                ..Args::default()
            };
        }
        let mut out = Args::default();
        let mut it = args.into_iter();
        while let Some(a) = it.next() {
            match a.as_str() {
                "--quit" => out.quit = true,
                "--smoke" => out.smoke = true,
                "--server" => out.server = it.next(),
                other => {
                    if let Some(v) = other.strip_prefix("--server=") {
                        out.server = Some(v.to_string());
                    }
                }
            }
        }
        out
    }
}

#[cfg(desktop)]
fn smoke_dir() -> PathBuf {
    std::env::temp_dir().join(format!("oryn-desktop-smoke-{}", std::process::id()))
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let raw: Vec<String> = std::env::args().skip(1).collect();
    let args = Args::parse(raw.iter().cloned());
    log_ignored(&args);

    // 插件顺序：single_instance（必须第一个）→ deep_link → window_state → global_shortcut
    // → notification（Android 不注册）→ opener → updater；
    // 移动端：deep_link → opener → oryn-mobile（Android）/ notification（iOS）→ barcode-scanner
    #[allow(unused_mut)]
    let mut builder = tauri::Builder::default();
    #[cfg(desktop)]
    if !args.smoke {
        builder = builder.plugin(tauri_plugin_single_instance::init(on_second_instance));
    }
    builder = builder.plugin(tauri_plugin_deep_link::init());
    #[cfg(desktop)]
    {
        let mut ws = tauri_plugin_window_state::Builder::default()
            .with_state_flags(host::window_state_flags());
        if args.smoke {
            ws = ws.with_filename(smoke_dir().join(".window-state.json").to_string_lossy());
        }
        builder = builder.plugin(ws.build()).plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(shortcut::on_event)
                .build(),
        );
    }
    #[cfg(desktop)]
    {
        builder = builder
            .plugin(tauri_plugin_notification::init())
            .plugin(tauri_plugin_opener::init())
            .plugin(tauri_plugin_updater::Builder::new().build());
    }
    #[cfg(mobile)]
    {
        builder = builder.plugin(tauri_plugin_opener::init());
        #[cfg(target_os = "android")]
        {
            builder = builder.plugin(tauri_plugin_oryn_mobile::init());
        }
        #[cfg(target_os = "ios")]
        {
            builder = builder.plugin(tauri_plugin_notification::init());
        }
        builder = builder.plugin(tauri_plugin_barcode_scanner::init());
    }

    let setup_args = args.clone();
    let app = builder
        .invoke_handler(tauri::generate_handler![
            host::launcher_state,
            host::launcher_connect,
            host::launcher_cancel,
            host::launcher_retry,
            host::launcher_open_browser,
            host::launcher_change_server,
            host::host_ready,
            host::host_notify,
            host::host_set_status,
            host::host_set_language,
            host::host_reauth,
            host::host_change_server,
            #[cfg(desktop)]
            host::host_info,
            #[cfg(desktop)]
            host::host_set_settings,
            #[cfg(desktop)]
            host::host_check_update,
            #[cfg(desktop)]
            host::host_install_update,
            #[cfg(desktop)]
            host::host_window,
            #[cfg(mobile)]
            mobile::host_mobile_info,
            #[cfg(mobile)]
            mobile::host_push_enable,
            #[cfg(mobile)]
            mobile::host_push_disable,
            #[cfg(mobile)]
            mobile::host_open_external,
            #[cfg(mobile)]
            mobile::host_share_ack,
            #[cfg(mobile)]
            mobile::host_set_chrome,
            #[cfg(mobile)]
            mobile::host_open_notification_settings,
        ])
        .setup(move |app| setup(app, &setup_args, &raw))
        .build(tauri::generate_context!())
        .expect("error while building Oryn");
    let smoke = args.smoke;
    app.run(move |app, event| on_run_event(app, event, smoke));
}

fn log_ignored(args: &Args) {
    if !args.ignored.is_empty() {
        eventlog::log("deeplink.args_ignored", json!({"args": args.ignored}));
    }
}

#[cfg(desktop)]
fn setup(app: &mut App, args: &Args, raw: &[String]) -> Result<(), Box<dyn std::error::Error>> {
    let handle = app.handle().clone();
    // 一律用 Tauri 配置的版本（`--config` 可覆盖，更新器比较的也是它），不用 crate 版本
    let version = handle.package_info().version.to_string();
    api::set_app_version(version.clone());
    if args.quit {
        // 走到这里说明没有已运行的实例（否则单实例插件已把参数转交并退出）
        handle.cleanup_before_exit();
        std::process::exit(0);
    }
    let dir = if args.smoke {
        smoke_dir()
    } else {
        handle.path().app_config_dir()?
    };
    std::fs::create_dir_all(&dir)?;
    eventlog::log("app.start", json!({"version": version, "args": raw}));

    let config = Config::load(&dir);
    let vault = if args.smoke {
        Vault::file_only(&dir)
    } else {
        Vault::open(config.token_store, &dir)
    };

    let remote = RemoteOrigin::default();
    let window = webview::create_main(&handle, &remote)?;
    let launcher_url = webview::launcher_url(&window)?;
    host::wire_window(&handle, &window);
    let _ = window.set_focus();

    let nav = SharedNav::default();
    let shell = TauriShell::new(handle.clone(), launcher_url, remote);
    let core = Core::new(
        Arc::new(HttpApi),
        shell,
        eventlog::handle(),
        vault,
        dir,
        config,
        nav.clone(),
        api::device_name(),
        api::platform(),
        true,
    );
    let (shortcut_cfg, lang) = (core.config().shortcut.clone(), core.config().lang());
    let d = Arc::new(Desktop::new(core, nav, args.smoke, version));
    app.manage(d.clone());

    // 通知点击（macOS / Windows / iOS 经插件；Linux 由 notify::deliver 的等待线程直接调 notify::clicked；
    // Android 不带通知插件）
    #[cfg(not(any(target_os = "linux", target_os = "android")))]
    {
        use tauri_plugin_notification::NotificationExt;
        let click = handle.clone();
        handle.notification().on_action(move |p| {
            if p.action_id() != "tap" {
                return;
            }
            if let Some(n) = p.notification().map(|n| n.id()) {
                notify::clicked(&click, n);
            }
        })?;
    }

    match tray::build(&handle, lang) {
        Some(t) => {
            let _ = d.tray.set(t);
            d.set_tray_available(true);
        }
        None => d.set_tray_available(false),
    }
    let _ = d.shortcut.apply(&handle, &shortcut_cfg);

    deeplink::setup(&handle, !args.smoke);

    let server = if args.smoke {
        None
    } else {
        args.server.clone()
    };
    let start = d.clone();
    tauri::async_runtime::spawn(async move {
        let next = start.core.lock().await.start(server.as_deref()).await;
        host::schedule(start, next);
    });

    if args.smoke {
        let watch = d.clone();
        let app = handle.clone();
        tauri::async_runtime::spawn(async move {
            tokio::time::sleep(SMOKE_TIMEOUT).await;
            if !watch.launcher_seen() {
                eprintln!(
                    "oryn-desktop: smoke failed: launcher page did not call launcher_state within {}s",
                    SMOKE_TIMEOUT.as_secs()
                );
                app.exit(1);
            }
        });
    }
    Ok(())
}

/// 移动端：没有命令行、冒烟模式、托盘与单实例；令牌存 Keystore / Keychain，事件日志由插件给路径（Android）。
#[cfg(mobile)]
fn setup(app: &mut App, args: &Args, raw: &[String]) -> Result<(), Box<dyn std::error::Error>> {
    let _ = args;
    let handle = app.handle().clone();
    let version = handle.package_info().version.to_string();
    api::set_app_version(version.clone());

    #[cfg(target_os = "android")]
    let (device, native) = {
        use tauri_plugin_oryn_mobile::OrynMobileExt;
        let plugin = handle.oryn_mobile().clone();
        let device = plugin.device_info();
        if let Some(path) = device
            .as_ref()
            .ok()
            .and_then(|d| d.event_log_path.as_deref())
        {
            eventlog::init_file(std::path::Path::new(path));
        }
        (device, plugin)
    };

    let dir = handle.path().app_config_dir()?;
    std::fs::create_dir_all(&dir)?;
    eventlog::log("app.start", json!({"version": version, "args": raw}));

    #[cfg(target_os = "android")]
    let (device, device_name) = match device {
        Ok(d) => {
            let name = Some(d.name.trim())
                .filter(|n| !n.is_empty())
                .unwrap_or("Android")
                .chars()
                .take(100)
                .collect::<String>();
            (d, name)
        }
        Err(e) => {
            eventlog::log("device.error", json!({"error": e.to_string()}));
            (Default::default(), "Android".to_string())
        }
    };
    #[cfg(target_os = "ios")]
    let device_name = "iOS".to_string();

    let config = Config::load(&dir);
    #[cfg(target_os = "android")]
    let vault = Vault::with_store(
        Box::new(secrets::KeystoreStore(native.clone())),
        config::TokenStoreKind::Keystore,
    );
    #[cfg(target_os = "ios")]
    let vault = Vault::with_store(
        Box::new(secrets::KeychainStore),
        config::TokenStoreKind::Keychain,
    );

    let remote = RemoteOrigin::default();
    let launcher_url = webview::launcher_url(&handle)?;
    webview::create_main(&handle, &remote)?;
    #[cfg(target_os = "android")]
    mobile::manage_main_life(&handle, remote.clone());

    let nav = SharedNav::default();
    let shell = TauriShell::new(handle.clone(), launcher_url, remote);
    let core = Core::new(
        Arc::new(HttpApi),
        shell,
        eventlog::handle(),
        vault,
        dir,
        config,
        nav.clone(),
        device_name,
        api::platform(),
        false,
    );
    let d = Arc::new(Desktop::new(core, nav, false, version));
    app.manage(d.clone());

    // iOS 的通知点击经插件的 on_action 回来（同 macOS）；Android 经深链接
    #[cfg(target_os = "ios")]
    {
        use tauri_plugin_notification::NotificationExt;
        let click = handle.clone();
        handle.notification().on_action(move |p| {
            if p.action_id() != "tap" {
                return;
            }
            if let Some(n) = p.notification().map(|n| n.id()) {
                notify::clicked(&click, n);
            }
        })?;
    }

    #[cfg(target_os = "android")]
    {
        let rt = mobile::MobileRt::new(
            mobile::NativeImpl(native.clone()),
            device.webview_version.clone().unwrap_or_default(),
        );
        rt.core.set_insets(device.corner_radii, device.insets);
        app.manage(Arc::new(rt));
        mobile::listen(&handle, &native);
    }
    #[cfg(target_os = "ios")]
    app.manage(Arc::new(mobile::MobileRt::new(
        mobile::NativeImpl,
        String::new(),
    )));

    deeplink::setup(&handle, false);

    let start = d.clone();
    tauri::async_runtime::spawn(async move {
        // WebView 建好之前的 navigate 会被丢弃：先等它，再按已存令牌进入应用
        start.webview_up.wait().await;
        let next = start.core.lock().await.start(None).await;
        host::schedule(start, next);
    });
    Ok(())
}

#[cfg(desktop)]
fn on_second_instance(app: &AppHandle, argv: Vec<String>, _cwd: String) {
    eventlog::log("instance.second", json!({"args": argv}));
    // argv[0] 是程序路径；带深链接时 --quit 等标志同样忽略
    let args = Args::parse(argv.into_iter().skip(1));
    log_ignored(&args);
    if args.quit {
        host::quit(app);
        return;
    }
    // 深链接 URL 已由插件的 deep-link feature 转给 on_open_url
    host::show_main(app);
}

/// Android：Activity 被系统销毁（不是配置变更）时 Tauri 删掉唯一的窗口并请求退出。拦下这次退出，进程留在后台，
/// 用户回来时为同一进程里的新 Activity 重建主窗口（见 [`mobile_logic::MainWindowLife`]）。
#[cfg(mobile)]
fn on_run_event(app: &AppHandle, event: RunEvent, _smoke: bool) {
    #[cfg(target_os = "android")]
    match event {
        RunEvent::ExitRequested { code, api, .. } => {
            if mobile_logic::MainWindowLife::keep_process(code) {
                eventlog::log("exit.prevented", json!({}));
                api.prevent_exit();
            }
        }
        RunEvent::WindowEvent {
            label,
            event: tauri::WindowEvent::Destroyed,
            ..
        } if label == host::MAIN => mobile::main_destroyed(app),
        _ => {}
    }
    #[cfg(target_os = "ios")]
    let _ = (app, event);
}

#[cfg(desktop)]
fn on_run_event(app: &AppHandle, event: RunEvent, smoke: bool) {
    match event {
        RunEvent::ExitRequested {
            code: None, api, ..
        } => {
            // 最后一个窗口关闭：藏在托盘时不退出；「退出 Oryn」、--quit、更新重启走显式 exit / restart
            if host::desktop(app).is_some_and(|d| d.close_to_tray()) {
                api.prevent_exit();
            }
        }
        #[cfg(target_os = "macos")]
        RunEvent::Reopen { .. } => host::show_main(app),
        RunEvent::Exit if smoke => {
            let _ = std::fs::remove_dir_all(smoke_dir());
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::Args;

    fn parse(v: &[&str]) -> Args {
        Args::parse(v.iter().map(|s| s.to_string()))
    }

    #[test]
    fn args() {
        assert_eq!(parse(&[]), Args::default());
        assert_eq!(
            parse(&["--server", "https://a.example", "--smoke"]),
            Args {
                server: Some("https://a.example".into()),
                quit: false,
                smoke: true,
                ignored: Vec::new(),
            }
        );
        assert_eq!(
            parse(&["--server=https://b.example"]).server.as_deref(),
            Some("https://b.example")
        );
        assert!(parse(&["--quit"]).quit);
        assert_eq!(parse(&["--server"]).server, None);
    }

    #[test]
    fn deep_link_args_ignore_flags() {
        let link = "oryn://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab";
        for argv in [
            vec![link, "--quit"],
            vec!["--server=https://evil.example", link],
            vec![link, "--server", "https://evil.example", "--smoke"],
            vec!["ORYN://open", "--quit"],
            vec!["oryn:x\"", "--server=https://evil.example"],
        ] {
            let a = parse(&argv);
            assert_eq!(a.server, None, "{argv:?}");
            assert!(!a.quit && !a.smoke, "{argv:?}");
            assert_eq!(a.ignored.len(), argv.len() - 1, "{argv:?}");
        }
        assert_eq!(parse(&[link]), Args::default());
        // 不是链接的参数照常解析
        assert!(parse(&["--quit", "https://a.example"]).quit);
    }

    /// 版本一律取 `package_info()`：壳的源码里只有 User-Agent 的单测兜底能用 crate 版本。
    #[test]
    fn version_comes_from_package_info() {
        for (name, src) in [
            ("lib.rs", include_str!("lib.rs")),
            ("host.rs", include_str!("host.rs")),
            ("webview.rs", include_str!("webview.rs")),
            ("session.rs", include_str!("session.rs")),
            ("updater.rs", include_str!("updater.rs")),
        ] {
            let needle = concat!("env!(\"CARGO_PKG", "_VERSION\")");
            assert!(!src.contains(needle), "{name} uses the crate version");
        }
        assert_eq!(
            include_str!("api.rs").matches("CARGO_PKG_VERSION").count(),
            1
        );
    }
}
