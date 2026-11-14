//! 无系统标题栏（M6 追加）：各平台窗口控制按钮的位置、窗口状态事件与 `host_window` 命令的执行。
//!
//! - macOS：`titleBarStyle: Overlay` + 隐藏标题，保留原生红绿灯，用 `trafficLightPosition` 放进左侧玻璃栏顶部
//!   （窗口坐标 ([`TRAFFIC_LIGHT_X`], [`TRAFFIC_LIGHT_Y`])，与页面 48px 标题行的中线对齐）。
//! - Windows：`decorations: false`，页面在右上自绘 最小化 / 最大化（还原）/ 关闭。
//! - Linux：同 Windows；按 GTK 的 `gtk-decoration-layout`（GNOME 由 `org.gnome.desktop.wm.preferences button-layout`
//!   同步过来）决定左右与顺序，读不到或一个按钮都没有时放右侧。
//!
//! `decorations: false` 时 Tauri 2.12 仍保留边缘拉伸：Linux 在 WebView 上挂了 5px 的边缘命中（`undecorated_resizing`），
//! Windows 由子窗口处理 `WM_NCHITTEST`，所以页面不需要自己做 `startResizeDragging` 热区。
//!
//! 页面从 `__ORYN_HOST__.chrome` 同步拿到布局（首帧前就能留好位置）；最大化 / 全屏 / 聚焦经 `window` 事件推送，
//! `host_window{action}` 执行动作并交回当前状态。

use std::sync::LazyLock;

use serde::{Deserialize, Serialize};
use serde_json::json;
use tauri::{AppHandle, WebviewWindow};

use crate::eventlog;

/// 页面标题行高度（CSS px）：侧栏 / 右面板顶部为按钮抬高的留白，与各页头同高。
pub const TITLE_ROW: f64 = 48.0;
// 下面三个常量与 `native_left` 只在 macOS 用到（其它平台留着给单测）
/// macOS 红绿灯第一个按钮（关闭）的左缘：侧栏玻璃离窗口左缘 8px，再往里 14px 避开 36px 超椭圆角。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub const TRAFFIC_LIGHT_X: f64 = 22.0;
/// macOS 红绿灯按钮框（14×16）的上缘：按钮中线落在 y = 32，即玻璃栏 / 纸面（离窗口上缘 8px）里 48px 标题行的中线。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub const TRAFFIC_LIGHT_Y: f64 = 24.0;
/// macOS 三个红绿灯占到的右缘（关闭 22 + 两个间距 20 + 按钮宽 14 = 76）再留 8px：页头左侧要让出的宽度。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const TRAFFIC_LIGHT_INSET: f64 = 84.0;

