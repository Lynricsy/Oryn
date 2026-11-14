//! 主窗口：创建与钩子、远程能力、Rust → 页面事件、回连接页。

use std::sync::Mutex;

use serde_json::{json, Value};
use tauri::ipc::CapabilityBuilder;
use tauri::utils::config::BackgroundThrottlingPolicy;
use tauri::webview::{NewWindowResponse, PageLoadEvent};
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use crate::session::{LauncherState, Shell};
use crate::{eventlog, host};

/// 远程页面可调用的宿主命令（= `capabilities` 里的 `allow-host-*`）。
pub const HOST_PERMISSIONS: [&str; 10] = [
    "allow-host-ready",
    "allow-host-notify",
    "allow-host-set-status",
    "allow-host-set-language",
    "allow-host-reauth",
    "allow-host-info",
    "allow-host-set-settings",
    "allow-host-check-update",
    "allow-host-install-update",
    "allow-host-change-server",
];

/// 宿主平台名（`window.__ORYN_HOST__.platform`）。
pub fn host_platform() -> &'static str {
    match std::env::consts::OS {
        "macos" => "macos",
        "windows" => "windows",
        "android" => "android",
        "ios" => "ios",
        _ => "linux",
    }
}

/// 注入每个主框架的宿主标识；`version` 取 Tauri 配置的应用版本（与更新器比较用的是同一个）。
pub fn init_script(version: &str) -> String {
    let host = json!({
        "kind": "tauri",
        "platform": host_platform(),
        "version": version,
        "bridge": host::BRIDGE,
    });
    format!("window.__ORYN_HOST__ = Object.freeze({host});")
}

/// 页面事件脚本：`window.dispatchEvent(new CustomEvent('oryn:host', {detail}))`。
pub fn event_script(detail: &Value) -> String {
    format!("window.dispatchEvent(new CustomEvent('oryn:host',{{detail:{detail}}}));")
}

/// 两个 URL 同源（按 scheme + host + port 比较：`tauri://` 这类非特殊 scheme 的 `Url::origin` 是不透明的，彼此永不相等）。
pub fn same_origin(a: &Url, b: &Url) -> bool {
    a.scheme() == b.scheme()
        && a.host_str() == b.host_str()
        && a.port_or_known_default() == b.port_or_known_default()
}

fn is_external(url: &Url) -> bool {
    matches!(url.scheme(), "http" | "https")
}

/// 创建主窗口（`tauri.conf.json` 的 `windows` 为空：钩子只能在代码里挂）。
pub fn create_main(app: &AppHandle) -> tauri::Result<WebviewWindow> {
    let opener = app.clone();
    let loader = app.clone();
    let builder =
        WebviewWindowBuilder::new(app, host::MAIN, WebviewUrl::App("launcher.html".into()))
            .title("Oryn")
            .inner_size(1280.0, 820.0)
            .min_inner_size(380.0, 560.0)
            .initialization_script(init_script(&app.package_info().version.to_string()))
            .on_new_window(move |url, _features| {
                // 外链（Markdown 链接、portal 的「新窗口」等）一律交给系统浏览器
                if is_external(&url) {
                    let _ = opener.opener().open_url(url.as_str(), None::<&str>);
                    eventlog::log(
                        "browser.open",
                        json!({"url": eventlog::redact_url(url.as_str())}),
                    );
                }
                NewWindowResponse::Deny
            })
            .on_page_load(move |window, payload| {
                let Some(d) = host::desktop(&loader) else {
                    return;
                };
                match payload.event() {
                    // 主框架开始加载任何页面（回连接页、兑换跳转、用户刷新）都先清掉就绪标志：
                    // 其间的 open_path 存为待开路径，由新页面的 host_ready 取走，不会派发给正在卸载的页面
                    PageLoadEvent::Started => d.nav().web_ready = false,
                    // 首个页面加载完时窗口已映射，外框才是恢复后的实际值（刚 build 时 GTK 上为 0×0）
                    PageLoadEvent::Finished => host::note_geometry(window.app_handle()),
                }
            })
            .disable_drag_drop_handler()
            .enable_clipboard_access()
            .background_throttling(BackgroundThrottlingPolicy::Disabled);
    #[cfg(desktop)]
    let builder = builder.on_download(crate::download::handler(app.clone()));
    builder.build()
}

/// [`Shell`] 的 Tauri 实现。
pub struct TauriShell {
    app: AppHandle,
    launcher_url: Url,
    /// 已开放宿主命令的 origin（每进程一个）。
    capability: Mutex<Option<String>>,
}

impl TauriShell {
    pub fn new(app: AppHandle, launcher_url: Url) -> Self {
        Self {
            app,
            launcher_url,
            capability: Mutex::new(None),
        }
    }

    fn window(&self) -> Option<WebviewWindow> {
        self.app.get_webview_window(host::MAIN)
    }

