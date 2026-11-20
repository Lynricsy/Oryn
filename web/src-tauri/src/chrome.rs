//! 无系统标题栏（M6 追加）：各平台窗口控制按钮的位置、窗口状态事件与 `host_window` 命令的执行。
//!
//! - macOS：`titleBarStyle: Overlay` + 隐藏标题，保留原生红绿灯，用 `trafficLightPosition` 放进左侧玻璃栏顶部，
//!   竖直中线对齐页面 48px 标题行的中线（离窗口上缘 [`LIGHTS_MID`]）。
//!   tao 0.37 的 `inset_traffic_lights` 只做两件事：把标题栏容器（关闭按钮的 superview.superview）高度设为
//!   「按钮高 + y」、贴住窗口上缘，把三个按钮横向排到 x 起；按钮在容器里的竖直位置不动，由系统主题框架决定，
//!   随系统版本变（macOS 27 实测 y = 24 时中线离上缘约 21.5，比目标高约 10），所以 y 不能写死。
//!   Tauri 2.12 只有建窗时的 `traffic_light_position`，于是建主窗口前先用一个不显示的同款临时窗口照 tao 的算法试探、
//!   量出按钮实际位置来修正 y（[`calibrate_y`]），顺带量出缩放按钮右缘得到页头左侧留白（[`lights_inset`]）；
//!   任何一步失败都退回 [`TRAFFIC_LIGHT_Y`] / [`TRAFFIC_LIGHT_INSET`]，并记 `window.lights`。
//! - macOS ≥ 26 再给主窗口挂一个空的 unified `NSToolbar`：只有标题栏的窗口外框圆角约 16，挂了 unified 工具栏的约 26
//!   （WWDC25「Build an AppKit app with the new design」），页面卡片按 `chrome.radius` 做同心圆角；
//!   工具栏下的发丝分隔线去掉。临时窗口同样挂上，量到的就是带工具栏时的按钮位置。
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
// 下面的常量、`Lights` 与 `native_left` 只在 macOS 用到（其它平台留着给单测）
/// macOS 红绿灯第一个按钮（关闭）的左缘：侧栏玻璃离窗口左缘 8px，再往里 14px 避开 36px 超椭圆角。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub const TRAFFIC_LIGHT_X: f64 = 22.0;
/// 红绿灯竖直中线离窗口上缘的目标距离：玻璃栏 / 纸面离窗口上缘 8px，里面 48px 标题行的中线（页面侧 `LIGHTS_MID`）。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub const LIGHTS_MID: f64 = 32.0;
/// 校准失败时的 `trafficLightPosition.y`：按「按钮框（14×16）上缘 = y、中线 = y + 8」推出来的旧值，新系统上偏高（见模块文档）。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const TRAFFIC_LIGHT_Y: f64 = 24.0;
/// 校准失败时页头左侧要让出的宽度：三个红绿灯的右缘（关闭 22 + 两个间距 20 + 按钮宽 14 = 76）再留 [`LIGHTS_GAP`]。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const TRAFFIC_LIGHT_INSET: f64 = 84.0;
/// 红绿灯右缘到页头内容的留白。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const LIGHTS_GAP: f64 = 8.0;
/// 校准收敛的误差（pt）：再小肉眼分不出，主题框架的取整也到不了。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const LIGHTS_TOLERANCE: f64 = 0.5;
/// 校准最多量几次（首次试探 + 修正复测）：按钮随 y 线性移动时第二次就收敛，多给两次兜住非线性。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const LIGHTS_ROUNDS: usize = 4;
/// 试探 y 的范围：负数会让容器矮过按钮；再大红绿灯就掉出 48px 标题行。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const LIGHTS_Y_RANGE: (f64, f64) = (0.0, 64.0);
/// 挂 unified 工具栏的最低 macOS 主版本：26（Tahoe）起才有按工具栏放大的新外框圆角。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const TOOLBAR_MIN_MAJOR: isize = 26;
/// 挂了 unified 工具栏的窗口外框圆角（WWDC25 给的值；`.unifiedCompact` 约 20，只有标题栏约 16）。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const TOOLBAR_RADIUS: f64 = 26.0;

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

/// `__ORYN_HOST__.chrome`：`buttons` 是自绘按钮从左到右的顺序（原生时为空）；`radius` 是窗口外框圆角（CSS px），
/// 页面据此做同心圆角，0 = 未知 / 不做（页面保持设计令牌；目前只有挂了 unified 工具栏的 macOS 有值）。
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Chrome {
    pub platform: &'static str,
    pub controls: Controls,
    pub buttons: Vec<Button>,
    pub inset: Inset,
    pub radius: f64,
}

