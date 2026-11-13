//! 托盘常驻：图标、状态行、待处理线程与固定菜单项。菜单只建一次，之后只改内容（Linux 上菜单不能替换）。

use std::sync::Mutex;

use serde_json::json;
use tauri::image::Image;
use tauri::menu::{Menu, MenuEvent, MenuItem, MenuItemKind, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, Wry};

use crate::host::{self, HostStatus, StatusKind};
use crate::i18n::{self, Key, Lang};
use crate::{eventlog, notify};

/// 线程项标题的最大字符数。
pub const TITLE_MAX: usize = 40;
/// 线程项最多条数。
pub const ITEMS_MAX: usize = 5;
const THREAD_PREFIX: &str = "thread:";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum IconKind {
    Normal,
    Attention,
}

impl IconKind {
    pub fn as_str(self) -> &'static str {
        match self {
            IconKind::Normal => "normal",
            IconKind::Attention => "attention",
        }
    }
}

/// 托盘的纯数据视图。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TrayModel {
    pub icon: IconKind,
    /// 状态行（禁用项）与 tooltip；都为 0 时 `None`。
    pub status_line: Option<String>,
    /// `(threadId, 菜单文字)`。
    pub items: Vec<(String, String)>,
    pub open: String,
    pub new_thread: String,
    /// 「检查更新…」或「重启以更新到 v…」。
    pub update: String,
    pub change_server: String,
    pub quit: String,
    /// macOS 菜单栏标题（待处理数，0 时 `None`）。
    pub title: Option<String>,
    /// 角标（待处理 + 未读，0 时 `None`）。
    pub badge: Option<i64>,
}

/// `update` = 已下载、待重启安装的版本。
pub fn model(status: &HostStatus, lang: Lang, update: Option<&str>) -> TrayModel {
    let total = status.awaiting.saturating_add(status.unread);
    let items = status
        .items
        .iter()
        .filter(|i| !i.thread_id.is_empty())
        .take(ITEMS_MAX)
        .map(|i| {
            let title = i.title.trim();
            let title = if title.is_empty() {
                i18n::t(lang, Key::UntitledThread)
            } else {
                title
            };
            let text = notify::truncate(title, TITLE_MAX);
            let text = match i.kind {
                StatusKind::Awaiting => format!("● {text}"),
                StatusKind::Unread => text,
            };
            (i.thread_id.clone(), text)
        })
        .collect();
    TrayModel {
        icon: if total > 0 {
            IconKind::Attention
        } else {
            IconKind::Normal
        },
        status_line: i18n::status_line(lang, status.awaiting, status.unread),
        items,
        open: i18n::t(lang, Key::OpenOryn).into(),
        new_thread: i18n::t(lang, Key::NewThread).into(),
        update: match update {
            Some(v) => i18n::restart_to_update(lang, v),
            None => i18n::t(lang, Key::CheckUpdates).into(),
        },
        change_server: i18n::t(lang, Key::ChangeServer).into(),
        quit: i18n::t(lang, Key::Quit).into(),
        title: (status.awaiting > 0).then(|| status.awaiting.to_string()),
        badge: (total > 0).then_some(i64::from(total)),
    }
}

/// 已建好的托盘。
pub struct Tray {
    icon: TrayIcon,
    menu: Menu<Wry>,
    dynamic: Mutex<Vec<MenuItemKind<Wry>>>,
    open: MenuItem<Wry>,
    new_thread: MenuItem<Wry>,
    update: MenuItem<Wry>,
    change_server: MenuItem<Wry>,
    quit: MenuItem<Wry>,
    last: Mutex<Option<TrayModel>>,
}

fn icon_image(kind: IconKind) -> Image<'static> {
    #[cfg(target_os = "macos")]
    {
        match kind {
            IconKind::Normal => tauri::include_image!("icons/tray-template.png"),
            IconKind::Attention => tauri::include_image!("icons/tray-attention-template.png"),
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        match kind {
            IconKind::Normal => tauri::include_image!("icons/tray.png"),
            IconKind::Attention => tauri::include_image!("icons/tray-attention.png"),
        }
    }
}

