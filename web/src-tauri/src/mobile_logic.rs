//! 移动端的纯逻辑：导航判定、推送状态归并与订阅同步、分享接收流程、`MobileInfo`、原生底栏入参校验。
//!
//! 不带平台 `cfg`、不依赖 Tauri：原生能力经 [`Native`]、网络经 [`Api`]，在主机上即可单测。
//! 桌面不调用这里（只在单测里覆盖），故整体允许未使用。
#![cfg_attr(not(mobile), allow(dead_code))]

use std::future::Future;
use std::path::Path;
use std::sync::{Arc, Mutex, MutexGuard};

use serde::Serialize;
use serde_json::{json, Value};
use tauri_plugin_oryn_mobile::{
    CornerRadii, HeaderIcon, HeaderItem, HeaderMenu, HeaderMenuCmd, HeaderMenuKind, HeaderOp,
    HeaderSeg, HeaderShow, HeaderSpec, Insets, Permission, PushChannel, PushState, ShareIntake,
    TabBarFrame, TabBarIcon, TabBarSpec,
};

use crate::api::{Api, ApiError, PushSubscription};
use crate::config::TokenStoreKind;
use crate::eventlog::EventLog;
use crate::i18n::{self, Lang};
use crate::session::Connection;

// ---------------------------------------------------------------- 导航

/// 主框架导航的处理方式。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NavDecision {
    /// 在 WebView 里打开。
    Allow,
    /// 交给系统（浏览器、邮件、拨号）。
    External,
    /// `oryn:` 深链接：走站内路径。
    DeepLink,
    /// 丢弃（`intent:`、`file:`、`javascript:` 等）。
    Block,
}

/// launcher = 连接页 origin（`http(s)://tauri.localhost`）；server = 已配对或正在连接的服务器 origin。
/// 主框架只放行这两个 origin 与 `about:blank`：`blob:` 只认服务器自己 origin 下的（portal 页面经 `window.open`
/// 弹出的 `blob:` 会把整个 App 换成无地址栏的伪造界面），`data:` 一律拦截。
pub fn nav_decision(url: &str, launcher: &str, server: Option<&str>) -> NavDecision {
    let Ok(u) = url::Url::parse(url) else {
        return NavDecision::Block;
    };
    match u.scheme() {
        "http" | "https" => {
            let origin = u.origin().ascii_serialization();
            if origin == launcher || Some(origin.as_str()) == server {
                NavDecision::Allow
            } else {
                NavDecision::External
            }
        }
        "about" if u.as_str() == "about:blank" => NavDecision::Allow,
        // `blob:<origin>/<uuid>` 的 origin 是内嵌地址的 origin；不透明 origin 序列化为 "null"，不会等于服务器
        "blob" if server.is_some_and(|s| u.origin().ascii_serialization() == s) => {
            NavDecision::Allow
        }
        "oryn" => NavDecision::DeepLink,
        "mailto" | "tel" => NavDecision::External,
        _ => NavDecision::Block,
    }
}

/// `host_open_external` 只接受 http / https；返回规范化后的地址。
pub fn external_url(url: &str) -> Option<String> {
    let u = url::Url::parse(url.trim()).ok()?;
    matches!(u.scheme(), "http" | "https").then(|| u.into())
}

/// `host_set_chrome` 的颜色：`^#[0-9a-fA-F]{6}$`。
pub fn valid_color(color: &str) -> bool {
    color.len() == 7 && color.starts_with('#') && color[1..].bytes().all(|b| b.is_ascii_hexdigit())
}

/// Activity 重建前主框架所在的地址 → 恢复会话后要回到的站内路径（含查询）。只认已配对服务器的页面；
/// 连接页、兑换地址 `/auth/app`（一次性码已用掉）与其它地址返回 `None`。
pub fn recreated_path(url: &str, server: Option<&str>) -> Option<String> {
    let u = url::Url::parse(url).ok()?;
    if Some(u.origin().ascii_serialization().as_str()) != server || u.path() == "/auth/app" {
        return None;
    }
    let path = match u.query() {
        Some(q) => format!("{}?{q}", u.path()),
        None => u.path().to_string(),
    };
    Some(crate::session::valid_app_path(&path))
}

/// 主 WebView 已建好的闸门。移动端的 WebView 要等 Activity 就绪后才异步创建，在此之前发出的
/// `navigate` 会被 wry 静默丢弃（`main_pipe.rs` 的 `LoadUrl` 找不到 webview 就什么也不做）：
/// 冷启动时若兑换会话比 Activity 快，主框架会留在随后加载的连接页上，看门狗到点报 `web_timeout`。
/// 会话启动先等这道闸门；首个页面加载事件或连接页的 `launcher_state` 打开它。
#[derive(Default)]
pub struct WebviewGate(tokio::sync::watch::Sender<bool>);

impl WebviewGate {
    /// 打开闸门（幂等）。
    pub fn open(&self) {
        self.0.send_replace(true);
    }

    /// 等到闸门打开；已打开则立即返回。
    pub async fn wait(&self) {
        let mut rx = self.0.subscribe();
        // 发送端就在 self 里，不会先于接收端关闭
        let _ = rx.wait_for(|up| *up).await;
    }
}

/// Android 主窗口与 Activity 的对应：Activity 被系统销毁（「不保留活动」、系统回收 Activity 但保留进程）时进程不退出，
/// 用户回来时为新 Activity 重建主窗口。
///
/// 不是配置变更的 `onDestroy` 会让 tao 发 `WindowEvent::Destroyed`，Tauri 删掉唯一的窗口后发
/// `ExitRequested { code: None }`（`tauri-runtime-wry` 的 `TaoWindowEvent::Destroyed` 分支）。放行就是 tao 的
/// `EventLoop::run` → `std::process::exit`，在模拟器上会死在 EGL 的静态析构里
/// （`FORTIFY: pthread_mutex_lock called on a destroyed mutex`）。所以拦下这种退出，进程照常留在后台；
/// 用户切回时 Android 在同一进程里新建 Activity（tao 的 `onCreate` 登记新的上下文，wry 登记 Activity 代理），
/// 插件报 `activity` 事件，这里为它重新建主窗口，之后走与配置变更重建相同的插件改绑 + 会话恢复。
///
/// 两个事件的先后不定：新 Activity 可能在旧窗口的 `Destroyed` 还没处理完时就到（同名窗口还在，建不了），
/// 这时记下来，等 `Destroyed` 再建。
#[derive(Debug)]
pub struct MainWindowLife {
    window: bool,
    activity_waiting: bool,
}

impl Default for MainWindowLife {
    /// `setup` 已建好主窗口。
    fn default() -> Self {
        Self {
            window: true,
            activity_waiting: false,
        }
    }
}

impl MainWindowLife {
    /// `ExitRequested` 是否拦下：`code` 为 `None` = 最后一个窗口随 Activity 销毁，保留进程；
    /// 显式的 `exit(code)` 放行。
    pub fn keep_process(code: Option<i32>) -> bool {
        code.is_none()
    }

    /// 主窗口已销毁。返回 `true` = 有新 Activity 在等，现在就为它建主窗口。
    pub fn destroyed(&mut self) -> bool {
        self.window = std::mem::take(&mut self.activity_waiting);
        self.window
    }

    /// 旧 Activity 销毁后同一进程里来了新 Activity。返回 `true` = 现在建主窗口；
    /// 旧窗口还没删掉时返回 `false`，等 [`Self::destroyed`]。
    pub fn activity_created(&mut self) -> bool {
        if self.window {
            self.activity_waiting = true;
            return false;
        }
        self.window = true;
        true
    }

    /// 建主窗口失败：下一个新 Activity 再试。
    pub fn create_failed(&mut self) {
        self.window = false;
    }
}

// ---------------------------------------------------------------- 推送状态

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Platform {
    Android,
    Ios,
}

impl Platform {
    pub fn current() -> Self {
        if cfg!(target_os = "ios") {
            Platform::Ios
        } else {
            Platform::Android
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Platform::Android => "android",
            Platform::Ios => "ios",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PushViewState {
    Unsupported,
    Disabled,
    PermissionDenied,
    NoDistributor,
    Registering,
    Active,
    Error,
}

/// `MobileInfo.push`。
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PushView {
    pub state: PushViewState,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub channel: Option<PushChannel>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub distributor: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub endpoint_host: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

impl PushView {
    fn bare(state: PushViewState) -> Self {
        Self {
            state,
            channel: None,
            distributor: None,
            endpoint_host: None,
            error: None,
        }
    }
}

/// 已登记到服务器时应当记下的键：FCM 为令牌，UnifiedPush 为端点。
fn sync_key(s: &PushState) -> Option<&str> {
    match s.channel {
        Some(PushChannel::Fcm) => s.fcm_token.as_deref(),
        _ => s.endpoint.as_deref(),
    }
}

/// 状态归并。ios → unsupported；!enabled → disabled；permission != granted → permission_denied；
/// 没有通道（无分发器且不走 FCM）→ no_distributor；原生或同步错误 → error；
/// 端点 / 令牌为空或与已同步的不同 → registering；否则 active。
pub fn push_view(platform: Platform, s: Option<&PushState>, sync_error: Option<&str>) -> PushView {
    if platform == Platform::Ios {
        return PushView::bare(PushViewState::Unsupported);
    }
    let Some(s) = s.filter(|s| s.enabled) else {
        return PushView::bare(PushViewState::Disabled);
    };
    let mut view = PushView {
        state: PushViewState::Active,
        channel: s.channel,
        distributor: s.distributor.clone(),
        endpoint_host: match s.channel {
            Some(PushChannel::Fcm) => None,
            _ => s
                .endpoint
                .as_deref()
                .and_then(|e| url::Url::parse(e).ok())
                .and_then(|u| u.host_str().map(str::to_string)),
        },
        error: None,
    };
    view.state = if s.permission != Permission::Granted {
        PushViewState::PermissionDenied
    } else if s.channel.is_none() && s.distributor.is_none() {
        PushViewState::NoDistributor
    } else if let Some(e) = s.error.as_deref().or(sync_error) {
        view.error = Some(e.to_string());
        PushViewState::Error
    } else if sync_key(s).is_none() || sync_key(s) != s.synced_endpoint.as_deref() {
        PushViewState::Registering
    } else {
        PushViewState::Active
    };
    view
}

/// 要登记的订阅；材料不全时 `None`。`force = false` 时已同步的也返回 `None`。
/// `force`：服务器可能已删掉订阅（令牌失效、M7 之前没有密钥的 FCM 订阅），本地「已同步」不可尽信；
/// 登记按端点 / 令牌 upsert，重复登记无害，所以每次进入应用都强制登记一次。
pub fn sync_target(s: &PushState, force: bool) -> Option<PushSubscription> {
    if !s.enabled {
        return None;
    }
    let sub = match s.channel {
        // FCM 同样必须带 RFC 8291 公钥：服务端只发密文，缺密钥的订阅会被拒（400 push_invalid）
        Some(PushChannel::Fcm) => PushSubscription::Fcm {
            token: s.fcm_token.clone()?,
            p256dh: s.p256dh.clone()?,
            auth: s.auth.clone()?,
        },
        _ => PushSubscription::WebPush {
            endpoint: s.endpoint.clone()?,
            p256dh: s.p256dh.clone()?,
            auth: s.auth.clone()?,
        },
    };
    (force || s.synced_endpoint.as_deref() != Some(sub.key())).then_some(sub)
}

/// `host_mobile_info` 的返回。
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct MobileInfo {
    pub version: String,
    pub platform: &'static str,
    pub token_store: TokenStoreKind,
    pub webview: String,
    pub push: PushView,
    pub corner_radii: CornerRadii,
    pub insets: Insets,
}

/// 页面事件 `{type:"insets", cornerRadii, insets}`。
pub fn insets_event(corner_radii: CornerRadii, insets: Insets) -> Value {
    json!({"type": "insets", "cornerRadii": corner_radii, "insets": insets})
}

// ---------------------------------------------------------------- 原生底栏

/// 底栏项 id：1–32 字节的 `[a-z0-9_-]`。
#[cfg_attr(target_os = "android", allow(dead_code))]
pub fn valid_tab_id(id: &str) -> bool {
    (1..=32).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'_' || b == b'-')
}

/// 页面事件 `{type:"tabbar", id}`。
#[cfg_attr(target_os = "android", allow(dead_code))]
pub fn tab_bar_event(id: &str) -> Value {
    json!({"type": "tabbar", "id": id})
}

/// SF Symbol 名：1..=64 字节 `[a-z0-9.]`。
fn valid_symbol(s: &str) -> bool {
    (1..=64).contains(&s.len())
        && s.bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'.')
}

fn valid_tab_icon(icon: &TabBarIcon) -> bool {
    match icon {
        TabBarIcon::Symbol(s) => valid_symbol(s),
        TabBarIcon::Glyph(g) => (1..=32).contains(&g.len()),
        TabBarIcon::Image(b64) => valid_base64(b64, 131_072),
    }
}

/// 1..=`max` 字节且全是 base64 字符（`A-Za-z0-9+/=`）。
fn valid_base64(s: &str, max: usize) -> bool {
    (1..=max).contains(&s.len())
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'+' | b'/' | b'='))
}