/// macOS 红绿灯与外框：校准结果，失败时为 [`Lights::FALLBACK`]。
#[derive(Debug, Clone, Copy, PartialEq)]
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
pub struct Lights {
    /// 建主窗口时的 `trafficLightPosition.y`。
    pub y: f64,
    /// 页头左侧要让出的宽度（`inset.left`）。
    pub inset_left: f64,
    /// 是否给主窗口挂 unified 空工具栏；挂了外框圆角才是 [`TOOLBAR_RADIUS`]。
    pub toolbar: bool,
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
impl Lights {
    /// 量不出来时用旧常量、不挂工具栏：常量是按只有标题栏的布局推的，带工具栏时按钮落在哪没量过。
    const FALLBACK: Self = Self {
        y: TRAFFIC_LIGHT_Y,
        inset_left: TRAFFIC_LIGHT_INSET,
        toolbar: false,
    };
}

/// macOS 主版本 → 是否挂 unified 空工具栏。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn wants_toolbar(major: isize) -> bool {
    major >= TOOLBAR_MIN_MAJOR
}

/// 外框圆角：挂了 unified 工具栏为 [`TOOLBAR_RADIUS`]；否则报 0（只有标题栏时各版本的值没量过，页面不做同心）。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn frame_radius(toolbar: bool) -> f64 {
    if toolbar {
        TOOLBAR_RADIUS
    } else {
        0.0
    }
}

/// 缩放按钮右缘（窗口坐标）→ `inset.left`：先取整到整 px，页头不压到按钮的抗锯齿边，再留 [`LIGHTS_GAP`]。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn lights_inset(zoom_right: f64) -> f64 {
    zoom_right.ceil() + LIGHTS_GAP
}

/// 校准结果：选定的试探 `y`、此时关闭按钮竖直中线离窗口上缘的距离 `mid`、一共量了几次。
#[derive(Debug, Clone, Copy, PartialEq)]
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
struct LightsFit {
    y: f64,
    mid: f64,
    rounds: usize,
}

/// 下一次试探的 y：量过两次就按最近两次的割线（按钮随 y 怎么移由主题框架决定，不一定一比一），
/// 只量过一次或斜率退化（两次中线几乎一样）时按一比一补差；结果夹在 [`LIGHTS_Y_RANGE`] 里。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn next_light_y(prev: Option<(f64, f64)>, (y, mid): (f64, f64)) -> f64 {
    let slope = prev
        .map(|(py, pm)| (mid - pm) / (y - py))
        .filter(|s| s.is_finite() && s.abs() >= 0.1)
        .unwrap_or(1.0);
    (y + (LIGHTS_MID - mid) / slope).clamp(LIGHTS_Y_RANGE.0, LIGHTS_Y_RANGE.1)
}

/// 红绿灯校准：从 `start` 起，`measure(y)` 把 y 套到临时窗口并量出中线离窗口上缘的距离，
/// 按与 [`LIGHTS_MID`] 的差修正后复测，收敛到 [`LIGHTS_TOLERANCE`] 内或量满 [`LIGHTS_ROUNDS`] 次为止，取误差最小的一次。
/// 任何一次量不出（`None` / 非有限值）返回 `None`，由调用方退回常量。
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn calibrate_y(start: f64, mut measure: impl FnMut(f64) -> Option<f64>) -> Option<LightsFit> {
    let err = |mid: f64| (mid - LIGHTS_MID).abs();
    let mut best: Option<LightsFit> = None;
    let mut prev = None;
    let mut y = start;
    let mut rounds = 0;
    while rounds < LIGHTS_ROUNDS {
        let mid = measure(y).filter(|m| m.is_finite())?;
        rounds += 1;
        if best.is_none_or(|b| err(mid) < err(b.mid)) {
            best = Some(LightsFit { y, mid, rounds: 0 });
        }
        if err(mid) <= LIGHTS_TOLERANCE {
            break;
        }
        let next = next_light_y(prev, (y, mid));
        // 已经顶到范围边上，再量也是同一个结果
        if next == y {
            break;
        }
        prev = Some((y, mid));
        y = next;
    }
    best.map(|b| LightsFit { rounds, ..b })
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
        radius: 0.0,
    }
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn native_left(lights: &Lights) -> Chrome {
    Chrome {
        platform: "macos",
        controls: Controls::NativeLeft,
        buttons: Vec::new(),
        inset: Inset {
            top: TITLE_ROW,
            left: lights.inset_left,
            right: 0.0,
        },
        radius: frame_radius(lights.toolbar),
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
    return native_left(lights());
    #[cfg(target_os = "windows")]
    return custom("windows", None);
    #[cfg(target_os = "linux")]
    return custom("linux", gtk_layout().as_deref());
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    return custom("linux", None);
}

/// 本进程的标题栏布局：第一次访问（创建主窗口生成初始化脚本时，即 `setup` 里，主线程，GTK 已初始化）时探测并缓存。
static CHROME: LazyLock<Chrome> = LazyLock::new(|| {
    let c = detect();
    eventlog::log("window.chrome", json!(c));
    c
});

pub fn current() -> &'static Chrome {
    &CHROME
}