/// 建托盘；失败（含 Linux 缺 `libayatana-appindicator` 时的 panic）返回 `None`。
pub fn build(app: &AppHandle, lang: Lang) -> Option<Tray> {
    let app = app.clone();
    let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| try_build(&app, lang)));
    match result {
        Ok(Ok(tray)) => {
            eventlog::log("tray", json!({"icon": "normal"}));
            Some(tray)
        }
        Ok(Err(e)) => {
            eventlog::log(
                "tray",
                json!({"icon": "unavailable", "error": e.to_string()}),
            );
            None
        }
        Err(_) => {
            eventlog::log("tray", json!({"icon": "unavailable", "error": "panic"}));
            None
        }
    }
}

fn try_build(app: &AppHandle, lang: Lang) -> tauri::Result<Tray> {
    let m = model(&HostStatus::default(), lang, None);
    let open = MenuItem::with_id(app, "open", &m.open, true, None::<&str>)?;
    let new_thread = MenuItem::with_id(app, "new", &m.new_thread, true, None::<&str>)?;
    let update = MenuItem::with_id(app, "update", &m.update, true, None::<&str>)?;
    let change_server = MenuItem::with_id(app, "change", &m.change_server, true, None::<&str>)?;
    let quit = MenuItem::with_id(app, "quit", &m.quit, true, None::<&str>)?;
    let menu = Menu::with_items(
        app,
        &[
            &open,
            &new_thread,
            &PredefinedMenuItem::separator(app)?,
            &update,
            &change_server,
            &PredefinedMenuItem::separator(app)?,
            &quit,
        ],
    )?;
    let mut builder = TrayIconBuilder::with_id("main")
        .icon(icon_image(IconKind::Normal))
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(on_menu)
        .on_tray_icon_event(on_icon);
    #[cfg(target_os = "macos")]
    {
        builder = builder.icon_as_template(true);
    }
    builder = builder.tooltip("Oryn");
    let icon = builder.build(app)?;
    Ok(Tray {
        icon,
        menu,
        dynamic: Mutex::new(Vec::new()),
        open,
        new_thread,
        update,
        change_server,
        quit,
        last: Mutex::new(Some(m)),
    })
}

impl Tray {
    /// 把模型应用到托盘；只改有变化的部分。
    pub fn apply(&self, app: &AppHandle, m: TrayModel) {
        let mut last = self.last.lock().unwrap_or_else(|e| e.into_inner());
        let prev = last.take();
        let icon_changed = prev.as_ref().map(|p| p.icon) != Some(m.icon);
        if icon_changed {
            let _ = self.icon.set_icon(Some(icon_image(m.icon)));
            #[cfg(target_os = "macos")]
            let _ = self.icon.set_icon_as_template(true);
        }
        if prev.as_ref().map(|p| (&p.status_line, &p.items)) != Some((&m.status_line, &m.items)) {
            self.rebuild_dynamic(app, &m);
        }
        let _ = self.open.set_text(&m.open);
        let _ = self.new_thread.set_text(&m.new_thread);
        let _ = self.update.set_text(&m.update);
        let _ = self.change_server.set_text(&m.change_server);
        let _ = self.quit.set_text(&m.quit);
        #[cfg(not(target_os = "linux"))]
        let _ = self
            .icon
            .set_tooltip(Some(m.status_line.clone().unwrap_or_else(|| "Oryn".into())));
        #[cfg(target_os = "macos")]
        let _ = self.icon.set_title(m.title.as_deref());
        if let Some(w) = app.get_webview_window(host::MAIN) {
            let _ = w.set_badge_count(m.badge);
        }
        if icon_changed && prev.is_some() {
            eventlog::log("tray", json!({"icon": m.icon.as_str()}));
        }
        *last = Some(m);
    }

    fn rebuild_dynamic(&self, app: &AppHandle, m: &TrayModel) {
        let mut dynamic = self.dynamic.lock().unwrap_or_else(|e| e.into_inner());
        for item in dynamic.drain(..) {
            let _ = self.menu.remove(&item);
        }
        let mut fresh: Vec<MenuItemKind<Wry>> = Vec::new();
        if let Some(line) = &m.status_line {
            if let Ok(item) = MenuItem::with_id(app, "status", line, false, None::<&str>) {
                fresh.push(MenuItemKind::MenuItem(item));
            }
        }
        for (id, text) in &m.items {
            if let Ok(item) = MenuItem::with_id(
                app,
                format!("{THREAD_PREFIX}{id}"),
                text,
                true,
                None::<&str>,
            ) {
                fresh.push(MenuItemKind::MenuItem(item));
            }
        }
        if !fresh.is_empty() {
            if let Ok(sep) = PredefinedMenuItem::separator(app) {
                fresh.push(MenuItemKind::Predefined(sep));
            }
        }
        for (pos, item) in fresh.iter().enumerate() {
            let _ = self.menu.insert(item, pos);
        }
        *dynamic = fresh;
    }
}

