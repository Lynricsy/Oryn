//! 全局快捷键：显示 / 隐藏主窗口。

use std::str::FromStr;
use std::sync::Mutex;

use serde_json::json;
use tauri::AppHandle;
use tauri_plugin_global_shortcut::{GlobalShortcutExt, Shortcut, ShortcutEvent, ShortcutState};

use crate::{eventlog, host};

/// 解析快捷键串（如 `Control+Alt+Space`）。
pub fn parse(accelerator: &str) -> Result<Shortcut, String> {
    Shortcut::from_str(accelerator.trim()).map_err(|e| e.to_string())
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Action {
    Hide,
    Show,
}

/// 主窗口可见且聚焦 → 隐藏；否则显示并聚焦。
pub fn action(visible: bool, focused: bool) -> Action {
    if visible && focused {
        Action::Hide
    } else {
        Action::Show
    }
}

/// 当前注册状态。
#[derive(Default)]
pub struct Registration {
    inner: Mutex<Inner>,
}

#[derive(Default)]
struct Inner {
    current: Option<(String, Shortcut)>,
    error: Option<String>,
}

impl Registration {
    fn lock(&self) -> std::sync::MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|e| e.into_inner())
    }

    pub fn error(&self) -> Option<String> {
        self.lock().error.clone()
    }

    /// 换成新的快捷键（空串 = 不注册）。解析失败或被占用 → 保留旧的注册，返回错误。
    pub fn apply(&self, app: &AppHandle, accelerator: &str) -> Result<(), String> {
        let accelerator = accelerator.trim();
        let mut g = self.lock();
        let result = swap(app, &mut g.current, accelerator);
        g.error = result.as_ref().err().cloned();
        eventlog::log(
            "shortcut",
            json!({"accelerator": accelerator, "ok": result.is_ok(), "error": g.error}),
        );
        result
    }
}

fn swap(
    app: &AppHandle,
    current: &mut Option<(String, Shortcut)>,
    accelerator: &str,
) -> Result<(), String> {
    let gs = app.global_shortcut();
    if accelerator.is_empty() {
        if let Some((_, old)) = current.take() {
            let _ = gs.unregister(old);
        }
        return Ok(());
    }
    let new = parse(accelerator)?;
    if let Some((_, old)) = current.as_ref() {
        if *old == new {
            return Ok(());
        }
        let _ = gs.unregister(*old);
    }
    match gs.register(new) {
        Ok(()) => {
            *current = Some((accelerator.to_string(), new));
            Ok(())
        }
        Err(e) => {
            // 新的注册失败：恢复旧的
            if let Some((_, old)) = current.as_ref() {
                let _ = gs.register(*old);
            }
            Err(e.to_string())
        }
    }
}

/// 插件级回调：只在按下时切换窗口。
pub fn on_event(app: &AppHandle, _shortcut: &Shortcut, event: ShortcutEvent) {
    if event.state() != ShortcutState::Pressed {
        return;
    }
    eventlog::log("shortcut.fired", json!({}));
    let (visible, focused) = host::main_window_flags(app);
    match action(visible, focused) {
        Action::Hide => host::hide_main(app),
        Action::Show => host::show_main(app),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_accelerators() {
        for ok in [
            "Control+Alt+Space",
            "Control+Alt+K",
            "CmdOrCtrl+Shift+O",
            " Alt+F1 ",
        ] {
            assert!(parse(ok).is_ok(), "{ok}");
        }
        for bad in ["", "Control+", "Control+Alt+NoSuchKey", "Hyper++"] {
            assert!(parse(bad).is_err(), "{bad}");
        }
        assert_eq!(parse("Control+Alt+Space"), parse("ctrl+alt+space"));
    }

    #[test]
    fn toggle_action() {
        assert_eq!(action(true, true), Action::Hide);
        assert_eq!(action(true, false), Action::Show);
        assert_eq!(action(false, false), Action::Show);
        assert_eq!(action(false, true), Action::Show);
    }
}
