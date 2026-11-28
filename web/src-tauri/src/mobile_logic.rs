//! 移动端的纯逻辑：导航判定、推送状态归并与订阅同步（含官方推送网关的登记编排）、分享接收流程、`MobileInfo`、
//! 原生底栏 / 页头 / 竖栏入参校验。
//!
//! 不带平台 `cfg`、不依赖 Tauri：原生能力经 [`Native`]、网络经 [`Api`]，在主机上即可单测。
//! 桌面不调用这里（只在单测里覆盖），故整体允许未使用。
#![cfg_attr(not(mobile), allow(dead_code))]

use std::collections::VecDeque;
use std::future::Future;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{Duration, Instant};

use serde::Serialize;
use serde_json::{json, Value};
use tauri_plugin_oryn_mobile::{
    CornerRadii, HeaderIcon, HeaderItem, HeaderMenu, HeaderMenuCmd, HeaderMenuKind, HeaderOp,
    HeaderPalette, HeaderSeg, HeaderShow, HeaderSpec, Insets, Permission, PushChannel, PushState,
    RailPart, RailSpec, ShareIntake, TabBarFrame, TabBarIcon, TabBarItem, TabBarSpec,
};

pub use tauri_plugin_oryn_mobile::PushTransport as Transport;

use crate::api::{Api, ApiError, GatewayCall, GatewayInfo, GatewayReply, PushSubscription};
use crate::config::TokenStoreKind;
use crate::eventlog::EventLog;
use crate::i18n::{self, Lang};
use crate::pushgw::{self, DeviceKey, Grant, Ledger, Revoke};
use crate::secrets::TokenStore;
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

/// 两个 URL 是否同源（按 scheme + host + port 比较：`tauri://` 这类非特殊 scheme 的 `Url::origin`
/// 是不透明的，彼此永不相等）。
pub fn same_origin(a: &url::Url, b: &url::Url) -> bool {
    a.scheme() == b.scheme()
        && a.host_str() == b.host_str()
        && a.port_or_known_default() == b.port_or_known_default()
}

/// launcher = 连接页 origin（`http(s)://tauri.localhost`、`tauri://localhost` 或开发态 `devUrl`）；
/// server = 已配对或正在连接的服务器 origin。
/// 主框架只放行这两个 origin 与 `about:blank`：`blob:` 只认服务器自己 origin 下的（portal 页面经 `window.open`
/// 弹出的 `blob:` 会把整个 App 换成无地址栏的伪造界面），`data:` 一律拦截。
pub fn nav_decision(url: &str, launcher: &str, server: Option<&str>) -> NavDecision {
    let Ok(u) = url::Url::parse(url) else {
        return NavDecision::Block;
    };
    if url::Url::parse(launcher).is_ok_and(|l| same_origin(&u, &l)) {
        return NavDecision::Allow;
    }
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

/// `host_open_external` 只接受 http / https / mailto / tel；返回规范化后的地址。
pub fn external_url(url: &str) -> Option<String> {
    let u = url::Url::parse(url.trim()).ok()?;
    matches!(u.scheme(), "http" | "https" | "mailto" | "tel").then(|| u.into())
}

/// 主框架逃逸恢复动作。
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RecoveryAction {
    /// 首次逃逸且存在同服务器上次就绪的 Oryn 路由：尝试恢复回该站内路由一次。
    Restore { url: String, open_external: bool },
    /// 从未就绪过（例如首次进入 `/` 即跨源 302）或恢复导航本身再次逃逸：
    /// 绝不重试同一跳转，直接回到内置连接页错误态。
    FallbackLauncher { open_external: bool },
}

/// 主框架逃逸恢复状态机：记住上次在 Oryn 页面就绪的合法路由，限制每次连续逃逸最多恢复一次，
/// 避免服务器返回跨源 302（如 SSO 网关）时无限重开系统浏览器并循环跳转。
#[derive(Debug, Default)]
pub struct NavRecovery {
    ready_origin: Option<String>,
    last_ready_path: Option<String>,
    recovering: bool,
}

impl NavRecovery {
    /// Oryn 页面通过 `host_ready` 报告就绪：记录当前服务器的合法站内路由并结束恢复态。
    pub fn note_ready(&mut self, origin: &str, path: &str) {
        if origin.is_empty() {
            return;
        }
        let path = crate::session::valid_app_path(path);
        if path == "/auth/app" || path.starts_with("/auth/app?") {
            return;
        }
        self.ready_origin = Some(origin.to_string());
        self.last_ready_path = Some(path);
        self.recovering = false;
    }

    /// 主框架逃逸离已配对 `server` 时决定下一步动作。
    pub fn on_escape(&mut self, server: &str) -> RecoveryAction {
        if !self.recovering && self.ready_origin.as_deref() == Some(server) {
            if let Some(path) = self.last_ready_path.as_deref() {
                self.recovering = true;
                return RecoveryAction::Restore {
                    url: format!("{server}{path}"),
                    open_external: true,
                };
            }
        }
        // 恢复导航自身再次逃逸时不再重复打开系统浏览器；首次即逃逸（从未就绪）允许打开一次（仍受令牌桶限流）
        let open_external = !self.recovering;
        self.recovering = false;
        self.ready_origin = None;
        self.last_ready_path = None;
        RecoveryAction::FallbackLauncher { open_external }
    }
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
    Disabled,
    PermissionDenied,
    NoDistributor,
    Registering,
    Active,
    Error,
}

/// `MobileInfo.push`（K6）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PushView {
    pub state: PushViewState,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub channel: Option<PushChannel>,
    /// 可切换到的另一通道（只有 gms 包有）：FCM ↔ UnifiedPush。
    #[serde(skip_serializing_if = "Option::is_none")]
    pub alt: Option<PushChannel>,
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
            alt: None,
            distributor: None,
            endpoint_host: None,
            error: None,
        }
    }
}

/// 经官方推送网关的通道（FCM / APNs）。
pub fn gateway_channel(channel: Option<PushChannel>) -> bool {
    matches!(channel, Some(PushChannel::Fcm | PushChannel::Apns))
}

/// 网关通道的推送令牌（FCM 注册令牌 / APNs 设备令牌）；空串当作没有。
fn push_token(s: &PushState) -> Option<&str> {
    match s.channel {
        Some(PushChannel::Fcm) => s.fcm_token.as_deref(),
        Some(PushChannel::Apns) => s.apns_token.as_deref(),
        _ => None,
    }
    .filter(|t| !t.is_empty())
}

/// 网关通道是否已登记完：有授权、授权时的令牌就是现在的令牌、服务器上登记的就是这个授权。
fn gateway_synced(s: &PushState, grant: Option<&Grant>) -> bool {
    match (grant, push_token(s)) {
        (Some(g), Some(token)) => {
            g.token_hash == pushgw::token_hash(token)
                && s.synced_endpoint.as_deref() == Some(g.endpoint().as_str())
        }
        _ => false,
    }
}

