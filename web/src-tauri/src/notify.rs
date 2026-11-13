//! 系统通知：限流、通知号分配与点击回查（官方 `tauri-plugin-notification`，只在 Rust 侧调用）。

use std::collections::VecDeque;
use std::sync::Mutex;

use serde::Deserialize;
use serde_json::json;
use tauri::{AppHandle, Runtime};
use tauri_plugin_notification::NotificationExt;

use crate::eventlog;
use crate::session::valid_app_path;

/// 标题与正文的最大字符数。
pub const TITLE_MAX: usize = 120;
pub const BODY_MAX: usize = 300;
/// 记住的通知条数。
pub const REGISTRY_CAP: usize = 64;
/// 令牌桶：突发 5，每 6 秒补 1。
pub const BURST: f64 = 5.0;
pub const REFILL_MS: f64 = 6_000.0;

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
pub fn show<R: Runtime>(app: &AppHandle<R>, notifier: &Notifier, n: HostNotification) {
    let id = n.id.clone();
    let Some(p) = notifier.prepare(n, eventlog::now_ms()) else {
        eventlog::log(
            "notify.dropped",
            json!({"id": id, "reason": "rate_limited"}),
        );
        return;
    };
    let result = app
        .notification()
        .builder()
        .id(p.n)
        .title(&p.title)
        .body(&p.body)
        .show();
    match result {
        Ok(()) => eventlog::log(
            "notify.shown",
            json!({"id": id, "n": p.n, "title": p.title, "path": p.path}),
        ),
        Err(e) => eventlog::log("notify.error", json!({"id": id, "error": e.to_string()})),
    }
}

/// 显示一条不可点击跳转的提示（如下载完成）。
pub fn show_plain<R: Runtime>(app: &AppHandle<R>, notifier: &Notifier, title: &str, body: &str) {
    let _ = app
        .notification()
        .builder()
        .id(notifier.next_id())
        .title(truncate(title, TITLE_MAX))
        .body(truncate(body, BODY_MAX))
        .show();
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
