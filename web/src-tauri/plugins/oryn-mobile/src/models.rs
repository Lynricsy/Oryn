//! Rust ↔ Kotlin 的数据形状（字段 camelCase，与 `android/` 里的插件实现一一对应）。
//!
//! 这些类型不依赖平台：壳的推送 / 分享编排在主机上单测时用的是同一组类型。

use serde::{Deserialize, Serialize};

/// 屏幕物理圆角（CSS px；取不到为 0）。
#[derive(Debug, Clone, Copy, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct CornerRadii {
    pub tl: f64,
    pub tr: f64,
    pub bl: f64,
    pub br: f64,
}

/// 原生容器加在 WebView 外的 padding（CSS px）。
#[derive(Debug, Clone, Copy, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Insets {
    pub top: f64,
    pub right: f64,
    pub bottom: f64,
    pub left: f64,
}

/// `deviceInfo` 的返回。
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct DeviceInfo {
    /// `MANUFACTURER MODEL`。
    pub name: String,
    pub sdk_int: u32,
    pub webview_version: Option<String>,
    pub debuggable: bool,
    /// 仅 debuggable 时给：Rust 与 Kotlin 共写的事件日志。
    pub event_log_path: Option<String>,
    pub corner_radii: CornerRadii,
    pub insets: Insets,
}

/// 系统通知权限。
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Permission {
    Granted,
    Denied,
    #[default]
    Prompt,
}

/// 实际在用的推送通道：有 UnifiedPush 分发器时 `unifiedpush`，否则 gms 构建走 `fcm`。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum PushChannel {
    #[serde(rename = "unifiedpush")]
    UnifiedPush,
    Fcm,
}

/// 原生侧保存的推送状态（`pushState` / `pushEnable` / `pushDisable` 的返回、`push` 事件的载荷）。
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct PushState {
    pub permission: Permission,
    pub enabled: bool,
    pub distributor: Option<String>,
    pub channel: Option<PushChannel>,
    /// UnifiedPush 端点与 RFC 8291 公钥（FCM 时为空）。
    pub endpoint: Option<String>,
    pub p256dh: Option<String>,
    pub auth: Option<String>,
    /// FCM 注册令牌（`channel = fcm` 时有值）。
    pub fcm_token: Option<String>,
    /// 已登记到服务器的端点（FCM 时为令牌）。
    pub synced_endpoint: Option<String>,
    pub subscription_id: Option<String>,
    pub error: Option<String>,
}

/// 分享进来的一个文件（已拷到应用缓存目录）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ShareFile {
    pub path: String,
    pub name: String,
    #[serde(default)]
    pub mime: String,
    #[serde(default)]
    pub size: u64,
}

/// 原生侧没收下的文件：`too_large` / `too_many` / `unreadable`。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ShareRejected {
    pub name: String,
    pub reason: String,
}

/// 一次系统分享（`ACTION_SEND` / `SEND_MULTIPLE`）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ShareIntake {
    pub id: String,
    #[serde(default)]
    pub text: Option<String>,
    #[serde(default)]
    pub files: Vec<ShareFile>,
    #[serde(default)]
    pub rejected: Vec<ShareRejected>,
}

/// Kotlin → Rust 事件（`trigger("event", {type, …})`）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(
    tag = "type",
    rename_all = "lowercase",
    rename_all_fields = "camelCase"
)]
pub enum Event {
    Resume,
    Stop,
    Push {
        state: PushState,
    },
    Share {
        share: ShareIntake,
    },
    Insets {
        #[serde(default)]
        corner_radii: CornerRadii,
        #[serde(default)]
        insets: Insets,
    },
}

/// `notify` 的参数。
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NotifyArgs {
    /// 页面给的通知 id。
    pub id: String,
    pub title: String,
    pub body: String,
    /// 点击后打开的站内路径。
    pub path: String,
    /// `zh-CN` | `en`（渠道名等原生文案）。
    pub lang: String,
}

/// `notify` 的返回。
#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct NotifyResult {
    pub posted: bool,
    #[serde(default)]
    pub reason: Option<String>,
}