/// 窗口控制按钮由谁画、在哪一侧（页面协议里还有 `none`：没有 `chrome` 时即是，壳不发它）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "kebab-case")]
pub enum Controls {
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    NativeLeft,
    CustomLeft,
    CustomRight,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
#[cfg_attr(target_os = "macos", allow(dead_code))]
pub enum Button {
    Minimize,
    Maximize,
    Close,
}

/// 原生控件占掉的区域（CSS px，从窗口左上 / 右上量）与标题行高度。
#[derive(Debug, Clone, Copy, PartialEq, Serialize)]
pub struct Inset {
    pub top: f64,
    pub left: f64,
    pub right: f64,
}

/// `__ORYN_HOST__.chrome`：`buttons` 是自绘按钮从左到右的顺序（原生时为空）。
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Chrome {
    pub platform: &'static str,
    pub controls: Controls,
    pub buttons: Vec<Button>,
    pub inset: Inset,
}

// 自绘按钮（DEFAULT_BUTTONS / parse_layout / custom）macOS 不用（留给单测）
#[cfg_attr(target_os = "macos", allow(dead_code))]
const DEFAULT_BUTTONS: [Button; 3] = [Button::Minimize, Button::Maximize, Button::Close];

/// 解析 GTK 的 `gtk-decoration-layout`（如 `appmenu:minimize,maximize,close`、`close,minimize,maximize:`）：
/// 冒号左边是左侧、右边是右侧；只认 minimize / maximize / close，其它（menu、icon、appmenu、spacer）忽略。
/// 两侧都有按钮时取带 close 的一侧；一个按钮都没有返回 `None`（由调用方退回默认右侧）。
#[cfg_attr(target_os = "macos", allow(dead_code))]
pub fn parse_layout(layout: &str) -> Option<(Controls, Vec<Button>)> {
    let names = |side: &str| -> Vec<Button> {
        let mut out = Vec::new();
        for name in side.split(',') {
            let b = match name.trim() {
                "minimize" => Button::Minimize,
                "maximize" => Button::Maximize,
                "close" => Button::Close,
                _ => continue,
            };
            if !out.contains(&b) {
                out.push(b);
            }
        }
        out
    };
    let (left, right) = layout.split_once(':').unwrap_or((layout, ""));
    let (left, right) = (names(left), names(right));
    let left_wins = !left.is_empty()
        && (right.is_empty() || (left.contains(&Button::Close) && !right.contains(&Button::Close)));
    if left_wins {
        Some((Controls::CustomLeft, left))
    } else if !right.is_empty() {
        Some((Controls::CustomRight, right))
    } else {
        None
    }
}

/// 自绘按钮的布局：按 GTK 布局（Linux）或默认右侧三个。
#[cfg_attr(target_os = "macos", allow(dead_code))]
fn custom(platform: &'static str, layout: Option<&str>) -> Chrome {
    let (controls, buttons) = layout
        .and_then(parse_layout)
        .unwrap_or((Controls::CustomRight, DEFAULT_BUTTONS.to_vec()));
    Chrome {
        platform,
        controls,
        buttons,
        inset: Inset {
            top: TITLE_ROW,
            left: 0.0,
            right: 0.0,
        },
    }
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn native_left() -> Chrome {
    Chrome {
        platform: "macos",
        controls: Controls::NativeLeft,
        buttons: Vec::new(),
        inset: Inset {
            top: TITLE_ROW,
            left: TRAFFIC_LIGHT_INSET,
            right: 0.0,
        },
    }
}

/// 读 GTK 设置（主线程、GTK 已初始化时）；读不到为 `None`。
#[cfg(target_os = "linux")]
fn gtk_layout() -> Option<String> {
    use gtk::prelude::GtkSettingsExt;
    if !gtk::is_initialized_main_thread() {
        return None;
    }
    gtk::Settings::default()
        .and_then(|s| s.gtk_decoration_layout())
        .map(|s| s.to_string())
}

fn detect() -> Chrome {
    #[cfg(target_os = "macos")]
    return native_left();
    #[cfg(target_os = "windows")]
    return custom("windows", None);
    #[cfg(target_os = "linux")]
    return custom("linux", gtk_layout().as_deref());
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    return custom("linux", None);
}

/// 本进程的标题栏布局：第一次访问（创建主窗口时，主线程，GTK 已初始化）时探测并缓存。
static CHROME: LazyLock<Chrome> = LazyLock::new(|| {
    let c = detect();
    eventlog::log("window.chrome", json!(c));
    c
});

pub fn current() -> &'static Chrome {
    &CHROME
}

/// 窗口状态：页面据此切换最大化图标、全屏时隐藏按钮、失焦时变淡。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
pub struct WindowFlags {
    pub maximized: bool,
    pub fullscreen: bool,
    pub focused: bool,
}

impl WindowFlags {
    pub fn read(w: &WebviewWindow) -> Self {
        Self {
            maximized: w.is_maximized().unwrap_or(false),
            fullscreen: w.is_fullscreen().unwrap_or(false),
            focused: w.is_focused().unwrap_or(false),
        }
    }
}

/// `host_window` 的动作。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum WindowAction {
    /// 只取当前状态。
    State,
    Minimize,
    ToggleMaximize,
    /// 等同于原生关闭：触发 `CloseRequested`，由 `host::wire_window` 按「关闭时隐藏到托盘」处理。
    Close,
    /// 拖动区按下鼠标：交给窗口管理器移动窗口。
    StartDrag,
}