/// `v` 有限且落在 `[lo, hi]` 内（NaN 与无穷都不算）。
fn within(v: f64, lo: f64, hi: f64) -> bool {
    v.is_finite() && (lo..=hi).contains(&v)
}

/// 原生叠层的矩形：四项有限，x、y ∈ [-100, 5000]，width ∈ [1, 2000]，height ∈ [1, 400]。
fn valid_frame(f: &TabBarFrame) -> bool {
    within(f.x, -100.0, 5000.0)
        && within(f.y, -100.0, 5000.0)
        && within(f.width, 1.0, 2000.0)
        && within(f.height, 1.0, 400.0)
}

/// `host_tab_bar_set` 的入参上限（命令可能被 portal iframe 调到，见 host.rs 文件头）：
/// items 1..=6、id 互不相同且 `valid_tab_id`；label 1..=64 个字符；
/// icon：symbol 1..=64 字节 `[a-z0-9.]`，glyph 1..=32 字节，image 1..=131072 字节且全是 base64 字符（`A-Za-z0-9+/=`）；
/// selected ≤ 32 字节；frame 四项有限，x、y ∈ [-100, 5000]，width ∈ [1, 2000]，height ∈ [1, 400]；
/// accent 为 None 或 `valid_color`。
pub fn valid_tab_bar(spec: &TabBarSpec) -> bool {
    let items = &spec.items;
    let items_ok = (1..=6).contains(&items.len())
        && items.iter().enumerate().all(|(i, item)| {
            valid_tab_id(&item.id)
                && items[..i].iter().all(|prev| prev.id != item.id)
                && (1..=64).contains(&item.label.chars().count())
                && valid_tab_icon(&item.icon)
        });
    items_ok
        && spec.selected.len() <= 32
        && valid_frame(&spec.frame)
        && spec.accent.as_deref().is_none_or(valid_color)
}

/// 原生底栏 / 页头的 set 是否放行（bridge 8）：撤掉（`!has_spec`）总是放行；非空 spec 要本页已 `host_sink_set(true)`。
/// 旧网页（bridge 7）不发 `host_sink_set`，非空 set 在这里被拒，退回网页底栏 / 页头。
pub fn native_chrome_allowed(sink_on: bool, has_spec: bool) -> bool {
    !has_spec || sink_on
}

// ---------------------------------------------------------------- 可替换图标（iOS）

/// 默认图标在页面上的 id（系统里对应 `alternateIconName == nil`）。
pub const APP_ICON_DEFAULT: &str = "default";

/// 可替换图标：页面用的 id 与 iOS 资源名（`web/scripts/gen-icons.py` 生成的 `<资源名>.appiconset`）。
pub const APP_ICONS: &[(&str, &str)] = &[("accent", "AppIconAccent")];

/// 页面给的 id → 资源名：默认图标 → `Some(None)`；不认识的 id → `None`（拒绝）。
pub fn app_icon_asset(id: &str) -> Option<Option<&'static str>> {
    if id == APP_ICON_DEFAULT {
        return Some(None);
    }
    APP_ICONS
        .iter()
        .find(|(i, _)| *i == id)
        .map(|(_, asset)| Some(*asset))
}

/// `host_app_icon_get` 的返回：当前 id 与本壳带的全部 id（默认在前）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct AppIconInfo {
    pub supported: bool,
    pub current: &'static str,
    pub options: Vec<&'static str>,
}

/// 系统报告的资源名 → 页面状态；不认识的资源名（例如以后删掉的图标）按默认处理。
pub fn app_icon_info(supported: bool, asset: Option<&str>) -> AppIconInfo {
    let current = asset
        .and_then(|a| APP_ICONS.iter().find(|(_, x)| *x == a))
        .map_or(APP_ICON_DEFAULT, |(id, _)| *id);
    AppIconInfo {
        supported,
        current,
        options: std::iter::once(APP_ICON_DEFAULT)
            .chain(APP_ICONS.iter().map(|(id, _)| *id))
            .collect(),
    }
}

// ---------------------------------------------------------------- 原生页头

/// 字符数（不是字节数）落在 `lo..=hi`。
fn chars_in(s: &str, lo: usize, hi: usize) -> bool {
    (lo..=hi).contains(&s.chars().count())
}

/// 线程 id：1..=96 字节，`[A-Za-z0-9_.:-]`。
pub fn valid_header_key(key: &str) -> bool {
    (1..=96).contains(&key.len())
        && key
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b':' | b'-'))
}

/// 菜单项 id（含子项的 `父/子`）：1..=96 字节，`[A-Za-z0-9_.:/#-]`。
pub fn valid_item_id(id: &str) -> bool {
    (1..=96).contains(&id.len())
        && id.bytes().all(|b| {
            b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b':' | b'/' | b'#' | b'-')
        })
}

/// 图标五选一：symbol / glyph（1..=32 字节）/ mask（base64，1..=16384 字节）/ dot / orb；`bg`、`fg` 只配 glyph 与 mask；
/// 颜色一律 `valid_color`。
fn valid_header_icon(i: &HeaderIcon) -> bool {
    let kinds = [
        i.symbol.is_some(),
        i.glyph.is_some(),
        i.mask.is_some(),
        i.dot.is_some(),
        i.orb.is_some(),
    ];
    kinds.iter().filter(|k| **k).count() == 1
        && i.symbol.as_deref().is_none_or(valid_symbol)
        && i.glyph
            .as_deref()
            .is_none_or(|g| (1..=32).contains(&g.len()))
        && i.mask.as_deref().is_none_or(|m| valid_base64(m, 16_384))
        && i.dot.as_deref().is_none_or(valid_color)
        && (i.glyph.is_some() || i.mask.is_some() || (i.bg.is_none() && i.fg.is_none()))
        && i.bg.as_deref().is_none_or(valid_color)
        && i.fg.as_deref().is_none_or(valid_color)
}

fn valid_seg(s: &HeaderSeg) -> bool {
    chars_in(&s.text, 1, 120) && s.icon.as_ref().is_none_or(valid_header_icon)
}

/// `top`：顶层项才能带子项（子页只有一级）。
fn valid_item(item: &HeaderItem, top: bool) -> bool {
    valid_item_id(&item.id)
        && chars_in(&item.label, 1, 200)
        && item.icon.as_ref().is_none_or(valid_header_icon)
        && item.sub.as_ref().is_none_or(valid_seg)
        && item.hint.as_deref().is_none_or(|h| chars_in(h, 1, 32))
        && item
            .copy
            .as_deref()
            .is_none_or(|c| (1..=2048).contains(&c.len()))
        && item.children.len() <= 24
        && (top || item.children.is_empty())
        && item.children.iter().all(|c| valid_item(c, false))
}

fn valid_menu(m: &HeaderMenu) -> bool {
    let tops = || {
        m.quick
            .iter()
            .chain(&m.status)
            .chain(m.groups.iter().flatten())
    };
    let mut ids: Vec<&str> = tops()
        .flat_map(|i| std::iter::once(i).chain(&i.children))
        .map(|i| i.id.as_str())
        .collect();
    let total = ids.len();
    ids.sort_unstable();
    ids.dedup();
    chars_in(&m.label, 1, 400)
        && m.quick.len() <= 4
        && m.status.len() <= 32
        && m.groups.len() <= 8
        && m.groups.iter().all(|g| (1..=24).contains(&g.len()))
        && total <= 160
        && ids.len() == total
        && tops().all(|i| valid_item(i, true))
}

