//! 系统通知：限流、通知号分配与点击回查（官方 `tauri-plugin-notification`，只在 Rust 侧调用）。

use std::collections::VecDeque;
use std::sync::Mutex;

use serde::Deserialize;
use serde_json::json;
use tauri::AppHandle;
#[cfg(not(any(target_os = "linux", target_os = "android")))]
use tauri_plugin_notification::NotificationExt;

use crate::session::valid_app_path;
use crate::{eventlog, host};

/// 标题与正文的最大字符数。
pub const TITLE_MAX: usize = 120;
pub const BODY_MAX: usize = 300;
/// 记住的通知条数。
pub const REGISTRY_CAP: usize = 64;
/// 令牌桶：突发 5，每 6 秒补 1。
pub const BURST: f64 = 5.0;
pub const REFILL_MS: f64 = 6_000.0;

/// Linux 通知的应用名（通知中心显示的名字、dunst 的 `appname`）。
pub const APP_NAME: &str = "Oryn";
/// 安装包里 `.desktop` 文件的名字（deb / rpm / AppImage 都是 `Oryn.desktop`，= `productName`）。
pub const DESKTOP_ENTRY: &str = "Oryn";
/// 已安装的图标名（hicolor 下的 `oryn-desktop.png`，= `mainBinaryName`）。
pub const ICON: &str = "oryn-desktop";
/// XDG 通知规范里「点击通知本体」对应的动作；只有声明了它的通知，服务器才会上报点击。
pub const DEFAULT_ACTION: &str = "default";

/// `host_notify` 的参数（来自远程页面）。
#[derive(Debug, Clone, Deserialize)]
pub struct HostNotification {
    pub id: String,
    pub title: String,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub path: String,
}

/// 按字符截断；截断时末尾补 `…`（计入上限）。
pub fn truncate(s: &str, max: usize) -> String {
    let s = s.trim();
    if s.chars().count() <= max {
        return s.to_string();
    }
    let mut out: String = s.chars().take(max.saturating_sub(1)).collect();
    out.push('…');
    out
}

/// 通知号 → {页面通知 id, 路径}，最多 [`REGISTRY_CAP`] 条，超出淘汰最旧。
#[derive(Default)]
pub struct Registry {
    entries: VecDeque<(i32, String, String)>,
}

impl Registry {
    pub fn insert(&mut self, n: i32, id: String, path: String) {
        if self.entries.len() >= REGISTRY_CAP {
            self.entries.pop_front();
        }
        self.entries.push_back((n, id, path));
    }

    /// 点击回查：返回 `(页面通知 id, 路径)`。
    pub fn find(&self, n: i32) -> Option<(&str, &str)> {
        self.entries
            .iter()
            .find(|(k, _, _)| *k == n)
            .map(|(_, id, path)| (id.as_str(), path.as_str()))
    }

    #[cfg(test)]
    pub fn len(&self) -> usize {
        self.entries.len()
    }
}

/// 令牌桶限流（时间由调用方注入）。
pub struct Bucket {
    tokens: f64,
    last_ms: Option<i64>,
}

impl Default for Bucket {
    fn default() -> Self {
        Self {
            tokens: BURST,
            last_ms: None,
        }
    }
}

impl Bucket {
    /// 取一个令牌；没有则 `false`（丢弃该通知）。
    pub fn take(&mut self, now_ms: i64) -> bool {
        if let Some(last) = self.last_ms {
            let elapsed = (now_ms - last).max(0) as f64;
            self.tokens = (self.tokens + elapsed / REFILL_MS).min(BURST);
        }
        self.last_ms = Some(now_ms);
        if self.tokens >= 1.0 {
            self.tokens -= 1.0;
            true
        } else {
            false
        }
    }
}

#[derive(Default)]
struct Inner {
    next: i32,
    registry: Registry,
    bucket: Bucket,
}

/// 进程级通知状态。
#[derive(Default)]
pub struct Notifier {
    inner: Mutex<Inner>,
}