/// macOS 红绿灯校准结果：随 [`current`] 第一次访问时在主线程建临时窗口量出并缓存（AppKit 只能在主线程碰；
/// 不在主线程时退回常量），所以 `create_main` 要先取 `current()` 再用 `lights().y` 建窗。
#[cfg(target_os = "macos")]
static LIGHTS: LazyLock<Lights> = LazyLock::new(mac::calibrate);

#[cfg(target_os = "macos")]
pub fn lights() -> &'static Lights {
    &LIGHTS
}

/// 给刚建好的主窗口挂 unified 空工具栏（校准时定了挂才挂，与 `chrome.radius` 一致）；挂不上记 `window.toolbar`。
#[cfg(target_os = "macos")]
pub fn attach_toolbar(w: &WebviewWindow) {
    if !lights().toolbar {
        return;
    }
    if let Err(e) = mac::attach_main(w) {
        eventlog::log("window.toolbar", json!({"error": e}));
    }
}

/// AppKit 部分：量红绿灯、挂工具栏。纯计算都在外面（[`calibrate_y`] 等），这里只做调用与测量。
#[cfg(target_os = "macos")]
mod mac {
    use objc2::rc::Retained;
    use objc2::MainThreadMarker;
    use objc2_app_kit::{
        NSBackingStoreType, NSButton, NSTitlebarSeparatorStyle, NSToolbar, NSView, NSWindow,
        NSWindowButton, NSWindowStyleMask, NSWindowTitleVisibility, NSWindowToolbarStyle,
    };
    use objc2_foundation::{NSPoint, NSProcessInfo, NSRect, NSSize, NSString};
    use serde_json::json;
    use tauri::WebviewWindow;

    use super::{
        calibrate_y, frame_radius, lights_inset, wants_toolbar, Lights, LightsFit, LIGHTS_MID,
        LIGHTS_TOLERANCE, TRAFFIC_LIGHT_X, TRAFFIC_LIGHT_Y,
    };
    use crate::eventlog;

    /// 主窗口工具栏的标识；临时窗口用另一个：同标识的工具栏会互相同步状态。
    const TOOLBAR_ID: &str = "oryn.main";
    const PROBE_TOOLBAR_ID: &str = "oryn.probe";

    /// 挂一个没有 item、不许自定义的 unified 工具栏：只为换上大圆角外框，不显示任何东西；去掉工具栏下的发丝线。
    fn attach(window: &NSWindow, mtm: MainThreadMarker, id: &str) {
        let toolbar = NSToolbar::initWithIdentifier(mtm.alloc(), &NSString::from_str(id));
        toolbar.setAllowsUserCustomization(false);
        window.setToolbar(Some(&toolbar));
        window.setToolbarStyle(NSWindowToolbarStyle::Unified);
        window.setTitlebarSeparatorStyle(NSTitlebarSeparatorStyle::None);
    }

    pub fn attach_main(w: &WebviewWindow) -> Result<(), String> {
        let mtm = MainThreadMarker::new().ok_or("not on main thread")?;
        let ptr = w.ns_window().map_err(|e| e.to_string())?;
        // SAFETY: Tauri 给的是这个（刚建好、仍存活的）窗口的 NSWindow 指针，只在主线程上临时借用
        let window = unsafe { &*ptr.cast::<NSWindow>() };
        attach(window, mtm, TOOLBAR_ID);
        Ok(())
    }