/// `host_header_set` 的入参上限（命令可能被 portal iframe 调到，见 host.rs 文件头）：
/// key `valid_header_key`；六个调色板颜色 `valid_color`；tune 两项 ∈ [0, 1]；text 两项 1..=32 个字符；
/// lead / trail：frame 同底栏（`valid_frame`）、symbol `valid_symbol`、label 1..=64 个字符；title / trail 可为 null（首页只有 lead）；
/// title（有的话）：text 1..=300、label 1..=600、error（有的话）1..=300 个字符，meta ≤ 6 段、marks ≤ 6 个；
/// 菜单：label 1..=400，quick ≤ 4、status ≤ 32、groups ≤ 8 组且每组 1..=24 项、子项 ≤ 24 且不再嵌套、
/// 全菜单 ≤ 160 项且 id 互不相同；项：label 1..=200、hint 1..=32、说明行 1..=120 个字符、copy 1..=2048 字节；
/// 图标五选一，`bg` / `fg` 只配 `glyph` 与 `mask`。
pub fn valid_header(spec: &HeaderSpec) -> bool {
    let p = &spec.palette;
    valid_header_key(&spec.key)
        && [&p.accent, &p.brand, &p.err, &p.warn, &p.good, &p.frost]
            .iter()
            .all(|c| valid_color(c))
        && within(spec.tune.scrim, 0.0, 1.0)
        && within(spec.tune.tint, 0.0, 1.0)
        && chars_in(&spec.text.back, 1, 32)
        && chars_in(&spec.text.close, 1, 32)
        && valid_frame(&spec.lead.frame)
        && valid_symbol(&spec.lead.symbol)
        && chars_in(&spec.lead.label, 1, 64)
        && spec.title.as_ref().is_none_or(|t| {
            valid_frame(&t.frame)
                && chars_in(&t.text, 1, 300)
                && chars_in(&t.label, 1, 600)
                && t.icon.as_ref().is_none_or(valid_header_icon)
                && t.error.as_deref().is_none_or(|e| chars_in(e, 1, 300))
                && t.meta.len() <= 6
                && t.meta.iter().all(valid_seg)
                && t.marks.len() <= 6
                && t.marks.iter().all(|m| valid_symbol(&m.symbol))
                && valid_menu(&t.menu)
        })
        && spec.trail.as_ref().is_none_or(|r| {
            valid_frame(&r.frame)
                && valid_symbol(&r.symbol)
                && chars_in(&r.label, 1, 64)
                && valid_menu(&r.menu)
        })
}

/// alpha ∈ [0, 1]；ms ≤ 2000。
pub fn valid_header_show(s: &HeaderShow) -> bool {
    within(s.alpha, 0.0, 1.0) && s.ms <= 2000
}

/// `page`（有的话）是合法的菜单项 id。
pub fn valid_header_menu(m: &HeaderMenuCmd) -> bool {
    m.page.as_deref().is_none_or(valid_item_id)
}

/// 插件事件 → 页面事件 `{type:"header", op, key, …}`；形状不对或越界的丢弃。
#[cfg_attr(target_os = "android", allow(dead_code))]
pub fn header_event(
    op: HeaderOp,
    key: &str,
    menu: Option<HeaderMenuKind>,
    id: Option<&str>,
) -> Option<Value> {
    if !valid_header_key(key) {
        return None;
    }
    match (op, menu, id) {
        (HeaderOp::Lead, None, None) => Some(json!({"type": "header", "op": "lead", "key": key})),
        (HeaderOp::Menu, menu, None) => {
            Some(json!({"type": "header", "op": "menu", "key": key, "menu": menu}))
        }
        (HeaderOp::Select, Some(menu), Some(id)) if valid_item_id(id) => {
            Some(json!({"type": "header", "op": "select", "key": key, "menu": menu, "id": id}))
        }
        _ => None,
    }
}

/// CI 演示用：夹具 header-set.json，只把三个 frame 换成该屏幕下网页会量出的位置（页头 `px-3 py-2`、圆钮 44、胶囊高 46、间距 8）：
/// lead = {12, safe_top + 9, 44, 44}；title = {64, safe_top + 8, width − 128, 46}；trail = {width − 56, safe_top + 9, 44, 44}。
#[cfg(any(test, all(debug_assertions, target_os = "ios")))]
pub fn demo_header(width: f64, safe_top: f64) -> HeaderSpec {
    let mut spec: HeaderSpec = serde_json::from_str(include_str!(
        "../plugins/oryn-mobile/fixtures/header-set.json"
    ))
    .expect("header-set.json 夹具");
    let round = |x: f64| TabBarFrame {
        x,
        y: safe_top + 9.0,
        width: 44.0,
        height: 44.0,
    };
    spec.lead.frame = round(12.0);
    if let Some(title) = spec.title.as_mut() {
        title.frame = TabBarFrame {
            x: 64.0,
            y: safe_top + 8.0,
            width: width - 128.0,
            height: 46.0,
        };
    }
    if let Some(trail) = spec.trail.as_mut() {
        trail.frame = round(width - 56.0);
    }
    spec
}

// ---------------------------------------------------------------- 分享

/// 上传失败或被原生侧拒收的文件。
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ShareFailed {
    pub name: String,
    /// `upload` / `too_large` / `too_many` / `unreadable`。
    pub reason: String,
}

/// 下发给页面的分享（`{type:"share", share}` 的 `share`）。
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct ShareOut {
    pub id: String,
    pub text: Option<String>,
    /// 服务端返回的 Attachment JSON 原样。
    pub attachments: Vec<Value>,
    pub failed: Vec<ShareFailed>,
}

impl ShareOut {
    pub fn event(&self) -> Value {
        json!({"type": "share", "share": self})
    }
}

/// 分享槽：同一时刻只保留最新一次（新的到来时旧的作废）。
/// 每次收到分享代次加一；上传跨网络 await，完成时只接受仍是最新代次的结果——不能拿 `incoming` 是否为空判断，
/// 较新的分享可能也已被取走上传、甚至已下发（代码审查第 4 条）。
#[derive(Debug, Default)]
pub struct Shares {
    /// 收到、尚未上传（未进入应用时在这里等待）。
    incoming: Option<ShareIntake>,
    /// 已上传、等页面确认。
    ready: Option<ShareOut>,
    /// 最近一次收到的分享的代次。
    latest: u64,
}

impl Shares {
    /// 收下新分享；返回被它取代、需要清掉缓存的旧分享 id。
    pub fn receive(&mut self, s: ShareIntake) -> Vec<String> {
        let mut stale: Vec<String> = Vec::new();
        stale.extend(self.incoming.take().map(|s| s.id));
        stale.extend(self.ready.take().map(|s| s.id));
        self.incoming = Some(s);
        self.latest += 1;
        stale
    }

    /// 取走待上传的分享与它的代次（[`Self::set_ready`] 时交回）。
    pub fn take_incoming(&mut self) -> Option<(ShareIntake, u64)> {
        self.incoming.take().map(|s| (s, self.latest))
    }

    /// 上传完成。上传期间又收到过新分享（代次已变）时这份作废：返回 `Err(它的 id)`。
    pub fn set_ready(&mut self, out: ShareOut, generation: u64) -> Result<(), String> {
        if generation != self.latest {
            return Err(out.id);
        }
        self.ready = Some(out);
        Ok(())
    }

    pub fn ready(&self) -> Option<&ShareOut> {
        self.ready.as_ref()
    }

    pub fn has_incoming(&self) -> bool {
        self.incoming.is_some()
    }

    /// 页面确认；只认当前待确认的那一次（id 来自页面，不能拿去删任意目录）。
    pub fn ack(&mut self, id: &str) -> bool {
        if self.ready.as_ref().is_some_and(|r| r.id == id) {
            self.ready = None;
            true
        } else {
            false
        }
    }
}

// ---------------------------------------------------------------- 编排

/// 原生侧能力（Android 为 oryn-mobile 插件；iOS 与单测各有实现）。
pub trait Native: Send + Sync + 'static {
    fn push_state(&self) -> Result<PushState, String>;
    fn push_enable(&self, vapid: &str) -> impl Future<Output = Result<PushState, String>> + Send;
    fn push_disable(&self) -> Result<PushState, String>;
    /// 记下已登记的端点 / 令牌、订阅 id 与服务器（原生后台任务在 App 不在前台时据此重新登记）。
    fn push_mark_synced(&self, endpoint: &str, id: &str, server: &str) -> Result<(), String>;
    fn share_ack(&self, id: &str);
    fn toast(&self, text: &str);
}

#[derive(Default)]
struct Inner {
    /// 最近一次登记被服务器以 4xx 拒绝的原因（网络错误不记，下次再试）。
    sync_error: Option<String>,
    shares: Shares,
    corner_radii: CornerRadii,
    insets: Insets,
}

/// 推送订阅同步与分享流程的编排（进程级一份）。
pub struct MobileCore<N: Native, A: Api> {
    native: N,
    api: Arc<A>,
    log: EventLog,
    platform: Platform,
    inner: Mutex<Inner>,
    /// 串行化订阅同步：插件事件、`host_ready`、回到前台可能同时触发。
    sync_lock: tokio::sync::Mutex<()>,
}

impl<N: Native, A: Api> MobileCore<N, A> {
    pub fn new(native: N, api: Arc<A>, log: EventLog, platform: Platform) -> Self {
        Self {
            native,
            api,
            log,
            platform,
            inner: Mutex::new(Inner::default()),
            sync_lock: tokio::sync::Mutex::new(()),
        }
    }

    pub fn native(&self) -> &N {
        &self.native
    }

    fn inner(&self) -> MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// 记下最新的屏幕圆角与原生 inset；值变了返回 `true`。
    pub fn set_insets(&self, corner_radii: CornerRadii, insets: Insets) -> bool {
        let mut g = self.inner();
        let changed = g.corner_radii != corner_radii || g.insets != insets;
        g.corner_radii = corner_radii;
        g.insets = insets;
        changed
    }

    pub fn push_view(&self) -> PushView {
        let state = self.native.push_state().ok();
        let sync_error = self.inner().sync_error.clone();
        push_view(self.platform, state.as_ref(), sync_error.as_deref())
    }

    pub fn info(&self, version: &str, token_store: TokenStoreKind, webview: &str) -> MobileInfo {
        let push = self.push_view();
        let g = self.inner();
        MobileInfo {
            version: version.to_string(),
            platform: self.platform.as_str(),
            token_store,
            webview: webview.to_string(),
            push,
            corner_radii: g.corner_radii,
            insets: g.insets,
        }
    }

    /// 把尚未登记（或端点 / 令牌变了；`force` 时无论如何）的订阅登记到服务器。返回页面是否需要重取 `MobileInfo`。
    pub async fn sync_push(&self, conn: Option<&Connection>, force: bool) -> bool {
        let Some(conn) = conn else { return false };
        let _serial = self.sync_lock.lock().await;
        let Ok(state) = self.native.push_state() else {
            return false;
        };
        let Some(sub) = sync_target(&state, force) else {
            return false;
        };
        let kind = match sub {
            PushSubscription::Fcm { .. } => "fcm",
            PushSubscription::WebPush { .. } => "webpush",
        };
        match self.api.push_subscribe(&conn.base, &conn.token, &sub).await {
            Ok(id) => {
                let unchanged = state.synced_endpoint.as_deref() == Some(sub.key())
                    && state.subscription_id.as_deref() == Some(id.as_str());
                if let Err(e) = self.native.push_mark_synced(sub.key(), &id, &conn.base) {
                    self.log.log("push.sync_error", json!({"error": e}));
                    return false;
                }
                let had_error = self.inner().sync_error.take().is_some();
                self.log.log(
                    "push.synced",
                    json!({"id": id, "kind": kind, "forced": force}),
                );
                !unchanged || had_error
            }
            Err(ApiError::Status { status, code, .. }) if (400..500).contains(&status) => {
                let reason = if code.is_empty() {
                    format!("http_{status}")
                } else {
                    code.clone()
                };
                self.inner().sync_error = Some(reason);
                self.log
                    .log("push.sync_error", json!({"status": status, "code": code}));
                true
            }
            Err(_) => false,
        }
    }