/// 一条经过净化、已分配号码的通知。
#[derive(Debug, PartialEq, Eq)]
pub struct Prepared {
    pub n: i32,
    pub title: String,
    pub body: String,
    pub path: String,
}

impl Notifier {
    fn lock(&self) -> std::sync::MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// 分配下一个通知号（从 1 递增，溢出回绕）。
    #[cfg(not(any(target_os = "linux", target_os = "android")))]
    pub fn next_id(&self) -> i32 {
        let mut g = self.lock();
        g.next = g.next.checked_add(1).unwrap_or(1);
        g.next
    }

    /// 净化、限流、登记；被限流时 `None`。
    pub fn prepare(&self, n: HostNotification, now_ms: i64) -> Option<Prepared> {
        let mut g = self.lock();
        if !g.bucket.take(now_ms) {
            return None;
        }
        g.next = g.next.checked_add(1).unwrap_or(1);
        let num = g.next;
        let path = valid_app_path(&n.path);
        g.registry.insert(num, n.id, path.clone());
        Some(Prepared {
            n: num,
            title: truncate(&n.title, TITLE_MAX),
            body: truncate(&n.body, BODY_MAX),
            path,
        })
    }

    /// 点击回查。
    pub fn lookup(&self, n: i32) -> Option<(String, String)> {
        let g = self.lock();
        g.registry
            .find(n)
            .map(|(id, path)| (id.to_string(), path.to_string()))
    }
}

/// `host_notify`：净化、限流后显示系统通知。
pub fn show(app: &AppHandle, notifier: &Notifier, n: HostNotification) {
    let id = n.id.clone();
    let Some(p) = notifier.prepare(n, eventlog::now_ms()) else {
        eventlog::log(
            "notify.dropped",
            json!({"id": id, "reason": "rate_limited"}),
        );
        return;
    };
    deliver(app, id, p);
}

fn log_shown(id: &str, p: &Prepared) {
    eventlog::log(
        "notify.shown",
        json!({"id": id, "n": p.n, "title": p.title, "path": p.path}),
    );
}

fn log_error(id: &str, e: impl ToString) {
    eventlog::log("notify.error", json!({"id": id, "error": e.to_string()}));
}

/// 点击通知本体：回查通知号 → 记录 `notify.clicked` → 显示主窗口并打开对应路径。
/// macOS / Windows 由插件的 `on_action` 调到这里，Linux 由 [`deliver`] 的等待线程调到这里。
pub fn clicked(app: &AppHandle, n: i32) {
    let Some(d) = host::desktop(app) else { return };
    if let Some((id, path)) = d.notifier.lookup(n) {
        eventlog::log("notify.clicked", json!({"id": id, "n": n, "path": path}));
        host::open_path(app, &path);
    }
}

/// 把纯文本转成通知正文的 markup 文本：`&` `<` `>` 换成实体（XDG 通知规范的 body-markup 是 XML 子集），
/// 正文里的 `<a href>`、`<img>` 等就只会原样显示。
#[cfg_attr(not(target_os = "linux"), allow(dead_code))]
pub fn escape_markup(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            c => out.push(c),
        }
    }
    out
}

/// 通知服务是否声明了 `body-markup`；查询失败按支持处理（宁可多转义，也不让正文被当成标记）。
#[cfg(target_os = "linux")]
fn server_body_markup() -> bool {
    notify_rust::get_capabilities().map_or(true, |caps| caps.iter().any(|c| c == "body-markup"))
}

/// 构建 Linux 通知：应用名 `Oryn`、`desktop-entry` 提示指向安装的 `.desktop`；
/// `clickable` 时声明 `default` 动作，服务器才会上报点击。
/// 正文来自 agent 输出：服务器支持 body-markup（`body_markup`）时先转义。标题按规范是纯文本，服务器不解析
/// 标记（GNOME、Plasma、dunst、mako 都按纯文本显示），转义反而会把 `&amp;` 原样显示出来，所以不动。
#[cfg(target_os = "linux")]
pub fn linux_notification(
    title: &str,
    body: &str,
    clickable: bool,
    body_markup: bool,
) -> notify_rust::Notification {
    let body = if body_markup {
        escape_markup(body)
    } else {
        body.to_string()
    };
    let mut n = notify_rust::Notification::new();
    n.appname(APP_NAME)
        .summary(title)
        .body(&body)
        .icon(ICON)
        .hint(notify_rust::Hint::DesktopEntry(DESKTOP_ENTRY.into()));
    if clickable {
        n.action(DEFAULT_ACTION, "");
    }
    n
}

