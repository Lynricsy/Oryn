//! 移动端的 Tauri 接线：原生插件事件、推送订阅同步、分享下发、导航处理、移动端宿主命令与 iOS 原生底栏 / 页头。
//!
//! 逻辑在 [`crate::mobile_logic`]（主机上单测）；这里只把它接到插件、会话状态机与页面事件上。

use std::sync::{Arc, Mutex};

use serde_json::json;
use tauri::{AppHandle, Manager, State, Url};
use tauri_plugin_opener::OpenerExt;
use tauri_plugin_oryn_mobile::{HeaderMenuCmd, HeaderShow, HeaderSpec, TabBarSpec};

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

/// 原生底栏（iOS 26+）：整份状态；`null` 撤掉。其它平台与更低系统版本返回 `unsupported`，页面继续用网页底栏。
/// 非空 spec 要等本页 `host_sink_set(true)` 生效后才接受（bridge 8）：旧网页按 bridge 7 的语义直接 set，
/// 在这里被拒，走它已有的「set 被拒 → 网页底栏」路径。
#[tauri::command]
pub async fn host_tab_bar_set(app: AppHandle, spec: Option<TabBarSpec>) -> Res<()> {
    if spec
        .as_ref()
        .is_some_and(|s| !mobile_logic::valid_tab_bar(s))
    {
        return Err("invalid_tab_bar".into());
    }
    #[cfg(target_os = "ios")]
    {
        if !mobile_logic::native_chrome_allowed(ios::sink_on(), spec.is_some()) {
            return Err("unsupported".into());
        }
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin
            .tab_bar_set(spec.as_ref())
            .map_err(|e| e.to_string())?;
        // selected / frame：CI 端到端检查据此确认网页的分区切换与胶囊位置（mobile.yml 的 ios-check）
        eventlog::log(
            "tabbar.set",
            json!({
                "on": spec.is_some(),
                "selected": spec.as_ref().map(|s| &s.selected),
                "frame": spec.as_ref().map(|s| &s.frame),
            }),
        );
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = app;
        Err("unsupported".into())
    }
}