    /// `host_push_enable`：取 VAPID 公钥 → 原生启用（可能弹权限、选分发器）→ 登记订阅。
    pub async fn push_enable(&self, conn: Option<&Connection>) -> Result<(), String> {
        let conn = conn.ok_or_else(|| "not_connected".to_string())?;
        let vapid = self
            .api
            .vapid_key(&conn.base, &conn.token)
            .await
            .map_err(|e| e.to_string())?;
        self.native.push_enable(&vapid).await?;
        self.inner().sync_error = None;
        self.sync_push(Some(conn), false).await;
        Ok(())
    }

    /// `host_push_disable`：有订阅 id 则先从服务器删掉（失败忽略），再停用原生推送。
    pub async fn push_disable(&self, conn: Option<&Connection>) {
        if let (Some(conn), Some(id)) = (conn, self.subscription_id()) {
            let _ = self
                .api
                .push_unsubscribe(&conn.base, &conn.token, &id)
                .await;
        }
        self.disable_local();
    }

    pub fn subscription_id(&self) -> Option<String> {
        self.native.push_state().ok()?.subscription_id
    }

    /// 只停用本机推送（令牌已失效或即将删除时）。
    pub fn disable_local(&self) {
        if let Err(e) = self.native.push_disable() {
            self.log.log("push.disable_error", json!({"error": e}));
        }
        self.inner().sync_error = None;
    }

    /// 原生侧收到系统分享。
    pub fn share_received(&self, s: ShareIntake) {
        self.log.log(
            "share.received",
            json!({"text": s.text.is_some(), "files": s.files.len(), "rejected": s.rejected.len()}),
        );
        let stale = self.inner().shares.receive(s);
        for id in stale {
            self.native.share_ack(&id);
        }
    }

    pub fn has_incoming_share(&self) -> bool {
        self.inner().shares.has_incoming()
    }

    /// 已进入应用时上传待处理的分享；返回要发给页面的 `share` 事件（没有待处理或已作废时 `None`）。
    pub async fn process_share(&self, conn: &Connection, lang: Lang) -> Option<Value> {
        let (intake, generation) = self.inner().shares.take_incoming()?;
        if !intake.files.is_empty() {
            self.native.toast(i18n::share_importing(lang));
        }
        let mut failed: Vec<ShareFailed> = intake
            .rejected
            .iter()
            .map(|r| ShareFailed {
                name: r.name.clone(),
                reason: r.reason.clone(),
            })
            .collect();
        let mut attachments = Vec::with_capacity(intake.files.len());
        for f in &intake.files {
            match self
                .api
                .upload_attachment(&conn.base, &conn.token, &f.name, Path::new(&f.path))
                .await
            {
                Ok(att) => attachments.push(att),
                Err(e) => {
                    self.log.log(
                        "share.upload_error",
                        json!({"name": f.name, "error": e.to_string()}),
                    );
                    failed.push(ShareFailed {
                        name: f.name.clone(),
                        reason: "upload".into(),
                    });
                }
            }
        }
        self.log.log(
            "share.uploaded",
            json!({"count": attachments.len(), "failed": failed.len()}),
        );
        let out = ShareOut {
            id: intake.id,
            text: intake.text,
            attachments,
            failed,
        };
        let event = out.event();
        let ready = self.inner().shares.set_ready(out, generation);
        match ready {
            Ok(()) => Some(event),
            Err(stale) => {
                self.native.share_ack(&stale);
                None
            }
        }
    }

    /// 尚未被页面确认的分享事件（`host_ready` 时重发）。
    pub fn ready_share_event(&self) -> Option<Value> {
        self.inner().shares.ready().map(ShareOut::event)
    }

    /// `host_share_ack`：确认后删掉原生侧的缓存目录。
    pub fn share_ack(&self, id: &str) {
        if self.inner().shares.ack(id) {
            self.native.share_ack(id);
            self.log.log("share.delivered", json!({"id": id}));
        }
    }
}

/// iOS（不带原生插件）：推送不可用，分享与 toast 为空操作。
#[cfg(not(target_os = "android"))]
pub struct Unsupported;

#[cfg(not(target_os = "android"))]
impl Native for Unsupported {
    fn push_state(&self) -> Result<PushState, String> {
        Err("unsupported".into())
    }
    async fn push_enable(&self, _vapid: &str) -> Result<PushState, String> {
        Err("unsupported".into())
    }
    fn push_disable(&self) -> Result<PushState, String> {
        Ok(PushState::default())
    }
    fn push_mark_synced(&self, _endpoint: &str, _id: &str, _server: &str) -> Result<(), String> {
        Err("unsupported".into())
    }
    fn share_ack(&self, _id: &str) {}
    fn toast(&self, _text: &str) {}
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{Health, PairPoll, PairStart};
    use crate::eventlog::testing::{memory, Buffer};
    use std::collections::VecDeque;
    use tauri_plugin_oryn_mobile::{Event, ShareFile, ShareRejected};

    const LAUNCHER: &str = "http://tauri.localhost";
    const SERVER: &str = "https://oryn.example.com";

    #[tokio::test]
    async fn webview_gate_holds_until_open() {
        let gate = Arc::new(WebviewGate::default());
        let waiter = {
            let g = gate.clone();
            tokio::spawn(async move { g.wait().await })
        };
        // 当前线程运行时：让出一次，等待方先跑到挂起点
        tokio::task::yield_now().await;
        assert!(!waiter.is_finished(), "未打开前不得放行");
        gate.open();
        tokio::time::timeout(std::time::Duration::from_secs(5), waiter)
            .await
            .expect("打开后放行")
            .unwrap();
        // 已打开：之后的等待立即返回；重复打开无害
        gate.open();
        tokio::time::timeout(std::time::Duration::from_secs(5), gate.wait())
            .await
            .expect("已打开的闸门立即放行");
    }

    /// Activity 被系统销毁（不是配置变更）：拦下退出、进程保留；用户回来时恰好为新 Activity 建一次主窗口。
    #[test]
    fn main_window_survives_activity_destroy() {
        assert!(
            MainWindowLife::keep_process(None),
            "随 Activity 销毁的退出要拦下"
        );
        assert!(!MainWindowLife::keep_process(Some(0)), "显式 exit 放行");

        // 先销毁、后来新 Activity：来时建
        let mut l = MainWindowLife::default();
        assert!(!l.destroyed());
        assert!(l.activity_created());
        assert!(!l.activity_created(), "已在建，不重复建");

        // 新 Activity 先到（旧窗口还没删完）：等 Destroyed 再建，且只建一次
        let mut l = MainWindowLife::default();
        assert!(!l.activity_created());
        assert!(l.destroyed());
        assert!(!l.activity_created());

        // 建失败：下一个 Activity 再试
        let mut l = MainWindowLife::default();
        l.destroyed();
        assert!(l.activity_created());
        l.create_failed();
        assert!(l.activity_created());
    }

