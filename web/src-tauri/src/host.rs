//! 宿主命令（连接页与远程页面调用的 IPC）与托盘 / 通知 / 深链接共用的窗口操作。
//!
//! 远程能力只含 `host_*`：它们可能被 portal iframe 里的任意页面调用（Linux / Android 上无法区分），
//! 所以没有任何返回令牌、读写文件或执行命令的命令；更换服务器只把窗口带回连接页由用户确认。

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use tauri::{AppHandle, Manager, State, WebviewWindow};

use crate::api::{Api, HttpApi};
use crate::config::TokenStoreKind;
use crate::eventlog;
use crate::i18n::Lang;
use crate::notify::{self, HostNotification, Notifier};
use crate::session::{Core, LauncherState, Nav, Next, SharedNav};
use crate::webview::{self, TauriShell};

/// 主窗口标签。
pub const MAIN: &str = "main";
/// 宿主命令集版本：以后新增命令就加一。
pub const BRIDGE: u32 = 1;

pub type SessionCore = Core<HttpApi, TauriShell>;

/// 同步回调（窗口事件、托盘、通知）需要读的偏好，镜像自配置。
#[derive(Debug, Clone, Copy)]
pub struct Prefs {
    pub close_to_tray: bool,
    pub auto_update: bool,
    pub lang: Lang,
}

/// 进程级状态（`app.manage(Arc<Desktop>)`）。
pub struct Desktop {
    pub core: tokio::sync::Mutex<SessionCore>,
    nav: SharedNav,
    prefs: Mutex<Prefs>,
    visible: AtomicBool,
    launcher_ready: AtomicBool,
    /// 托盘可用；不可用时本次运行关窗即退出。
    tray_available: AtomicBool,
    restored_logged: AtomicBool,
    geometry_gen: std::sync::atomic::AtomicU64,
    /// `--smoke`：首次 `launcher_state` 后退出 0。
    pub smoke: bool,
    /// 应用版本：Tauri 配置的 `package_info().version`（可被 `--config` 覆盖，更新器比较的就是它）。
    pub version: String,
    pub notifier: Notifier,
    status: Mutex<HostStatus>,
    #[cfg(desktop)]
    pub tray: std::sync::OnceLock<crate::tray::Tray>,
    #[cfg(desktop)]
    pub shortcut: crate::shortcut::Registration,
    #[cfg(desktop)]
    pub updater: crate::updater::Updater,
}

impl Desktop {
    pub fn new(core: SessionCore, nav: SharedNav, smoke: bool, version: String) -> Self {
        let cfg = core.config();
        let prefs = Prefs {
            close_to_tray: cfg.close_to_tray,
            auto_update: cfg.auto_update,
            lang: cfg.lang(),
        };
        Self {
            core: tokio::sync::Mutex::new(core),
            nav,
            prefs: Mutex::new(prefs),
            visible: AtomicBool::new(true),
            launcher_ready: AtomicBool::new(false),
            tray_available: AtomicBool::new(false),
            restored_logged: AtomicBool::new(false),
            geometry_gen: Default::default(),
            smoke,
            version,
            notifier: Notifier::default(),
            status: Mutex::new(HostStatus::default()),
            #[cfg(desktop)]
            tray: std::sync::OnceLock::new(),
            #[cfg(desktop)]
            shortcut: Default::default(),
            #[cfg(desktop)]
            updater: Default::default(),
        }
    }

    pub fn nav(&self) -> MutexGuard<'_, Nav> {
        self.nav.lock().unwrap_or_else(|e| e.into_inner())
    }

    pub fn prefs(&self) -> Prefs {
        *self.prefs.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn set_prefs(&self, f: impl FnOnce(&mut Prefs)) {
        f(&mut self.prefs.lock().unwrap_or_else(|e| e.into_inner()));
    }

    pub fn lang(&self) -> Lang {
        self.prefs().lang
    }

    /// 记录一次 `window.restored{width,height,x,y}`：尺寸取内容区（与窗口状态插件保存 / 恢复的值、
    /// `xdotool windowsize` 设定的值同一口径），位置取外框左上角。
    fn log_window_restored(&self, window: &WebviewWindow) {
        if self.restored_logged.load(Ordering::SeqCst) {
            return;
        }
        // 窗口映射前（GTK 收到 configure 之前）外框为 0×0：等到有实际尺寸再记
        let (Ok(size), Ok(pos)) = (window.inner_size(), window.outer_position()) else {
            return;
        };
        if size.width == 0 || size.height == 0 {
            return;
        }
        if !self.restored_logged.swap(true, Ordering::SeqCst) {
            eventlog::log(
                "window.restored",
                json!({"width": size.width, "height": size.height, "x": pos.x, "y": pos.y}),
            );
        }
    }

    /// 连接页已调用过 `launcher_state`。
    pub fn launcher_seen(&self) -> bool {
        self.launcher_ready.load(Ordering::SeqCst)
    }

    pub fn set_tray_available(&self, ok: bool) {
        self.tray_available.store(ok, Ordering::SeqCst);
    }

    /// 关窗时是否藏到托盘（托盘不可用时为 false，避免「藏起来却没有入口」）。
    pub fn close_to_tray(&self) -> bool {
        self.prefs().close_to_tray && self.tray_available.load(Ordering::SeqCst)
    }
}