/// 网页内容（菜单、弹层、软键盘）是否压在底栏位置上：只决定原生底栏接不接触摸（卡片位置原生自己从根滚动算）。
/// 遮挡变化时发一条。还没 set 过时 Swift 侧空操作并成功。
#[tauri::command]
pub async fn host_tab_bar_cover(app: AppHandle, covered: bool) -> Res<()> {
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin.tab_bar_cover(covered).map_err(|e| e.to_string())?;
        // 仅 debug：供 CI 端到端检查确认遮挡状态；发布包不写
        #[cfg(debug_assertions)]
        eventlog::log("tabbar.cover", json!({"covered": covered}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = (app, covered);
        Err("unsupported".into())
    }
}

/// 下沉侧栏改由 WKWebView 的根滚动驱动（iOS 26+，bridge 8）：`true` 时原生按 `contentOffset` 跟随，
/// 之后本页的原生底栏与页头才允许接管；`false` 撤掉跟随。其它平台与更低系统版本返回 `unsupported`。
#[tauri::command]
pub async fn host_sink_set(app: AppHandle, on: bool) -> Res<()> {
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin.sink_set(on).map_err(|e| e.to_string())?;
        ios::set_sink_on(on);
        // CI 端到端检查据此确认接管顺序（sink.set 早于第一条 tabbar.set）
        eventlog::log("sink.set", json!({"on": on}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = (app, on);
        Err("unsupported".into())
    }
}

/// 程序化开合：原生把根滚动视图滚到打开位 / 关闭位（`animated` = UIKit 滚动动画）。网页自己发起的 smooth 滚动
/// 在 WKWebView 主框架上会被拉回（M20 探针），平滑开合因此交给原生。没有 `host_sink_set(true)` 时返回 `unsupported`。
#[tauri::command]
pub async fn host_sink_show(app: AppHandle, open: bool, animated: bool) -> Res<()> {
    #[cfg(target_os = "ios")]
    {
        if !ios::sink_on() {
            return Err("unsupported".into());
        }
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin
            .sink_show(open, animated)
            .map_err(|e| e.to_string())?;
        // 仅 debug：供 CI 端到端检查确认开合请求；发布包不写
        #[cfg(debug_assertions)]
        eventlog::log("sink.show", json!({"open": open, "animated": animated}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = (app, open, animated);
        Err("unsupported".into())
    }
}

/// 原生页头（iOS 26+，线程页的侧栏钮 / 标题胶囊 / 视图钮与两个菜单）：整份状态；`null` 撤掉。
/// 应答 = 原生已把它挂到窗口上。其它平台与更低系统版本返回 `unsupported`，页面继续用网页页头；
/// 非空 spec 同 `host_tab_bar_set` 要等 `host_sink_set(true)` 生效。
#[tauri::command]
pub async fn host_header_set(app: AppHandle, spec: Option<HeaderSpec>) -> Res<()> {
    if spec
        .as_ref()
        .is_some_and(|s| !mobile_logic::valid_header(s))
    {
        return Err("invalid_header".into());
    }
    #[cfg(target_os = "ios")]
    {
        if !mobile_logic::native_chrome_allowed(ios::sink_on(), spec.is_some()) {
            return Err("unsupported".into());
        }
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin
            .header_set(spec.as_ref())
            .await
            .map_err(|e| e.to_string())?;
        // key / 三件矩形：CI 端到端检查据此确认网页接管了页头与量出的位置（mobile.yml 的 ios-check）
        eventlog::log(
            "header.set",
            json!({
                "on": spec.is_some(),
                "key": spec.as_ref().map(|s| &s.key),
                "lead": spec.as_ref().map(|s| &s.lead.frame),
                "title": spec.as_ref().map(|s| &s.title.frame),
                "trail": spec.as_ref().and_then(|s| s.trail.as_ref()).map(|t| &t.frame),
            }),
        );
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = app;
        Err("unsupported".into())
    }
}

/// 原生页头控件层的透明度（水平位移与是否接触摸由原生按根滚动自己算）。还没 set 过时 Swift 侧空操作并成功。
#[tauri::command]
pub async fn host_header_show(app: AppHandle, show: HeaderShow) -> Res<()> {
    if !mobile_logic::valid_header_show(&show) {
        return Err("invalid_show".into());
    }
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin.header_show(&show).map_err(|e| e.to_string())?;
        // 仅 debug：供 CI 端到端检查断言让位 / 出现；发布包不写
        #[cfg(debug_assertions)]
        eventlog::log("header.show", json!({"alpha": show.alpha, "ms": show.ms}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = app;
        Err("unsupported".into())
    }
}

/// 打开 / 收起原生页头的菜单（网页强制收起：软键盘弹出、Esc）。不产生 `menu` 事件。
#[tauri::command]
pub async fn host_header_menu(app: AppHandle, menu: HeaderMenuCmd) -> Res<()> {
    if !mobile_logic::valid_header_menu(&menu) {
        return Err("invalid_menu".into());
    }
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin.header_menu(&menu).map_err(|e| e.to_string())?;
        // 仅 debug：供 CI 端到端检查断言网页的强制收起；发布包不写
        #[cfg(debug_assertions)]
        eventlog::log("header.menu", json!({"open": menu.open, "page": menu.page}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = app;
        Err("unsupported".into())
    }
}

/// 可替换图标（只有 iOS）：`{supported, current, options}`，id 见 `mobile_logic::APP_ICONS`。其它平台返回 `unsupported`。
#[tauri::command]
pub async fn host_app_icon_get(app: AppHandle) -> Res<mobile_logic::AppIconInfo> {
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        let s = plugin.app_icon_get().await.map_err(|e| e.to_string())?;
        Ok(mobile_logic::app_icon_info(
            s.supported,
            s.current.as_deref(),
        ))
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = app;
        Err("unsupported".into())
    }
}

/// 换成 id 对应的图标；系统改完（并弹出系统提示）才返回。不认识的 id 返回 `invalid_app_icon`。
#[tauri::command]
pub async fn host_app_icon_set(app: AppHandle, id: String) -> Res<()> {
    let asset = mobile_logic::app_icon_asset(&id).ok_or("invalid_app_icon")?;
    #[cfg(target_os = "ios")]
    {
        let plugin = ios::plugin(&app).ok_or("unsupported")?;
        plugin
            .app_icon_set(asset)
            .await
            .map_err(|e| e.to_string())?;
        eventlog::log("appicon.set", json!({"id": id}));
        Ok(())
    }
    #[cfg(not(target_os = "ios"))]
    {
        let _ = (app, asset);
        Err("unsupported".into())
    }
}

// ---------------------------------------------------------------- iOS 原生插件（原生底栏与页头）

#[cfg(target_os = "ios")]
mod ios {
    use std::sync::atomic::{AtomicBool, Ordering};

    use serde_json::json;
    use tauri::{AppHandle, Manager};
    use tauri_plugin_oryn_mobile::{Event, OrynMobile};

    use crate::mobile_logic;
    use crate::{eventlog, host};

    /// 当前页面的 `host_sink_set` 是否生效：原生底栏与页头的非空 set 以它为前提（整页导航时由 `sink_reset` 清掉）。
    static SINK_ON: AtomicBool = AtomicBool::new(false);

    pub fn sink_on() -> bool {
        SINK_ON.load(Ordering::Acquire)
    }

    pub fn set_sink_on(on: bool) {
        SINK_ON.store(on, Ordering::Release);
    }

    pub fn plugin(app: &AppHandle) -> Option<OrynMobile<tauri::Wry>> {
        app.try_state::<OrynMobile<tauri::Wry>>()
            .map(|s| s.inner().clone())
    }

    /// 插件事件：底栏点击转成页面事件 `{type:"tabbar", id}`，页头交互转成 `{type:"header", op, key, …}`。
    pub fn listen(app: &AppHandle) {
        let Some(p) = plugin(app) else { return };
        let handle = app.clone();
        let res = p.on_event(move |event| match event {
            Event::TabBar { id } => {
                if mobile_logic::valid_tab_id(&id) {
                    eventlog::log("tabbar.tap", json!({"id": id}));
                    host::emit(&handle, mobile_logic::tab_bar_event(&id));
                }
            }
            Event::Header { op, key, menu, id } => {
                if let Some(detail) = mobile_logic::header_event(op, &key, menu, id.as_deref()) {
                    // CI 端到端检查据此断言真实触摸的落点（select 的 menu / id）
                    eventlog::log(
                        "header.event",
                        json!({"op": op, "key": key, "menu": menu, "id": id}),
                    );
                    host::emit(&handle, detail);
                }
            }
            // 仅 debug：根滚动的轨迹（CI 端到端据此区分「原生动画晚起步 / 没跑」与「被拉回」，move 带调用栈查卡片被谁挪走）；
            // 发布包不写
            #[cfg(debug_assertions)]
            Event::Sink { op, x, to, stack } => {
                eventlog::log(
                    "sink.trace",
                    json!({"op": op, "x": x, "to": to, "stack": stack}),
                );
            }
            _ => {}
        });
        if let Err(e) = res {
            eventlog::log("mobile.listen_error", json!({"error": e.to_string()}));
        }
    }

    /// 主框架开始加载新文档：旧页面的底栏撤掉（页面来不及自己撤）。回调在主线程上，插件调用放进异步任务。
    pub fn tab_bar_reset(app: &AppHandle) {
        let Some(p) = plugin(app) else { return };
        tauri::async_runtime::spawn(async move {
            let _ = p.tab_bar_set(None);
        });
    }

    /// 主框架开始加载新文档：旧页面的原生页头撤掉。回调在主线程上，插件调用放进异步任务
    /// （`header_set` 要等 Swift 在主线程挂好 / 撤掉才应答，绝不能在主线程上等）。
    pub fn header_reset(app: &AppHandle) {
        let Some(p) = plugin(app) else { return };
        tauri::async_runtime::spawn(async move {
            let _ = p.header_set(None).await;
        });
    }

    /// 主框架开始加载新文档：清掉接管标志并撤掉根滚动跟随（新页面要重新 `host_sink_set`）。
    /// 标志同步清（新页面的 set 不会在它之前被放行），插件调用放进异步任务。
    pub fn sink_reset(app: &AppHandle) {
        set_sink_on(false);
        let Some(p) = plugin(app) else { return };
        tauri::async_runtime::spawn(async move {
            let _ = p.sink_set(false);
        });
    }

    /// CI 用（仅 debug 构建）：环境变量 `ORYN_HEADER_DEMO` = `hidden` / `bar` / `title` / `title:<子页 id>` / `trail` 时，
    /// 在连接页上按夹具摆出页头（`hidden` 之外都显示），按模式打开菜单，再取触摸探测结果，记到事件日志 `header.demo`。
    /// 经与页面相同的 Rust → Swift 路径。
    #[cfg(debug_assertions)]
    pub async fn header_demo(app: &AppHandle) {
        use tauri_plugin_oryn_mobile::{HeaderMenuCmd, HeaderMenuKind, HeaderShow};
        let Ok(mode) = std::env::var("ORYN_HEADER_DEMO") else {
            return;
        };
        // 等连接页的 PageLoadEvent::Started 那次撤除先跑完
        tokio::time::sleep(std::time::Duration::from_millis(1500)).await;
        let fail =
            |error: String| eventlog::log("header.demo", json!({"ok": false, "error": error}));
        let (Some(plugin), Some(w)) = (plugin(app), app.get_webview_window(host::MAIN)) else {
            return fail("no plugin or window".into());
        };
        // tao 在 iOS 上：outer = 屏幕框，inner = 安全区
        let scale = w.scale_factor().unwrap_or(1.0);
        let (Ok(outer), Ok(inner_pos), Ok(outer_pos)) =
            (w.outer_size(), w.inner_position(), w.outer_position())
        else {
            return fail("no window size".into());
        };
        let (width, height) = (outer.width as f64 / scale, outer.height as f64 / scale);
        let safe_top = ((inner_pos.y - outer_pos.y) as f64 / scale).max(0.0);
        let spec = mobile_logic::demo_header(width, safe_top);
        let (open, page) = match mode.split_once(':') {
            Some(("title", page)) => (Some(HeaderMenuKind::Title), Some(page.to_string())),
            None if mode == "title" => (Some(HeaderMenuKind::Title), None),
            None if mode == "trail" => (Some(HeaderMenuKind::Trail), None),
            None if mode == "bar" || mode == "hidden" => (None, None),
            _ => return fail(format!("unknown mode {mode}")),
        };
        let show = HeaderShow {
            alpha: if mode == "hidden" { 0.0 } else { 1.0 },
            ms: 0,
        };
        let res = async {
            plugin.header_set(Some(&spec)).await?;
            plugin.header_show(&show)?;
            plugin.header_menu(&HeaderMenuCmd { open, page })?;
            // 菜单的出现动画走完再探测
            tokio::time::sleep(std::time::Duration::from_millis(400)).await;
            plugin.header_probe().await
        }
        .await;
        match res {
            Ok(probe) => eventlog::log(
                "header.demo",
                json!({"ok": true, "mode": mode, "lead": spec.lead.frame, "title": spec.title.frame,
                       "trail": spec.trail.as_ref().map(|t| t.frame), "width": width, "height": height,
                       "scale": scale, "probe": probe}),
            ),
            Err(e) => fail(e.to_string()),
        }
    }

    /// CI 用（仅 debug 构建）：环境变量 `ORYN_E2E_URL=<http(s) 地址>` 时不读令牌、不配对，
    /// 把它当作已配对服务器的页面打开（[`crate::session::Core::open_unpaired`]：与生产相同的 `open_remote`，
    /// 远程能力与 `MOBILE_HOST_PERMISSIONS` 照常注入），结果记到事件日志 `e2e.open`。返回是否接管了启动。
    #[cfg(debug_assertions)]
    pub async fn e2e_open(d: &crate::host::Desktop) -> bool {
        let Ok(raw) = std::env::var("ORYN_E2E_URL") else {
            return false;
        };
        let res = match tauri::Url::parse(&raw) {
            Ok(u) if matches!(u.scheme(), "http" | "https") => {
                let origin = u.origin().ascii_serialization();
                d.core.lock().await.open_unpaired(&origin, u.as_str())
            }
            _ => Err("ORYN_E2E_URL is not an http(s) URL".to_string()),
        };
        let ok = res.is_ok();
        eventlog::log(
            "e2e.open",
            json!({"url": raw, "ok": ok, "error": res.err()}),
        );
        true
    }
}

#[cfg(all(debug_assertions, target_os = "ios"))]
pub use ios::{e2e_open, header_demo};
#[cfg(target_os = "ios")]
pub use ios::{header_reset, listen as listen_ios, sink_reset, tab_bar_reset};

// ---------------------------------------------------------------- Android 原生插件

#[cfg(target_os = "android")]
mod android {
    use std::sync::Mutex;

    use serde_json::json;
    use tauri::{AppHandle, Manager};
    use tauri_plugin_oryn_mobile::{Event, OrynMobile, PushState};

    use super::{deliver_share, runtime, sync_push};
    use crate::eventlog;
    use crate::host;
    use crate::mobile_logic::{self, MainWindowLife, Native};
    use crate::webview::{self, RemoteOrigin};

    /// 主窗口随 Activity 销毁后重建要用的状态（`app.manage`）。
    pub struct MainLife {
        life: Mutex<MainWindowLife>,
        remote: RemoteOrigin,
    }

    /// `setup` 建好主窗口后调用。
    pub fn manage_main_life(app: &AppHandle, remote: RemoteOrigin) {
        app.manage(MainLife {
            life: Mutex::new(MainWindowLife::default()),
            remote,
        });
    }

    /// 对主窗口生命周期状态做一步；`true` = 现在为新 Activity 建主窗口。
    fn step(app: &AppHandle, f: impl FnOnce(&mut MainWindowLife) -> bool) -> bool {
        let Some(m) = app.try_state::<MainLife>() else {
            return false;
        };
        let mut life = m.life.lock().unwrap_or_else(|e| e.into_inner());
        f(&mut life)
    }

    /// 为同一进程里新建的 Activity 重建主窗口（tao 取尚无窗口的 Activity 上下文，wry 在它里面建 WebView）；
    /// WebView 建好后插件改绑并报 `recreated`，新连接页按设备令牌恢复会话。
    fn recreate_main(app: &AppHandle) {
        let app = app.clone();
        tauri::async_runtime::spawn(async move {
            let Some(m) = app.try_state::<MainLife>() else {
                return;
            };
            match webview::create_main(&app, &m.remote) {
                Ok(_) => eventlog::log("window.recreated", json!({})),
                Err(e) => {
                    m.life
                        .lock()
                        .unwrap_or_else(|e| e.into_inner())
                        .create_failed();
                    eventlog::log("window.recreate_error", json!({"error": e.to_string()}));
                }
            }
        });
    }

    /// 主窗口随 Activity 销毁（`RunEvent::WindowEvent::Destroyed`；此时 Tauri 已删掉这个标签）。
    /// 页面没了：之后的待开路径留给新页面的 `host_ready`，不再派发给不存在的页面。
    pub fn main_destroyed(app: &AppHandle) {
        eventlog::log("window.destroyed", json!({}));
        if let Some(d) = host::desktop(app) {
            d.nav().web_ready = false;
        }
        if step(app, MainWindowLife::destroyed) {
            recreate_main(app);
        }
    }

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
            Event::Activity => {
                eventlog::log("lifecycle", json!({"state": "activity"}));
                if step(&app, MainWindowLife::activity_created) {
                    recreate_main(&app);
                }
            }
            // 原生底栏与页头只在 iOS 上有，Kotlin 不发
            Event::TabBar { .. } | Event::Header { .. } => {}
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
pub use android::{listen, main_destroyed, manage_main_life};