    #[test]
    fn activity_event_parses() {
        let e: Event = serde_json::from_str(r#"{"type":"activity"}"#).unwrap();
        assert_eq!(e, Event::Activity);
    }

    #[test]
    fn nav_table() {
        use NavDecision::*;
        let s = Some(SERVER);
        for (url, server, want) in [
            ("http://tauri.localhost/launcher.html", s, Allow),
            ("https://oryn.example.com/t/x?y#z", s, Allow),
            ("https://oryn.example.com:443/", s, Allow),
            ("https://oryn.example.com:8443/", s, External),
            ("http://oryn.example.com/", s, External),
            ("https://evil.example/", s, External),
            ("https://github.com/Lynricsy/Oryn", s, External),
            (
                "oryn://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab",
                s,
                DeepLink,
            ),
            ("ORYN://open", s, DeepLink),
            ("about:blank", s, Allow),
            ("about:srcdoc", s, Block),
            ("about:blank#x", s, Block),
            // blob: 只认服务器自己的 origin（安全审查第 6 条）
            ("blob:https://oryn.example.com/0b1c", s, Allow),
            ("blob:https://t-abc-p3000.orb.example.com/0b1c", s, Block),
            ("blob:https://oryn.example.com:8443/0b1c", s, Block),
            ("blob:http://tauri.localhost/0b1c", s, Block),
            ("blob:null/0b1c", s, Block),
            ("blob:https://oryn.example.com/0b1c", None, Block),
            // data: 一律拦截
            ("data:text/html,hi", s, Block),
            ("data:text/html;base64,PGgxPk9yeW48L2gxPg==", s, Block),
            ("DATA:image/png;base64,AAAA", s, Block),
            ("mailto:a@example.com", s, External),
            ("tel:+100", s, External),
            ("intent://scan/#Intent;scheme=zxing;end", s, Block),
            ("file:///sdcard/x", s, Block),
            ("javascript:alert(1)", s, Block),
            ("not a url", s, Block),
            // 尚未配对：只放行连接页
            ("http://tauri.localhost/x", None, Allow),
            ("https://oryn.example.com/", None, External),
        ] {
            assert_eq!(nav_decision(url, LAUNCHER, server), want, "{url}");
        }
    }

    #[test]
    fn recreated_paths() {
        let s = Some(SERVER);
        let id = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab";
        assert_eq!(
            recreated_path(&format!("{SERVER}/t/{id}?tab=files#x"), s).as_deref(),
            Some(format!("/t/{id}?tab=files").as_str())
        );
        assert_eq!(
            recreated_path(&format!("{SERVER}/"), s).as_deref(),
            Some("/")
        );
        for (url, server) in [
            ("http://tauri.localhost/launcher.html", s),
            ("https://oryn.example.com/auth/app?code=oac_1&next=%2F", s),
            ("https://evil.example/t/x", s),
            ("https://oryn.example.com/t/x", None),
            ("about:blank", s),
            ("not a url", s),
        ] {
            assert_eq!(recreated_path(url, server), None, "{url}");
        }
    }

    #[test]
    fn external_and_color() {
        assert_eq!(
            external_url(" https://a.example/x ").as_deref(),
            Some("https://a.example/x")
        );
        assert_eq!(
            external_url("http://a.example").as_deref(),
            Some("http://a.example/")
        );
        for bad in [
            "javascript:alert(1)",
            "intent://x",
            "file:///etc",
            "oryn://open",
            "",
        ] {
            assert_eq!(external_url(bad), None, "{bad}");
        }
        assert!(valid_color("#eef0f4"));
        assert!(valid_color("#0A0B0C"));
        for bad in [
            "eef0f4",
            "#eef0f",
            "#eef0f40",
            "#ggg000",
            "red",
            "#eef0f4;x",
        ] {
            assert!(!valid_color(bad), "{bad}");
        }
    }

    const TAB_BAR_SET: &str = include_str!("../plugins/oryn-mobile/fixtures/tab-bar-set.json");

    /// 整数与浮点在 `Value` 里不相等（`12` ≠ `12.0`）：统一成浮点再比。
    fn norm(v: Value) -> Value {
        match v {
            Value::Number(n) => json!(n.as_f64()),
            Value::Array(a) => Value::Array(a.into_iter().map(norm).collect()),
            Value::Object(o) => Value::Object(o.into_iter().map(|(k, v)| (k, norm(v))).collect()),
            other => other,
        }
    }

    fn fixture_spec() -> TabBarSpec {
        serde_json::from_str(TAB_BAR_SET).unwrap()
    }

    /// 夹具 = TS、Rust、Swift 三方的共同契约：Rust 能原样读回、写出。
    #[test]
    fn tab_bar_fixtures_round_trip() {
        let raw: Value = serde_json::from_str(TAB_BAR_SET).unwrap();
        assert_eq!(
            norm(serde_json::to_value(fixture_spec()).unwrap()),
            norm(raw)
        );
    }

    fn tab_item(id: &str) -> tauri_plugin_oryn_mobile::TabBarItem {
        tauri_plugin_oryn_mobile::TabBarItem {
            id: id.into(),
            label: id.into(),
            icon: TabBarIcon::Symbol("folder".into()),
            badge: None,
        }
    }

    /// 在夹具上改一处后应被 `valid_tab_bar` 拒绝。
    fn rejects_spec(name: &str, mutate: impl FnOnce(&mut TabBarSpec)) {
        let mut s = fixture_spec();
        mutate(&mut s);
        assert!(!valid_tab_bar(&s), "{name}");
    }

    #[test]
    fn tab_bar_limits() {
        assert!(valid_tab_bar(&fixture_spec()));
        rejects_spec("7 项", |s| {
            s.items = (0..7).map(|i| tab_item(&format!("t{i}"))).collect()
        });
        rejects_spec("没有项", |s| s.items.clear());
        rejects_spec("重复 id", |s| s.items[1].id = "threads".into());
        rejects_spec("id 含大写", |s| s.items[0].id = "Threads".into());
        rejects_spec("label 65 字符", |s| s.items[0].label = "字".repeat(65));
        rejects_spec("label 为空", |s| s.items[0].label.clear());
        rejects_spec("symbol 含大写", |s| {
            s.items[0].icon = TabBarIcon::Symbol("Folder".into())
        });
        rejects_spec("image 131073 字节", |s| {
            s.items[0].icon = TabBarIcon::Image("A".repeat(131_073))
        });
        rejects_spec("image 非 base64", |s| {
            s.items[0].icon = TabBarIcon::Image("data:image/png".into())
        });
        rejects_spec("glyph 33 字节", |s| {
            s.items[0].icon = TabBarIcon::Glyph("a".repeat(33))
        });
        rejects_spec("selected 33 字节", |s| s.selected = "a".repeat(33));
        rejects_spec("frame 宽 0", |s| s.frame.width = 0.0);
        rejects_spec("frame 高 401", |s| s.frame.height = 401.0);
        rejects_spec("frame y 越界", |s| s.frame.y = 5000.5);
        rejects_spec("frame x 为 NaN", |s| s.frame.x = f64::NAN);
        rejects_spec("frame 宽为无穷", |s| s.frame.width = f64::INFINITY);
        rejects_spec("accent red", |s| s.accent = Some("red".into()));
        // 边界上的合法值
        let mut s = fixture_spec();
        s.items = (0..6).map(|i| tab_item(&format!("t{i}"))).collect();
        s.items[0].label = "字".repeat(64);
        s.items[1].icon = TabBarIcon::Image("A".repeat(131_072));
        s.items[2].icon = TabBarIcon::Glyph("🦊".into());
        s.accent = None;
        s.selected = String::new();
        assert!(valid_tab_bar(&s));
    }

    /// 旧网页（不发 host_sink_set）的非空 set 被拒；撤掉总是放行。
    #[test]
    fn native_chrome_requires_sink() {
        assert!(!native_chrome_allowed(false, true));
        assert!(native_chrome_allowed(true, true));
        assert!(native_chrome_allowed(false, false));
        assert!(native_chrome_allowed(true, false));
    }

    #[test]
    fn tab_bar_event() {
        assert_eq!(
            super::tab_bar_event("more"),
            json!({"type": "tabbar", "id": "more"})
        );
        assert!(valid_tab_id("more"));
        assert!(valid_tab_id("a_b-9"));
        for bad in ["", "More", "a b", "x.y", &"a".repeat(33)] {
            assert!(!valid_tab_id(bad), "{bad}");
        }
    }

    const HEADER_SET: &str = include_str!("../plugins/oryn-mobile/fixtures/header-set.json");
    const HEADER_SHOW: &str = include_str!("../plugins/oryn-mobile/fixtures/header-show.json");
    const HEADER_MENU: &str = include_str!("../plugins/oryn-mobile/fixtures/header-menu.json");
    const HEADER_LEAD: &str = include_str!("../plugins/oryn-mobile/fixtures/header-lead.json");

    fn fixture_header() -> HeaderSpec {
        serde_json::from_str(HEADER_SET).unwrap()
    }

    fn fixture_show() -> HeaderShow {
        serde_json::from_str(HEADER_SHOW).unwrap()
    }

    fn header_item(id: &str) -> HeaderItem {
        serde_json::from_value(json!({"id": id, "label": id})).unwrap()
    }

    /// 页头的四个夹具同样是 TS、Rust、Swift 三方的共同契约。
    #[test]
    fn header_fixtures_round_trip() {
        for (raw, back) in [
            (HEADER_SET, serde_json::to_value(fixture_header()).unwrap()),
            (HEADER_SHOW, serde_json::to_value(fixture_show()).unwrap()),
            (
                HEADER_LEAD,
                serde_json::to_value(serde_json::from_str::<HeaderSpec>(HEADER_LEAD).unwrap())
                    .unwrap(),
            ),
            (
                HEADER_MENU,
                serde_json::to_value(serde_json::from_str::<HeaderMenuCmd>(HEADER_MENU).unwrap())
                    .unwrap(),
            ),
        ] {
            assert_eq!(norm(back), norm(serde_json::from_str(raw).unwrap()));
        }
        assert!(valid_header(&fixture_header()));
        assert!(valid_header(&serde_json::from_str(HEADER_LEAD).unwrap()));
        assert!(valid_header_show(&fixture_show()));
        assert!(valid_header_menu(
            &serde_json::from_str(HEADER_MENU).unwrap()
        ));
    }

    /// 在夹具上改一处后应被 `valid_header` 拒绝。
    fn rejects_header(name: &str, mutate: impl FnOnce(&mut HeaderSpec)) {
        let mut s = fixture_header();
        mutate(&mut s);
        assert!(!valid_header(&s), "{name}");
    }

    /// 在夹具的标题胶囊上改一处后应被 `valid_header` 拒绝。
    fn rejects_title(name: &str, mutate: impl FnOnce(&mut tauri_plugin_oryn_mobile::HeaderTitle)) {
        rejects_header(name, |s| mutate(s.title.as_mut().unwrap()));
    }

    #[test]
    fn header_limits() {
        rejects_header("key 含空格", |s| s.key = "T 1".into());
        rejects_header("key 97 字节", |s| s.key = "a".repeat(97));
        rejects_header("key 为空", |s| s.key.clear());
        rejects_header("palette.err red", |s| s.palette.err = "red".into());
        rejects_header("tune.scrim 1.5", |s| s.tune.scrim = 1.5);
        rejects_header("tune.tint NaN", |s| s.tune.tint = f64::NAN);
        rejects_header("text.back 为空", |s| s.text.back.clear());
        rejects_header("lead.symbol 含大写", |s| {
            s.lead.symbol = "Sidebar.left".into()
        });
        rejects_header("lead.label 65 字符", |s| s.lead.label = "字".repeat(65));
        rejects_title("title.text 为空", |t| t.text.clear());
        rejects_title("title.text 301 字符", |t| t.text = "字".repeat(301));
        rejects_title("title.label 601 字符", |t| t.label = "字".repeat(601));
        rejects_title("title.error 为空", |t| t.error = Some(String::new()));
        rejects_title("meta 7 段", |t| {
            let seg = t.meta[0].clone();
            t.meta = vec![seg; 7];
        });
        rejects_title("meta 文字 121 字符", |t| {
            t.meta[0].text = "字".repeat(121)
        });
        rejects_title("marks 7 个", |t| {
            let m = t.marks[0].clone();
            t.marks = vec![m; 7];
        });
        rejects_title("mark symbol 含空格", |t| {
            t.marks[0].symbol = "a b".into()
        });
        rejects_title("图标同时有 symbol 与 dot", |t| {
            t.menu.quick[0].icon = Some(HeaderIcon {
                symbol: Some("pin".into()),
                dot: Some("#123456".into()),
                ..Default::default()
            })
        });
        rejects_title("图标一项都没有", |t| {
            t.menu.quick[0].icon = Some(HeaderIcon::default())
        });
        rejects_title("bg 配在 symbol 上", |t| {
            t.menu.quick[0].icon = Some(HeaderIcon {
                symbol: Some("pin".into()),
                bg: Some("#123456".into()),
                ..Default::default()
            })
        });
        rejects_title("glyph 的 fg 不是颜色", |t| {
            t.icon.as_mut().unwrap().fg = Some("black".into())
        });
        rejects_title("glyph 33 字节", |t| {
            t.icon.as_mut().unwrap().glyph = Some("a".repeat(33))
        });
        let mask = |m: &str| HeaderIcon {
            mask: Some(m.into()),
            bg: Some("#123456".into()),
            fg: Some("#abcdef".into()),
            ..Default::default()
        };
        rejects_title("mask 同时有 glyph", |t| {
            let mut i = mask("iVBORw0KGgo=");
            i.glyph = Some("x".into());
            t.icon = Some(i)
        });
        rejects_title("mask 非 base64", |t| {
            t.icon = Some(mask("data:image/png;base64,AAAA"))
        });
        rejects_title("mask 16385 字节", |t| {
            t.icon = Some(mask(&"A".repeat(16_385)))
        });
        rejects_title("mask 为空", |t| t.icon = Some(mask("")));
        rejects_title("dot 不是颜色", |t| {
            t.menu.quick[0].icon = Some(HeaderIcon {
                dot: Some("red".into()),
                ..Default::default()
            })
        });
        rejects_title("quick 5 项", |t| {
            t.menu.quick = (0..5).map(|i| header_item(&format!("q{i}"))).collect()
        });
        rejects_title("status 33 项", |t| {
            t.menu.status = (0..33).map(|i| header_item(&format!("s{i}"))).collect()
        });
        rejects_title("9 组", |t| {
            t.menu.groups = (0..9)
                .map(|i| vec![header_item(&format!("g{i}"))])
                .collect()
        });
        rejects_title("某组为空", |t| t.menu.groups.push(vec![]));
        rejects_title("某组 25 项", |t| {
            t.menu.groups[0] = (0..25).map(|i| header_item(&format!("g{i}"))).collect()
        });
        rejects_title("全菜单 161 项", |t| {
            let m = &mut t.menu;
            m.quick.clear();
            m.status.clear();
            m.groups = (0..7)
                .map(|g| (0..23).map(|i| header_item(&format!("g{g}-{i}"))).collect())
                .collect();
        });
        rejects_title("子项 25 个", |t| {
            t.menu.status[0].children = (0..25).map(|i| header_item(&format!("env/{i}"))).collect()
        });
        rejects_title("子项再带 children", |t| {
            t.menu.status[0].children[0].children = vec![header_item("env/info/x")]
        });
        rejects_title("两项同 id", |t| {
            t.menu.groups[0][1].children.push(header_item("env"))
        });
        rejects_title("项 id 含空格", |t| t.menu.quick[0].id = "a b".into());
        rejects_title("项 id 97 字节", |t| t.menu.quick[0].id = "a".repeat(97));
        rejects_title("item.label 201 字符", |t| {
            t.menu.quick[0].label = "字".repeat(201)
        });
        rejects_title("item.label 为空", |t| t.menu.quick[0].label.clear());
        rejects_title("hint 33 字符", |t| {
            t.menu.groups[1][0].hint = Some("字".repeat(33))
        });
        rejects_title("说明行为空", |t| {
            t.menu.status[1].sub.as_mut().unwrap().text.clear()
        });
        rejects_title("copy 2049 字节", |t| {
            t.menu.groups[1][1].copy = Some("a".repeat(2049))
        });
        rejects_title("菜单 label 为空", |t| t.menu.label.clear());
        rejects_header("trail 菜单 label 为空", |s| {
            s.trail.as_mut().unwrap().menu.label.clear()
        });
        rejects_header("trail.frame 宽 0", |s| {
            s.trail.as_mut().unwrap().frame.width = 0.0
        });
        rejects_header("trail.symbol 为空", |s| {
            s.trail.as_mut().unwrap().symbol.clear()
        });
        rejects_title("title.frame x 为 NaN", |t| t.frame.x = f64::NAN);
        rejects_header("lead.frame 高 401", |s| s.lead.frame.height = 401.0);
        // 边界上的合法值
        let mut s = fixture_header();
        s.trail = None;
        let t = s.title.as_mut().unwrap();
        t.error = Some("x".into());
        t.menu.status = (0..32).map(|i| header_item(&format!("s{i}"))).collect();
        t.menu.groups = vec![(0..24).map(|i| header_item(&format!("g{i}"))).collect()];
        t.menu.groups[0][0].label = "字".repeat(200);
        t.menu.groups[0][1].hint = Some("字".repeat(32));
        t.menu.groups[0][2].copy = Some("a".repeat(2048));
        t.menu.groups[0][3].children = (0..24).map(|i| header_item(&format!("g3/{i}"))).collect();
        t.text = "字".repeat(300);
        t.icon = Some(mask(&"A".repeat(16_384)));
        s.key = "a".repeat(96);
        assert!(valid_header(&s));
        // 菜单项 id 允许 `/` 与 `#`，线程 id 不允许
        assert!(valid_item_id("pr-#12/open"));
        assert!(!valid_header_key("T/1"));
    }

    /// 只有侧栏钮（首页）：title / trail 都为 null 也合法，lead 与公共字段照样校验；带视图钮也行。
    #[test]
    fn header_lead_only() {
        let lead: HeaderSpec = serde_json::from_str(HEADER_LEAD).unwrap();
        assert_eq!((lead.title.is_none(), lead.trail.is_none()), (true, true));
        assert!(valid_header(&lead));
        let rejects = |name: &str, mutate: fn(&mut HeaderSpec)| {
            let mut s = lead.clone();
            mutate(&mut s);
            assert!(!valid_header(&s), "{name}");
        };
        rejects("key 为空", |s| s.key.clear());
        rejects("lead.label 为空", |s| s.lead.label.clear());
        rejects("lead.frame 宽 0", |s| s.lead.frame.width = 0.0);
        rejects("palette.accent red", |s| s.palette.accent = "red".into());
        // 夹具线程页的标题胶囊去掉：仍合法（视图钮照样校验）
        let mut s = fixture_header();
        s.title = None;
        assert!(valid_header(&s));
        s.trail.as_mut().unwrap().symbol.clear();
        assert!(!valid_header(&s));
    }

    #[test]
    fn header_show_limits() {
        assert!(valid_header_show(&HeaderShow { alpha: 0.0, ms: 0 }));
        assert!(valid_header_show(&HeaderShow {
            alpha: 1.0,
            ms: 2000,
        }));
        for (name, bad) in [
            ("alpha 1.1", HeaderShow { alpha: 1.1, ms: 0 }),
            ("alpha 为负", HeaderShow { alpha: -0.1, ms: 0 }),
            (
                "alpha 为 NaN",
                HeaderShow {
                    alpha: f64::NAN,
                    ms: 0,
                },
            ),
            (
                "ms 2001",
                HeaderShow {
                    alpha: 1.0,
                    ms: 2001,
                },
            ),
        ] {
            assert!(!valid_header_show(&bad), "{name}");
        }
    }

    #[test]
    fn header_menu_limits() {
        assert!(valid_header_menu(&HeaderMenuCmd {
            open: None,
            page: None,
        }));
        assert!(valid_header_menu(&HeaderMenuCmd {
            open: Some(HeaderMenuKind::Title),
            page: Some("env".into()),
        }));
        assert!(!valid_header_menu(&HeaderMenuCmd {
            open: Some(HeaderMenuKind::Title),
            page: Some("a b".into()),
        }));
        assert!(!valid_header_menu(&HeaderMenuCmd {
            open: Some(HeaderMenuKind::Title),
            page: Some(String::new()),
        }));
    }

    #[test]
    fn header_events() {
        use HeaderMenuKind::{Title, Trail};
        assert_eq!(
            header_event(HeaderOp::Lead, "T-1", None, None),
            Some(json!({"type": "header", "op": "lead", "key": "T-1"}))
        );
        assert_eq!(
            header_event(HeaderOp::Menu, "T-1", None, None),
            Some(json!({"type": "header", "op": "menu", "key": "T-1", "menu": null}))
        );
        assert_eq!(
            header_event(HeaderOp::Menu, "T-1", Some(Trail), None),
            Some(json!({"type": "header", "op": "menu", "key": "T-1", "menu": "trail"}))
        );
        assert_eq!(
            header_event(HeaderOp::Select, "T-1", Some(Title), Some("env/freeze")),
            Some(json!({
                "type": "header", "op": "select", "key": "T-1", "menu": "title", "id": "env/freeze"
            }))
        );
        for (name, e) in [
            (
                "select 没有 menu",
                header_event(HeaderOp::Select, "T-1", None, Some("x")),
            ),
            (
                "select 没有 id",
                header_event(HeaderOp::Select, "T-1", Some(Title), None),
            ),
            (
                "select 的 id 含空格",
                header_event(HeaderOp::Select, "T-1", Some(Title), Some("a b")),
            ),
            (
                "key 含空格",
                header_event(HeaderOp::Lead, "bad key", None, None),
            ),
            (
                "lead 带 menu",
                header_event(HeaderOp::Lead, "T-1", Some(Title), None),
            ),
            (
                "menu 带 id",
                header_event(HeaderOp::Menu, "T-1", Some(Title), Some("x")),
            ),
        ] {
            assert_eq!(e, None, "{name}");
        }
    }

    /// 演示用 spec：393pt 宽、顶部安全区 59pt 时正好是夹具（三个 frame 就是按这块屏幕写的）。
    #[test]
    fn header_demo_spec() {
        let spec = demo_header(393.0, 59.0);
        assert_eq!(spec, fixture_header());
        assert!(valid_header(&spec));
        let spec = demo_header(320.0, 20.0);
        assert_eq!(
            spec.title.unwrap().frame,
            TabBarFrame {
                x: 64.0,
                y: 28.0,
                width: 192.0,
                height: 46.0,
            }
        );
        assert_eq!(spec.lead.frame.y, 29.0);
        assert_eq!(spec.trail.unwrap().frame.x, 264.0);
    }

    fn up_state() -> PushState {
        PushState {
            permission: Permission::Granted,
            enabled: true,
            distributor: Some("io.heckel.ntfy".into()),
            channel: Some(PushChannel::UnifiedPush),
            endpoint: Some("http://localhost:7680/upAbc?up=1".into()),
            p256dh: Some("pk".into()),
            auth: Some("au".into()),
            ..PushState::default()
        }
    }

    #[test]
    fn push_view_states() {
        use PushViewState::*;
        let a = Platform::Android;
        let base = up_state();
        assert_eq!(
            push_view(Platform::Ios, Some(&base), None).state,
            Unsupported
        );
        assert_eq!(push_view(a, None, None).state, Disabled);
        let off = PushState {
            enabled: false,
            ..base.clone()
        };
        assert_eq!(push_view(a, Some(&off), None).state, Disabled);
        let denied = PushState {
            permission: Permission::Denied,
            ..base.clone()
        };
        assert_eq!(push_view(a, Some(&denied), None).state, PermissionDenied);
        let none = PushState {
            distributor: None,
            channel: None,
            endpoint: None,
            ..base.clone()
        };
        assert_eq!(push_view(a, Some(&none), None).state, NoDistributor);
        let failed = PushState {
            error: Some("ACTION_NOT_ALLOWED".into()),
            ..base.clone()
        };
        let v = push_view(a, Some(&failed), None);
        assert_eq!(
            (v.state, v.error.as_deref()),
            (Error, Some("ACTION_NOT_ALLOWED"))
        );
        let v = push_view(a, Some(&base), Some("fcm_unavailable"));
        assert_eq!(
            (v.state, v.error.as_deref()),
            (Error, Some("fcm_unavailable"))
        );
        assert_eq!(push_view(a, Some(&base), None).state, Registering);
        let synced = PushState {
            synced_endpoint: base.endpoint.clone(),
            subscription_id: Some("ps_1".into()),
            ..base.clone()
        };
        let v = push_view(a, Some(&synced), None);
        assert_eq!(v.state, Active);
        assert_eq!(v.endpoint_host.as_deref(), Some("localhost"));
        assert_eq!(v.distributor.as_deref(), Some("io.heckel.ntfy"));
        assert_eq!(
            serde_json::to_value(&v).unwrap(),
            json!({"state": "active", "channel": "unifiedpush", "distributor": "io.heckel.ntfy", "endpointHost": "localhost"})
        );

        // FCM：没有分发器也不算 no_distributor；以令牌判断是否已同步
        let fcm = PushState {
            distributor: None,
            channel: Some(PushChannel::Fcm),
            endpoint: None,
            p256dh: None,
            auth: None,
            fcm_token: Some("fcm_tok".into()),
            ..base.clone()
        };
        assert_eq!(push_view(a, Some(&fcm), None).state, Registering);
        let fcm_synced = PushState {
            synced_endpoint: Some("fcm_tok".into()),
            ..fcm.clone()
        };
        let v = push_view(a, Some(&fcm_synced), None);
        assert_eq!(v.state, Active);
        assert_eq!(v.endpoint_host, None);
        assert_eq!(
            serde_json::to_value(&v).unwrap(),
            json!({"state": "active", "channel": "fcm"})
        );
        assert_eq!(
            serde_json::to_value(push_view(a, None, None)).unwrap(),
            json!({"state": "disabled"})
        );
        assert_eq!(
            serde_json::to_value(push_view(a, Some(&denied), None)).unwrap()["state"],
            "permission_denied"
        );
    }

    #[test]
    fn sync_targets() {
        let s = up_state();
        assert_eq!(
            sync_target(&s, false),
            Some(PushSubscription::WebPush {
                endpoint: "http://localhost:7680/upAbc?up=1".into(),
                p256dh: "pk".into(),
                auth: "au".into()
            })
        );
        let synced = PushState {
            synced_endpoint: s.endpoint.clone(),
            ..s.clone()
        };
        assert_eq!(sync_target(&synced, false), None);
        // 强制：服务器可能已删掉订阅，照样登记
        assert!(sync_target(&synced, true).is_some());
        let no_keys = PushState {
            auth: None,
            ..s.clone()
        };
        assert_eq!(sync_target(&no_keys, true), None);
        let off = PushState {
            enabled: false,
            ..s.clone()
        };
        assert_eq!(sync_target(&off, true), None);
        let fcm = PushState {
            channel: Some(PushChannel::Fcm),
            endpoint: None,
            fcm_token: Some("fcm_tok".into()),
            p256dh: Some("fpk".into()),
            auth: Some("fau".into()),
            synced_endpoint: s.endpoint.clone(),
            ..s.clone()
        };
        assert_eq!(
            sync_target(&fcm, false),
            Some(PushSubscription::Fcm {
                token: "fcm_tok".into(),
                p256dh: "fpk".into(),
                auth: "fau".into()
            })
        );
        // FCM 没有密钥（M7 之前的状态）：不登记明文订阅
        let fcm_no_keys = PushState {
            p256dh: None,
            ..fcm.clone()
        };
        assert_eq!(sync_target(&fcm_no_keys, true), None);
    }

    #[test]
    fn native_shapes_parse() {
        let e: Event = serde_json::from_value(json!({
            "type": "push",
            "state": {"permission": "granted", "enabled": true, "distributor": "io.heckel.ntfy",
                      "channel": "unifiedpush", "endpoint": "http://localhost:7680/upX",
                      "p256dh": "k", "auth": "a", "syncedEndpoint": null, "subscriptionId": null, "error": null}
        }))
        .unwrap();
        let Event::Push { state } = e else { panic!() };
        assert_eq!(state.channel, Some(PushChannel::UnifiedPush));
        assert_eq!(state.fcm_token, None);
        let e: Event = serde_json::from_value(json!({
            "type": "share",
            "share": {"id": "1-1", "text": null, "files": [{"path": "/c/s/1/0-a.png", "name": "a.png", "mime": "image/png", "size": 3}],
                      "rejected": [{"name": "big.bin", "reason": "too_large"}]}
        }))
        .unwrap();
        let Event::Share { share } = e else { panic!() };
        assert_eq!(share.files[0].name, "a.png");
        assert_eq!(share.rejected[0].reason, "too_large");
        let e: Event = serde_json::from_value(json!({
            "type": "insets", "cornerRadii": {"tl": 40.5, "tr": 40.5, "bl": 40.5, "br": 40.5},
            "insets": {"top": 24, "right": 0, "bottom": 48, "left": 0}
        }))
        .unwrap();
        let Event::Insets {
            corner_radii,
            insets,
        } = e
        else {
            panic!()
        };
        assert_eq!(corner_radii.tl, 40.5);
        assert_eq!(insets.bottom, 48.0);
        assert_eq!(
            insets_event(corner_radii, insets),
            json!({"type": "insets", "cornerRadii": {"tl": 40.5, "tr": 40.5, "bl": 40.5, "br": 40.5},
                   "insets": {"top": 24.0, "right": 0.0, "bottom": 48.0, "left": 0.0}})
        );
        assert_eq!(
            serde_json::from_value::<Event>(json!({"type": "stop"})).unwrap(),
            Event::Stop
        );
        assert!(serde_json::from_value::<Event>(json!({"type": "nope"})).is_err());
        let s: PushState = serde_json::from_value(json!({
            "permission": "granted", "enabled": true, "distributor": null, "channel": "fcm",
            "fcmToken": "tok", "syncedEndpoint": "tok", "subscriptionId": "ps_1"
        }))
        .unwrap();
        assert_eq!(s.channel, Some(PushChannel::Fcm));
        let d: tauri_plugin_oryn_mobile::DeviceInfo = serde_json::from_value(json!({
            "name": "Google Pixel 7", "sdkInt": 35, "webviewVersion": "144.0", "debuggable": true
        }))
        .unwrap();
        assert_eq!(d.corner_radii, CornerRadii::default());
        assert_eq!(d.event_log_path, None);
    }

    // ------------------------------------------------------------ 假原生 / 假网络

    #[derive(Default)]
    struct FakeNative {
        state: Mutex<PushState>,
        acked: Mutex<Vec<String>>,
        toasts: Mutex<Vec<String>>,
        enabled_with: Mutex<Option<String>>,
    }

    impl Native for Arc<FakeNative> {
        fn push_state(&self) -> Result<PushState, String> {
            Ok(self.state.lock().unwrap().clone())
        }
        async fn push_enable(&self, vapid: &str) -> Result<PushState, String> {
            *self.enabled_with.lock().unwrap() = Some(vapid.into());
            let mut s = self.state.lock().unwrap();
            *s = up_state();
            Ok(s.clone())
        }
        fn push_disable(&self) -> Result<PushState, String> {
            let mut s = self.state.lock().unwrap();
            *s = PushState::default();
            Ok(s.clone())
        }
        fn push_mark_synced(&self, endpoint: &str, id: &str, server: &str) -> Result<(), String> {
            assert_eq!(server, SERVER);
            let mut s = self.state.lock().unwrap();
            s.synced_endpoint = Some(endpoint.into());
            s.subscription_id = Some(id.into());
            Ok(())
        }
        fn share_ack(&self, id: &str) {
            self.acked.lock().unwrap().push(id.into());
        }
        fn toast(&self, text: &str) {
            self.toasts.lock().unwrap().push(text.into());
        }
    }

    #[derive(Default)]
    struct FakeApi {
        subscribe: Mutex<VecDeque<Result<String, ApiError>>>,
        subscribed: Mutex<Vec<(String, PushSubscription)>>,
        unsubscribed: Mutex<Vec<String>>,
        uploads: Mutex<Vec<(String, String, Vec<u8>)>>,
        /// 这些文件名的上传失败。
        fail_names: Vec<&'static str>,
    }

    impl Api for FakeApi {
        async fn healthz(&self, _: &str) -> Result<Health, ApiError> {
            unreachable!()
        }
        async fn pair_start(&self, _: &str, _: &str, _: &str) -> Result<PairStart, ApiError> {
            unreachable!()
        }
        async fn pair_poll(&self, _: &str, _: &str, _: &str) -> Result<PairPoll, ApiError> {
            unreachable!()
        }
        async fn app_session(&self, _: &str, _: &str) -> Result<String, ApiError> {
            unreachable!()
        }
        async fn revoke_self(&self, _: &str, _: &str, _: &str) -> Result<(), ApiError> {
            unreachable!()
        }
        async fn vapid_key(&self, _: &str, token: &str) -> Result<String, ApiError> {
            assert_eq!(token, "odt_secret");
            Ok("BVapid".into())
        }
        async fn push_subscribe(
            &self,
            _: &str,
            token: &str,
            sub: &PushSubscription,
        ) -> Result<String, ApiError> {
            self.subscribed
                .lock()
                .unwrap()
                .push((token.into(), sub.clone()));
            self.subscribe
                .lock()
                .unwrap()
                .pop_front()
                .unwrap_or(Ok("ps_1".into()))
        }
        async fn push_unsubscribe(&self, _: &str, _: &str, id: &str) -> Result<(), ApiError> {
            self.unsubscribed.lock().unwrap().push(id.into());
            Ok(())
        }
        async fn upload_attachment(
            &self,
            _: &str,
            token: &str,
            name: &str,
            path: &Path,
        ) -> Result<Value, ApiError> {
            if self.fail_names.contains(&name) {
                return Err(ApiError::Status {
                    status: 413,
                    code: "too_large".into(),
                    message: String::new(),
                });
            }
            let bytes = std::fs::read(path).map_err(|e| ApiError::Network(e.to_string()))?;
            let n = self.uploads.lock().unwrap().len();
            self.uploads
                .lock()
                .unwrap()
                .push((token.into(), name.into(), bytes.clone()));
            Ok(json!({"id": format!("att_{n}"), "name": name, "size": bytes.len()}))
        }
    }

    struct Rig {
        core: MobileCore<Arc<FakeNative>, FakeApi>,
        native: Arc<FakeNative>,
        api: Arc<FakeApi>,
        log: Buffer,
    }

    fn rig(api: FakeApi) -> Rig {
        let native = Arc::new(FakeNative::default());
        let api = Arc::new(api);
        let (log, buf) = memory();
        Rig {
            core: MobileCore::new(native.clone(), api.clone(), log, Platform::Android),
            native,
            api,
            log: buf,
        }
    }

    impl Drop for Rig {
        fn drop(&mut self) {
            let text = self.log.text();
            assert!(!text.contains("odt_"), "event log leaked a token: {text}");
            assert!(!text.contains("\"pk\""), "event log leaked p256dh: {text}");
            assert!(!text.contains("\"au\""), "event log leaked auth: {text}");
        }
    }

    fn conn() -> Connection {
        Connection {
            base: SERVER.into(),
            token: "odt_secret".into(),
        }
    }

    fn events(b: &Buffer) -> Vec<String> {
        b.events()
            .iter()
            .map(|e| e["event"].as_str().unwrap().to_string())
            .collect()
    }

    #[tokio::test]
    async fn enable_syncs_once_and_disable_unsubscribes() {
        let r = rig(FakeApi::default());
        // 未进入应用：不能启用，也不同步
        assert_eq!(r.core.push_enable(None).await, Err("not_connected".into()));
        assert!(!r.core.sync_push(None, true).await);
        r.core.push_enable(Some(&conn())).await.unwrap();
        assert_eq!(
            r.native.enabled_with.lock().unwrap().as_deref(),
            Some("BVapid")
        );
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        assert_eq!(r.core.subscription_id().as_deref(), Some("ps_1"));
        // 已同步：再触发不重复登记
        assert!(!r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.api.subscribed.lock().unwrap().len(), 1);
        assert_eq!(r.api.subscribed.lock().unwrap()[0].0, "odt_secret");
        // 进入应用时强制登记：照样 POST（服务器可能已删掉订阅），id 没变则页面不必重取
        assert!(!r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.api.subscribed.lock().unwrap().len(), 2);
        // 服务器删掉过订阅、重建出新 id：页面要重取
        r.api.subscribe.lock().unwrap().push_back(Ok("ps_2".into()));
        assert!(r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.core.subscription_id().as_deref(), Some("ps_2"));
        // 分发器换了端点：重新登记
        r.native.state.lock().unwrap().endpoint = Some("http://localhost:7680/upNew".into());
        assert_eq!(r.core.push_view().state, PushViewState::Registering);
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.api.subscribed.lock().unwrap().len(), 4);

        r.core.push_disable(Some(&conn())).await;
        assert_eq!(r.api.unsubscribed.lock().unwrap().as_slice(), ["ps_1"]);
        assert_eq!(r.core.push_view().state, PushViewState::Disabled);
        let ev = r.log.events();
        assert_eq!(ev[0]["event"], "push.synced");
        assert_eq!(ev[0]["kind"], "webpush");
    }

