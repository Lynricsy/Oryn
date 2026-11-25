//! 主窗口：创建与钩子、远程能力、Rust → 页面事件、回连接页。

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};
use tauri::ipc::CapabilityBuilder;
#[cfg(desktop)]
use tauri::utils::config::BackgroundThrottlingPolicy;
#[cfg(desktop)]
use tauri::webview::NewWindowResponse;
use tauri::webview::PageLoadEvent;
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use crate::session::{LauncherState, Shell};
use crate::{eventlog, host};

/// 远程页面可调用的宿主命令（= `capabilities` 里的 `allow-host-*`），按平台取。
#[cfg(desktop)]
pub const HOST_PERMISSIONS: &[&str] = DESKTOP_HOST_PERMISSIONS;
#[cfg(mobile)]
pub const HOST_PERMISSIONS: &[&str] = MOBILE_HOST_PERMISSIONS;

#[cfg_attr(mobile, allow(dead_code))]
const DESKTOP_HOST_PERMISSIONS: &[&str] = &[
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
    "allow-host-open-external",
    "allow-host-window",
];

/// 移动端：没有托盘、快捷键与更新器的命令，另有推送、外链、分享确认、系统栏、下沉侧栏、原生底栏、原生页头、可替换图标等十六个。
#[cfg_attr(desktop, allow(dead_code))]
const MOBILE_HOST_PERMISSIONS: &[&str] = &[
    "allow-host-ready",
    "allow-host-notify",
    "allow-host-set-status",
    "allow-host-set-language",
    "allow-host-reauth",
    "allow-host-change-server",
    "allow-host-mobile-info",
    "allow-host-push-enable",
    "allow-host-push-disable",
    "allow-host-open-external",
    "allow-host-share-ack",
    "allow-host-set-chrome",
    "allow-host-open-notification-settings",
    "allow-host-sync-assistant-avatar",
    "allow-host-tab-bar-set",
    "allow-host-tab-bar-cover",
    "allow-host-sink-set",
    "allow-host-sink-show",
    "allow-host-header-set",
    "allow-host-header-show",
    "allow-host-header-menu",
    "allow-host-app-icon-get",
    "allow-host-app-icon-set",
];

/// 已开放宿主命令、允许主框架导航的服务器 origin（Android 的导航处理与 [`TauriShell`] 共用）。
pub type RemoteOrigin = Arc<Mutex<Option<String>>>;

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

/// 主框架外链拦截脚本（仅随 `initialization_script` 注入主框架，绝不触碰 portal / artifact 等 iframe）：
/// - 跳过连接页本身（非 http(s) 或 `tauri.localhost` 或 `/launcher.html`）；
/// - 在 `window` 捕获期拦截 `click` 与中键 `auxclick` 的外链（`target="_blank"`、`<base target="_blank">`、
///   跨源 `http(s)`、修饰键 Ctrl/Meta/Shift 点击、`mailto:`/`tel:`），调用 `host_open_external` 在系统浏览器打开；
/// - 覆写主框架 `window.open`，绕过 WebKit / WKWebView 弹窗拦截器并直接交由 `host_open_external`；
/// - 只调 `e.preventDefault()` 不阻止冒泡，避免 `host.ts` 重复触发的同时不影响菜单 / 弹层的点击收起。
const EXTERNAL_LINK_GUARD_SCRIPT: &str = r##"(function(){if(window!==window.top)return;var loc=window.location;if(!loc||(loc.protocol!=="http:"&&loc.protocol!=="https:"))return;if(loc.hostname==="tauri.localhost"||loc.pathname==="/launcher.html")return;var openExt=function(u){var inv=(window.__TAURI__&&window.__TAURI__.core&&window.__TAURI__.core.invoke)||(window.__TAURI_INTERNALS__&&window.__TAURI_INTERNALS__.invoke);if(inv)inv("host_open_external",{url:u}).catch(function(){});};var resolveExt=function(a,forceNew){var raw=a.getAttribute("href");if(!raw||raw.charAt(0)==="#")return null;var cur=window.location;var u;try{u=new URL(raw,cur.href);}catch(_){return null;}if(u.protocol==="mailto:"||u.protocol==="tel:")return u.href;if(u.protocol!=="http:"&&u.protocol!=="https:")return null;var t=a.getAttribute("target");if(!t){var b=a.ownerDocument&&a.ownerDocument.querySelector("base[target]");if(b)t=b.getAttribute("target");}if(forceNew||t==="_blank"||u.origin!==cur.origin)return u.href;return null;};var findAnchor=function(e){if(typeof e.composedPath==="function"){var p=e.composedPath();for(var i=0;i<p.length;i++){var n=p[i];if(n&&n.nodeType===1&&n.tagName&&n.tagName.toUpperCase()==="A"&&n.hasAttribute("href"))return n;}}var t=e.target;var el=t&&(t.nodeType===1?t:t.parentElement);return el&&typeof el.closest==="function"?el.closest("a[href]"):null;};var onClick=function(e){if(e.defaultPrevented)return;var aux=e.type==="auxclick";if(aux?e.button!==1:e.button!==0)return;var a=findAnchor(e);if(!a||a.hasAttribute("download"))return;var href=resolveExt(a,aux||e.ctrlKey||e.metaKey||e.shiftKey);if(!href)return;e.preventDefault();openExt(href);};window.addEventListener("click",onClick,true);window.addEventListener("auxclick",onClick,true);window.open=function(u){if(u!==undefined&&u!==""){try{var parsed=new URL(String(u),window.location.href);if(parsed.protocol==="http:"||parsed.protocol==="https:"||parsed.protocol==="mailto:"||parsed.protocol==="tel:")openExt(parsed.href);}catch(_){}}return null;};})();"##;