/// Linux：在单独线程里发送，并用同一连接等待用户动作（dunst 只把信号发给发送通知的连接）。
/// 不设 notify-rust 的 `id`：那是 D-Bus 的 `replaces_id`（服务器全局编号），设成我们的通知号会顶掉别的通知。
#[cfg(target_os = "linux")]
fn deliver(app: &AppHandle, id: String, p: Prepared) {
    let app = app.clone();
    let log_id = id.clone();
    let spawned = std::thread::Builder::new()
        .name("notification".into())
        .spawn(move || {
            match linux_notification(&p.title, &p.body, true, server_body_markup()).show() {
                Ok(handle) => {
                    log_shown(&id, &p);
                    // 关闭时回调收到 `__closed`，只处理点击
                    handle.wait_for_action(|action| {
                        if action == DEFAULT_ACTION {
                            clicked(&app, p.n);
                        }
                    });
                }
                Err(e) => log_error(&id, e),
            }
        });
    if let Err(e) = spawned {
        log_error(&log_id, e);
    }
}

/// macOS / Windows / iOS：走官方插件，点击经 `on_action` 回到 [`clicked`]。
#[cfg(not(any(target_os = "linux", target_os = "android")))]
fn deliver(app: &AppHandle, id: String, p: Prepared) {
    let result = app
        .notification()
        .builder()
        .id(p.n)
        .title(&p.title)
        .body(&p.body)
        .show();
    match result {
        Ok(()) => log_shown(&id, &p),
        Err(e) => log_error(&id, e),
    }
}

/// Android：暂不显示（不带通知插件；由 oryn-mobile 插件的 `notify` 实现，见 M7 切片 A）。
#[cfg(target_os = "android")]
fn deliver(_app: &AppHandle, id: String, p: Prepared) {
    eventlog::log(
        "notify.dropped",
        json!({"id": id, "n": p.n, "reason": "unsupported"}),
    );
}

/// 显示一条不可点击跳转的提示（如下载完成）。
pub fn show_plain(app: &AppHandle, notifier: &Notifier, title: &str, body: &str) {
    let (title, body) = (truncate(title, TITLE_MAX), truncate(body, BODY_MAX));
    #[cfg(target_os = "linux")]
    {
        let _ = (app, notifier);
        let _ = std::thread::Builder::new()
            .name("notification".into())
            .spawn(move || {
                if let Err(e) =
                    linux_notification(&title, &body, false, server_body_markup()).show()
                {
                    log_error("", e);
                }
            });
    }
    #[cfg(not(any(target_os = "linux", target_os = "android")))]
    {
        let _ = app
            .notification()
            .builder()
            .id(notifier.next_id())
            .title(title)
            .body(body)
            .show();
    }
    // Android：暂不显示（同 deliver）
    #[cfg(target_os = "android")]
    let _ = (app, notifier, title, body);
}

#[cfg(test)]
mod tests {
    use super::*;

    fn note(id: &str, path: &str) -> HostNotification {
        HostNotification {
            id: id.into(),
            title: "t".into(),
            body: "b".into(),
            path: path.into(),
        }
    }

    #[test]
    fn registry_evicts_oldest() {
        let mut r = Registry::default();
        for i in 0..(REGISTRY_CAP as i32 + 3) {
            r.insert(i, format!("n{i}"), format!("/t/{i}"));
        }
        assert_eq!(r.len(), REGISTRY_CAP);
        assert_eq!(r.find(0), None);
        assert_eq!(r.find(2), None);
        assert_eq!(r.find(3), Some(("n3", "/t/3")));
        assert_eq!(r.find(REGISTRY_CAP as i32 + 2), Some(("n66", "/t/66")));
    }