    fn on_launcher(&self) -> bool {
        self.window()
            .and_then(|w| w.url().ok())
            .is_some_and(|u| same_origin(&u, &self.launcher_url))
    }

    fn ensure_capability(&self, origin: &str) -> Result<(), String> {
        let mut cap = self.capability.lock().unwrap_or_else(|e| e.into_inner());
        match cap.as_deref() {
            Some(o) if o == origin => Ok(()),
            Some(_) => {
                // 已为另一个 origin 开放过（没有移除能力的 API）：重启进程，启动时按新配置进入
                host::save_window_state(&self.app);
                self.app.restart();
            }
            None => {
                let mut builder = CapabilityBuilder::new("remote-host")
                    .local(false)
                    .remote(format!("{origin}/*"))
                    .window(host::MAIN);
                for p in HOST_PERMISSIONS {
                    builder = builder.permission(p);
                }
                self.app
                    .add_capability(builder)
                    .map_err(|e| e.to_string())?;
                *cap = Some(origin.to_string());
                Ok(())
            }
        }
    }
}

impl Shell for TauriShell {
    fn now_ms(&self) -> i64 {
        eventlog::now_ms()
    }

    fn open_browser(&self, url: &str) {
        if let Err(e) = self.app.opener().open_url(url, None::<&str>) {
            eventlog::log("browser.error", json!({"error": e.to_string()}));
        }
    }

    fn open_remote(&self, origin: &str, url: &str) -> Result<(), String> {
        self.ensure_capability(origin)?;
        let url = Url::parse(url).map_err(|e| e.to_string())?;
        let window = self.window().ok_or("main window missing")?;
        window.navigate(url).map_err(|e| e.to_string())
    }

    fn show_launcher(&self) {
        if let Some(d) = host::desktop(&self.app) {
            d.nav().web_ready = false;
        }
        if self.on_launcher() {
            return;
        }
        if let Some(w) = self.window() {
            let _ = w.navigate(self.launcher_url.clone());
        }
    }

    fn emit_state(&self, state: &LauncherState) {
        if !self.on_launcher() {
            return;
        }
        if let Some(w) = self.window() {
            let _ = w.eval(event_script(&json!({"type": "launcher", "state": state})));
        }
    }

    /// 先逐个删掉该 origin 的 Cookie（各平台的 `delete_cookie` 都等到引擎确认才返回，接着重启也不会丢），
    /// 再尽力清掉其余站点数据（`clear_all_browsing_data` 是异步的，不保证在重启前完成）。
    /// 必须在异步任务里调用：Windows 上在主线程同步读 Cookie 会死锁。
    fn clear_web_data(&self, origin: &str) {
        let Some(w) = self.window() else { return };
        let mut deleted = 0u32;
        let mut error = None;
        match Url::parse(origin)
            .map_err(|e| e.to_string())
            .and_then(|u| w.cookies_for_url(u).map_err(|e| e.to_string()))
        {
            Ok(cookies) => {
                for c in cookies {
                    match w.delete_cookie(c) {
                        Ok(()) => deleted += 1,
                        Err(e) => error = Some(e.to_string()),
                    }
                }
            }
            Err(e) => error = Some(e),
        }
        if let Err(e) = w.clear_all_browsing_data() {
            error.get_or_insert(e.to_string());
        }
        eventlog::log(
            "webdata.cleared",
            json!({"origin": origin, "cookies": deleted, "error": error}),
        );
    }

    fn restart(&self) {
        host::save_window_state(&self.app);
        self.app.restart();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scripts() {
        let s = init_script("9.8.7-rc.1");
        assert!(s.starts_with("window.__ORYN_HOST__ = Object.freeze({"));
        assert!(s.contains("\"version\":\"9.8.7-rc.1\""));
        assert!(s.contains("\"kind\":\"tauri\""));
        assert!(s.contains("\"bridge\":1"));
        let e = event_script(&json!({"type": "navigate", "path": "/t/x"}));
        assert!(e.starts_with("window.dispatchEvent(new CustomEvent('oryn:host',{detail:{"));
        assert!(e.ends_with("}}));"));
        assert!(e.contains("\"path\":\"/t/x\""));
    }

    #[test]
    fn origins() {
        let launcher = Url::parse("tauri://localhost/launcher.html").unwrap();
        assert!(same_origin(
            &Url::parse("tauri://localhost/other").unwrap(),
            &launcher
        ));
        assert!(!same_origin(
            &Url::parse("https://oryn.example.com/").unwrap(),
            &launcher
        ));
        assert!(same_origin(
            &Url::parse("https://oryn.example.com:443/t/x").unwrap(),
            &Url::parse("https://oryn.example.com/").unwrap()
        ));
        assert!(!same_origin(
            &Url::parse("http://tauri.localhost/").unwrap(),
            &Url::parse("https://tauri.localhost/").unwrap()
        ));
        assert_eq!(HOST_PERMISSIONS.len(), 10);
    }
}