/// 取进程级状态（`setup` 完成前为 `None`）。
pub fn desktop(app: &AppHandle) -> Option<Arc<Desktop>> {
    app.try_state::<Arc<Desktop>>().map(|s| s.inner().clone())
}

// ---------------------------------------------------------------- 托盘状态

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum StatusKind {
    Awaiting,
    Unread,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct StatusItem {
    pub thread_id: String,
    #[serde(default)]
    pub title: String,
    pub kind: StatusKind,
}

/// `host_set_status` 的参数。
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct HostStatus {
    pub awaiting: u32,
    pub unread: u32,
    #[serde(default)]
    pub items: Vec<StatusItem>,
}

// ---------------------------------------------------------------- 窗口操作

fn main_window(app: &AppHandle) -> Option<WebviewWindow> {
    app.get_webview_window(MAIN)
}

/// 向当前页面派发 `oryn:host` 事件。
pub fn emit(app: &AppHandle, detail: Value) {
    if let Some(w) = main_window(app) {
        let _ = w.eval(webview::event_script(&detail));
    }
}

/// `(visible, focused)`；`visible = is_visible && !is_minimized`。
pub fn main_window_flags(app: &AppHandle) -> (bool, bool) {
    let Some(w) = main_window(app) else {
        return (false, false);
    };
    let visible = w.is_visible().unwrap_or(false) && !w.is_minimized().unwrap_or(false);
    (visible, w.is_focused().unwrap_or(false))
}

/// 重新计算可见性；变化时记录 `window{visible}` 并通知页面。
pub fn refresh_visibility(app: &AppHandle) {
    let Some(d) = desktop(app) else { return };
    let (visible, _) = main_window_flags(app);
    if d.visible.swap(visible, Ordering::SeqCst) != visible {
        eventlog::log("window", json!({"visible": visible}));
        emit(app, json!({"type": "visibility", "visible": visible}));
    }
}

/// 显示并聚焦主窗口。
pub fn show_main(app: &AppHandle) {
    if let Some(w) = main_window(app) {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
    refresh_visibility(app);
}

/// 隐藏主窗口（先保存窗口状态）。
pub fn hide_main(app: &AppHandle) {
    save_window_state(app);
    if let Some(w) = main_window(app) {
        let _ = w.hide();
    }
    refresh_visibility(app);
}

/// 保存窗口状态（不记「可见」）。
pub fn save_window_state(app: &AppHandle) {
    #[cfg(desktop)]
    {
        use tauri_plugin_window_state::{AppHandleExt, StateFlags};
        let _ = app.save_window_state(StateFlags::all() & !StateFlags::VISIBLE);
    }
    #[cfg(mobile)]
    let _ = app;
}

/// 托盘 / 通知 / 深链接共用：显示主窗口；远程页面就绪 → 发 `navigate`，否则存为待开路径。
pub fn open_path(app: &AppHandle, path: &str) {
    let path = crate::session::valid_app_path(path);
    show_main(app);
    let Some(d) = desktop(app) else { return };
    let delivered = {
        let mut nav = d.nav();
        if nav.web_ready {
            true
        } else {
            nav.pending = Some(path.clone());
            false
        }
    };
    if delivered {
        emit(app, json!({"type": "navigate", "path": path}));
    }
    eventlog::log("open", json!({"path": path, "delivered": delivered}));
}

/// 托盘「退出 Oryn」、`--quit`：保存窗口状态后显式退出。
pub fn quit(app: &AppHandle) {
    save_window_state(app);
    app.exit(0);
}

/// 托盘「更换服务器…」：回连接页等待确认。
pub fn change_server(app: &AppHandle) {
    let Some(d) = desktop(app) else { return };
    show_main(app);
    tauri::async_runtime::spawn(async move {
        d.core.lock().await.request_change();
    });
}

/// 托盘的更新项：已下载 → 安装并重启；否则立即检查。
pub fn tray_update(app: &AppHandle) {
    #[cfg(desktop)]
    {
        let Some(d) = desktop(app) else { return };
        if d.updater.ready_version().is_some() {
            crate::updater::install(app, &d);
            return;
        }
        let app = app.clone();
        tauri::async_runtime::spawn(async move {
            crate::updater::check(&app, &d).await;
        });
    }
    #[cfg(mobile)]
    let _ = app;
}

/// 按最新的状态、语言、更新重画托盘。
pub fn refresh_tray(app: &AppHandle, d: &Desktop) {
    #[cfg(desktop)]
    if let Some(tray) = d.tray.get() {
        let status = d.status.lock().unwrap_or_else(|e| e.into_inner()).clone();
        let ready = d.updater.ready_version();
        tray.apply(app, crate::tray::model(&status, d.lang(), ready.as_deref()));
    }
    #[cfg(mobile)]
    let _ = (app, d);
}

/// 主窗口事件：可见性、关窗藏托盘。
pub fn wire_window(app: &AppHandle, window: &WebviewWindow) {
    let app = app.clone();
    window.on_window_event(move |event| match event {
        tauri::WindowEvent::CloseRequested { api, .. } => {
            if desktop(&app).is_some_and(|d| d.close_to_tray()) {
                api.prevent_close();
                hide_main(&app);
            } else {
                save_window_state(&app);
            }
        }
        tauri::WindowEvent::Focused(_) => refresh_visibility(&app),
        tauri::WindowEvent::Resized(_) => {
            refresh_visibility(&app);
            note_geometry(&app);
        }
        tauri::WindowEvent::Moved(_) => note_geometry(&app),
        _ => {}
    });
}

/// 外框稳定多久之后记 `window.restored`。
const GEOMETRY_SETTLE: std::time::Duration = std::time::Duration::from_millis(500);

/// 窗口几何变化（或首个页面加载完）：稳定 [`GEOMETRY_SETTLE`] 后记一次 `window.restored`。
/// 刚建好时 GTK 外框为 0×0，窗口管理器映射时还会先报一次自然尺寸，所以不能取第一次的值。
pub fn note_geometry(app: &AppHandle) {
    let Some(d) = desktop(app) else { return };
    if d.restored_logged.load(Ordering::SeqCst) {
        return;
    }
    let gen = d.geometry_gen.fetch_add(1, Ordering::SeqCst) + 1;
    let app = app.clone();
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(GEOMETRY_SETTLE).await;
        if d.geometry_gen.load(Ordering::SeqCst) == gen {
            if let Some(w) = main_window(&app) {
                d.log_window_restored(&w);
            }
        }
    });
}