    #[test]
    fn prepare_allocates_and_looks_up() {
        let n = Notifier::default();
        let a = n.prepare(note("a", "/t/T-1"), 0).unwrap();
        let b = n.prepare(note("b", "//evil"), 0).unwrap();
        assert_eq!(a.n + 1, b.n);
        assert_eq!(b.path, "/");
        assert_eq!(n.lookup(a.n), Some(("a".into(), "/t/T-1".into())));
        assert_eq!(n.lookup(999), None);
    }

    #[test]
    fn truncation() {
        assert_eq!(truncate("  短  ", 10), "短");
        let long = "字".repeat(130);
        let t = truncate(&long, TITLE_MAX);
        assert_eq!(t.chars().count(), TITLE_MAX);
        assert!(t.ends_with('…'));
        let p = Notifier::default()
            .prepare(
                HostNotification {
                    id: "x".into(),
                    title: long,
                    body: "b".repeat(400),
                    path: "/".into(),
                },
                0,
            )
            .unwrap();
        assert_eq!(p.title.chars().count(), TITLE_MAX);
        assert_eq!(p.body.chars().count(), BODY_MAX);
    }

    #[test]
    fn linux_identity_matches_bundle() {
        // .desktop 文件名取 productName、图标名取 mainBinaryName：改配置时这里一起改
        let conf: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        assert_eq!(conf["productName"], DESKTOP_ENTRY);
        assert_eq!(conf["mainBinaryName"], ICON);
        assert_eq!(APP_NAME, "Oryn");
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_notification_fields() {
        let n = linux_notification("标题", "正文", true, true);
        assert_eq!(n.appname, "Oryn");
        assert_eq!(n.summary, "标题");
        assert_eq!(n.body, "正文");
        assert_eq!(n.icon, "oryn-desktop");
        assert!(n
            .hints
            .contains(&notify_rust::Hint::DesktopEntry("Oryn".into())));
        assert_eq!(n.actions, ["default", ""]);
        let plain = linux_notification("t", "b", false, true);
        assert!(plain.actions.is_empty());
        assert_eq!(plain.appname, "Oryn");
    }

    #[test]
    fn markup_escaping() {
        assert_eq!(
            escape_markup("<a href='x'>y</a> & <img src=\"z\"/>"),
            "&lt;a href='x'&gt;y&lt;/a&gt; &amp; &lt;img src=\"z\"/&gt;"
        );
        assert_eq!(escape_markup("&amp;"), "&amp;amp;");
        assert_eq!(escape_markup("普通文本"), "普通文本");
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_body_is_escaped_when_server_parses_markup() {
        let evil = "<a href='https://evil.example'>点我</a> & co";
        let n = linux_notification("A & <B>", evil, true, true);
        assert_eq!(
            n.body,
            "&lt;a href='https://evil.example'&gt;点我&lt;/a&gt; &amp; co"
        );
        // 标题是纯文本，不转义
        assert_eq!(n.summary, "A & <B>");
        // 服务器不解析标记时原样发送，避免显示出实体
        assert_eq!(linux_notification("t", evil, true, false).body, evil);
    }

    #[test]
    fn rate_limit() {
        let n = Notifier::default();
        for _ in 0..5 {
            assert!(n.prepare(note("x", "/"), 1_000).is_some());
        }
        assert!(n.prepare(note("x", "/"), 1_000).is_none());
        // 5.999 秒还不够补一个
        assert!(n.prepare(note("x", "/"), 6_999).is_none());
        assert!(n.prepare(note("x", "/"), 7_100).is_some());
        assert!(n.prepare(note("x", "/"), 7_100).is_none());
        // 很久之后最多补满 5 个
        for _ in 0..5 {
            assert!(n.prepare(note("x", "/"), 1_000_000).is_some());
        }
        assert!(n.prepare(note("x", "/"), 1_000_000).is_none());
    }
}