    /// 量红绿灯：读系统版本定是否挂工具栏，在临时窗口上校准 y 与 `inset.left`；结果或失败原因都记 `window.lights`。
    pub fn calibrate() -> Lights {
        let v = NSProcessInfo::processInfo().operatingSystemVersion();
        let os = format!("{}.{}.{}", v.majorVersion, v.minorVersion, v.patchVersion);
        let toolbar = wants_toolbar(v.majorVersion);
        let probed = MainThreadMarker::new()
            .ok_or("not on main thread")
            .and_then(|mtm| probe(mtm, toolbar));
        match probed {
            Ok(p) => {
                let lights = Lights {
                    y: p.fit.y,
                    inset_left: lights_inset(p.zoom_right),
                    toolbar,
                };
                eventlog::log(
                    "window.lights",
                    json!({
                        "os": os,
                        "toolbar": toolbar,
                        "radius": frame_radius(toolbar),
                        "y": lights.y,
                        "mid": p.fit.mid,
                        "converged": (p.fit.mid - LIGHTS_MID).abs() <= LIGHTS_TOLERANCE,
                        "rounds": p.fit.rounds,
                        "closeLeft": p.close_left,
                        "zoomRight": p.zoom_right,
                        "insetLeft": lights.inset_left,
                    }),
                );
                lights
            }
            Err(e) => {
                let lights = Lights::FALLBACK;
                eventlog::log(
                    "window.lights",
                    json!({
                        "os": os,
                        "error": e,
                        "toolbar": lights.toolbar,
                        "radius": frame_radius(lights.toolbar),
                        "y": lights.y,
                        "insetLeft": lights.inset_left,
                    }),
                );
                lights
            }
        }
    }

    struct Probe {
        fit: LightsFit,
        close_left: f64,
        zoom_right: f64,
    }