    #[tokio::test]
    async fn rejected_subscription_reports_error_network_does_not() {
        let r = rig(FakeApi::default());
        *r.native.state.lock().unwrap() = PushState {
            channel: Some(PushChannel::Fcm),
            distributor: None,
            endpoint: None,
            fcm_token: Some("fcm_tok".into()),
            ..up_state()
        };
        r.api
            .subscribe
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("offline".into())));
        assert!(!r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.core.push_view().state, PushViewState::Registering);
        r.api
            .subscribe
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 409,
                code: "fcm_unavailable".into(),
                message: String::new(),
            }));
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let v = r.core.push_view();
        assert_eq!(
            (v.state, v.error.as_deref()),
            (PushViewState::Error, Some("fcm_unavailable"))
        );
        // 下次成功即清掉错误
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        assert_eq!(
            r.native.state.lock().unwrap().synced_endpoint.as_deref(),
            Some("fcm_tok")
        );
        assert_eq!(
            r.api.subscribed.lock().unwrap().last().unwrap().1,
            PushSubscription::Fcm {
                token: "fcm_tok".into(),
                p256dh: "pk".into(),
                auth: "au".into()
            }
        );
        assert_eq!(events(&r.log), ["push.sync_error", "push.synced"]);
        assert_eq!(r.log.events()[1]["kind"], "fcm");
    }

    fn intake(dir: &Path, id: &str, names: &[&str]) -> ShareIntake {
        let files = names
            .iter()
            .enumerate()
            .map(|(i, name)| {
                let path = dir.join(format!("{i}-{name}"));
                std::fs::write(&path, name.as_bytes()).unwrap();
                ShareFile {
                    path: path.to_string_lossy().into_owned(),
                    name: (*name).into(),
                    mime: "application/octet-stream".into(),
                    size: name.len() as u64,
                }
            })
            .collect();
        ShareIntake {
            id: id.into(),
            text: Some("m7 share".into()),
            files,
            rejected: vec![ShareRejected {
                name: "huge.bin".into(),
                reason: "too_large".into(),
            }],
        }
    }

    #[tokio::test]
    async fn share_waits_uploads_delivers_and_acks() {
        let dir = tempfile::tempdir().unwrap();
        let r = rig(FakeApi {
            fail_names: vec!["bad.png"],
            ..FakeApi::default()
        });
        // 未进入应用：先存着
        r.core
            .share_received(intake(dir.path(), "s1", &["m7.png", "bad.png"]));
        assert!(r.core.has_incoming_share());
        assert_eq!(r.core.ready_share_event(), None);
        assert!(r.api.uploads.lock().unwrap().is_empty());

        // 进入应用后上传并下发
        let event = r.core.process_share(&conn(), Lang::En).await.unwrap();
        assert_eq!(
            event,
            json!({"type": "share", "share": {
                "id": "s1", "text": "m7 share",
                "attachments": [{"id": "att_0", "name": "m7.png", "size": 6}],
                "failed": [{"name": "huge.bin", "reason": "too_large"}, {"name": "bad.png", "reason": "upload"}]
            }})
        );
        let uploads = r.api.uploads.lock().unwrap().clone();
        assert_eq!(
            uploads,
            [(
                "odt_secret".to_string(),
                "m7.png".to_string(),
                b"m7.png".to_vec()
            )]
        );
        assert_eq!(
            r.native.toasts.lock().unwrap().as_slice(),
            ["Importing into Oryn…"]
        );
        // 页面确认前，每次 host_ready 重发同一事件
        assert_eq!(r.core.ready_share_event(), Some(event));
        assert_eq!(r.core.process_share(&conn(), Lang::En).await, None);

        // 页面给的未知 id 不删任何东西
        r.core.share_ack("../../shared_prefs");
        assert!(r.native.acked.lock().unwrap().is_empty());
        r.core.share_ack("s1");
        assert_eq!(r.native.acked.lock().unwrap().as_slice(), ["s1"]);
        assert_eq!(r.core.ready_share_event(), None);
        assert_eq!(
            events(&r.log),
            [
                "share.received",
                "share.upload_error",
                "share.uploaded",
                "share.delivered"
            ]
        );
        let ev = r.log.events();
        assert_eq!(ev[0]["text"], true);
        assert_eq!(ev[0]["files"], 2);
        assert_eq!(ev[0]["rejected"], 1);
        assert_eq!(ev[2]["count"], 1);
        assert_eq!(ev[2]["failed"], 2);
    }

    #[tokio::test]
    async fn newer_share_replaces_older() {
        let dir = tempfile::tempdir().unwrap();
        let r = rig(FakeApi::default());
        r.core.share_received(intake(dir.path(), "s1", &[]));
        r.core.share_received(intake(dir.path(), "s2", &[]));
        assert_eq!(r.native.acked.lock().unwrap().as_slice(), ["s1"]);
        let ev = r.core.process_share(&conn(), Lang::ZhCn).await.unwrap();
        assert_eq!(ev["share"]["id"], "s2");
        // 只有文字：不弹「正在导入」
        assert!(r.native.toasts.lock().unwrap().is_empty());
        // 待确认时又来一次：旧的作废
        r.core.share_received(intake(dir.path(), "s3", &[]));
        assert_eq!(r.native.acked.lock().unwrap().as_slice(), ["s1", "s2"]);
        assert_eq!(r.core.ready_share_event(), None);

        // 上传期间又来新的：上传完的那份作废，不下发
        let out = |id: &str| ShareOut {
            id: id.into(),
            text: None,
            attachments: vec![],
            failed: vec![],
        };
        let mut shares = Shares::default();
        shares.receive(intake(dir.path(), "a", &[]));
        let (_, ga) = shares.take_incoming().unwrap();
        shares.receive(intake(dir.path(), "b", &[]));
        assert_eq!(shares.set_ready(out("a"), ga), Err("a".into()));
        assert!(shares.has_incoming());

        // 代码审查第 4 条：新分享也已被取走上传（incoming 又空了），旧的大文件后完成也必须作废
        let mut shares = Shares::default();
        shares.receive(intake(dir.path(), "a", &[]));
        let (_, ga) = shares.take_incoming().unwrap();
        shares.receive(intake(dir.path(), "b", &[]));
        let (_, gb) = shares.take_incoming().unwrap();
        assert!(!shares.has_incoming());
        // b 尚在上传时 a 先完成
        assert_eq!(shares.set_ready(out("a"), ga), Err("a".into()));
        assert!(shares.ready().is_none());
        // b 完成并被页面确认之后 a 才完成
        assert_eq!(shares.set_ready(out("b"), gb), Ok(()));
        assert!(shares.ack("b"));
        assert_eq!(shares.set_ready(out("a"), ga), Err("a".into()));
        assert!(shares.ready().is_none());
    }

    #[test]
    fn info_shape() {
        let r = rig(FakeApi::default());
        let radii = CornerRadii {
            tl: 32.0,
            tr: 32.0,
            bl: 32.0,
            br: 32.0,
        };
        let insets = Insets {
            top: 24.0,
            right: 0.0,
            bottom: 16.0,
            left: 0.0,
        };
        assert!(r.core.set_insets(radii, insets));
        assert!(!r.core.set_insets(radii, insets));
        let info = r
            .core
            .info("0.1.0", TokenStoreKind::Keystore, "144.0.7559.59");
        assert_eq!(
            serde_json::to_value(&info).unwrap(),
            json!({
                "version": "0.1.0", "platform": "android", "tokenStore": "keystore",
                "webview": "144.0.7559.59", "push": {"state": "disabled"},
                "cornerRadii": {"tl": 32.0, "tr": 32.0, "bl": 32.0, "br": 32.0},
                "insets": {"top": 24.0, "right": 0.0, "bottom": 16.0, "left": 0.0}
            })
        );
        let ios = MobileCore::new(
            Unsupported,
            r.api.clone(),
            EventLog::default(),
            Platform::Ios,
        );
        let info = ios.info("0.1.0", TokenStoreKind::Keychain, "");
        assert_eq!(info.push.state, PushViewState::Unsupported);
        assert_eq!(info.platform, "ios");
    }

    #[test]
    fn app_icon_ids_round_trip_and_reject_unknown() {
        assert_eq!(app_icon_asset("default"), Some(None));
        assert_eq!(app_icon_asset("accent"), Some(Some("AppIconAccent")));
        // 页面不能拿任意资源名去换图标
        assert_eq!(app_icon_asset("AppIconAccent"), None);
        assert_eq!(app_icon_asset(""), None);

        let info = app_icon_info(true, Some("AppIconAccent"));
        assert_eq!(info.current, "accent");
        assert_eq!(info.options, vec!["default", "accent"]);
        assert_eq!(app_icon_info(true, None).current, "default");
        // 系统里还留着已删掉的资源名时按默认显示，不会选中一个不存在的选项
        assert_eq!(app_icon_info(true, Some("AppIconGone")).current, "default");
        assert!(!app_icon_info(false, None).supported);
    }
}
