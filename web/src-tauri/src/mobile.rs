//! 移动端的 Tauri 接线：原生插件事件、推送订阅同步、分享下发、导航处理与移动端宿主命令。
//!
//! 逻辑在 [`crate::mobile_logic`]（主机上单测）；这里只把它接到插件、会话状态机与页面事件上。

use std::sync::{Arc, Mutex};

use serde_json::json;
use tauri::{AppHandle, Manager, State, Url};
use tauri_plugin_opener::OpenerExt;

use crate::api::HttpApi;
use crate::eventlog;
use crate::host::{self, Desktop};
use crate::mobile_logic::{self, MobileCore, MobileInfo, Platform};
use crate::notify::Bucket;
use crate::session::Connection;

#[cfg(target_os = "ios")]
pub use crate::mobile_logic::Unsupported as NativeImpl;
#[cfg(target_os = "android")]
pub use android::AndroidNative as NativeImpl;

/// `host_open_external` 的令牌桶：突发 3，每 2 秒补 1。
const OPEN_BURST: f64 = 3.0;
const OPEN_REFILL_MS: f64 = 2_000.0;

/// 进程级移动端状态（`app.manage(Arc<MobileRt>)`）。
pub struct MobileRt {
    pub core: MobileCore<NativeImpl, HttpApi>,
    /// 系统 WebView 版本（启动时取一次）。
    pub webview: String,
    open_bucket: Mutex<Bucket>,
}

impl MobileRt {
    pub fn new(native: NativeImpl, webview: String) -> Self {
        Self {
            core: MobileCore::new(
                native,
                Arc::new(HttpApi),
                eventlog::handle(),
                Platform::current(),
            ),
            webview,
            open_bucket: Mutex::new(Bucket::new(OPEN_BURST, OPEN_REFILL_MS)),
        }
    }
}

/// 取移动端状态（`setup` 完成前为 `None`）。
pub fn runtime(app: &AppHandle) -> Option<Arc<MobileRt>> {
    app.try_state::<Arc<MobileRt>>().map(|s| s.inner().clone())
}

async fn connection(d: &Desktop) -> Option<Connection> {
    d.core.lock().await.connection()
}

/// 同步推送订阅（`force` 见 [`mobile_logic::sync_target`]）；结果变了就让页面重取 `MobileInfo`。
async fn sync_push(app: &AppHandle, d: &Desktop, m: &MobileRt, force: bool) {
    let conn = connection(d).await;
    if m.core.sync_push(conn.as_ref(), force).await {
        host::emit(app, json!({"type": "mobile"}));
    }
}

/// 已进入应用时上传并下发待处理的分享。
async fn deliver_share(app: &AppHandle, d: &Desktop, m: &MobileRt) {
    if !m.core.has_incoming_share() {
        return;
    }
    let Some(conn) = connection(d).await else {
        return;
    };
    if let Some(event) = m.core.process_share(&conn, d.lang()).await {
        host::emit(app, event);
    }
}

/// `host_ready` 的移动端部分：清返回栈（返回键不会退回连接页）、强制登记一次推送订阅、重发 / 继续分享。
pub fn host_ready(app: &AppHandle, d: Arc<Desktop>) {
    let Some(m) = runtime(app) else { return };
    #[cfg(target_os = "android")]
    if let Err(e) = m.core.native().0.clear_history() {
        eventlog::log("history.error", json!({"error": e.to_string()}));
    }
    if let Some(event) = m.core.ready_share_event() {
        host::emit(app, event);
    }
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        sync_push(&app, &d, &m, true).await;
        deliver_share(&app, &d, &m).await;
    });
}

