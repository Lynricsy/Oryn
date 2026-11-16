//! Rust ↔ 原生插件的数据形状（字段 camelCase，与 `android/` 的 Kotlin、`ios/` 的 Swift 实现一一对应）。
//!
//! 这些类型不依赖平台：壳的推送 / 分享编排与原生底栏的校验在主机上单测时用的是同一组类型。

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
    /// UnifiedPush 端点（FCM 时为空）；RFC 8291 公钥两种通道都有（FCM 的由插件自己生成，见 `FcmKeys.kt`）。
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

/// 原生底栏一项的图标：SF Symbol 名、单个字形，或 PNG 的 base64（不带 `data:` 前缀）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TabBarIcon {
    Symbol(String),
    Glyph(String),
    Image(String),
}

/// 原生底栏一项右上角的状态点。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum TabBarBadge {
    Accent,
    Warn,
    Err,
}

/// 原生底栏的一项。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct TabBarItem {
    pub id: String,
    pub label: String,
    pub icon: TabBarIcon,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub badge: Option<TabBarBadge>,
}

/// 胶囊在网页布局视口里的矩形（CSS px）。
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct TabBarFrame {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// 原生底栏的整份状态（`tabBarSet` 的 `spec`）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TabBarSpec {
    pub items: Vec<TabBarItem>,
    /// 选中项 id（= 当前路由的分区；原生点击不自己改它）。
    pub selected: String,
    pub frame: TabBarFrame,
    pub dark: bool,
    /// `#rrggbb`；`None` = 系统强调色。
    pub accent: Option<String>,
}

/// 揭示状态（`tabBarReveal` 的载荷）：`x`（视口 x，CSS px）左侧可见；`ms` 为 0 立即生效，
/// 否则等 `delay` 毫秒后按 `ease`（cubic-bezier 控制点）过渡 `ms` 毫秒。
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct TabBarReveal {
    pub x: f64,
    pub alpha: f64,
    #[serde(default)]
    pub ms: u32,
    #[serde(default)]
    pub delay: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ease: Option<[f64; 4]>,
}

/// 原生 → Rust 事件（`trigger("event", {type, …})`）。
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
    /// 插件已绑定新的 WebView（Activity 重建后，或被系统销毁后在同一进程里新建并重建了主窗口）；
    /// `url` 是销毁前主框架所在地址（那时还没加载页面则为空）。
    Recreated {
        #[serde(default)]
        url: Option<String>,
    },
    /// 先前的 Activity 已被系统销毁（不是配置变更），同一进程里又新建了 Activity：Rust 要为它重建主窗口。
    Activity,
    /// 原生底栏被点了一项（iOS；点已选中项同样上报）。
    TabBar {
        id: String,
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

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};

    const SET: &str = include_str!("../fixtures/tab-bar-set.json");
    const REVEAL: &str = include_str!("../fixtures/tab-bar-reveal.json");

    /// JSON 里整数与浮点不相等（`12` ≠ `12.0`），比较前统一成浮点。
    fn norm(v: Value) -> Value {
        match v {
            Value::Number(n) => json!(n.as_f64()),
            Value::Array(a) => Value::Array(a.into_iter().map(norm).collect()),
            Value::Object(o) => Value::Object(o.into_iter().map(|(k, v)| (k, norm(v))).collect()),
            other => other,
        }
    }

    #[test]
    fn tab_bar_set_fixture_round_trips() {
        let raw: Value = serde_json::from_str(SET).unwrap();
        let spec: TabBarSpec = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(spec.items.len(), 4);
        assert_eq!(
            spec.items[1].icon,
            TabBarIcon::Glyph("🦊".into()),
            "图标是外部标记的单键对象"
        );
        assert_eq!(spec.items[1].badge, Some(TabBarBadge::Accent));
        assert_eq!(spec.items[2].badge, None);
        assert_eq!(norm(serde_json::to_value(&spec).unwrap()), norm(raw));
        // 撤栏：`{"spec": null}`
        let none: Option<TabBarSpec> = serde_json::from_value(Value::Null).unwrap();
        assert_eq!(json!({ "spec": none }), json!({ "spec": null }));
    }

    #[test]
    fn tab_bar_reveal_fixture_round_trips() {
        let raw: Value = serde_json::from_str(REVEAL).unwrap();
        let reveal: TabBarReveal = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(reveal.ms, 260);
        assert_eq!(reveal.delay, 34);
        assert_eq!(norm(serde_json::to_value(reveal).unwrap()), norm(raw));
        // ms / delay / ease 可省
        let bare: TabBarReveal = serde_json::from_value(json!({"x": 0, "alpha": 0})).unwrap();
        assert_eq!((bare.ms, bare.delay, bare.ease), (0, 0, None));
        assert_eq!(
            serde_json::to_value(bare).unwrap(),
            json!({"x": 0.0, "alpha": 0.0, "ms": 0, "delay": 0})
        );
    }

    #[test]
    fn tab_bar_event_parses() {
        let event: Event = serde_json::from_value(json!({"type": "tabbar", "id": "more"})).unwrap();
        assert_eq!(event, Event::TabBar { id: "more".into() });
    }
}