/// 执行动作。
pub fn act(w: &WebviewWindow, action: WindowAction) -> tauri::Result<()> {
    match action {
        WindowAction::State => Ok(()),
        WindowAction::Minimize => w.minimize(),
        WindowAction::ToggleMaximize => {
            if w.is_maximized()? {
                w.unmaximize()
            } else {
                w.maximize()
            }
        }
        WindowAction::Close => w.close(),
        WindowAction::StartDrag => w.start_dragging(),
    }
}

/// 变化时记 `window.state` 并推给页面 `{type: "window", maximized, fullscreen, focused}`。
pub fn publish(app: &AppHandle, last: &std::sync::Mutex<Option<WindowFlags>>, now: WindowFlags) {
    {
        let mut prev = last.lock().unwrap_or_else(|e| e.into_inner());
        if *prev == Some(now) {
            return;
        }
        *prev = Some(now);
    }
    eventlog::log("window.state", json!(now));
    let mut detail = json!(now);
    detail["type"] = json!("window");
    crate::host::emit(app, detail);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn layouts() {
        use Button::*;
        assert_eq!(
            parse_layout("menu:minimize,maximize,close"),
            Some((Controls::CustomRight, vec![Minimize, Maximize, Close]))
        );
        // GNOME 默认只有关闭
        assert_eq!(
            parse_layout("appmenu:close"),
            Some((Controls::CustomRight, vec![Close]))
        );
        // elementary / macOS 风格：放左边，按给定顺序
        assert_eq!(
            parse_layout("close,minimize,maximize:"),
            Some((Controls::CustomLeft, vec![Close, Minimize, Maximize]))
        );
        assert_eq!(
            parse_layout(" close , maximize : menu"),
            Some((Controls::CustomLeft, vec![Close, Maximize]))
        );
        // 两侧都有：取带 close 的一侧
        assert_eq!(
            parse_layout("close:minimize"),
            Some((Controls::CustomLeft, vec![Close]))
        );
        assert_eq!(
            parse_layout("minimize:close"),
            Some((Controls::CustomRight, vec![Close]))
        );
        // 没有冒号 = 全在左侧；重复的去掉
        assert_eq!(
            parse_layout("close,close"),
            Some((Controls::CustomLeft, vec![Close]))
        );
        assert_eq!(parse_layout("menu:"), None);
        assert_eq!(parse_layout(""), None);
    }

    #[test]
    fn chrome_shapes() {
        let c = custom("linux", Some("icon:"));
        assert_eq!(c.controls, Controls::CustomRight);
        assert_eq!(c.buttons, DEFAULT_BUTTONS.to_vec());
        assert_eq!(
            serde_json::to_value(custom("windows", None)).unwrap(),
            json!({
                "platform": "windows",
                "controls": "custom-right",
                "buttons": ["minimize", "maximize", "close"],
                "inset": {"top": 48.0, "left": 0.0, "right": 0.0}
            })
        );
        let mac = serde_json::to_value(native_left()).unwrap();
        assert_eq!(mac["controls"], "native-left");
        assert_eq!(mac["buttons"], json!([]));
        assert_eq!(mac["inset"]["left"], 84.0);
        assert_eq!(
            serde_json::to_value(custom("linux", Some("close:"))).unwrap()["controls"],
            "custom-left"
        );
    }

    #[test]
    fn actions_deserialize_camel_case() {
        let a: WindowAction = serde_json::from_value(json!("toggleMaximize")).unwrap();
        assert_eq!(a, WindowAction::ToggleMaximize);
        let a: WindowAction = serde_json::from_value(json!("startDrag")).unwrap();
        assert_eq!(a, WindowAction::StartDrag);
        assert!(serde_json::from_value::<WindowAction>(json!("destroy")).is_err());
        assert_eq!(
            serde_json::to_value(WindowFlags {
                maximized: true,
                fullscreen: false,
                focused: true
            })
            .unwrap(),
            json!({"maximized": true, "fullscreen": false, "focused": true})
        );
    }
}
