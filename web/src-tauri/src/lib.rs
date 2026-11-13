//! Oryn 桌面壳：加载所配对服务器自己的 Web UI，壳内只内置连接页。
//!
//! 模块分工见 `docs/desktop.md` 与 M6 计划：配对 / 会话换取（`session`）、宿主命令（`host`）、
//! 主窗口与远程能力（`webview`）、托盘 / 快捷键 / 更新 / 下载（仅桌面）。

mod api;
mod config;
mod deeplink;
mod eventlog;
mod host;
mod i18n;
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

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use serde_json::json;
use tauri::{App, AppHandle, Manager, RunEvent};
use tauri_plugin_notification::NotificationExt;

use crate::api::HttpApi;
use crate::config::Config;
use crate::host::Desktop;
use crate::secrets::Vault;
use crate::session::{Core, SharedNav};
use crate::webview::TauriShell;

/// 冒烟模式的超时。
const SMOKE_TIMEOUT: Duration = Duration::from_secs(30);

/// 命令行参数。深链接 URL 由 deep-link 插件自己从 argv 取。
#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub struct Args {
    /// `--server <url>`：预填并直接连接。
    pub server: Option<String>,
    /// `--quit`：让已运行的实例正常退出；没有实例时直接退出 0。
    pub quit: bool,
    /// `--smoke`：临时配置目录、不注册单实例、不碰凭据库与网络，连接页就绪即退出 0。
    pub smoke: bool,
}

impl Args {
    pub fn parse<I: IntoIterator<Item = String>>(args: I) -> Self {
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

fn smoke_dir() -> PathBuf {
    std::env::temp_dir().join(format!("oryn-desktop-smoke-{}", std::process::id()))
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let raw: Vec<String> = std::env::args().skip(1).collect();
    let args = Args::parse(raw.iter().cloned());

    // 插件顺序：single_instance（必须第一个）→ deep_link → window_state → global_shortcut
    // → notification → opener → updater
    #[allow(unused_mut)]
    let mut builder = tauri::Builder::default();
    #[cfg(desktop)]
    if !args.smoke {
        builder = builder.plugin(tauri_plugin_single_instance::init(on_second_instance));
    }
    builder = builder.plugin(tauri_plugin_deep_link::init());
    #[cfg(desktop)]
    {
        use tauri_plugin_window_state::StateFlags;
        let mut ws = tauri_plugin_window_state::Builder::default()
            .with_state_flags(StateFlags::all() & !StateFlags::VISIBLE);
        if args.smoke {
            ws = ws.with_filename(smoke_dir().join(".window-state.json").to_string_lossy());
        }
        builder = builder.plugin(ws.build()).plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(shortcut::on_event)
                .build(),
        );
    }
    builder = builder
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_opener::init());
    #[cfg(desktop)]
    {
        builder = builder.plugin(tauri_plugin_updater::Builder::new().build());
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
            host::host_info,
            host::host_set_settings,
            host::host_check_update,
            host::host_install_update,
            host::host_change_server,
        ])
        .setup(move |app| setup(app, &setup_args, &raw))
        .build(tauri::generate_context!())
        .expect("error while building Oryn");
    let smoke = args.smoke;
    app.run(move |app, event| on_run_event(app, event, smoke));
}

fn setup(app: &mut App, args: &Args, raw: &[String]) -> Result<(), Box<dyn std::error::Error>> {
    let handle = app.handle().clone();
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
    eventlog::log(
        "app.start",
        json!({"version": env!("CARGO_PKG_VERSION"), "args": raw}),
    );

    let config = Config::load(&dir);
    let vault = if args.smoke {
        Vault::file_only(&dir)
    } else {
        Vault::open(config.token_store, &dir)
    };

    let window = webview::create_main(&handle)?;
    let launcher_url = window.url()?;
    host::wire_window(&handle, &window);
    let _ = window.set_focus();

    let nav = SharedNav::default();
    let shell = TauriShell::new(handle.clone(), launcher_url);
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
    );
    #[cfg(desktop)]
    let (shortcut_cfg, lang) = (core.config().shortcut.clone(), core.config().lang());
    let d = Arc::new(Desktop::new(core, nav, args.smoke));
    app.manage(d.clone());

    // 通知点击：回查通知号 → 打开对应路径
    let click = handle.clone();
    handle.notification().on_action(move |p| {
        if p.action_id() != "tap" {
            return;
        }
        let Some(n) = p.notification().map(|n| n.id()) else {
            return;
        };
        let Some(d) = host::desktop(&click) else {
            return;
        };
        if let Some((id, path)) = d.notifier.lookup(n) {
            eventlog::log("notify.clicked", json!({"id": id, "n": n, "path": path}));
            host::open_path(&click, &path);
        }
    })?;

    #[cfg(desktop)]
    {
        match tray::build(&handle, lang) {
            Some(t) => {
                let _ = d.tray.set(t);
                d.set_tray_available(true);
            }
            None => d.set_tray_available(false),
        }
        let _ = d.shortcut.apply(&handle, &shortcut_cfg);
    }

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

#[cfg(desktop)]
fn on_second_instance(app: &AppHandle, argv: Vec<String>, _cwd: String) {
    eventlog::log("instance.second", json!({"args": argv}));
    if argv.iter().any(|a| a == "--quit") {
        host::quit(app);
        return;
    }
    // 深链接 URL 已由插件的 deep-link feature 转给 on_open_url
    host::show_main(app);
}

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
                smoke: true
            }
        );
        assert_eq!(
            parse(&["--server=https://b.example"]).server.as_deref(),
            Some("https://b.example")
        );
        assert!(parse(&["oryn://thread/T-1", "--quit"]).quit);
        assert_eq!(parse(&["--server"]).server, None);
    }
}