fn on_menu(app: &AppHandle, event: MenuEvent) {
    let id = event.id().as_ref();
    match id {
        "open" => host::show_main(app),
        "new" => host::open_path(app, "/"),
        "update" => host::tray_update(app),
        "change" => host::change_server(app),
        "quit" => host::quit(app),
        other => {
            if let Some(thread) = other.strip_prefix(THREAD_PREFIX) {
                host::open_path(app, &format!("/t/{thread}"));
            }
        }
    }
}

fn on_icon(tray: &TrayIcon, event: TrayIconEvent) {
    // Windows / macOS：左键单击显示并聚焦主窗口（Linux 托盘没有点击事件）
    if let TrayIconEvent::Click {
        button: MouseButton::Left,
        button_state: MouseButtonState::Up,
        ..
    } = event
    {
        host::show_main(tray.app_handle());
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::host::StatusItem;

    fn item(id: &str, title: &str, kind: StatusKind) -> StatusItem {
        StatusItem {
            thread_id: id.into(),
            title: title.into(),
            kind,
        }
    }

    #[test]
    fn icon_and_status_line() {
        let empty = model(&HostStatus::default(), Lang::ZhCn, None);
        assert_eq!(empty.icon, IconKind::Normal);
        assert_eq!(empty.status_line, None);
        assert_eq!(empty.badge, None);
        assert_eq!(empty.title, None);
        assert_eq!(empty.update, "检查更新…");

        let s = HostStatus {
            awaiting: 1,
            unread: 2,
            items: vec![],
        };
        let zh = model(&s, Lang::ZhCn, None);
        assert_eq!(zh.icon, IconKind::Attention);
        assert_eq!(zh.status_line.as_deref(), Some("1 个待你处理 · 2 个未读"));
        assert_eq!(zh.badge, Some(3));
        assert_eq!(zh.title.as_deref(), Some("1"));
        let en = model(&s, Lang::En, None);
        assert_eq!(en.status_line.as_deref(), Some("1 awaiting you · 2 unread"));
        assert_eq!(en.quit, "Quit Oryn");

        let unread_only = model(
            &HostStatus {
                awaiting: 0,
                unread: 4,
                items: vec![],
            },
            Lang::En,
            None,
        );
        assert_eq!(unread_only.icon, IconKind::Attention);
        assert_eq!(unread_only.title, None);
    }

    #[test]
    fn items_limit_and_truncation() {
        let long = "x".repeat(60);
        let s = HostStatus {
            awaiting: 2,
            unread: 5,
            items: vec![
                item("T-1", &long, StatusKind::Awaiting),
                item("T-2", "  ", StatusKind::Unread),
                item("", "skipped", StatusKind::Unread),
                item("T-3", "c", StatusKind::Unread),
                item("T-4", "d", StatusKind::Unread),
                item("T-5", "e", StatusKind::Unread),
                item("T-6", "f", StatusKind::Unread),
            ],
        };
        let m = model(&s, Lang::ZhCn, None);
        assert_eq!(m.items.len(), ITEMS_MAX);
        assert_eq!(m.items[0].0, "T-1");
        assert!(m.items[0].1.starts_with("● "));
        assert_eq!(m.items[0].1.chars().count(), TITLE_MAX + 2);
        assert_eq!(m.items[1], ("T-2".into(), "未命名线程".into()));
        assert_eq!(m.items[4].0, "T-5");
    }

    #[test]
    fn update_item_text() {
        let s = HostStatus::default();
        assert_eq!(
            model(&s, Lang::ZhCn, Some("0.2.0")).update,
            "重启以更新到 v0.2.0"
        );
        assert_eq!(
            model(&s, Lang::En, Some("0.2.0")).update,
            "Restart to update to v0.2.0"
        );
        assert_eq!(model(&s, Lang::En, None).update, "Check for updates…");
    }
}