    /// 建一个不显示、样式与 tao Overlay 主窗口一致的临时窗口（titled | closable | miniaturizable | resizable |
    /// fullSizeContentView、标题栏透明、标题隐藏，≥ 26 同样挂工具栏），在上面校准，量完关掉。
    fn probe(mtm: MainThreadMarker, toolbar: bool) -> Result<Probe, &'static str> {
        let style = NSWindowStyleMask::Titled
            | NSWindowStyleMask::Closable
            | NSWindowStyleMask::Miniaturizable
            | NSWindowStyleMask::Resizable
            | NSWindowStyleMask::FullSizeContentView;
        // 与主窗口默认尺寸一致；只用到窗口高度换算坐标，按钮位置与宽高无关
        let rect = NSRect::new(NSPoint::new(0.0, 0.0), NSSize::new(1280.0, 820.0));
        // SAFETY: 主线程上按文档参数初始化；始终不 orderFront，关闭前一直由 Retained 持有
        let window = unsafe {
            NSWindow::initWithContentRect_styleMask_backing_defer(
                mtm.alloc(),
                rect,
                style,
                NSBackingStoreType::Buffered,
                false,
            )
        };
        // SAFETY: 默认 close 时 AppKit 会再 release 一次，与 Retained 自己的释放叠加就是过早释放
        unsafe { window.setReleasedWhenClosed(false) };
        window.setTitlebarAppearsTransparent(true);
        window.setTitleVisibility(NSWindowTitleVisibility::Hidden);
        if toolbar {
            attach(&window, mtm, PROBE_TOOLBAR_ID);
        }
        let result = measure(&window);
        window.close();
        result
    }

    fn measure(window: &NSWindow) -> Result<Probe, &'static str> {
        let button = |b| {
            window
                .standardWindowButton(b)
                .ok_or("missing window button")
        };
        let buttons = [
            button(NSWindowButton::CloseButton)?,
            button(NSWindowButton::MiniaturizeButton)?,
            button(NSWindowButton::ZoomButton)?,
        ];
        // SAFETY: 主线程上只读视图层级
        let theme = window
            .contentView()
            .and_then(|v| unsafe { v.superview() })
            .ok_or("missing theme frame")?;
        let fit = calibrate_y(TRAFFIC_LIGHT_Y, |y| {
            inset(window, &buttons, y)?;
            // 让主题框架（含工具栏）完成布局；布局可能把按钮挪回默认位置，tao 在每次 drawRect 里会再套一次，这里照做
            theme.layoutSubtreeIfNeeded();
            inset(window, &buttons, y)?;
            let r = window_rect(&buttons[0]);
            Some(window.frame().size.height - (r.origin.y + r.size.height / 2.0))
        })
        .ok_or("cannot measure traffic lights")?;
        let close = window_rect(&buttons[0]);
        let zoom = window_rect(&buttons[2]);
        Ok(Probe {
            fit,
            close_left: close.origin.x,
            zoom_right: zoom.origin.x + zoom.size.width,
        })
    }

    /// 视图在窗口坐标里的外框（原点在窗口左下，高度与 `window.frame()` 一致）。
    fn window_rect(v: &NSView) -> NSRect {
        v.convertRect_toView(v.bounds(), None)
    }

    /// 照抄 tao 0.37 `inset_traffic_lights`：标题栏容器高 = 关闭按钮高 + y、贴住窗口上缘；
    /// 三个按钮从 [`TRAFFIC_LIGHT_X`] 起按原间距横排，竖直位置不动。
    fn inset(window: &NSWindow, buttons: &[Retained<NSButton>; 3], y: f64) -> Option<()> {
        let [close, mini, zoom] = buttons;
        // SAFETY: 主线程上只读视图层级
        let container = unsafe { close.superview()?.superview()? };
        let close_rect = close.frame();
        let mut rect = container.frame();
        rect.size.height = close_rect.size.height + y;
        rect.origin.y = window.frame().size.height - rect.size.height;
        container.setFrame(rect);
        let space = mini.frame().origin.x - close_rect.origin.x;
        for (i, b) in [close, mini, zoom].into_iter().enumerate() {
            let mut origin = b.frame().origin;
            origin.x = TRAFFIC_LIGHT_X + i as f64 * space;
            b.setFrameOrigin(origin);
        }
        Some(())
    }
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
                "inset": {"top": 48.0, "left": 0.0, "right": 0.0},
                "radius": 0.0
            })
        );
        // 校准失败：旧常量、不挂工具栏、不做同心圆角
        let mac = serde_json::to_value(native_left(&Lights::FALLBACK)).unwrap();
        assert_eq!(mac["controls"], "native-left");
        assert_eq!(mac["buttons"], json!([]));
        assert_eq!(mac["inset"]["left"], 84.0);
        assert_eq!(mac["radius"], 0.0);
        // 校准成功且挂了 unified 工具栏：量出的留白、26 的外框圆角
        let mac = serde_json::to_value(native_left(&Lights {
            y: 34.5,
            inset_left: 85.0,
            toolbar: true,
        }))
        .unwrap();
        assert_eq!(
            mac["inset"],
            json!({"top": 48.0, "left": 85.0, "right": 0.0})
        );
        assert_eq!(mac["radius"], 26.0);
        assert_eq!(
            serde_json::to_value(custom("linux", Some("close:"))).unwrap()["controls"],
            "custom-left"
        );
    }

    #[test]
    fn toolbar_by_version() {
        assert!(!wants_toolbar(15));
        assert!(wants_toolbar(26));
        assert!(wants_toolbar(27));
        assert_eq!(frame_radius(true), 26.0);
        assert_eq!(frame_radius(false), 0.0);
        // 右缘取整再留 8：76 → 84，带小数的抗锯齿边往外进一格
        assert_eq!(lights_inset(76.0), 84.0);
        assert_eq!(lights_inset(75.3), 84.0);
        assert_eq!(lights_inset(76.2), 85.0);
    }

    #[test]
    fn calibrates_light_y() {
        // 按钮随 y 一比一移动（macOS 27 实测：y = 24 时中线 21.5）：补一次差就收敛
        let fit = calibrate_y(24.0, |y| Some(y - 2.5)).unwrap();
        assert_eq!(
            fit,
            LightsFit {
                y: 34.5,
                mid: 32.0,
                rounds: 2
            }
        );
        // 只移一半（系统在容器里居中按钮）：第二次按一比一补不够，第三次按割线补准
        let fit = calibrate_y(24.0, |y| Some(9.5 + y / 2.0)).unwrap();
        assert_eq!(fit.rounds, 3);
        assert!((fit.y - 45.0).abs() < 1e-9 && (fit.mid - 32.0).abs() < 1e-9);
        // 一开始就对：不再试探
        let fit = calibrate_y(24.0, |y| Some(y + 8.0)).unwrap();
        assert_eq!(
            fit,
            LightsFit {
                y: 24.0,
                mid: 32.0,
                rounds: 1
            }
        );
        // 按钮根本不随 y 动：量满次数，取误差最小（同分取最早的一次）
        let mut tried = Vec::new();
        let fit = calibrate_y(24.0, |y| {
            tried.push(y);
            Some(20.0)
        })
        .unwrap();
        assert_eq!(
            fit,
            LightsFit {
                y: 24.0,
                mid: 20.0,
                rounds: LIGHTS_ROUNDS
            }
        );
        assert_eq!(tried, vec![24.0, 36.0, 48.0, 60.0]);
        // 要的 y 落到范围外：夹到 0，顶住后不再重复量
        let fit = calibrate_y(24.0, |y| Some(y + 40.0)).unwrap();
        assert_eq!(
            fit,
            LightsFit {
                y: 0.0,
                mid: 40.0,
                rounds: 2
            }
        );
        // 量不出来（拿不到按钮 / 非有限值）：交给调用方退回常量
        assert_eq!(calibrate_y(24.0, |_| None), None);
        assert_eq!(calibrate_y(24.0, |_| Some(f64::NAN)), None);
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