/// 按状态机返回的 [`Next`] 安排计时。
pub fn schedule(d: Arc<Desktop>, next: Next) {
    match next {
        Next::Idle => {}
        Next::Poll { epoch, after } => {
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(after).await;
                // 锁外发请求：轮询期间连接页的「取消」不必等网络
                let (req, api) = {
                    let core = d.core.lock().await;
                    (core.poll_request(epoch), core.api())
                };
                let Some(req) = req else { return };
                let result = api.pair_poll(&req.base, &req.code, &req.verifier).await;
                let next = d.core.lock().await.apply_poll(epoch, result).await;
                schedule(d, next);
            });
        }
        Next::Watchdog { gen, after } => {
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(after).await;
                d.core.lock().await.watchdog_fired(gen);
            });
        }
        Next::Retry { gen, after } => {
            tauri::async_runtime::spawn(async move {
                tokio::time::sleep(after).await;
                let next = d.core.lock().await.retry_fired(gen).await;
                schedule(d, next);
            });
        }
    }
}

// ---------------------------------------------------------------- 返回类型

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LauncherInfo {
    pub state: LauncherState,
    pub lang: &'static str,
    pub version: String,
    pub token_store: TokenStoreKind,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct HostReady {
    pub bridge: u32,
    pub visible: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pending_path: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DesktopSettings {
    pub shortcut: String,
    pub close_to_tray: bool,
    pub auto_update: bool,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SettingsPatch {
    pub shortcut: Option<String>,
    pub close_to_tray: Option<bool>,
    pub auto_update: Option<bool>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct DesktopInfo {
    pub version: String,
    pub settings: DesktopSettings,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub shortcut_error: Option<String>,
    pub token_store: TokenStoreKind,
    pub update: Value,
}

async fn desktop_info(d: &Desktop) -> DesktopInfo {
    let (settings, token_store) = {
        let core = d.core.lock().await;
        let cfg = core.config();
        (
            DesktopSettings {
                shortcut: cfg.shortcut.clone(),
                close_to_tray: cfg.close_to_tray,
                auto_update: cfg.auto_update,
            },
            core.token_store(),
        )
    };
    #[cfg(desktop)]
    let (shortcut_error, update) = (
        d.shortcut.error(),
        serde_json::to_value(d.updater.status()).unwrap_or(Value::Null),
    );
    #[cfg(mobile)]
    let (shortcut_error, update) = (None, json!({"state": "none"}));
    DesktopInfo {
        version: d.version.clone(),
        settings,
        shortcut_error,
        token_store,
        update,
    }
}

type Res<T> = Result<T, String>;

// ---------------------------------------------------------------- 连接页命令（本地能力）

#[tauri::command]
pub async fn launcher_state(app: AppHandle, d: State<'_, Arc<Desktop>>) -> Res<LauncherInfo> {
    let info = {
        let core = d.core.lock().await;
        LauncherInfo {
            state: core.state().clone(),
            lang: core.config().lang().as_str(),
            version: d.version.clone(),
            token_store: core.token_store(),
        }
    };
    if !d.launcher_ready.swap(true, Ordering::SeqCst) {
        eventlog::log("launcher.ready", json!({}));
        if d.smoke {
            eprintln!("oryn-desktop: smoke ok");
            app.exit(0);
        }
    }
    Ok(info)
}

/// 在会话锁内执行一步，按返回的 [`Next`] 安排计时，交出新状态。
macro_rules! step {
    ($d:expr, |$core:ident| $body:expr) => {{
        let (next, state) = {
            let mut $core = $d.core.lock().await;
            let next = $body;
            (next, $core.state().clone())
        };
        schedule($d.inner().clone(), next);
        Ok(state)
    }};
}

#[tauri::command]
pub async fn launcher_connect(url: String, d: State<'_, Arc<Desktop>>) -> Res<LauncherState> {
    step!(d, |core| core.connect(&url).await)
}

#[tauri::command]
pub async fn launcher_cancel(d: State<'_, Arc<Desktop>>) -> Res<LauncherState> {
    let mut core = d.core.lock().await;
    core.cancel();
    Ok(core.state().clone())
}

#[tauri::command]
pub async fn launcher_retry(d: State<'_, Arc<Desktop>>) -> Res<LauncherState> {
    step!(d, |core| core.retry().await)
}

#[tauri::command]
pub async fn launcher_open_browser(d: State<'_, Arc<Desktop>>) -> Res<LauncherState> {
    let core = d.core.lock().await;
    core.reopen_browser();
    Ok(core.state().clone())
}

#[tauri::command]
pub async fn launcher_change_server(
    confirm: bool,
    d: State<'_, Arc<Desktop>>,
) -> Res<LauncherState> {
    step!(d, |core| core.change_server(confirm).await)
}

// ---------------------------------------------------------------- 宿主命令（远程能力）

#[tauri::command]
pub async fn host_ready(
    app: AppHandle,
    bridge: u32,
    lang: String,
    path: String,
    d: State<'_, Arc<Desktop>>,
) -> Res<HostReady> {
    let _ = path;
    let origin = main_window(&app)
        .and_then(|w| w.url().ok())
        .map(|u| u.origin().ascii_serialization())
        .unwrap_or_default();
    let pending_path = d.core.lock().await.host_ready(&origin, bridge, &lang);
    d.set_prefs(|p| p.lang = Lang::from_tag(&lang));
    refresh_tray(&app, &d);
    #[cfg(desktop)]
    crate::updater::start_loop(&app, &d);
    let (visible, _) = main_window_flags(&app);
    Ok(HostReady {
        bridge: BRIDGE,
        visible,
        pending_path,
    })
}

#[tauri::command]
pub async fn host_notify(
    app: AppHandle,
    notification: HostNotification,
    d: State<'_, Arc<Desktop>>,
) -> Res<()> {
    notify::show(&app, &d.notifier, notification);
    Ok(())
}

#[tauri::command]
pub async fn host_set_status(
    app: AppHandle,
    status: HostStatus,
    d: State<'_, Arc<Desktop>>,
) -> Res<()> {
    let changed = {
        let mut cur = d.status.lock().unwrap_or_else(|e| e.into_inner());
        let changed = *cur != status;
        *cur = status.clone();
        changed
    };
    if changed {
        eventlog::log(
            "status",
            json!({"awaiting": status.awaiting, "unread": status.unread}),
        );
        refresh_tray(&app, &d);
    }
    Ok(())
}

#[tauri::command]
pub async fn host_set_language(
    app: AppHandle,
    lang: String,
    d: State<'_, Arc<Desktop>>,
) -> Res<()> {
    let lang = Lang::from_tag(&lang);
    d.core
        .lock()
        .await
        .update_config(|c| c.lang = lang.as_str().to_string());
    d.set_prefs(|p| p.lang = lang);
    refresh_tray(&app, &d);
    Ok(())
}

#[tauri::command]
pub async fn host_reauth(path: String, d: State<'_, Arc<Desktop>>) -> Res<()> {
    let next = d.core.lock().await.reauth(&path).await;
    schedule(d.inner().clone(), next);
    Ok(())
}

#[tauri::command]
pub async fn host_info(d: State<'_, Arc<Desktop>>) -> Res<DesktopInfo> {
    Ok(desktop_info(&d).await)
}

#[tauri::command]
pub async fn host_set_settings(
    app: AppHandle,
    patch: SettingsPatch,
    d: State<'_, Arc<Desktop>>,
) -> Res<DesktopInfo> {
    #[cfg(desktop)]
    if let Some(shortcut) = patch.shortcut.as_deref() {
        if d.shortcut.apply(&app, shortcut).is_ok() {
            let shortcut = shortcut.trim().to_string();
            d.core.lock().await.update_config(|c| c.shortcut = shortcut);
        }
    }
    #[cfg(mobile)]
    let _ = &app;
    if patch.close_to_tray.is_some() || patch.auto_update.is_some() {
        d.core.lock().await.update_config(|c| {
            if let Some(v) = patch.close_to_tray {
                c.close_to_tray = v;
            }
            if let Some(v) = patch.auto_update {
                c.auto_update = v;
            }
        });
        d.set_prefs(|p| {
            if let Some(v) = patch.close_to_tray {
                p.close_to_tray = v;
            }
            if let Some(v) = patch.auto_update {
                p.auto_update = v;
            }
        });
    }
    Ok(desktop_info(&d).await)
}

#[tauri::command]
pub async fn host_check_update(app: AppHandle, d: State<'_, Arc<Desktop>>) -> Res<Option<Value>> {
    #[cfg(desktop)]
    {
        let found = crate::updater::check(&app, &d).await;
        Ok(found.and_then(|f| serde_json::to_value(f).ok()))
    }
    #[cfg(mobile)]
    {
        let _ = (app, d);
        Ok(None)
    }
}

#[tauri::command]
pub async fn host_install_update(app: AppHandle, d: State<'_, Arc<Desktop>>) -> Res<()> {
    #[cfg(desktop)]
    crate::updater::install(&app, &d);
    #[cfg(mobile)]
    let _ = (app, d);
    Ok(())
}

#[tauri::command]
pub async fn host_change_server(app: AppHandle, d: State<'_, Arc<Desktop>>) -> Res<()> {
    d.core.lock().await.request_change();
    show_main(&app);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn status_deserializes_camel_case() {
        let s: HostStatus = serde_json::from_value(json!({
            "awaiting": 1, "unread": 2,
            "items": [{"threadId": "T-1", "title": "a", "kind": "awaiting"}, {"threadId": "T-2", "kind": "unread"}]
        }))
        .unwrap();
        assert_eq!(s.items.len(), 2);
        assert_eq!(s.items[0].kind, StatusKind::Awaiting);
        assert_eq!(s.items[1].title, "");
        let p: SettingsPatch =
            serde_json::from_value(json!({"closeToTray": false, "shortcut": ""})).unwrap();
        assert_eq!(p.close_to_tray, Some(false));
        assert_eq!(p.shortcut.as_deref(), Some(""));
        assert_eq!(p.auto_update, None);
    }

    #[test]
    fn desktop_info_shape() {
        let info = DesktopInfo {
            version: "0.1.0".into(),
            settings: DesktopSettings {
                shortcut: "Control+Alt+Space".into(),
                close_to_tray: true,
                auto_update: false,
            },
            shortcut_error: None,
            token_store: TokenStoreKind::File,
            update: json!({"state": "none"}),
        };
        assert_eq!(
            serde_json::to_value(&info).unwrap(),
            json!({
                "version": "0.1.0",
                "settings": {"shortcut": "Control+Alt+Space", "closeToTray": true, "autoUpdate": false},
                "tokenStore": "file",
                "update": {"state": "none"}
            })
        );
    }
}