/// 主框架导航（Android 的 `shouldOverrideUrlLoading`）。返回是否放行。
/// 回调在 Java 的 UI 线程上：同步的插件调用会等 UI 线程处理而死锁，副作用一律放到异步任务里。
#[cfg_attr(target_os = "ios", allow(dead_code))]
pub fn on_navigation(app: &AppHandle, url: &Url, launcher: &str, server: Option<&str>) -> bool {
    use mobile_logic::NavDecision;
    let decision = mobile_logic::nav_decision(url.as_str(), launcher, server);
    let (app, url) = (app.clone(), url.to_string());
    match decision {
        NavDecision::Allow => return true,
        NavDecision::External => {
            tauri::async_runtime::spawn(async move {
                let _ = app.opener().open_url(&url, None::<&str>);
                eventlog::log("nav.external", json!({"url": eventlog::redact_url(&url)}));
            });
        }
        NavDecision::DeepLink => {
            tauri::async_runtime::spawn(async move {
                crate::deeplink::open_url(&app, &url);
            });
        }
        NavDecision::Block => {}
    }
    false
}

// ---------------------------------------------------------------- 宿主命令（移动端）

type Res<T> = Result<T, String>;

async fn info(d: &Desktop, m: &MobileRt) -> MobileInfo {
    let store = d.core.lock().await.token_store();
    m.core.info(&d.version, store, &m.webview)
}

#[tauri::command]
pub async fn host_mobile_info(
    d: State<'_, Arc<Desktop>>,
    m: State<'_, Arc<MobileRt>>,
) -> Res<MobileInfo> {
    Ok(info(&d, &m).await)
}

#[tauri::command]
pub async fn host_push_enable(
    d: State<'_, Arc<Desktop>>,
    m: State<'_, Arc<MobileRt>>,
) -> Res<MobileInfo> {
    let conn = connection(&d).await;
    m.core.push_enable(conn.as_ref()).await?;
    Ok(info(&d, &m).await)
}

#[tauri::command]
pub async fn host_push_disable(
    d: State<'_, Arc<Desktop>>,
    m: State<'_, Arc<MobileRt>>,
) -> Res<MobileInfo> {
    let conn = connection(&d).await;
    m.core.push_disable(conn.as_ref()).await;
    Ok(info(&d, &m).await)
}

#[tauri::command]
pub async fn host_open_external(
    app: AppHandle,
    url: String,
    m: State<'_, Arc<MobileRt>>,
) -> Res<()> {
    let Some(url) = mobile_logic::external_url(&url) else {
        return Err("invalid_url".into());
    };
    let logged = eventlog::redact_url(&url);
    let allowed = m
        .open_bucket
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .take(eventlog::now_ms());
    if !allowed {
        eventlog::log(
            "open.external",
            json!({"url": logged, "ok": false, "reason": "rate_limited"}),
        );
        return Ok(());
    }
    let ok = app.opener().open_url(&url, None::<&str>).is_ok();
    eventlog::log("open.external", json!({"url": logged, "ok": ok}));
    Ok(())
}

#[tauri::command]
pub async fn host_share_ack(id: String, m: State<'_, Arc<MobileRt>>) -> Res<()> {
    m.core.share_ack(&id);
    Ok(())
}

#[tauri::command]
pub async fn host_set_chrome(dark: bool, color: String, m: State<'_, Arc<MobileRt>>) -> Res<()> {
    if !mobile_logic::valid_color(&color) {
        return Err("invalid_color".into());
    }
    #[cfg(target_os = "android")]
    m.core
        .native()
        .0
        .set_chrome(dark, &color)
        .map_err(|e| e.to_string())?;
    #[cfg(target_os = "ios")]
    let _ = &m;
    eventlog::log("chrome", json!({"dark": dark}));
    Ok(())
}

#[tauri::command]
pub async fn host_open_notification_settings(m: State<'_, Arc<MobileRt>>) -> Res<()> {
    #[cfg(target_os = "android")]
    m.core
        .native()
        .0
        .open_notification_settings()
        .map_err(|e| e.to_string())?;
    #[cfg(target_os = "ios")]
    let _ = &m;
    Ok(())
}

// ---------------------------------------------------------------- Android 原生插件

#[cfg(target_os = "android")]
mod android {
    use serde_json::json;
    use tauri::AppHandle;
    use tauri_plugin_oryn_mobile::{Event, OrynMobile, PushState};

