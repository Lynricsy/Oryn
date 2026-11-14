//! 全局快捷键：显示 / 隐藏主窗口。

use std::str::FromStr;
use std::sync::Mutex;

use serde_json::json;
use tauri::AppHandle;
use tauri_plugin_global_shortcut::{
    Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutEvent, ShortcutState,
};

use crate::{eventlog, host};

/// 修饰键不足的错误。
pub const NEEDS_MODIFIER: &str = "shortcut must include Control, Alt or Super";
/// 与系统 / 编辑常用组合冲突的错误。
pub const RESERVED: &str = "shortcut is reserved by the system";

/// 不允许注册的组合（修饰键须完全一致）：全局注册会让所有程序都收不到它们。
const RESERVED_COMBOS: &[(Modifiers, Code)] = &[
    // 编辑与窗口常用（Windows / Linux 用 Control，macOS 用 Command = Super）
    (Modifiers::CONTROL, Code::KeyA),
    (Modifiers::CONTROL, Code::KeyC),
    (Modifiers::CONTROL, Code::KeyV),
    (Modifiers::CONTROL, Code::KeyX),
    (Modifiers::CONTROL, Code::KeyZ),
    (Modifiers::CONTROL, Code::KeyY),
    (Modifiers::CONTROL, Code::KeyS),
    (Modifiers::CONTROL, Code::KeyW),
    (Modifiers::CONTROL, Code::KeyQ),
    (Modifiers::CONTROL.union(Modifiers::SHIFT), Code::KeyZ),
    (Modifiers::SUPER, Code::KeyA),
    (Modifiers::SUPER, Code::KeyC),
    (Modifiers::SUPER, Code::KeyV),
    (Modifiers::SUPER, Code::KeyX),
    (Modifiers::SUPER, Code::KeyZ),
    (Modifiers::SUPER, Code::KeyS),
    (Modifiers::SUPER, Code::KeyW),
    (Modifiers::SUPER, Code::KeyQ),
    (Modifiers::SUPER.union(Modifiers::SHIFT), Code::KeyZ),
    // 系统级：关窗、切换窗口 / 输入法、锁屏、安全选项、Spotlight
    (Modifiers::ALT, Code::F4),
    (Modifiers::ALT, Code::Tab),
    (Modifiers::ALT.union(Modifiers::SHIFT), Code::Tab),
    (Modifiers::SUPER, Code::Tab),
    (Modifiers::SUPER, Code::Space),
    (Modifiers::SUPER, Code::KeyL),
    (Modifiers::CONTROL, Code::Escape),
    (Modifiers::CONTROL.union(Modifiers::ALT), Code::Delete),
];

/// 解析并校验快捷键串（如 `Control+Alt+Space`）：至少含 Control / Alt / Super 之一（只有 Shift 不算），
/// 且不在 [`RESERVED_COMBOS`] 里。远程页面经 `host_set_settings` 也能设置它，配置文件也可能被改过，
/// 所以注册前（设置时与启动加载时）都走这里。
pub fn parse(accelerator: &str) -> Result<Shortcut, String> {
    let s = Shortcut::from_str(accelerator.trim()).map_err(|e| e.to_string())?;
    if !s
        .mods
        .intersects(Modifiers::CONTROL | Modifiers::ALT | Modifiers::SUPER)
    {
        return Err(NEEDS_MODIFIER.into());
    }
    if RESERVED_COMBOS
        .iter()
        .any(|(mods, key)| s.mods == *mods && s.key == *key)
    {
        return Err(RESERVED.into());
    }
    Ok(s)
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
    fn rejects_unmodified_and_reserved() {
        for bare in ["Enter", "KeyA", "Space", "F1", "Shift+A", "Shift+Enter"] {
            assert_eq!(parse(bare).unwrap_err(), NEEDS_MODIFIER, "{bare}");
        }
        for reserved in [
            "Control+C",
            "ctrl+v",
            "Control+X",
            "Control+Z",
            "Control+A",
            "Control+Shift+Z",
            "Super+C",
            "Cmd+V",
            "Alt+F4",
            "Alt+Tab",
            "Alt+Shift+Tab",
            "Control+Alt+Delete",
            "Super+L",
        ] {
            assert_eq!(parse(reserved).unwrap_err(), RESERVED, "{reserved}");
        }
        // 修饰键不同就不是保留组合
        for ok in [
            "Control+Alt+C",
            "Control+Shift+V",
            "Alt+F5",
            "Super+Shift+K",
        ] {
            assert!(parse(ok).is_ok(), "{ok}");
        }
        assert!(parse(crate::config::DEFAULT_SHORTCUT).is_ok());
    }

    #[test]
    fn toggle_action() {
        assert_eq!(action(true, true), Action::Hide);
        assert_eq!(action(true, false), Action::Show);
        assert_eq!(action(false, false), Action::Show);
        assert_eq!(action(false, true), Action::Show);
    }
}