/// 状态归并。!enabled → disabled；permission != granted → permission_denied；
/// 没有通道（无分发器且不走网关）→ no_distributor；原生或同步错误 → error；
/// UnifiedPush：端点为空或与已同步的不同 → registering；网关通道：见 [`gateway_synced`]；否则 active。
/// `alt`：gms 包（`fcmAvailable`）在 FCM 与 UnifiedPush 之间可互切。
pub fn push_view(
    s: Option<&PushState>,
    sync_error: Option<&str>,
    grant: Option<&Grant>,
) -> PushView {
    let Some(s) = s.filter(|s| s.enabled) else {
        return PushView::bare(PushViewState::Disabled);
    };
    let gateway = gateway_channel(s.channel);
    let mut view = PushView {
        state: PushViewState::Active,
        channel: s.channel,
        alt: match s.channel {
            Some(PushChannel::Fcm) if s.fcm_available => Some(PushChannel::UnifiedPush),
            Some(PushChannel::UnifiedPush) if s.fcm_available => Some(PushChannel::Fcm),
            _ => None,
        },
        distributor: s.distributor.clone(),
        endpoint_host: if gateway {
            None
        } else {
            s.endpoint
                .as_deref()
                .and_then(|e| url::Url::parse(e).ok())
                .and_then(|u| u.host_str().map(str::to_string))
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
    } else if gateway {
        if gateway_synced(s, grant) {
            PushViewState::Active
        } else {
            PushViewState::Registering
        }
    } else if s.endpoint.is_none() || s.endpoint != s.synced_endpoint {
        PushViewState::Registering
    } else {
        PushViewState::Active
    };
    view
}

/// UnifiedPush 要登记的订阅；材料不全或不是 UnifiedPush 通道时 `None`。`force = false` 时已同步的也返回 `None`。
/// `force`：服务器可能已删掉订阅，本地「已同步」不可尽信；登记按端点 upsert，重复登记无害，
/// 所以每次进入应用都强制登记一次。网关通道的订阅由 [`MobileCore::sync_push`] 在授权之后构造。
pub fn sync_target(s: &PushState, force: bool) -> Option<PushSubscription> {
    if !s.enabled || gateway_channel(s.channel) {
        return None;
    }
    let sub = PushSubscription::WebPush {
        endpoint: s.endpoint.clone()?,
        p256dh: s.p256dh.clone()?,
        auth: s.auth.clone()?,
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
    pub safe_area: Insets,
}

/// 页面事件 `{type:"insets", cornerRadii, insets, safeArea}`。
pub fn insets_event(corner_radii: CornerRadii, insets: Insets, safe_area: Insets) -> Value {
    json!({"type": "insets", "cornerRadii": corner_radii, "insets": insets, "safeArea": safe_area})
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

/// mask 图标的边长（像素）：网页 `nerd-mask.ts` 的 `NERD_MASK_PX`，原生 `HeaderMaskCache` 同样只认这个尺寸。
pub const MASK_PX: u32 = 54;

/// mask：base64（≤ 16384 字节）的 PNG，签名与 IHDR 声明的宽高都是 `MASK_PX`。只解出头 24 字节核对，
/// 声明超大像素或不是 PNG 的数据到不了原生的图像解码器（命令可能被 portal iframe 调到）。
fn valid_mask(m: &str) -> bool {
    if !valid_base64(m, 16_384) || m.len() < 32 {
        return false;
    }
    let Some(head) = decode_base64_head(&m.as_bytes()[..32]) else {
        return false;
    };
    let dim = |at: usize| u32::from_be_bytes([head[at], head[at + 1], head[at + 2], head[at + 3]]);
    head[..8] == *b"\x89PNG\r\n\x1a\n"
        && head[12..16] == *b"IHDR"
        && dim(16) == MASK_PX
        && dim(20) == MASK_PX
}

/// 32 个 base64 字符（不含 `=`）→ 24 字节。
fn decode_base64_head(chars: &[u8]) -> Option<[u8; 24]> {
    let val = |c: u8| -> Option<u32> {
        Some(match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            _ => return None,
        } as u32)
    };
    let mut out = [0u8; 24];
    for g in 0..8 {
        let mut n = 0u32;
        for &c in &chars[g * 4..g * 4 + 4] {
            n = n << 6 | val(c)?;
        }
        out[g * 3..g * 3 + 3].copy_from_slice(&n.to_be_bytes()[1..]);
    }
    Some(out)
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
    valid_tab_items(&spec.items)
        && spec.selected.len() <= 32
        && valid_frame(&spec.frame)
        && spec.accent.as_deref().is_none_or(valid_color)
}

/// 底栏 / 竖栏分区的项：1..=6 项、id 互不相同且 `valid_tab_id`；label 1..=64 个字符；icon 见 `valid_tab_icon`。
fn valid_tab_items(items: &[TabBarItem]) -> bool {
    (1..=6).contains(&items.len())
        && items.iter().enumerate().all(|(i, item)| {
            valid_tab_id(&item.id)
                && items[..i].iter().all(|prev| prev.id != item.id)
                && (1..=64).contains(&item.label.chars().count())
                && valid_tab_icon(&item.icon)
        })
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

/// 图标五选一：symbol / glyph（1..=32 字节）/ mask（`valid_mask`）/ dot / orb；`bg`、`fg` 只配 glyph 与 mask；
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
        && i.mask.as_deref().is_none_or(valid_mask)
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
    valid_header_key(&spec.key)
        && valid_palette(&spec.palette)
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

/// 六个调色板颜色都 `valid_color`。
fn valid_palette(p: &HeaderPalette) -> bool {
    [&p.accent, &p.brand, &p.err, &p.warn, &p.good, &p.frost]
        .iter()
        .all(|c| valid_color(c))
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

// ---------------------------------------------------------------- 原生竖栏（iPhone Duo）

/// 竖栏的页面键：1..=200 个字符。
fn valid_rail_key(key: &str) -> bool {
    chars_in(key, 1, 200)
}

/// 竖栏操作钮 id：1..=64 个字符。
fn valid_rail_action_id(id: &str) -> bool {
    chars_in(id, 1, 64)
}

/// `host_rail_set` 的入参上限（命令可能被 portal iframe 调到，见 host.rs 文件头）：
/// key 1..=200 个字符；调色板同页头（`valid_palette`）；lead.label 1..=64 个字符；
/// actions ≤ 4 个、id 1..=64 个字符且互不相同、symbol `valid_symbol`、label 1..=64 个字符、menu 同页头菜单（`valid_menu`）；
/// tabs：label 1..=64 个字符，items 同底栏（`valid_tab_items`），selected 必须是某一项的 id。
/// `dot` 只有 accent / warn 由类型保证。
pub fn valid_rail(spec: &RailSpec) -> bool {
    let actions = &spec.actions;
    let tabs = &spec.tabs;
    valid_rail_key(&spec.key)
        && valid_palette(&spec.palette)
        && chars_in(&spec.lead.label, 1, 64)
        && actions.len() <= 4
        && actions.iter().enumerate().all(|(i, a)| {
            valid_rail_action_id(&a.id)
                && actions[..i].iter().all(|prev| prev.id != a.id)
                && valid_symbol(&a.symbol)
                && chars_in(&a.label, 1, 64)
                && a.menu.as_ref().is_none_or(valid_menu)
        })
        && chars_in(&tabs.label, 1, 64)
        && valid_tab_items(&tabs.items)
        && tabs.items.iter().any(|t| t.id == tabs.selected)
}

/// 插件事件 → 页面事件 `{type:"railtap", key, part, id?, item?}`（缺省的 id / item 不输出）；形状不对或越界的丢弃：
/// `lead` 不带 id 与 item；`action` 只带操作钮 id；`item` 带操作钮 id 与菜单项路径 id（`valid_item_id`）；
/// `tab` 只带分区 id（`valid_tab_id`）。
#[cfg_attr(target_os = "android", allow(dead_code))]
pub fn rail_tap_event(
    key: &str,
    part: RailPart,
    id: Option<&str>,
    item: Option<&str>,
) -> Option<Value> {
    let ok = valid_rail_key(key)
        && match (part, id, item) {
            (RailPart::Lead, None, None) => true,
            (RailPart::Action, Some(id), None) => valid_rail_action_id(id),
            (RailPart::Item, Some(id), Some(item)) => {
                valid_rail_action_id(id) && valid_item_id(item)
            }
            (RailPart::Tab, Some(id), None) => valid_tab_id(id),
            _ => false,
        };
    if !ok {
        return None;
    }
    let mut detail = json!({"type": "railtap", "key": key, "part": part});
    if let Some(id) = id {
        detail["id"] = json!(id);
    }
    if let Some(item) = item {
        detail["item"] = json!(item);
    }
    Some(detail)
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

// ---------------------------------------------------------------- 网关登记的错误码（K6）

/// 网关地址不在本构建的受信列表里。
pub const GATEWAY_UNTRUSTED: &str = "gateway_untrusted";
/// `/v1/info` 里没有本 App 的 APNs topic / 环境或 Firebase 项目。
pub const GATEWAY_APP_MISMATCH: &str = "gateway_app_mismatch";
/// 30 秒内没等到 challenge 推送。
pub const GATEWAY_CHALLENGE_TIMEOUT: &str = "gateway_challenge_timeout";
/// 网关的其它 4xx。
pub const GATEWAY_REJECTED: &str = "gateway_rejected";

/// 等 challenge 推送的上限。
pub const CHALLENGE_WAIT: Duration = Duration::from_secs(30);
/// 先于 `challenges` 应答到达的 nonce 的缓冲：条数与有效期。
const CHALLENGE_KEEP: usize = 8;
const CHALLENGE_TTL: Duration = Duration::from_secs(300);

/// iOS 的 APNs 环境：debug 构建走 sandbox。
fn apns_environment() -> &'static str {
    if cfg!(debug_assertions) {
        "sandbox"
    } else {
        "production"
    }
}

/// 收到的 challenge（id → nonce），等待方先查缓冲再等通知。
#[derive(Default)]
struct Challenges {
    buf: Mutex<VecDeque<(String, String, Instant)>>,
    notify: tokio::sync::Notify,
}

impl Challenges {
    fn buf(&self) -> MutexGuard<'_, VecDeque<(String, String, Instant)>> {
        let mut g = self.buf.lock().unwrap_or_else(|e| e.into_inner());
        g.retain(|(_, _, at)| at.elapsed() < CHALLENGE_TTL);
        g
    }

    fn put(&self, id: &str, nonce: &str) {
        {
            let mut g = self.buf();
            g.retain(|(i, _, _)| i != id);
            g.push_back((id.to_string(), nonce.to_string(), Instant::now()));
            while g.len() > CHALLENGE_KEEP {
                g.pop_front();
            }
        }
        self.notify.notify_waiters();
    }

    fn take(&self, id: &str) -> Option<String> {
        let mut g = self.buf();
        let at = g.iter().position(|(i, _, _)| i == id)?;
        g.remove(at).map(|(_, nonce, _)| nonce)
    }

    /// 等 `id` 的 nonce，至多 `timeout`。
    async fn wait(&self, id: &str, timeout: Duration) -> Option<String> {
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            let mut notified = std::pin::pin!(self.notify.notified());
            // 先登记再查缓冲：查完到开始等待之间到达的 nonce 不会漏掉
            notified.as_mut().enable();
            if let Some(nonce) = self.take(id) {
                return Some(nonce);
            }
            if tokio::time::timeout_at(deadline, notified).await.is_err() {
                return self.take(id);
            }
        }
    }
}

/// 网关一步失败的处理方式。
enum GwFail {
    /// 记成页面可见的错误码。
    Code(&'static str),
    /// 网络错误、429、5xx：不记错误，下次同步再试。
    Retry,
}

#[derive(serde::Deserialize, Default)]
struct GwErrorBody {
    #[serde(default)]
    code: String,
}

// ---------------------------------------------------------------- 编排

/// 原生侧能力（oryn-mobile 插件的 Kotlin / Swift 实现；单测有假实现）。
pub trait Native: Send + Sync + 'static {
    fn push_state(&self) -> Result<PushState, String>;
    /// `vapid` 只有 UnifiedPush 用（gms 包缺省走 FCM、iOS 走 APNs 时为 `None`）。
    fn push_enable(
        &self,
        vapid: Option<&str>,
        transport: Transport,
    ) -> impl Future<Output = Result<PushState, String>> + Send;
    fn push_disable(&self) -> Result<PushState, String>;
    /// 记下已登记的端点、订阅 id 与服务器（Android 的后台任务在 App 不在前台时据此重新登记 UnifiedPush）。
    fn push_mark_synced(&self, endpoint: &str, id: &str, server: &str) -> Result<(), String>;
    fn share_ack(&self, id: &str);
    fn toast(&self, text: &str);
}

#[derive(Default)]
struct Inner {
    /// 最近一次登记被服务器或网关拒绝的原因（网络错误不记，下次再试）。
    sync_error: Option<String>,
    shares: Shares,
    corner_radii: CornerRadii,
    insets: Insets,
    safe_area: Insets,
}

/// 推送订阅同步（含网关授权）与分享流程的编排（进程级一份）。
pub struct MobileCore<N: Native, A: Api> {
    native: N,
    api: Arc<A>,
    log: EventLog,
    platform: Platform,
    inner: Mutex<Inner>,
    /// 串行化订阅同步：插件事件、`host_ready`、回到前台可能同时触发。
    sync_lock: tokio::sync::Mutex<()>,
    /// 账本所在目录（`<app_config_dir>`）。
    dir: PathBuf,
    ledger: Mutex<Ledger>,
    /// 设备密钥的存储（与设备令牌同一后端）；密钥首次用到时读出或生成。
    keys: Box<dyn TokenStore>,
    device: Mutex<Option<Arc<DeviceKey>>>,
    /// 应用标识：APNs topic / 网关的 FCM `app_id`。
    app_id: String,
    /// 本构建信任的网关 origin（[`pushgw::trusted_origins`]）。
    trusted: Vec<String>,
    challenges: Challenges,
    challenge_wait: Duration,
}

impl<N: Native, A: Api> MobileCore<N, A> {
    pub fn new(
        native: N,
        api: Arc<A>,
        log: EventLog,
        platform: Platform,
        dir: PathBuf,
        keys: Box<dyn TokenStore>,
        app_id: String,
    ) -> Self {
        let ledger = Ledger::load(&dir);
        Self {
            native,
            api,
            log,
            platform,
            inner: Mutex::new(Inner::default()),
            sync_lock: tokio::sync::Mutex::new(()),
            dir,
            ledger: Mutex::new(ledger),
            keys,
            device: Mutex::new(None),
            app_id,
            trusted: pushgw::trusted_origins(),
            challenges: Challenges::default(),
            challenge_wait: CHALLENGE_WAIT,
        }
    }

    pub fn native(&self) -> &N {
        &self.native
    }

    fn inner(&self) -> MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn ledger(&self) -> MutexGuard<'_, Ledger> {
        self.ledger.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// 改账本；有变化才写盘。
    fn update_ledger<T>(&self, f: impl FnOnce(&mut Ledger) -> T) -> T {
        let mut g = self.ledger();
        let before = g.clone();
        let out = f(&mut g);
        if *g != before {
            if let Err(e) = g.save(&self.dir) {
                self.log
                    .log("push.ledger_error", json!({"error": e.to_string()}));
            }
        }
        out
    }

    fn is_trusted(&self, origin: &str) -> bool {
        self.trusted.iter().any(|o| o == origin)
    }

    fn device_key(&self) -> Result<Arc<DeviceKey>, String> {
        let mut g = self.device.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(k) = g.as_ref() {
            return Ok(k.clone());
        }
        let key = Arc::new(DeviceKey::load_or_create(self.keys.as_ref())?);
        *g = Some(key.clone());
        Ok(key)
    }

    /// 记下最新的屏幕圆角、原生外边距与安全区；值变了返回 `true`。
    pub fn set_insets(&self, corner_radii: CornerRadii, insets: Insets, safe_area: Insets) -> bool {
        let mut g = self.inner();
        let changed =
            g.corner_radii != corner_radii || g.insets != insets || g.safe_area != safe_area;
        g.corner_radii = corner_radii;
        g.insets = insets;
        g.safe_area = safe_area;
        changed
    }

    /// 当前记录的屏幕几何与安全区快照。
    pub fn geometry(&self) -> (CornerRadii, Insets, Insets) {
        let g = self.inner();
        (g.corner_radii, g.insets, g.safe_area)
    }

    pub fn push_view(&self) -> PushView {
        let state = self.native.push_state().ok();
        let sync_error = self.inner().sync_error.clone();
        let grant = self.ledger().grant.clone();
        push_view(state.as_ref(), sync_error.as_deref(), grant.as_ref())
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
            safe_area: g.safe_area,
        }
    }

    /// 把尚未登记（或端点 / 令牌 / 网关变了；`force` 时无论如何）的订阅登记到服务器。返回页面是否需要重取 `MobileInfo`。
    /// 每次同步先重试待吊销的授权。
    pub async fn sync_push(&self, conn: Option<&Connection>, force: bool) -> bool {
        let Some(conn) = conn else { return false };
        let _serial = self.sync_lock.lock().await;
        self.flush_revokes().await;
        let Ok(state) = self.native.push_state() else {
            return false;
        };
        if state.enabled && gateway_channel(state.channel) {
            return self.sync_gateway(conn, &state, force).await;
        }
        let Some(sub) = sync_target(&state, force) else {
            return false;
        };
        match self.api.push_subscribe(&conn.base, &conn.token, &sub).await {
            Ok(id) => self.synced(conn, &state, &sub, &id, force),
            Err(e) => self.sync_failed(e),
        }
    }

    /// 登记成功：记到原生侧，清掉旧错误。
    fn synced(
        &self,
        conn: &Connection,
        state: &PushState,
        sub: &PushSubscription,
        id: &str,
        force: bool,
    ) -> bool {
        let unchanged = state.synced_endpoint.as_deref() == Some(sub.key())
            && state.subscription_id.as_deref() == Some(id);
        if let Err(e) = self.native.push_mark_synced(sub.key(), id, &conn.base) {
            self.log.log("push.sync_error", json!({"error": e}));
            return false;
        }
        let had_error = self.inner().sync_error.take().is_some();
        let kind = match sub {
            PushSubscription::WebPush { .. } => "webpush",
            PushSubscription::Gateway { .. } => "gateway",
        };
        self.log.log(
            "push.synced",
            json!({"id": id, "kind": kind, "forced": force}),
        );
        !unchanged || had_error
    }

    /// orynd 拒绝（4xx）记成错误；网络错误与 5xx 下次再试。
    fn sync_failed(&self, e: ApiError) -> bool {
        match e {
            ApiError::Status { status, code, .. } if (400..500).contains(&status) => {
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
            _ => false,
        }
    }

    /// 记下网关登记的错误码（K6）。
    fn gateway_error(&self, code: &'static str) -> bool {
        self.inner().sync_error = Some(code.to_string());
        self.log.log("push.sync_error", json!({"code": code}));
        true
    }

    /// 网关通道（FCM / APNs）的同步：权限 → 删 UnifiedPush 时代的旧订阅 → 读 orynd 的网关 → 受信检查 →
    /// 授权与网关 / 主机密钥不符则吊销旧授权 → 没有授权或令牌变了就登记（info → challenges → nonce → grants）→
    /// 登记订阅（409 `gateway_grant_invalid` / `gateway_changed` 时清掉授权重登一次）→ 记为已同步。
    async fn sync_gateway(&self, conn: &Connection, state: &PushState, force: bool) -> bool {
        if state.permission != Permission::Granted {
            return false;
        }
        // D5：从 UnifiedPush 迁到网关 FCM（或 M28 之前的直发 FCM 订阅）：服务器上那条旧订阅先删掉。
        // 删不掉（离线、5xx）就先不登记，免得同一设备在两条通道上各收一份。
        if let Some(id) = state.subscription_id.as_deref() {
            if !state
                .synced_endpoint
                .as_deref()
                .is_some_and(pushgw::is_grant_endpoint)
            {
                match self.api.push_unsubscribe(&conn.base, &conn.token, id).await {
                    Ok(()) => self.log.log("push.migrated", json!({"id": id})),
                    Err(ApiError::Status { status, .. }) if status < 500 => {}
                    Err(_) => return false,
                }
            }
        }
        let gw = match self.api.push_gateway(&conn.base, &conn.token).await {
            Ok(gw) => gw,
            Err(e) => return self.sync_failed(e),
        };
        if !self.is_trusted(&gw.origin) {
            self.log
                .log("push.gateway_untrusted", json!({"origin": gw.origin}));
            return self.gateway_error(GATEWAY_UNTRUSTED);
        }
        let stale = self
            .ledger()
            .grant
            .clone()
            .is_some_and(|g| g.origin != gw.origin || g.host_key != gw.host_key);
        if stale {
            self.log.log("push.grant_stale", json!({}));
            self.retire_grant().await;
        }
        let (Some(token), Some(p256dh), Some(auth)) =
            (push_token(state), state.p256dh.clone(), state.auth.clone())
        else {
            // 原生侧还没拿到令牌 / 密钥：等它的 push 事件
            return false;
        };
        let key = match self.device_key() {
            Ok(k) => k,
            Err(e) => {
                self.log.log("push.sync_error", json!({"error": e}));
                return false;
            }
        };
        let hash = pushgw::token_hash(token);
        let mut retried = false;
        loop {
            let current = self.ledger().grant.clone().filter(|g| g.token_hash == hash);
            let (grant, fresh) = match current {
                Some(g) => (g, false),
                None => match self.enroll(&key, &gw, state, token).await {
                    Ok(g) => (g, true),
                    Err(GwFail::Code(code)) => return self.gateway_error(code),
                    Err(GwFail::Retry) => return false,
                },
            };
            let endpoint = grant.endpoint();
            if !force && !fresh && state.synced_endpoint.as_deref() == Some(endpoint.as_str()) {
                return false;
            }
            let sub = PushSubscription::Gateway {
                endpoint,
                gateway: grant.origin.clone(),
                grant: grant.grant.clone(),
                p256dh: p256dh.clone(),
                auth: auth.clone(),
            };
            match self.api.push_subscribe(&conn.base, &conn.token, &sub).await {
                Ok(id) => return self.synced(conn, state, &sub, &id, force),
                Err(ApiError::Status {
                    status: 409, code, ..
                }) if !retried
                    && (code == "gateway_grant_invalid" || code == "gateway_changed") =>
                {
                    // 网关已判这份授权无效（或 orynd 刚换了网关）：清掉、重新授权一次
                    self.log.log("push.grant_invalid", json!({"code": code}));
                    self.update_ledger(|l| l.grant = None);
                    retried = true;
                }
                Err(e) => return self.sync_failed(e),
            }
        }
    }

    /// 向网关发一次请求（`key` 有值时带设备签名）。不在受信列表里的 origin 一律不发。
    async fn gateway(
        &self,
        key: Option<&DeviceKey>,
        method: &'static str,
        origin: &str,
        path: &str,
        body: Vec<u8>,
    ) -> Result<GatewayReply, ApiError> {
        if !self.is_trusted(origin) {
            return Err(ApiError::Network("untrusted push gateway".into()));
        }
        let headers = key
            .map(|k| k.headers(method, origin, path, pushgw::now_ms(), &body))
            .unwrap_or_default();
        self.api
            .gateway_call(GatewayCall {
                method,
                origin: origin.to_string(),
                path: path.to_string(),
                headers,
                body,
            })
            .await
    }

    /// 网关应答 → 期望的状态时解出 JSON 体；否则按 K6 归类（日志只记路径、状态与网关错误码）。
    fn gateway_reply(
        &self,
        path: &str,
        res: Result<GatewayReply, ApiError>,
        want: u16,
    ) -> Result<Value, GwFail> {
        let reply = match res {
            Ok(r) => r,
            Err(e) => {
                self.log.log(
                    "push.gateway_error",
                    json!({"path": path, "error": e.to_string()}),
                );
                return Err(GwFail::Retry);
            }
        };
        if reply.status == want {
            return serde_json::from_slice(&reply.body).map_err(|_| GwFail::Code(GATEWAY_REJECTED));
        }
        let code = serde_json::from_slice::<GwErrorBody>(&reply.body)
            .unwrap_or_default()
            .code;
        self.log.log(
            "push.gateway_error",
            json!({"path": path, "status": reply.status, "code": code}),
        );
        Err(match reply.status {
            409 if code == "app_mismatch" => GwFail::Code(GATEWAY_APP_MISMATCH),
            429 | 500.. => GwFail::Retry,
            _ => GwFail::Code(GATEWAY_REJECTED),
        })
    }

    /// `/v1/info` 与本 App 兼容：APNs 比对 topic 与环境，FCM 比对 Firebase 项目。
    fn compatible(&self, info: &Value, state: &PushState) -> bool {
        if info["v"] != 1 {
            return false;
        }
        match state.channel {
            Some(PushChannel::Apns) => {
                info["apns"]["topic"] == self.app_id.as_str()
                    && info["apns"]["environments"]
                        .as_array()
                        .is_some_and(|envs| envs.iter().any(|e| e == apns_environment()))
            }
            Some(PushChannel::Fcm) => state
                .fcm_project_id
                .as_deref()
                .is_some_and(|p| info["fcm"]["projectId"] == p),
            _ => false,
        }
    }

    /// 向网关申请授权：info → challenges → 等 nonce → grants，成功后写进账本。
    async fn enroll(
        &self,
        key: &DeviceKey,
        gw: &GatewayInfo,
        state: &PushState,
        token: &str,
    ) -> Result<Grant, GwFail> {
        let origin = gw.origin.as_str();
        let res = self
            .gateway(None, "GET", origin, pushgw::PATH_INFO, Vec::new())
            .await;
        let info = self.gateway_reply(pushgw::PATH_INFO, res, 200)?;
        if !self.compatible(&info, state) {
            self.log
                .log("push.gateway_mismatch", json!({"origin": origin}));
            return Err(GwFail::Code(GATEWAY_APP_MISMATCH));
        }
        let apns = state.channel == Some(PushChannel::Apns);
        let mut body = json!({
            "channel": if apns { "apns" } else { "fcm" },
            "appId": self.app_id,
            "token": token,
        });
        if apns {
            body["environment"] = json!(apns_environment());
        }
        let res = self
            .gateway(
                Some(key),
                "POST",
                origin,
                pushgw::PATH_CHALLENGES,
                body.to_string().into_bytes(),
            )
            .await;
        let challenge = self.gateway_reply(pushgw::PATH_CHALLENGES, res, 202)?;
        let Some(id) = challenge["challengeId"]
            .as_str()
            .filter(|id| pushgw::valid_id(id))
        else {
            return Err(GwFail::Code(GATEWAY_REJECTED));
        };
        let Some(nonce) = self.challenges.wait(id, self.challenge_wait).await else {
            self.log.log("push.challenge_timeout", json!({}));
            return Err(GwFail::Code(GATEWAY_CHALLENGE_TIMEOUT));
        };
        let body = json!({"challengeId": id, "nonce": nonce, "hostKey": gw.host_key});
        let res = self
            .gateway(
                Some(key),
                "POST",
                origin,
                pushgw::PATH_GRANTS,
                body.to_string().into_bytes(),
            )
            .await;
        let granted = self.gateway_reply(pushgw::PATH_GRANTS, res, 201)?;
        let Some(grant) = granted["grant"].as_str().filter(|g| pushgw::valid_id(g)) else {
            return Err(GwFail::Code(GATEWAY_REJECTED));
        };
        let grant = Grant {
            origin: origin.to_string(),
            host_key: gw.host_key.clone(),
            grant: grant.to_string(),
            token_hash: pushgw::token_hash(token),
        };
        self.update_ledger(|l| l.grant = Some(grant.clone()));
        self.log.log(
            "push.granted",
            json!({"origin": origin, "channel": if apns { "apns" } else { "fcm" }}),
        );
        Ok(grant)
    }

    /// 以设备签名 `DELETE /v1/grants/{id}`。返回是否了结（成功，或网关明确拒绝、重试也没用）；
    /// 网络错误、401、429、5xx 留待下次。不受信的 origin 直接了结、不发请求。
    async fn revoke(&self, key: &DeviceKey, r: &Revoke) -> bool {
        if !self.is_trusted(&r.origin) {
            return true;
        }
        let path = format!("{}/{}", pushgw::PATH_GRANTS, r.grant);
        match self
            .gateway(Some(key), "DELETE", &r.origin, &path, Vec::new())
            .await
        {
            Ok(reply) if !matches!(reply.status, 401 | 429 | 500..) => {
                self.log
                    .log("push.revoked", json!({"status": reply.status}));
                true
            }
            Ok(reply) => {
                self.log
                    .log("push.revoke_error", json!({"status": reply.status}));
                false
            }
            Err(e) => {
                self.log
                    .log("push.revoke_error", json!({"error": e.to_string()}));
                false
            }
        }
    }

    /// 本机授权作废：先挪进待吊销队列（中途退出也不丢），再尽力吊销，成功即移出队列。
    async fn retire_grant(&self) {
        let Some(r) = self.update_ledger(|l| {
            let g = l.grant.take()?;
            let r = Revoke {
                origin: g.origin,
                grant: g.grant,
            };
            l.queue_revoke(r.clone());
            Some(r)
        }) else {
            return;
        };
        let Ok(key) = self.device_key() else { return };
        if self.revoke(&key, &r).await {
            self.update_ledger(|l| l.pending_revokes.retain(|x| x != &r));
        }
    }

    /// 重试待吊销的授权（每次同步、解除配对之后）。
    pub async fn flush_revokes(&self) {
        let pending = self.ledger().pending_revokes.clone();
        if pending.is_empty() {
            return;
        }
        let key = match self.device_key() {
            Ok(k) => k,
            Err(e) => {
                self.log.log("push.revoke_error", json!({"error": e}));
                return;
            }
        };
        for r in pending {
            if self.revoke(&key, &r).await {
                self.update_ledger(|l| l.pending_revokes.retain(|x| x != &r));
            }
        }
    }

    /// 原生侧收到网关的 challenge 推送（可能先于 `challenges` 的应答到达，先进缓冲）。日志不记 nonce。
    pub fn challenge_received(&self, id: &str, nonce: &str) {
        if !pushgw::valid_id(id) || !pushgw::valid_id(nonce) {
            self.log.log("push.challenge_invalid", json!({}));
            return;
        }
        self.log.log("push.challenge", json!({}));
        self.challenges.put(id, nonce);
    }

    /// 配对后是否还欠一次自动启用（取走即清）。
    pub fn take_auto_enable(&self) -> bool {
        self.update_ledger(|l| std::mem::take(&mut l.auto_pending))
    }

    /// `host_ready` 的推送部分：配对后第一次进入应用时，iOS 或带 FCM 的 gms 包自动启用一次推送；
    /// 否则强制登记一次。返回页面是否需要重取 `MobileInfo`。
    pub async fn ready_push(&self, conn: Option<&Connection>) -> bool {
        let Some(conn) = conn else { return false };
        if self.take_auto_enable() {
            let eligible = self.platform == Platform::Ios
                || self.native.push_state().is_ok_and(|s| s.fcm_available);
            if eligible {
                self.log.log("push.auto_enable", json!({}));
                if let Err(e) = self.push_enable(Some(conn), Transport::Default).await {
                    self.log.log("push.enable_error", json!({"error": e}));
                }
                return true;
            }
        }
        self.sync_push(Some(conn), true).await
    }

    /// `host_push_enable`：（UnifiedPush 需要时）取 VAPID 公钥 → 原生启用（可能弹权限、选分发器、取令牌）→
    /// 换了通道就删掉旧通道在服务器上的订阅、离开网关通道就吊销授权 → 登记订阅。
    pub async fn push_enable(
        &self,
        conn: Option<&Connection>,
        transport: Transport,
    ) -> Result<(), String> {
        let conn = conn.ok_or_else(|| "not_connected".to_string())?;
        let prev = self.native.push_state().ok();
        let gateway_default =
            transport == Transport::Default && prev.as_ref().is_some_and(|s| s.fcm_available);
        let vapid = if self.platform == Platform::Android && !gateway_default {
            Some(
                self.api
                    .vapid_key(&conn.base, &conn.token)
                    .await
                    .map_err(|e| e.to_string())?,
            )
        } else {
            None
        };
        let now = self.native.push_enable(vapid.as_deref(), transport).await?;
        self.inner().sync_error = None;
        if let Some(prev) =
            prev.filter(|p| p.enabled && p.channel.is_some() && p.channel != now.channel)
        {
            if let Some(id) = prev.subscription_id.as_deref() {
                let _ = self.api.push_unsubscribe(&conn.base, &conn.token, id).await;
                self.log.log(
                    "push.switched",
                    json!({"from": prev.channel, "to": now.channel}),
                );
            }
        }
        if !gateway_channel(now.channel) {
            self.retire_grant().await;
        }
        self.sync_push(Some(conn), false).await;
        Ok(())
    }

    /// `host_push_disable`：尽力吊销网关授权（失败进待吊销队列）→ 尽力删服务器上的订阅 → 无条件停用本机推送。
    pub async fn push_disable(&self, conn: Option<&Connection>) {
        self.retire_grant().await;
        if let (Some(conn), Some(id)) = (conn, self.subscription_id()) {
            let _ = self
                .api
                .push_unsubscribe(&conn.base, &conn.token, &id)
                .await;
        }
        self.disable_local();
    }

    /// 解除配对（换服务器、设备被吊销）：授权挪进待吊销队列、停用本机推送、下次配对后自动启用一次。
    /// 同步执行（会话状态机里调用）；吊销由调用方另起 [`Self::flush_revokes`]。
    pub fn unpaired(&self) {
        self.update_ledger(|l| {
            if let Some(g) = l.grant.take() {
                l.queue_revoke(Revoke {
                    origin: g.origin,
                    grant: g.grant,
                });
            }
            l.auto_pending = true;
        });
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{Health, PairPoll, PairStart};
    use crate::eventlog::testing::{memory, Buffer};
    use crate::pushgw::tests::verify;
    use crate::secrets::testing::FakeStore;
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
        // macOS / Linux / iOS 连接页使用非特殊 scheme `tauri://localhost`
        assert_eq!(
            nav_decision("tauri://localhost/launcher.html", "tauri://localhost", s),
            Allow
        );
        assert_eq!(
            nav_decision("https://evil.example/", "tauri://localhost", s),
            External
        );
    }

    #[test]
    fn nav_recovery_restores_once_and_breaks_redirect_loops() {
        // 场景 1：已配对站点 `/` 返回跨源 302（从未调用过 host_ready）→ 不重试 `/`，仅允许开一次浏览器并直接回连接页
        let mut r = NavRecovery::default();
        assert_eq!(
            r.on_escape(SERVER),
            RecoveryAction::FallbackLauncher {
                open_external: true
            }
        );

        // 场景 2：在 `/t/T-1?tab=files` 就绪后发生逃逸 → 恢复回上次就绪路由（仅一次）
        r.note_ready(SERVER, "/t/T-1?tab=files");
        assert_eq!(
            r.on_escape(SERVER),
            RecoveryAction::Restore {
                url: format!("{SERVER}/t/T-1?tab=files"),
                open_external: true
            }
        );

        // 恢复导航本身再次跨源 302 逃逸 → 不再重试也不重复开浏览器，直接回连接页
        assert_eq!(
            r.on_escape(SERVER),
            RecoveryAction::FallbackLauncher {
                open_external: false
            }
        );

        // 场景 3：恢复成功后再次就绪 → 重置恢复态，后续单次逃逸仍可恢复
        r.note_ready(SERVER, "/t/T-2");
        assert_eq!(
            r.on_escape(SERVER),
            RecoveryAction::Restore {
                url: format!("{SERVER}/t/T-2"),
                open_external: true
            }
        );
        r.note_ready(SERVER, "/t/T-2");
        assert_eq!(
            r.on_escape(SERVER),
            RecoveryAction::Restore {
                url: format!("{SERVER}/t/T-2"),
                open_external: true
            }
        );

        // 场景 4：换服务器后旧服务器的就绪路由不可用于新服务器
        r.note_ready(SERVER, "/t/T-3");
        assert_eq!(
            r.on_escape("https://other.example.com"),
            RecoveryAction::FallbackLauncher {
                open_external: true
            }
        );
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
        assert_eq!(
            external_url("mailto:hi@example.com").as_deref(),
            Some("mailto:hi@example.com")
        );
        assert_eq!(external_url("tel:+10086").as_deref(), Some("tel:+10086"));
        for bad in [
            "javascript:alert(1)",
            "data:text/html,hi",
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

    /// 标准 base64（带 `=` 补齐），只给测试拼 mask 用。
    fn b64(bytes: &[u8]) -> String {
        const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
        let mut s = String::new();
        for c in bytes.chunks(3) {
            let n = (c[0] as u32) << 16
                | (*c.get(1).unwrap_or(&0) as u32) << 8
                | *c.get(2).unwrap_or(&0) as u32;
            for i in 0..4 {
                s.push(if i <= c.len() {
                    T[(n >> (18 - 6 * i) & 63) as usize] as char
                } else {
                    '='
                });
            }
        }
        s
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
        // 夹具里的是网页实际画出的 54×54 模板图
        let real = fixture_header().title.unwrap().icon.unwrap().mask.unwrap();
        assert!(valid_mask(&real));
        rejects_title("mask 同时有 glyph", |t| {
            let mut i = mask(&real);
            i.glyph = Some("x".into());
            t.icon = Some(i)
        });
        rejects_title("mask 非 base64", |t| {
            t.icon = Some(mask("data:image/png;base64,AAAA"))
        });
        rejects_title("mask 16385 字节", |t| {
            t.icon = Some(mask(&format!("{real}{}", "A".repeat(16_385 - real.len()))))
        });
        rejects_title("mask 为空", |t| t.icon = Some(mask("")));
        let png_head = |w: u32, h: u32| {
            let mut b = b"\x89PNG\r\n\x1a\n\0\0\0\x0dIHDR".to_vec();
            b.extend_from_slice(&w.to_be_bytes());
            b.extend_from_slice(&h.to_be_bytes());
            b.extend_from_slice(&[8, 6, 0, 0, 0, 0, 0, 0, 0]);
            b64(&b)
        };
        assert!(valid_mask(&png_head(54, 54)));
        rejects_title("mask 声明 4096×4096", |t| {
            t.icon = Some(mask(&png_head(4096, 4096)))
        });
        rejects_title("mask 54×55", |t| t.icon = Some(mask(&png_head(54, 55))));
        rejects_title("mask 不是 PNG（JPEG 头）", |t| {
            t.icon = Some(mask(&b64(&[
                0xff, 0xd8, 0xff, 0xe0, 0, 0x10, b'J', b'F', b'I', b'F', 0, 1, 1, 0, 0, 1, 0, 1, 0,
                0, 0, 0, 0, 0,
            ])))
        });
        rejects_title("mask 不足 32 个字符", |t| {
            t.icon = Some(mask(&real[..28]))
        });
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
        t.icon = Some(mask(&format!("{real}{}", "A".repeat(16_384 - real.len()))));
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

    const RAIL_SET: &str = include_str!("../plugins/oryn-mobile/fixtures/rail-set.json");
    const RAIL_TAP: &str = include_str!("../plugins/oryn-mobile/fixtures/rail-tap.json");

    fn fixture_rail() -> RailSpec {
        serde_json::from_str::<tauri_plugin_oryn_mobile::RailSetArgs>(RAIL_SET)
            .unwrap()
            .spec
            .unwrap()
    }

    fn rail_action(id: &str) -> tauri_plugin_oryn_mobile::RailAction {
        serde_json::from_value(json!({"id": id, "symbol": "star", "label": id})).unwrap()
    }

    /// 竖栏的两个夹具同样是 TS、Rust、Swift 三方的共同契约。
    #[test]
    fn rail_fixtures_round_trip() {
        let raw: Value = serde_json::from_str(RAIL_SET).unwrap();
        let back = tauri_plugin_oryn_mobile::RailSetArgs {
            spec: Some(fixture_rail()),
        };
        assert_eq!(norm(serde_json::to_value(back).unwrap()), norm(raw));
        assert!(valid_rail(&fixture_rail()));
    }

    /// 在夹具上改一处后应被 `valid_rail` 拒绝。
    fn rejects_rail(name: &str, mutate: impl FnOnce(&mut RailSpec)) {
        let mut s = fixture_rail();
        mutate(&mut s);
        assert!(!valid_rail(&s), "{name}");
    }

    #[test]
    fn rail_limits() {
        rejects_rail("key 为空", |s| s.key.clear());
        rejects_rail("key 201 字符", |s| s.key = "字".repeat(201));
        rejects_rail("palette.warn red", |s| s.palette.warn = "red".into());
        rejects_rail("lead.label 为空", |s| s.lead.label.clear());
        rejects_rail("lead.label 65 字符", |s| s.lead.label = "字".repeat(65));
        rejects_rail("5 个 action", |s| {
            s.actions = (0..5).map(|i| rail_action(&format!("a{i}"))).collect()
        });
        rejects_rail("action id 重复", |s| s.actions[1].id = "tree".into());
        rejects_rail("action id 为空", |s| s.actions[0].id.clear());
        rejects_rail("action id 65 字符", |s| s.actions[0].id = "a".repeat(65));
        rejects_rail("action symbol 为空", |s| s.actions[0].symbol.clear());
        rejects_rail("action symbol 含大写", |s| {
            s.actions[0].symbol = "Star".into()
        });
        rejects_rail("action symbol 65 字节", |s| {
            s.actions[0].symbol = "a".repeat(65)
        });
        rejects_rail("action label 为空", |s| s.actions[0].label.clear());
        rejects_rail("action label 65 字符", |s| {
            s.actions[0].label = "字".repeat(65)
        });
        rejects_rail("menu label 为空", |s| {
            s.actions[1].menu.as_mut().unwrap().label.clear()
        });
        rejects_rail("menu 空组", |s| {
            s.actions[1].menu.as_mut().unwrap().groups.push(vec![])
        });
        rejects_rail("menu 项 id 重复", |s| {
            let g = &mut s.actions[1].menu.as_mut().unwrap().groups[0];
            g[1].id = g[0].id.clone();
        });
        rejects_rail("menu 项 id 含空格", |s| {
            s.actions[1].menu.as_mut().unwrap().groups[0][0].id = "a b".into()
        });
        rejects_rail("tabs.label 为空", |s| s.tabs.label.clear());
        rejects_rail("tabs.label 65 字符", |s| s.tabs.label = "字".repeat(65));
        rejects_rail("tabs 没有项", |s| s.tabs.items.clear());
        rejects_rail("tabs 7 项", |s| {
            s.tabs.items = (0..7).map(|i| tab_item(&format!("t{i}"))).collect()
        });
        rejects_rail("tabs id 重复", |s| s.tabs.items[1].id = "threads".into());
        rejects_rail("tabs id 含大写", |s| {
            s.tabs.items[0].id = "Threads".into()
        });
        rejects_rail("tabs glyph 33 字节", |s| {
            s.tabs.items[0].icon = TabBarIcon::Glyph("a".repeat(33))
        });
        rejects_rail("selected 不是任何一项", |s| {
            s.tabs.selected = "inbox".into()
        });
        rejects_rail("selected 为空", |s| s.tabs.selected.clear());
        // dot 只有 accent / warn
        assert!(
            serde_json::from_value::<tauri_plugin_oryn_mobile::RailAction>(
                json!({"id": "a", "symbol": "star", "label": "a", "dot": "err"})
            )
            .is_err()
        );
        // 边界上的合法值
        let mut s = fixture_rail();
        s.key = "字".repeat(200);
        s.lead.label = "字".repeat(64);
        s.actions = (0..4).map(|i| rail_action(&format!("a{i}"))).collect();
        s.actions[0].id = "字".repeat(64);
        s.actions[1].label = "字".repeat(64);
        s.actions[2].symbol = "a".repeat(64);
        s.tabs.label = "字".repeat(64);
        s.tabs.items = (0..6).map(|i| tab_item(&format!("t{i}"))).collect();
        s.tabs.selected = "t5".into();
        assert!(valid_rail(&s));
        s.actions.clear();
        assert!(valid_rail(&s), "没有 action");
    }

    #[test]
    fn rail_tap() {
        use tauri_plugin_oryn_mobile::Event;
        // 夹具里的四种 part：解析进 Event::RailTap 后原样发给网页（缺省的 id / item 不输出）
        let raw: Vec<Value> = serde_json::from_str(RAIL_TAP).unwrap();
        for v in raw {
            let Ok(Event::RailTap {
                key,
                part,
                id,
                item,
            }) = serde_json::from_value(v.clone())
            else {
                panic!("{v}");
            };
            assert_eq!(
                rail_tap_event(&key, part, id.as_deref(), item.as_deref()),
                Some(v)
            );
        }
        use RailPart::{Action, Item, Lead, Tab};
        for (name, e) in [
            ("lead 带 id", rail_tap_event("k", Lead, Some("x"), None)),
            ("action 没有 id", rail_tap_event("k", Action, None, None)),
            (
                "action 带 item",
                rail_tap_event("k", Action, Some("tree"), Some("x")),
            ),
            (
                "action id 65 字符",
                rail_tap_event("k", Action, Some(&"a".repeat(65)), None),
            ),
            (
                "item 没有 item",
                rail_tap_event("k", Item, Some("more"), None),
            ),
            (
                "item 没有 id",
                rail_tap_event("k", Item, None, Some("archive")),
            ),
            (
                "item 的菜单项 id 含空格",
                rail_tap_event("k", Item, Some("more"), Some("a b")),
            ),
            ("tab 没有 id", rail_tap_event("k", Tab, None, None)),
            (
                "tab id 含大写",
                rail_tap_event("k", Tab, Some("Projects"), None),
            ),
            ("key 为空", rail_tap_event("", Lead, None, None)),
            (
                "key 201 字符",
                rail_tap_event(&"字".repeat(201), Lead, None, None),
            ),
        ] {
            assert_eq!(e, None, "{name}");
        }
        // 菜单项路径可含 `/`
        assert_eq!(
            rail_tap_event("k", Item, Some("more"), Some("move/p-1")),
            Some(
                json!({"type": "railtap", "key": "k", "part": "item", "id": "more", "item": "move/p-1"})
            )
        );
    }

    const GW: &str = "https://push.oryn.best";
    const GW2: &str = "https://gw2.example.com";
    const HOST_A: &str = "IVL40Zt5HSRFMkLhXy6rbLfP-ntqXtMAl5YOBpiB2xI";
    const HOST_B: &str = "hostKeyB_rotated";
    const APP_ID: &str = "com.lynricsy.oryn";
    const FCM_TOKEN: &str = "fcm-token-SECRET-1";
    const APNS_TOKEN: &str = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789";
    const NONCE: &str = "NONCEnonceNONCEnonce_-";
    const UP_ENDPOINT: &str = "http://localhost:7680/upAbc?up=1";

    fn up_state() -> PushState {
        PushState {
            permission: Permission::Granted,
            enabled: true,
            distributor: Some("io.heckel.ntfy".into()),
            channel: Some(PushChannel::UnifiedPush),
            endpoint: Some(UP_ENDPOINT.into()),
            p256dh: Some("pk".into()),
            auth: Some("au".into()),
            ..PushState::default()
        }
    }

    /// gms 包、经网关走 FCM。
    fn fcm_state() -> PushState {
        PushState {
            permission: Permission::Granted,
            enabled: true,
            channel: Some(PushChannel::Fcm),
            fcm_token: Some(FCM_TOKEN.into()),
            p256dh: Some("pk".into()),
            auth: Some("au".into()),
            fcm_available: true,
            fcm_project_id: Some("oryn-test".into()),
            ..PushState::default()
        }
    }

    fn apns_state() -> PushState {
        PushState {
            permission: Permission::Granted,
            enabled: true,
            channel: Some(PushChannel::Apns),
            apns_token: Some(APNS_TOKEN.into()),
            p256dh: Some("pk".into()),
            auth: Some("au".into()),
            ..PushState::default()
        }
    }

    fn grant_for(token: &str) -> Grant {
        Grant {
            origin: GW.into(),
            host_key: HOST_A.into(),
            grant: "AAAAAAAAAAAAAAAAAAAAAA".into(),
            token_hash: pushgw::token_hash(token),
        }
    }

    #[test]
    fn push_view_states() {
        use PushViewState::*;
        let base = up_state();
        assert_eq!(push_view(None, None, None).state, Disabled);
        let off = PushState {
            enabled: false,
            ..base.clone()
        };
        assert_eq!(push_view(Some(&off), None, None).state, Disabled);
        let denied = PushState {
            permission: Permission::Denied,
            ..base.clone()
        };
        assert_eq!(push_view(Some(&denied), None, None).state, PermissionDenied);
        let none = PushState {
            distributor: None,
            channel: None,
            endpoint: None,
            ..base.clone()
        };
        assert_eq!(push_view(Some(&none), None, None).state, NoDistributor);
        let failed = PushState {
            error: Some("ACTION_NOT_ALLOWED".into()),
            ..base.clone()
        };
        let v = push_view(Some(&failed), None, None);
        assert_eq!(
            (v.state, v.error.as_deref()),
            (Error, Some("ACTION_NOT_ALLOWED"))
        );
        let v = push_view(Some(&base), Some("push_invalid"), None);
        assert_eq!((v.state, v.error.as_deref()), (Error, Some("push_invalid")));
        assert_eq!(push_view(Some(&base), None, None).state, Registering);
        let synced = PushState {
            synced_endpoint: base.endpoint.clone(),
            subscription_id: Some("ps_1".into()),
            ..base.clone()
        };
        let v = push_view(Some(&synced), None, None);
        assert_eq!(v.state, Active);
        assert_eq!(v.endpoint_host.as_deref(), Some("localhost"));
        assert_eq!(v.distributor.as_deref(), Some("io.heckel.ntfy"));
        // foss 包：没有可切换的通道
        assert_eq!(
            serde_json::to_value(&v).unwrap(),
            json!({"state": "active", "channel": "unifiedpush", "distributor": "io.heckel.ntfy", "endpointHost": "localhost"})
        );
        assert_eq!(
            serde_json::to_value(push_view(None, None, None)).unwrap(),
            json!({"state": "disabled"})
        );
        assert_eq!(
            serde_json::to_value(push_view(Some(&denied), None, None)).unwrap()["state"],
            "permission_denied"
        );
    }

    /// 网关通道只有在授权存在、令牌哈希一致、且已同步的端点等于授权端点时才是 active；`alt` 只给 gms 包。
    #[test]
    fn push_view_gateway_states() {
        use PushViewState::*;
        let s = fcm_state();
        let g = grant_for(FCM_TOKEN);
        let v = push_view(Some(&s), None, None);
        assert_eq!(
            (v.state, v.alt),
            (Registering, Some(PushChannel::UnifiedPush))
        );
        // 有授权但服务器上还没登记
        assert_eq!(push_view(Some(&s), None, Some(&g)).state, Registering);
        let synced = PushState {
            synced_endpoint: Some(g.endpoint()),
            subscription_id: Some("ps_1".into()),
            ..s.clone()
        };
        let v = push_view(Some(&synced), None, Some(&g));
        assert_eq!(v.state, Active);
        assert_eq!(
            serde_json::to_value(&v).unwrap(),
            json!({"state": "active", "channel": "fcm", "alt": "unifiedpush"})
        );
        // 已同步的是别的授权、或令牌变了：registering
        let other = Grant {
            grant: "BBBBBBBBBBBBBBBBBBBBBB".into(),
            ..g.clone()
        };
        assert_eq!(
            push_view(Some(&synced), None, Some(&other)).state,
            Registering
        );
        let rotated = PushState {
            fcm_token: Some("fcm-token-SECRET-2".into()),
            ..synced.clone()
        };
        assert_eq!(push_view(Some(&rotated), None, Some(&g)).state, Registering);
        // 没有令牌（原生侧还在取）
        let no_token = PushState {
            fcm_token: None,
            ..synced.clone()
        };
        assert_eq!(
            push_view(Some(&no_token), None, Some(&g)).state,
            Registering
        );
        let v = push_view(Some(&synced), Some(GATEWAY_UNTRUSTED), Some(&g));
        assert_eq!(
            serde_json::to_value(&v).unwrap(),
            json!({"state": "error", "channel": "fcm", "alt": "unifiedpush", "error": "gateway_untrusted"})
        );
        // iOS：APNs，没有 alt，不再是 unsupported
        let ios = apns_state();
        let g = grant_for(APNS_TOKEN);
        let ios_synced = PushState {
            synced_endpoint: Some(g.endpoint()),
            ..ios.clone()
        };
        assert_eq!(
            serde_json::to_value(push_view(Some(&ios_synced), None, Some(&g))).unwrap(),
            json!({"state": "active", "channel": "apns"})
        );
        assert_eq!(push_view(Some(&ios), None, None).state, Registering);
        // gms 包手动切到了 UnifiedPush：可切回 FCM
        let up = PushState {
            fcm_available: true,
            ..up_state()
        };
        assert_eq!(push_view(Some(&up), None, None).alt, Some(PushChannel::Fcm));
    }

    #[test]
    fn sync_targets() {
        let s = up_state();
        assert_eq!(
            sync_target(&s, false),
            Some(PushSubscription::WebPush {
                endpoint: UP_ENDPOINT.into(),
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
        // 网关通道不走这里（要先授权）
        assert_eq!(sync_target(&fcm_state(), true), None);
        assert_eq!(sync_target(&apns_state(), true), None);
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
            "insets": {"top": 0, "right": 0, "bottom": 48, "left": 0},
            "safeArea": {"top": 24, "right": 0, "bottom": 0, "left": 0}
        }))
        .unwrap();
        let Event::Insets {
            corner_radii,
            insets,
            safe_area,
        } = e
        else {
            panic!()
        };
        assert_eq!(corner_radii.tl, 40.5);
        assert_eq!(insets.bottom, 48.0);
        assert_eq!(safe_area.top, 24.0);
        assert_eq!(
            insets_event(corner_radii, insets, safe_area),
            json!({"type": "insets", "cornerRadii": {"tl": 40.5, "tr": 40.5, "bl": 40.5, "br": 40.5},
                   "insets": {"top": 0.0, "right": 0.0, "bottom": 48.0, "left": 0.0},
                   "safeArea": {"top": 24.0, "right": 0.0, "bottom": 0.0, "left": 0.0}})
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

    // ------------------------------------------------------------ 假原生 / 假网络 / 假网关

    #[derive(Default)]
    struct FakeNative {
        state: Mutex<PushState>,
        /// `push_enable` 之后的状态（缺省 `up_state()`）。
        enable_to: Mutex<Option<PushState>>,
        acked: Mutex<Vec<String>>,
        toasts: Mutex<Vec<String>>,
        enabled_with: Mutex<Vec<(Option<String>, Transport)>>,
    }

    impl Native for Arc<FakeNative> {
        fn push_state(&self) -> Result<PushState, String> {
            Ok(self.state.lock().unwrap().clone())
        }
        async fn push_enable(
            &self,
            vapid: Option<&str>,
            transport: Transport,
        ) -> Result<PushState, String> {
            self.enabled_with
                .lock()
                .unwrap()
                .push((vapid.map(str::to_string), transport));
            let next = self
                .enable_to
                .lock()
                .unwrap()
                .clone()
                .unwrap_or_else(up_state);
            let mut s = self.state.lock().unwrap();
            // 原生侧保留已登记记录（与 Kotlin 一致，由 Rust 判断要不要删旧订阅）
            *s = PushState {
                synced_endpoint: s.synced_endpoint.clone(),
                subscription_id: s.subscription_id.clone(),
                ..next
            };
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

    type Core = MobileCore<Arc<FakeNative>, FakeApi>;

    /// 假网关什么时候把 challenge 推送交给原生侧。
    #[derive(Debug, Clone, Copy, Default)]
    enum NonceMode {
        /// `challenges` 应答之后（正常顺序）。
        #[default]
        After,
        /// 应答之前就到了。
        Before,
        /// 一直没到。
        Never,
    }

    #[derive(Default)]
    struct FakeApi {
        subscribe: Mutex<VecDeque<Result<String, ApiError>>>,
        subscribed: Mutex<Vec<(String, PushSubscription)>>,
        unsubscribe: Mutex<VecDeque<ApiError>>,
        unsubscribed: Mutex<Vec<String>>,
        uploads: Mutex<Vec<(String, String, Vec<u8>)>>,
        /// 这些文件名的上传失败。
        fail_names: Vec<&'static str>,
        /// `GET /api/v1/push/gateway`；`None` = `{origin: GW, hostKey: HOST_A}`。
        gateway: Mutex<Option<Result<GatewayInfo, ApiError>>>,
        /// 假网关：`/v1/info` 的应答体。
        info: Mutex<Value>,
        /// 假网关：按 `"<METHOD> <路径前缀>"` 预置的应答，先到先用。
        script: Mutex<Vec<(&'static str, Result<GatewayReply, ApiError>)>>,
        calls: Mutex<Vec<GatewayCall>>,
        nonce: Mutex<NonceMode>,
        core: Mutex<Option<std::sync::Weak<Core>>>,
        seq: std::sync::atomic::AtomicU32,
    }

    fn reply(status: u16, body: Value) -> Result<GatewayReply, ApiError> {
        Ok(GatewayReply {
            status,
            body: body.to_string().into_bytes(),
        })
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
            match self.unsubscribe.lock().unwrap().pop_front() {
                Some(e) => Err(e),
                None => Ok(()),
            }
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
        async fn push_gateway(&self, _: &str, token: &str) -> Result<GatewayInfo, ApiError> {
            assert_eq!(token, "odt_secret");
            self.gateway
                .lock()
                .unwrap()
                .clone()
                .unwrap_or(Ok(GatewayInfo {
                    v: 1,
                    origin: GW.into(),
                    host_key: HOST_A.into(),
                }))
        }
        async fn gateway_call(&self, call: GatewayCall) -> Result<GatewayReply, ApiError> {
            self.calls.lock().unwrap().push(call.clone());
            let key = format!("{} {}", call.method, call.path);
            {
                let mut script = self.script.lock().unwrap();
                if let Some(i) = script.iter().position(|(k, _)| key.starts_with(k)) {
                    return script.remove(i).1;
                }
            }
            let n = self.seq.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            match (call.method, call.path.as_str()) {
                ("GET", "/v1/info") => reply(200, self.info.lock().unwrap().clone()),
                ("POST", "/v1/challenges") => {
                    let id = format!("CH{n:020}");
                    let core = self.core.lock().unwrap().as_ref().and_then(|w| w.upgrade());
                    let mode = *self.nonce.lock().unwrap();
                    match mode {
                        NonceMode::Before => core.unwrap().challenge_received(&id, NONCE),
                        NonceMode::After => {
                            let id = id.clone();
                            tokio::spawn(async move {
                                tokio::task::yield_now().await;
                                core.unwrap().challenge_received(&id, NONCE);
                            });
                        }
                        NonceMode::Never => {}
                    }
                    reply(202, json!({"challengeId": id}))
                }
                ("POST", "/v1/grants") => {
                    reply(201, json!({"grant": format!("GR{n:020}"), "createdAt": 1}))
                }
                ("DELETE", _) => reply(204, Value::Null),
                other => unreachable!("unexpected gateway call {other:?}"),
            }
        }
    }

    struct Rig {
        core: Arc<Core>,
        native: Arc<FakeNative>,
        api: Arc<FakeApi>,
        log: Buffer,
        keys: FakeStore,
        dir: tempfile::TempDir,
    }

    fn rig(api: FakeApi) -> Rig {
        rig_with(api, Platform::Android, Duration::from_secs(5))
    }

    fn rig_with(api: FakeApi, platform: Platform, challenge_wait: Duration) -> Rig {
        let native = Arc::new(FakeNative::default());
        let api = Arc::new(api);
        *api.info.lock().unwrap() = json!({
            "v": 1,
            "apns": {"topic": APP_ID, "environments": ["production", "sandbox"]},
            "fcm": {"projectId": "oryn-test"}
        });
        let (log, buf) = memory();
        let dir = tempfile::tempdir().unwrap();
        let keys = FakeStore::default();
        let mut core = MobileCore::new(
            native.clone(),
            api.clone(),
            log,
            platform,
            dir.path().to_path_buf(),
            Box::new(keys.clone()),
            APP_ID.into(),
        );
        core.trusted = vec![GW.into(), GW2.into()];
        core.challenge_wait = challenge_wait;
        let core = Arc::new(core);
        *api.core.lock().unwrap() = Some(Arc::downgrade(&core));
        Rig {
            core,
            native,
            api,
            log: buf,
            keys,
            dir,
        }
    }

    impl Drop for Rig {
        fn drop(&mut self) {
            let text = self.log.text();
            assert!(!text.contains("odt_"), "event log leaked a token: {text}");
            assert!(!text.contains("\"pk\""), "event log leaked p256dh: {text}");
            assert!(!text.contains("\"au\""), "event log leaked auth: {text}");
            assert!(
                !text.contains("fcm-token-"),
                "event log leaked an FCM token: {text}"
            );
            assert!(
                !text.contains(APNS_TOKEN),
                "event log leaked an APNs token: {text}"
            );
            assert!(!text.contains(NONCE), "event log leaked a nonce: {text}");
        }
    }

    impl Rig {
        /// 假网关收到的请求，`"<METHOD> <路径>"`。
        fn gw(&self) -> Vec<String> {
            self.api
                .calls
                .lock()
                .unwrap()
                .iter()
                .map(|c| format!("{} {}", c.method, c.path))
                .collect()
        }

        fn call(&self, i: usize) -> GatewayCall {
            self.api.calls.lock().unwrap()[i].clone()
        }

        fn ledger(&self) -> Ledger {
            Ledger::load(self.dir.path())
        }

        fn set_state(&self, s: PushState) {
            *self.native.state.lock().unwrap() = s;
        }

        fn state(&self) -> PushState {
            self.native.state.lock().unwrap().clone()
        }

        fn last_sub(&self) -> PushSubscription {
            self.api
                .subscribed
                .lock()
                .unwrap()
                .last()
                .unwrap()
                .1
                .clone()
        }

        /// 请求带设备签名，且能用本机设备公钥验过（与网关 `pushproto.Verify` 同一算法）。
        fn assert_signed(&self, c: &GatewayCall) {
            let h = |name: &str| {
                c.headers
                    .iter()
                    .find(|(k, _)| k == name)
                    .map(|(_, v)| v.clone())
                    .unwrap_or_else(|| panic!("{name} missing on {} {}", c.method, c.path))
            };
            let key = h(pushgw::HEADER_DEVICE_KEY);
            assert_eq!(key, DeviceKey::load_or_create(&self.keys).unwrap().public());
            let ts: i64 = h(pushgw::HEADER_TIMESTAMP).parse().unwrap();
            let input = pushgw::signing_input(c.method, &c.origin, &c.path, ts, &c.body);
            assert!(
                verify(&key, input.as_bytes(), &h(pushgw::HEADER_SIGNATURE)),
                "bad signature on {} {}",
                c.method,
                c.path
            );
        }
    }

    fn body(c: &GatewayCall) -> Value {
        serde_json::from_slice(&c.body).unwrap()
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

    fn status(status: u16, code: &str) -> ApiError {
        ApiError::Status {
            status,
            code: code.into(),
            message: String::new(),
        }
    }

    #[tokio::test]
    async fn enable_syncs_once_and_disable_unsubscribes() {
        let r = rig(FakeApi::default());
        // 未进入应用：不能启用，也不同步
        assert_eq!(
            r.core.push_enable(None, Transport::Default).await,
            Err("not_connected".into())
        );
        assert!(!r.core.sync_push(None, true).await);
        r.core
            .push_enable(Some(&conn()), Transport::Default)
            .await
            .unwrap();
        // foss 包：走 UnifiedPush，要 VAPID
        assert_eq!(
            r.native.enabled_with.lock().unwrap().as_slice(),
            [(Some("BVapid".to_string()), Transport::Default)]
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
        // UnifiedPush 不碰网关
        assert!(r.gw().is_empty());
        let ev = r.log.events();
        assert_eq!(ev[0]["event"], "push.synced");
        assert_eq!(ev[0]["kind"], "webpush");
    }

    #[tokio::test]
    async fn rejected_subscription_reports_error_network_does_not() {
        let r = rig(FakeApi::default());
        r.set_state(up_state());
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
            .push_back(Err(status(400, "push_invalid")));
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let v = r.core.push_view();
        assert_eq!(
            (v.state, v.error.as_deref()),
            (PushViewState::Error, Some("push_invalid"))
        );
        // 下次成功即清掉错误
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        assert_eq!(r.state().synced_endpoint.as_deref(), Some(UP_ENDPOINT));
        assert_eq!(events(&r.log), ["push.sync_error", "push.synced"]);
    }

    #[tokio::test]
    async fn untrusted_origin_sends_nothing() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        for origin in ["https://evil.example", "http://localhost:7700"] {
            *r.api.gateway.lock().unwrap() = Some(Ok(GatewayInfo {
                v: 1,
                origin: origin.into(),
                host_key: HOST_A.into(),
            }));
            assert!(r.core.sync_push(Some(&conn()), true).await);
            let v = r.core.push_view();
            assert_eq!(
                (v.state, v.error.as_deref()),
                (PushViewState::Error, Some(GATEWAY_UNTRUSTED))
            );
        }
        // 不读 /v1/info、不提交令牌，也不向 orynd 登记
        assert!(r.gw().is_empty());
        assert!(r.api.subscribed.lock().unwrap().is_empty());
        assert_eq!(r.ledger().grant, None);
    }

    /// 登记全程：info（不签名）→ challenges → 等 nonce → grants（都带设备签名）→ 登记 `{kind: gateway}` → 已同步。
    #[tokio::test]
    async fn enroll_challenge_grant_subscribe() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert_eq!(r.core.push_view().state, PushViewState::Registering);
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(
            r.gw(),
            ["GET /v1/info", "POST /v1/challenges", "POST /v1/grants"]
        );
        let info = r.call(0);
        assert!(info.headers.is_empty() && info.body.is_empty());
        assert_eq!(info.origin, GW);
        let ch = r.call(1);
        r.assert_signed(&ch);
        assert_eq!(
            body(&ch),
            json!({"channel": "fcm", "appId": APP_ID, "token": FCM_TOKEN})
        );
        let gr = r.call(2);
        r.assert_signed(&gr);
        let challenge_id = format!("CH{:020}", 1);
        assert_eq!(
            body(&gr),
            json!({"challengeId": challenge_id, "nonce": NONCE, "hostKey": HOST_A})
        );
        let grant = format!("GR{:020}", 2);
        let endpoint = format!("{GW}/v1/grants/{grant}");
        assert_eq!(
            r.last_sub(),
            PushSubscription::Gateway {
                endpoint: endpoint.clone(),
                gateway: GW.into(),
                grant: grant.clone(),
                p256dh: "pk".into(),
                auth: "au".into()
            }
        );
        assert_eq!(
            r.state().synced_endpoint.as_deref(),
            Some(endpoint.as_str())
        );
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        let ledger = r.ledger();
        assert_eq!(
            ledger.grant,
            Some(Grant {
                origin: GW.into(),
                host_key: HOST_A.into(),
                grant,
                token_hash: pushgw::token_hash(FCM_TOKEN),
            })
        );
        // 账本里没有令牌本身
        let raw = std::fs::read_to_string(Ledger::path(r.dir.path())).unwrap();
        assert!(!raw.contains(FCM_TOKEN));
        // 已同步：不再联系网关；强制登记只重新 POST 订阅
        assert!(!r.core.sync_push(Some(&conn()), false).await);
        assert!(!r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.gw().len(), 3);
        assert_eq!(r.api.subscribed.lock().unwrap().len(), 2);
        assert_eq!(r.log.events().last().unwrap()["kind"], "gateway");
    }

    /// iOS：APNs 通道带环境（debug 构建 sandbox），兼容检查看 topic 与环境。
    #[tokio::test]
    async fn apns_enroll_sends_environment() {
        let r = rig_with(FakeApi::default(), Platform::Ios, Duration::from_secs(5));
        r.set_state(apns_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(
            body(&r.call(1)),
            json!({"channel": "apns", "appId": APP_ID, "environment": apns_environment(), "token": APNS_TOKEN})
        );
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        // 网关只配了另一个环境：不兼容
        let r = rig_with(FakeApi::default(), Platform::Ios, Duration::from_secs(5));
        r.set_state(apns_state());
        let other = if apns_environment() == "sandbox" {
            "production"
        } else {
            "sandbox"
        };
        *r.api.info.lock().unwrap() =
            json!({"v": 1, "apns": {"topic": APP_ID, "environments": [other]}});
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(
            r.core.push_view().error.as_deref(),
            Some(GATEWAY_APP_MISMATCH)
        );
        assert_eq!(r.gw(), ["GET /v1/info"]);
    }

    #[tokio::test]
    async fn challenge_before_response_is_buffered() {
        let r = rig(FakeApi::default());
        *r.api.nonce.lock().unwrap() = NonceMode::Before;
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        assert_eq!(body(&r.call(2))["nonce"], NONCE);
        // 缓冲：形状不对的丢弃；至多 8 条，最旧的先出
        r.core.challenge_received("short", NONCE);
        r.core.challenge_received(&"A".repeat(22), "not-a-nonce");
        for i in 0..10 {
            r.core.challenge_received(&format!("ID{i:020}"), NONCE);
        }
        assert_eq!(r.core.challenges.take(&format!("ID{:020}", 1)), None);
        assert_eq!(
            r.core.challenges.take(&format!("ID{:020}", 9)).as_deref(),
            Some(NONCE)
        );
        assert_eq!(r.core.challenges.take(&"A".repeat(22)), None);
    }

    #[tokio::test]
    async fn challenge_timeout_reports_error() {
        let r = rig_with(
            FakeApi::default(),
            Platform::Android,
            Duration::from_millis(50),
        );
        *r.api.nonce.lock().unwrap() = NonceMode::Never;
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let v = r.core.push_view();
        assert_eq!(
            (v.state, v.error.as_deref()),
            (PushViewState::Error, Some(GATEWAY_CHALLENGE_TIMEOUT))
        );
        assert_eq!(r.gw(), ["GET /v1/info", "POST /v1/challenges"]);
        assert_eq!(r.ledger().grant, None);
        assert!(r.api.subscribed.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn token_change_reenrolls() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let first = r.ledger().grant.unwrap();
        r.native.state.lock().unwrap().fcm_token = Some("fcm-token-SECRET-2".into());
        assert_eq!(r.core.push_view().state, PushViewState::Registering);
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(
            r.gw()[3..],
            ["GET /v1/info", "POST /v1/challenges", "POST /v1/grants"]
        );
        assert_eq!(body(&r.call(4))["token"], "fcm-token-SECRET-2");
        let second = r.ledger().grant.unwrap();
        assert_eq!(second.token_hash, pushgw::token_hash("fcm-token-SECRET-2"));
        assert_ne!(second.grant, first.grant);
        // 同一主机：不吊销旧授权（网关按同一对设备 / 主机更新令牌）
        assert!(!r.gw().iter().any(|c| c.starts_with("DELETE")));
        assert_eq!(
            r.state().synced_endpoint.as_deref(),
            Some(second.endpoint().as_str())
        );
        assert_eq!(r.core.push_view().state, PushViewState::Active);
    }

    #[tokio::test]
    async fn host_key_change_revokes_then_reenrolls() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let old = r.ledger().grant.unwrap();
        // orynd 轮换了主机密钥：旧授权尽力吊销，再对新主机授权
        *r.api.gateway.lock().unwrap() = Some(Ok(GatewayInfo {
            v: 1,
            origin: GW.into(),
            host_key: HOST_B.into(),
        }));
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert_eq!(
            r.gw()[3..],
            [
                format!("DELETE /v1/grants/{}", old.grant),
                "GET /v1/info".into(),
                "POST /v1/challenges".into(),
                "POST /v1/grants".into()
            ]
        );
        r.assert_signed(&r.call(3));
        assert_eq!(body(&r.call(6))["hostKey"], HOST_B);
        let new = r.ledger().grant.unwrap();
        assert_eq!(new.host_key, HOST_B);
        assert!(r.ledger().pending_revokes.is_empty());
        // 换了网关：在旧网关吊销，在新网关授权（旧授权从不带到新网关）
        *r.api.gateway.lock().unwrap() = Some(Ok(GatewayInfo {
            v: 1,
            origin: GW2.into(),
            host_key: HOST_B.into(),
        }));
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let calls = r.api.calls.lock().unwrap().clone();
        let tail: Vec<(String, String)> = calls[7..]
            .iter()
            .map(|c| (format!("{} {}", c.method, c.path), c.origin.clone()))
            .collect();
        assert_eq!(
            tail,
            [
                (format!("DELETE /v1/grants/{}", new.grant), GW.to_string()),
                ("GET /v1/info".into(), GW2.into()),
                ("POST /v1/challenges".into(), GW2.into()),
                ("POST /v1/grants".into(), GW2.into())
            ]
        );
        assert_eq!(r.ledger().grant.unwrap().origin, GW2);
        assert_eq!(r.core.push_view().state, PushViewState::Active);
    }

    #[tokio::test]
    async fn grant_invalid_reenrolls_once() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        // orynd 验证网关授权失败：清掉授权、重新授权一次后登记成功
        r.api
            .subscribe
            .lock()
            .unwrap()
            .push_back(Err(status(409, "gateway_grant_invalid")));
        r.api.subscribe.lock().unwrap().push_back(Ok("ps_2".into()));
        assert!(r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.gw().len(), 6);
        assert_eq!(r.core.subscription_id().as_deref(), Some("ps_2"));
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        // 网关已判失效的授权不必再吊销
        assert!(!r.gw().iter().any(|c| c.starts_with("DELETE")));
        // 重登之后仍被拒：只重试一次，然后报错
        for code in ["gateway_changed", "gateway_grant_invalid"] {
            r.api
                .subscribe
                .lock()
                .unwrap()
                .push_back(Err(status(409, code)));
        }
        assert!(r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.gw().len(), 9);
        let v = r.core.push_view();
        assert_eq!(
            (v.state, v.error.as_deref()),
            (PushViewState::Error, Some("gateway_grant_invalid"))
        );
    }

    #[tokio::test]
    async fn disable_revokes_and_queues_on_failure() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let g1 = r.ledger().grant.unwrap();
        // 在线：吊销成功 → 删订阅 → 停用
        r.core.push_disable(Some(&conn())).await;
        assert_eq!(r.gw()[3], format!("DELETE /v1/grants/{}", g1.grant));
        r.assert_signed(&r.call(3));
        assert_eq!(r.api.unsubscribed.lock().unwrap().as_slice(), ["ps_1"]);
        let l = r.ledger();
        assert_eq!((l.grant, l.pending_revokes.len()), (None, 0));
        assert_eq!(r.core.push_view().state, PushViewState::Disabled);

        // 离线：吊销失败进队列，本机照样停用
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let g2 = r.ledger().grant.unwrap();
        r.api
            .script
            .lock()
            .unwrap()
            .push(("DELETE ", Err(ApiError::Network("offline".into()))));
        r.core.push_disable(None).await;
        assert_eq!(r.core.push_view().state, PushViewState::Disabled);
        assert_eq!(
            r.ledger().pending_revokes,
            [Revoke {
                origin: GW.into(),
                grant: g2.grant.clone()
            }]
        );
        // 下次同步先重试（5xx 留着，成功才移出）
        r.api
            .script
            .lock()
            .unwrap()
            .push(("DELETE ", reply(503, json!({"code": "unavailable"}))));
        r.core.sync_push(Some(&conn()), false).await;
        assert_eq!(r.ledger().pending_revokes.len(), 1);
        r.core.sync_push(Some(&conn()), false).await;
        assert!(r.ledger().pending_revokes.is_empty());
        let deletes = r.gw().iter().filter(|c| c.starts_with("DELETE")).count();
        assert_eq!(deletes, 4);

        // 解除配对：授权挪进队列、停用、下次配对自动启用；随后的 flush 吊销掉
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        assert!(r.core.take_auto_enable(), "新装待自动启用");
        r.core.unpaired();
        let l = r.ledger();
        assert!(l.auto_pending && l.grant.is_none());
        assert_eq!(l.pending_revokes.len(), 1);
        assert_eq!(r.core.push_view().state, PushViewState::Disabled);
        r.core.flush_revokes().await;
        assert!(r.ledger().pending_revokes.is_empty());
    }

    #[tokio::test]
    async fn auto_enable_once_per_pairing() {
        // gms 包：配对后第一次进入应用自动启用（走 FCM，不取 VAPID）
        let r = rig(FakeApi::default());
        r.set_state(PushState {
            fcm_available: true,
            fcm_project_id: Some("oryn-test".into()),
            ..PushState::default()
        });
        *r.native.enable_to.lock().unwrap() = Some(fcm_state());
        // 还没进入应用：不消耗
        assert!(!r.core.ready_push(None).await);
        assert!(r.ledger().auto_pending);
        assert!(r.core.ready_push(Some(&conn())).await);
        assert_eq!(
            r.native.enabled_with.lock().unwrap().as_slice(),
            [(None, Transport::Default)]
        );
        assert!(!r.ledger().auto_pending);
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        // 之后的 host_ready 只强制登记，不再启用
        r.core.ready_push(Some(&conn())).await;
        assert_eq!(r.native.enabled_with.lock().unwrap().len(), 1);
        // 解除配对后再配对：再自动启用一次
        r.core.unpaired();
        r.set_state(PushState {
            fcm_available: true,
            ..PushState::default()
        });
        assert!(r.core.ready_push(Some(&conn())).await);
        assert_eq!(r.native.enabled_with.lock().unwrap().len(), 2);
        r.core.ready_push(Some(&conn())).await;
        assert_eq!(r.native.enabled_with.lock().unwrap().len(), 2);

        // foss 包（没有 FCM）：不自动启用，标记照样用掉
        let r = rig(FakeApi::default());
        r.core.ready_push(Some(&conn())).await;
        assert!(r.native.enabled_with.lock().unwrap().is_empty());
        assert!(!r.ledger().auto_pending);

        // iOS：自动启用（不取 VAPID）
        let r = rig_with(FakeApi::default(), Platform::Ios, Duration::from_secs(5));
        *r.native.enable_to.lock().unwrap() = Some(apns_state());
        assert!(r.core.ready_push(Some(&conn())).await);
        assert_eq!(
            r.native.enabled_with.lock().unwrap().as_slice(),
            [(None, Transport::Default)]
        );
        assert_eq!(r.core.push_view().state, PushViewState::Active);
    }

    #[tokio::test]
    async fn info_mismatch_reports_gateway_app_mismatch() {
        for info in [
            json!({"v": 1, "fcm": {"projectId": "someone-else"}}),
            json!({"v": 1, "apns": {"topic": APP_ID, "environments": ["production", "sandbox"]}}),
            json!({"v": 2, "fcm": {"projectId": "oryn-test"}}),
        ] {
            let r = rig(FakeApi::default());
            r.set_state(fcm_state());
            *r.api.info.lock().unwrap() = info;
            assert!(r.core.sync_push(Some(&conn()), false).await);
            let v = r.core.push_view();
            assert_eq!(
                (v.state, v.error.as_deref()),
                (PushViewState::Error, Some(GATEWAY_APP_MISMATCH))
            );
            // 兼容检查不过：不提交令牌
            assert_eq!(r.gw(), ["GET /v1/info"]);
        }
        // 上游判定令牌不属于本 App：同一个错误码；其它 4xx 是 gateway_rejected；5xx 下次再试
        for (resp, want) in [
            (
                reply(409, json!({"code": "app_mismatch"})),
                Some(GATEWAY_APP_MISMATCH),
            ),
            (
                reply(400, json!({"code": "malformed"})),
                Some(GATEWAY_REJECTED),
            ),
            (reply(502, json!({"code": "upstream"})), None),
        ] {
            let r = rig(FakeApi::default());
            r.set_state(fcm_state());
            r.api
                .script
                .lock()
                .unwrap()
                .push(("POST /v1/challenges", resp));
            r.core.sync_push(Some(&conn()), false).await;
            assert_eq!(r.core.push_view().error.as_deref(), want);
        }
        // orynd 关了网关 / 旧 orynd：原样显示
        for code in ["gateway_disabled", "gateway_unsupported"] {
            let r = rig(FakeApi::default());
            r.set_state(fcm_state());
            *r.api.gateway.lock().unwrap() = Some(Err(status(404, code)));
            assert!(r.core.sync_push(Some(&conn()), false).await);
            assert_eq!(r.core.push_view().error.as_deref(), Some(code));
            assert!(r.gw().is_empty());
        }
    }

    /// D5：已用 UnifiedPush 的 gms 老用户升级后原生侧改报 `fcm`：先删 orynd 上旧的 Web Push 订阅，再走网关登记。
    #[tokio::test]
    async fn unifiedpush_to_fcm_migration_drops_webpush_subscription() {
        let r = rig(FakeApi::default());
        let migrated = PushState {
            synced_endpoint: Some(UP_ENDPOINT.into()),
            subscription_id: Some("ps_up".into()),
            distributor: Some("io.heckel.ntfy".into()),
            ..fcm_state()
        };
        // 删不掉（离线）：先不登记，免得两条通道各收一份
        r.set_state(migrated.clone());
        r.api
            .unsubscribe
            .lock()
            .unwrap()
            .push_back(ApiError::Network("offline".into()));
        assert!(!r.core.sync_push(Some(&conn()), true).await);
        assert!(r.gw().is_empty());
        // 恢复联网：删旧订阅 → 网关登记 → 登记网关订阅
        assert!(r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(
            r.api.unsubscribed.lock().unwrap().as_slice(),
            ["ps_up", "ps_up"]
        );
        assert_eq!(
            r.gw(),
            ["GET /v1/info", "POST /v1/challenges", "POST /v1/grants"]
        );
        assert!(matches!(r.last_sub(), PushSubscription::Gateway { .. }));
        assert_eq!(r.core.subscription_id().as_deref(), Some("ps_1"));
        assert_eq!(r.core.push_view().state, PushViewState::Active);
        // 之后不再删任何订阅
        assert!(!r.core.sync_push(Some(&conn()), true).await);
        assert_eq!(r.api.unsubscribed.lock().unwrap().len(), 2);
        assert!(events(&r.log).contains(&"push.migrated".to_string()));
    }

    /// gms 包里手动切到 UnifiedPush：删掉网关订阅、吊销授权，改登记 Web Push。
    #[tokio::test]
    async fn switch_to_unifiedpush_retires_grant() {
        let r = rig(FakeApi::default());
        r.set_state(fcm_state());
        assert!(r.core.sync_push(Some(&conn()), false).await);
        let g = r.ledger().grant.unwrap();
        *r.native.enable_to.lock().unwrap() = Some(PushState {
            fcm_available: true,
            ..up_state()
        });
        r.api
            .subscribe
            .lock()
            .unwrap()
            .push_back(Ok("ps_up".into()));
        r.core
            .push_enable(Some(&conn()), Transport::UnifiedPush)
            .await
            .unwrap();
        assert_eq!(
            r.native.enabled_with.lock().unwrap().as_slice(),
            [(Some("BVapid".to_string()), Transport::UnifiedPush)]
        );
        assert_eq!(r.api.unsubscribed.lock().unwrap().as_slice(), ["ps_1"]);
        assert_eq!(r.gw()[3], format!("DELETE /v1/grants/{}", g.grant));
        assert_eq!(r.ledger().grant, None);
        assert!(matches!(r.last_sub(), PushSubscription::WebPush { .. }));
        let v = r.core.push_view();
        assert_eq!(
            (v.state, v.channel, v.alt),
            (
                PushViewState::Active,
                Some(PushChannel::UnifiedPush),
                Some(PushChannel::Fcm)
            )
        );
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
            top: 0.0,
            right: 0.0,
            bottom: 0.0,
            left: 0.0,
        };
        let safe_area = Insets {
            top: 24.0,
            right: 0.0,
            bottom: 16.0,
            left: 0.0,
        };
        assert!(r.core.set_insets(radii, insets, safe_area));
        assert!(!r.core.set_insets(radii, insets, safe_area));
        let info = r
            .core
            .info("0.1.0", TokenStoreKind::Keystore, "144.0.7559.59");
        assert_eq!(
            serde_json::to_value(&info).unwrap(),
            json!({
                "version": "0.1.0", "platform": "android", "tokenStore": "keystore",
                "webview": "144.0.7559.59", "push": {"state": "disabled"},
                "cornerRadii": {"tl": 32.0, "tr": 32.0, "bl": 32.0, "br": 32.0},
                "insets": {"top": 0.0, "right": 0.0, "bottom": 0.0, "left": 0.0},
                "safeArea": {"top": 24.0, "right": 0.0, "bottom": 16.0, "left": 0.0}
            })
        );
        // iOS 走同一个插件：推送状态来自原生（APNs），不再是 unsupported
        let ios = rig_with(FakeApi::default(), Platform::Ios, Duration::from_secs(5));
        let info = ios.core.info("0.1.0", TokenStoreKind::Keychain, "");
        assert_eq!(info.push.state, PushViewState::Disabled);
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
