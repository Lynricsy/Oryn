//! 自动更新：GitHub Releases 滚动发布 `desktop-updater` 的 `latest.json`，内嵌公钥校验签名。
//!
//! 策略：发现新版本 → 后台下载（字节留在内存）→ `ready`，托盘出现「重启以更新到 v…」→ 用户确认后安装并重启；
//! `autoUpdate = true` 时下载完立即安装并重启。

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Serialize;
use serde_json::json;
use tauri::AppHandle;
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::eventlog;
use crate::host::{self, Desktop};

/// 周期检查间隔。
pub const INTERVAL: Duration = Duration::from_secs(6 * 3600);

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum UpdateState {
    #[default]
    None,
    Available,
    Downloading,
    Ready,
    Error,
}

impl UpdateState {
    fn as_str(self) -> &'static str {
        match self {
            UpdateState::None => "none",
            UpdateState::Available => "available",
            UpdateState::Downloading => "downloading",
            UpdateState::Ready => "ready",
            UpdateState::Error => "error",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Default)]
pub struct UpdateStatus {
    pub state: UpdateState,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// `host_check_update` 的返回。
#[derive(Debug, Clone, Serialize)]
pub struct Found {
    pub version: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
}

#[derive(Default)]
pub struct Updater {
    status: Mutex<UpdateStatus>,
    ready: Mutex<Option<(Update, Vec<u8>)>>,
    busy: AtomicBool,
    looping: AtomicBool,
}

impl Updater {
    pub fn status(&self) -> UpdateStatus {
        self.status
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .clone()
    }

    /// 已下载、待安装的版本。
    pub fn ready_version(&self) -> Option<String> {
        let s = self.status();
        (s.state == UpdateState::Ready)
            .then_some(s.version)
            .flatten()
    }
}

fn set_status(app: &AppHandle, d: &Desktop, status: UpdateStatus) {
    {
        let mut cur = d.updater.status.lock().unwrap_or_else(|e| e.into_inner());
        if *cur == status {
            return;
        }
        *cur = status.clone();
    }
    eventlog::log(
        "update",
        json!({"state": status.state.as_str(), "version": status.version, "error": status.error}),
    );
    host::emit(
        app,
        json!({"type": "update", "state": status.state.as_str(), "version": status.version}),
    );
    host::refresh_tray(app, d);
}

fn error(app: &AppHandle, d: &Desktop, version: Option<String>, e: impl ToString) {
    set_status(
        app,
        d,
        UpdateStatus {
            state: UpdateState::Error,
            version,
            error: Some(e.to_string()),
        },
    );
}

/// 立即检查一次；发现新版本时在后台下载。检查出错只记录状态，返回 `None`。
pub async fn check(app: &AppHandle, d: &Arc<Desktop>) -> Option<Found> {
    let cur = d.updater.status();
    if matches!(
        cur.state,
        UpdateState::Ready | UpdateState::Downloading | UpdateState::Available
    ) {
        return cur.version.map(|version| Found {
            version,
            notes: None,
        });
    }
    if d.updater.busy.swap(true, Ordering::SeqCst) {
        return None;
    }
    let result = match app.updater() {
        Ok(u) => u.check().await,
        Err(e) => Err(e),
    };
    d.updater.busy.store(false, Ordering::SeqCst);
    match result {
        Ok(None) => {
            set_status(app, d, UpdateStatus::default());
            None
        }
        Ok(Some(update)) => {
            let found = Found {
                version: update.version.clone(),
                notes: update.body.clone().filter(|b| !b.is_empty()),
            };
            set_status(
                app,
                d,
                UpdateStatus {
                    state: UpdateState::Available,
                    version: Some(update.version.clone()),
                    error: None,
                },
            );
            let (app, d) = (app.clone(), d.clone());
            tauri::async_runtime::spawn(async move { download(&app, &d, update).await });
            Some(found)
        }
        Err(e) => {
            error(app, d, None, e);
            None
        }
    }
}

async fn download(app: &AppHandle, d: &Arc<Desktop>, update: Update) {
    let version = update.version.clone();
    set_status(
        app,
        d,
        UpdateStatus {
            state: UpdateState::Downloading,
            version: Some(version.clone()),
            error: None,
        },
    );
    match update.download(|_, _| {}, || {}).await {
        Ok(bytes) => {
            *d.updater.ready.lock().unwrap_or_else(|e| e.into_inner()) = Some((update, bytes));
            set_status(
                app,
                d,
                UpdateStatus {
                    state: UpdateState::Ready,
                    version: Some(version),
                    error: None,
                },
            );
            if d.prefs().auto_update {
                install(app, d);
            }
        }
        Err(e) => error(app, d, Some(version), e),
    }
}

/// 安装已下载的更新并重启；没有已下载的更新时为空操作。
pub fn install(app: &AppHandle, d: &Desktop) {
    let taken = d
        .updater
        .ready
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .take();
    let Some((update, bytes)) = taken else { return };
    let version = update.version.clone();
    match save_then_install(|| host::save_window_state(app), || update.install(&bytes)) {
        Ok(()) => {
            eventlog::log("update", json!({"state": "installed", "version": version}));
            app.restart();
        }
        Err(e) => error(app, d, Some(version), e),
    }
}

/// 先保存窗口状态再安装：Windows 上 `Update::install` 启动安装器后直接 `exit(0)`，不会返回到调用方，
/// 而窗口状态插件只在显式保存或 `RunEvent::Exit` 时落盘。
fn save_then_install<E>(
    save: impl FnOnce(),
    install: impl FnOnce() -> Result<(), E>,
) -> Result<(), E> {
    save();
    install()
}

/// 进入应用后启动周期检查（每进程一次；debug 构建不检查）。
pub fn start_loop(app: &AppHandle, d: &Arc<Desktop>) {
    if cfg!(debug_assertions) || d.smoke || d.updater.looping.swap(true, Ordering::SeqCst) {
        return;
    }
    let (app, d) = (app.clone(), d.clone());
    tauri::async_runtime::spawn(async move {
        loop {
            check(&app, &d).await;
            tokio::time::sleep(INTERVAL).await;
        }
    });
}

#[cfg(test)]
mod tests {
    use super::save_then_install;
    use std::cell::RefCell;

    #[test]
    fn window_state_is_saved_before_installer_takes_over() {
        let order = RefCell::new(Vec::new());
        // 安装器「退出进程」：安装之后的任何步骤都不会执行，所以只看安装前发生了什么
        let r: Result<(), ()> = save_then_install(
            || order.borrow_mut().push("save"),
            || {
                order.borrow_mut().push("install");
                Ok(())
            },
        );
        assert!(r.is_ok());
        assert_eq!(*order.borrow(), ["save", "install"]);
        // 安装失败也已保存过（无害），错误原样返回
        let order = RefCell::new(Vec::new());
        let r = save_then_install(
            || order.borrow_mut().push("save"),
            || Err::<(), _>("bad signature"),
        );
        assert_eq!(r, Err("bad signature"));
        assert_eq!(*order.borrow(), ["save"]);
    }
}