/// 注入每个主框架的宿主标识与外链保护；`version` 取 Tauri 配置的应用版本（与更新器比较用的是同一个）。
/// `chrome`：桌面的标题栏布局（见 `chrome.rs`），页面首帧前就按它留位置；移动端没有。
pub fn init_script(version: &str, chrome: Option<Value>) -> String {
    let mut host = json!({
        "kind": "tauri",
        "platform": host_platform(),
        "version": version,
        "bridge": host::BRIDGE,
    });
    if let Some(c) = chrome {
        host["chrome"] = c;
    }
    format!("window.__ORYN_HOST__ = Object.freeze({host});\n{EXTERNAL_LINK_GUARD_SCRIPT}")
}

/// 页面事件脚本：`window.dispatchEvent(new CustomEvent('oryn:host', {detail}))`。
pub fn event_script(detail: &Value) -> String {
    format!("window.dispatchEvent(new CustomEvent('oryn:host',{{detail:{detail}}}));")
}

/// 两个 URL 同源（按 scheme + host + port 比较：`tauri://` 这类非特殊 scheme 的 `Url::origin` 是不透明的，彼此永不相等）。
pub fn same_origin(a: &Url, b: &Url) -> bool {
    crate::mobile_logic::same_origin(a, b)
}

/// 主框架导航拦截（Android `shouldOverrideUrlLoading`、Windows WebView2 `NavigationStarting`，两者均只触发主框架，不影响 iframe）。
/// 副作用全部放到异步任务，避免在 WebView UI 线程回调中重入或死锁。
#[cfg(any(target_os = "android", target_os = "windows"))]
fn on_navigation(app: &AppHandle, url: &Url, launcher: &str, server: Option<&str>) -> bool {
    use crate::mobile_logic::NavDecision;
    let decision = crate::mobile_logic::nav_decision(url.as_str(), launcher, server);
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

/// 连接页在应用资源里的路径。
const LAUNCHER_PAGE: &str = "launcher.html";
/// 创建主窗口（`tauri.conf.json` 的 `windows` 为空：钩子只能在代码里挂）。
/// `remote` 为已开放的服务器 origin（Android 的导航白名单用）。
pub fn create_main(app: &AppHandle, remote: &RemoteOrigin) -> tauri::Result<WebviewWindow> {
    let launcher = launcher_url(app)?;
    #[cfg(any(target_os = "android", target_os = "windows"))]
    let launcher_origin = launcher.as_str().to_string();
    let loader = app.clone();
    let load_remote = remote.clone();
    let load_launcher = launcher.as_str().to_string();
    #[cfg(desktop)]
    let chrome = serde_json::to_value(crate::chrome::current()).ok();
    #[cfg(mobile)]
    let chrome = None;
    let builder = WebviewWindowBuilder::new(app, host::MAIN, WebviewUrl::App(LAUNCHER_PAGE.into()))
        .title("Oryn")
        .initialization_script(init_script(&app.package_info().version.to_string(), chrome))
        .on_page_load(move |_window, payload| {
            let Some(d) = host::desktop(&loader) else {
                return;
            };
            // 有页面加载事件 = 主 WebView 已建好，此后的 navigate 不会再被丢弃
            #[cfg(mobile)]
            d.webview_up.open();
            match payload.event() {
                // 主框架开始加载任何页面（回连接页、兑换跳转、用户刷新）都先清掉就绪标志：
                // 其间的 open_path 存为待开路径，由新页面的 host_ready 取走，不会派发给正在卸载的页面
                PageLoadEvent::Started => {
                    d.nav().web_ready = false;
                    // 整页导航时旧页面来不及自己撤掉原生底栏与页头
                    #[cfg(target_os = "ios")]
                    crate::mobile::tab_bar_reset(&loader);
                    #[cfg(target_os = "ios")]
                    crate::mobile::header_reset(&loader);
                    #[cfg(target_os = "ios")]
                    crate::mobile::sink_reset(&loader);
                    // 主框架逃逸兜底（on_page_load 在全平台均严格只针对主框架，不影响 portal / artifact iframe）：
                    // 若服务端 302、拖拽链接或 iframe target="_top" 等把主窗口带离 Oryn，立即交给系统浏览器并拉回应用
                    let server = load_remote
                        .lock()
                        .unwrap_or_else(|e| e.into_inner())
                        .clone();
                    if let Some(server) = server {
                        use crate::mobile_logic::NavDecision;
                        let url = payload.url().to_string();
                        let decision =
                            crate::mobile_logic::nav_decision(&url, &load_launcher, Some(&server));
                        if decision != NavDecision::Allow {
                            let app = loader.clone();
                            tauri::async_runtime::spawn(async move {
                                match decision {
                                    NavDecision::External => {
                                        let _ = app.opener().open_url(&url, None::<&str>);
                                        eventlog::log(
                                            "nav.external",
                                            json!({"url": eventlog::redact_url(&url)}),
                                        );
                                    }
                                    NavDecision::DeepLink => {
                                        crate::deeplink::open_url(&app, &url);
                                    }
                                    NavDecision::Block | NavDecision::Allow => {}
                                }
                                if let (Ok(back), Some(w)) =
                                    (Url::parse(&server), app.get_webview_window(host::MAIN))
                                {
                                    let _ = w.navigate(back);
                                    eventlog::log("nav.recovered", json!({"to": server}));
                                }
                            });
                        }
                    }
                }
                // 首个页面加载完时窗口已映射，外框才是恢复后的实际值（刚 build 时 GTK 上为 0×0）
                PageLoadEvent::Finished => {
                    #[cfg(desktop)]
                    host::note_geometry(_window.app_handle());
                }
            }
        })
        .enable_clipboard_access();
    #[cfg(desktop)]
    let builder = {
        let opener = app.clone();
        builder
            .inner_size(1280.0, 820.0)
            .min_inner_size(380.0, 560.0)
            .on_new_window(move |url, _features| {
                // 兜住 iframe（portal / artifact）或旧页面的新窗口请求：一律交给系统浏览器
                if let Some(ext) = crate::mobile_logic::external_url(url.as_str()) {
                    let _ = opener.opener().open_url(&ext, None::<&str>);
                    eventlog::log("browser.open", json!({"url": eventlog::redact_url(&ext)}));
                }
                NewWindowResponse::Deny
            })
            .disable_drag_drop_handler()
            .background_throttling(BackgroundThrottlingPolicy::Disabled)
            .on_download(crate::download::handler(app.clone()))
    };
    // 无系统标题栏（chrome.rs）：macOS 保留原生红绿灯、内容铺到标题栏下；Windows / Linux 去掉装饰，页面自绘按钮
    #[cfg(target_os = "macos")]
    let builder = builder
        .title_bar_style(tauri::TitleBarStyle::Overlay)
        .hidden_title(true)
        .traffic_light_position(tauri::LogicalPosition::new(
            crate::chrome::TRAFFIC_LIGHT_X,
            crate::chrome::lights().y,
        ));
    #[cfg(all(desktop, not(target_os = "macos")))]
    let builder = builder.decorations(false);
    // Android（shouldOverrideUrlLoading）与 Windows（WebView2 NavigationStarting）的 on_navigation
    // 均只触发主框架导航，不影响 portal / artifact 的 http(s) iframe，直接前置拦截；
    // macOS / Linux / iOS 的 wry on_navigation 会拦截 iframe，故由上面的主框架 init_script + on_page_load 兜底。
    #[cfg(any(target_os = "android", target_os = "windows"))]
    let builder = {
        let app = app.clone();
        let remote = remote.clone();
        builder.on_navigation(move |url| {
            let server = remote.lock().unwrap_or_else(|e| e.into_inner()).clone();
            on_navigation(&app, url, &launcher_origin, server.as_deref())
        })
    };
    let window = builder.build()?;
    // Tauri 没有工具栏的 builder 选项：建好后（仍在主线程）再挂，换上与 `chrome.radius` 对应的大圆角外框
    #[cfg(target_os = "macos")]
    crate::chrome::attach_toolbar(&window);
    Ok(window)
}

/// 连接页的 URL。移动端 WebView 异步创建、`build()` 返回时 `url()` 还是空串，
/// 桌面端在 `create_main` 内挂载钩子时也尚未拿到 `window.url()`，统一按 Tauri 的规则推出：
/// 开发态取 devUrl，否则 Android / Windows 为 `http://tauri.localhost/`、
/// macOS / Linux / iOS 为 `tauri://localhost/`（未开 useHttpsScheme）。
pub fn launcher_url(app: &AppHandle) -> tauri::Result<Url> {
    let base = match app.config().build.dev_url.clone() {
        Some(dev) if tauri::is_dev() => dev,
        _ => Url::parse(if cfg!(any(target_os = "android", target_os = "windows")) {
            "http://tauri.localhost/"
        } else {
            "tauri://localhost/"
        })
        .map_err(tauri::Error::InvalidUrl)?,
    };
    base.join(LAUNCHER_PAGE).map_err(tauri::Error::InvalidUrl)
}

/// [`Shell`] 的 Tauri 实现。
pub struct TauriShell {
    app: AppHandle,
    launcher_url: Url,
    /// 已开放宿主命令的 origin（桌面每进程一个）。
    capability: RemoteOrigin,
    /// 已添加的远程能力个数（移动端换服务器不重启，每个 origin 各加一个）。
    #[cfg(mobile)]
    added: std::sync::atomic::AtomicU32,
}

impl TauriShell {
    pub fn new(app: AppHandle, launcher_url: Url, capability: RemoteOrigin) -> Self {
        Self {
            app,
            launcher_url,
            capability,
            #[cfg(mobile)]
            added: Default::default(),
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
            // 已为另一个 origin 开放过（没有移除能力的 API）：重启进程，启动时按新配置进入
            #[cfg(desktop)]
            Some(_) => {
                host::save_window_state(&self.app);
                self.app.restart();
            }
            _ => {
                // 移动端不能重启进程：给新 origin 另加一个能力；旧 origin 不再能被主框架导航到（见 mobile_logic::nav_decision）
                #[cfg(desktop)]
                let id = "remote-host".to_string();
                #[cfg(mobile)]
                let id = match self.added.fetch_add(1, std::sync::atomic::Ordering::SeqCst) {
                    0 => "remote-host".to_string(),
                    n => format!("remote-host-{n}"),
                };
                let mut builder = CapabilityBuilder::new(id)
                    .local(false)
                    .remote(format!("{origin}/*"))
                    .window(host::MAIN);
                for &p in HOST_PERMISSIONS {
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

    /// 桌面：重启进程（丢掉旧 origin 的远程能力）。移动端没有重启：留在连接页。
    fn restart(&self) {
        #[cfg(desktop)]
        {
            host::save_window_state(&self.app);
            self.app.restart();
        }
        #[cfg(mobile)]
        self.show_launcher();
    }

    fn push_subscription_id(&self) -> Option<String> {
        #[cfg(mobile)]
        return crate::mobile::runtime(&self.app).and_then(|m| m.core.subscription_id());
        #[cfg(desktop)]
        None
    }

    /// 解除配对：网关授权挪进待吊销队列、停用本机推送、下次配对后自动启用；随后异步尽力吊销。
    fn push_disable(&self) {
        #[cfg(mobile)]
        if let Some(m) = crate::mobile::runtime(&self.app) {
            m.core.unpaired();
            tauri::async_runtime::spawn(async move {
                m.core.flush_revokes().await;
            });
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scripts() {
        let s = init_script("9.8.7-rc.1", None);
        assert!(s.starts_with("window.__ORYN_HOST__ = Object.freeze({"));
        assert!(s.contains("\"version\":\"9.8.7-rc.1\""));
        assert!(s.contains("\"kind\":\"tauri\""));
        assert!(s.contains("\"bridge\":11"));
        assert!(s.contains("host_open_external"));
        assert!(s.contains("window!==window.top"));
        assert!(!s.contains("chrome"));
        let s = init_script("1.0.0", Some(json!({"controls": "custom-right"})));
        assert!(s.contains("\"chrome\":{\"controls\":\"custom-right\"}"));
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
        assert_eq!(DESKTOP_HOST_PERMISSIONS.len(), 12);
        assert!(DESKTOP_HOST_PERMISSIONS.contains(&"allow-host-open-external"));
        assert_eq!(MOBILE_HOST_PERMISSIONS.len(), 23);
    }
    /// 两个平台的远程权限都对应 `build.rs` 里声明的命令；移动端连接页能力只在移动平台生效、
    /// 除系统栏颜色外不含任何宿主命令。
    #[test]
    fn permissions_match_commands() {
        let build = include_str!("../build.rs");
        for p in DESKTOP_HOST_PERMISSIONS
            .iter()
            .chain(MOBILE_HOST_PERMISSIONS)
        {
            let cmd = p.strip_prefix("allow-").unwrap().replace('-', "_");
            assert!(build.contains(&format!("\"{cmd}\"")), "{p} not in build.rs");
        }
        for desktop_only in [
            "allow-host-info",
            "allow-host-set-settings",
            "allow-host-check-update",
            "allow-host-install-update",
            "allow-host-window",
        ] {
            assert!(!MOBILE_HOST_PERMISSIONS.contains(&desktop_only));
        }
        // 桌面连接页只追加窗口操作一项，且只在桌面平台生效
        let cap: Value =
            serde_json::from_str(include_str!("../capabilities/launcher-desktop.json")).unwrap();
        assert_eq!(cap["platforms"], json!(["linux", "macOS", "windows"]));
        assert_eq!(cap["local"], true);
        assert_eq!(cap["permissions"], json!(["allow-host-window"]));
        // 通用连接页能力不含任何宿主命令
        let cap: Value =
            serde_json::from_str(include_str!("../capabilities/launcher.json")).unwrap();
        for p in cap["permissions"].as_array().unwrap() {
            assert!(p.as_str().unwrap().starts_with("allow-launcher-"), "{p}");
        }
        let cap: Value =
            serde_json::from_str(include_str!("../capabilities/launcher-mobile.json")).unwrap();
        assert_eq!(cap["platforms"], json!(["android", "iOS"]));
        assert_eq!(cap["local"], true);
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| p.as_str().unwrap())
            .collect();
        // 移动连接页只多两项：扫码期间监听返回键以取消扫码（core:app 的监听注册 / 移除），不放开 core:app 的其它命令
        const APP_LISTENER: [&str; 2] = [
            "core:app:allow-register-listener",
            "core:app:allow-remove-listener",
        ];
        for p in &perms {
            assert!(
                *p == "allow-host-set-chrome"
                    || p.starts_with("barcode-scanner:")
                    || APP_LISTENER.contains(p),
                "{p}"
            );
        }
    }
}