    use super::{deliver_share, runtime, sync_push};
    use crate::eventlog;
    use crate::host;
    use crate::mobile_logic::{self, Native};

    /// [`Native`] 的 Android 实现：oryn-mobile 插件。
    pub struct AndroidNative(pub OrynMobile<tauri::Wry>);

    fn err(e: tauri_plugin_oryn_mobile::Error) -> String {
        e.to_string()
    }

    impl Native for AndroidNative {
        fn push_state(&self) -> Result<PushState, String> {
            self.0.push_state().map_err(err)
        }
        async fn push_enable(&self, vapid: &str) -> Result<PushState, String> {
            self.0.push_enable(vapid).await.map_err(err)
        }
        fn push_disable(&self) -> Result<PushState, String> {
            self.0.push_disable().map_err(err)
        }
        fn push_mark_synced(&self, endpoint: &str, id: &str, server: &str) -> Result<(), String> {
            self.0.push_mark_synced(endpoint, id, server).map_err(err)
        }
        fn share_ack(&self, id: &str) {
            if let Err(e) = self.0.share_ack(id) {
                eventlog::log("share.ack_error", json!({"error": e.to_string()}));
            }
        }
        fn toast(&self, text: &str) {
            let _ = self.0.toast(text);
        }
    }

    /// 插件事件：前后台切换、推送状态变化、系统分享、屏幕圆角与 inset、Activity 重建。
    /// 回调在 Java 线程上：凡是要同步调插件的都放进异步任务（否则等 UI 线程处理时死锁）。
    pub fn on_event(app: &AppHandle, event: Event) {
        let (Some(d), Some(m)) = (host::desktop(app), runtime(app)) else {
            return;
        };
        let app = app.clone();
        match event {
            Event::Resume => {
                eventlog::log("lifecycle", json!({"state": "resume"}));
                tauri::async_runtime::spawn(async move {
                    let next = d.core.lock().await.resumed();
                    host::schedule(d.clone(), next);
                    sync_push(&app, &d, &m, false).await;
                    deliver_share(&app, &d, &m).await;
                });
            }
            Event::Stop => {
                eventlog::log("lifecycle", json!({"state": "stop"}));
                tauri::async_runtime::spawn(async move {
                    d.core.lock().await.stopped();
                });
            }
            Event::Push { .. } => {
                host::emit(&app, json!({"type": "mobile"}));
                tauri::async_runtime::spawn(async move {
                    sync_push(&app, &d, &m, false).await;
                });
            }
            Event::Share { share } => {
                tauri::async_runtime::spawn(async move {
                    m.core.share_received(share);
                    deliver_share(&app, &d, &m).await;
                });
            }
            Event::Insets {
                corner_radii,
                insets,
            } => {
                if m.core.set_insets(corner_radii, insets) {
                    host::emit(&app, mobile_logic::insets_event(corner_radii, insets));
                }
            }
            // 插件已重新绑定新的 Activity / WebView；重建前所在的服务器页面作为待开路径，
            // 新连接页的 `launcher_state` 恢复会话时（或之后新页面的 host_ready）带走
            Event::Recreated { url } => {
                tauri::async_runtime::spawn(async move {
                    let server = d.core.lock().await.config().server_url.clone();
                    let path = url
                        .as_deref()
                        .and_then(|u| mobile_logic::recreated_path(u, server.as_deref()));
                    eventlog::log("webview.recreated", json!({"path": path}));
                    if let Some(path) = path {
                        let mut nav = d.nav();
                        if nav.pending.is_none() {
                            nav.pending = Some(path);
                        }
                    }
                });
            }
        }
    }

    /// 注册插件事件回调。
    pub fn listen(app: &AppHandle, plugin: &OrynMobile<tauri::Wry>) {
        let handle = app.clone();
        if let Err(e) = plugin.on_event(move |event| on_event(&handle, event)) {
            eventlog::log("mobile.listen_error", json!({"error": e.to_string()}));
        }
    }
}

#[cfg(target_os = "android")]
pub use android::listen;
