//! Rust ↔ 原生插件的数据形状（字段 camelCase，与 `android/` 的 Kotlin、`ios/` 的 Swift 实现一一对应）。
//!
//! 这些类型不依赖平台：壳的推送 / 分享编排与原生底栏、原生页头的校验在主机上单测时用的是同一组类型。

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

/// 原生底栏的整份状态（`tabBarSet` 的 `spec`）。底栏垫在 WebView 之下（bridge ≥ 7），页面在接管期间透明。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct TabBarSpec {
    pub items: Vec<TabBarItem>,
    /// 选中项 id（= 当前路由的分区；原生点击不自己改它）。
    pub selected: String,
    pub frame: TabBarFrame,
    pub dark: bool,
    /// `#rrggbb`；`None` = 系统强调色。
    pub accent: Option<String>,
    /// 页面底色 `#rrggbb`：原生垫底按它铺满，页面透明的那部分看起来与原来一样。
    /// 必填：不带它的是旧网页（还以为底栏叠在 WebView 上面），壳拒绝，旧网页退回网页底栏。
    pub bg: String,
}

/// 网页内容（菜单、弹层、软键盘）是否压在底栏位置上（`tabBarCover` 的载荷）。原生底栏只在卡片左缘越过底栏右缘
/// （原生自己从根滚动位移算出）且没被压住时接触摸；看得见的部分由网页合成器决定。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct TabBarCover {
    pub covered: bool,
}

/// 下沉侧栏改由 WKWebView 的根滚动驱动（`sinkSet` 的载荷，bridge ≥ 8）：`on` 时原生按 `contentOffset` 跟随，
/// 原生底栏与页头才允许接管。`trace`：原生回传根滚动轨迹（`Event::Sink`，含 move 的调用栈）；只有 debug 包为真，
/// 发布包不装轨迹回调，滚动的 KVO 里不取栈、不发事件。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct SinkSet {
    pub on: bool,
    #[serde(default)]
    pub trace: bool,
}

/// 程序化开合（`sinkShow` 的载荷）：原生把根滚动视图滚到打开位（`open`）或关闭位，`animated` 时用 UIKit 的滚动动画。
/// 网页发起的 smooth 滚动在 WKWebView 的主框架上会被拉回（M20 探针），所以平滑开合由原生做。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct SinkShow {
    pub open: bool,
    pub animated: bool,
}

/// 原生页头里各控件的矩形（CSS px）；与底栏同形。
pub type HeaderFrame = TabBarFrame;

/// orb 状态珠的画法（网页 `OrbBead` 的各档 + `err` = 7pt 错误色圆点）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum HeaderOrb {
    Booting,
    Ready,
    Working,
    Freezing,
    Frozen,
    Stopped,
    Deleted,
    Failed,
    Err,
}

/// 四选一：`symbol` / `glyph`（可带 `bg`、`fg`）/ `dot` / `orb`（校验在壳的 `mobile_logic::valid_header`）。颜色一律 `#rrggbb`。
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderIcon {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub symbol: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub glyph: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bg: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub fg: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dot: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub orb: Option<HeaderOrb>,
}

/// 说明行的语气（目前只有错误色）。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum HeaderTone {
    Err,
}

/// 视图钮 / 菜单项文字后的提醒点。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum HeaderDot {
    Accent,
    Warn,
}

/// 一段「图形 + 文字」：标题胶囊第二行的一段、菜单行的说明行。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderSeg {
    pub text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub icon: Option<HeaderIcon>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tone: Option<HeaderTone>,
}

/// 标题胶囊第二行末尾的标记图标（定时、公开、PR…）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderMark {
    pub symbol: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dim: Option<bool>,
}

/// 菜单的一项：`id` 在菜单内唯一，子项是 `<父 id>/<key>`；原生点击原样回传。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderItem {
    pub id: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub icon: Option<HeaderIcon>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sub: Option<HeaderSeg>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub hint: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dot: Option<HeaderDot>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pulse: Option<bool>,
    /// 给出即单选项；`true` 时右侧打勾。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub checked: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub danger: Option<bool>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub disabled: Option<bool>,
    /// 选中时原生把这段文字写进剪贴板。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub copy: Option<String>,
    /// 一级子页（子项不能再有 children）。
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub children: Vec<HeaderItem>,
}

/// 一个弹出菜单：顶部宫格、浅底状态块、其余分组（组间一条细线）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderMenu {
    /// 读屏用的菜单名。
    pub label: String,
    /// 打开时是否压暗页面（标题菜单 true，视图菜单 false）。
    pub dim: bool,
    #[serde(default)]
    pub quick: Vec<HeaderItem>,
    #[serde(default)]
    pub status: Vec<HeaderItem>,
    pub groups: Vec<Vec<HeaderItem>>,
}

/// 网页给的调色板（`#rrggbb`）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderPalette {
    pub accent: String,
    pub brand: String,
    pub err: String,
    pub warn: String,
    pub good: String,
    pub frost: String,
}

/// 可调参数（0–1）：遮罩不透明度、面板底色着色强度。
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct HeaderTune {
    pub scrim: f64,
    pub tint: f64,
}

/// 读屏文案：子页的返回行、关闭区。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderText {
    pub back: String,
    pub close: String,
}

/// 侧栏圆钮。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct HeaderLead {
    pub frame: HeaderFrame,
    pub symbol: String,
    pub label: String,
}

/// 两行标题胶囊与它的菜单。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct HeaderTitle {
    pub frame: HeaderFrame,
    pub text: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub icon: Option<HeaderIcon>,
    /// 有值时第二行只显示它（错误色）。
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub meta: Vec<HeaderSeg>,
    pub marks: Vec<HeaderMark>,
    /// 读屏读的整句。
    pub label: String,
    pub menu: HeaderMenu,
}

/// 视图圆钮与它的菜单。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct HeaderTrail {
    pub frame: HeaderFrame,
    pub symbol: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub dot: Option<HeaderDot>,
    pub menu: HeaderMenu,
}

/// 原生页头的整份状态（`headerSet` 的 `spec`）。
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct HeaderSpec {
    /// 线程 id：变了原生收起菜单；事件原样带回。
    pub key: String,
    pub dark: bool,
    pub palette: HeaderPalette,
    pub tune: HeaderTune,
    pub text: HeaderText,
    pub lead: HeaderLead,
    pub title: HeaderTitle,
    /// 没有视图钮时为 `null`（不省略）。
    pub trail: Option<HeaderTrail>,
}

/// 控件层的呈现状态（`headerShow` 的载荷）：透明度；`ms` 为 0 立即生效，否则淡入淡出 `ms` 毫秒。
/// 水平位移与是否接触摸由原生按根滚动位移自己算（bridge ≥ 8），网页不发。
#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
pub struct HeaderShow {
    pub alpha: f64,
    #[serde(default)]
    pub ms: u32,
}

/// 两个菜单。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum HeaderMenuKind {
    Title,
    Trail,
}

/// `headerMenu` 的载荷：`open` 为空 = 收起（不省略）；`page` = 直接停在某个子页（父项 id）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderMenuCmd {
    pub open: Option<HeaderMenuKind>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub page: Option<String>,
}

/// 原生页头事件的种类：`lead` 点了侧栏钮；`menu` 用户打开 / 关闭了菜单；`select` 选了一项。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum HeaderOp {
    Lead,
    Menu,
    Select,
}

/// `headerProbe` 的返回（调试）：屏幕中心与三件控件中心的触摸是否被原生层截住。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct HeaderProbe {
    pub center: bool,
    pub lead: bool,
    pub title: bool,
    #[serde(default)]
    pub trail: bool,
}

/// `appIconGet` 的返回：系统是否支持可替换图标，当前用的资源名（`None` = 默认图标）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct AppIconState {
    pub supported: bool,
    #[serde(default)]
    pub current: Option<String>,
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
    /// 原生页头的交互（iOS）：`lead` 点了侧栏钮；`menu` 用户打开 / 关闭了菜单（`menu` 缺省 = 关闭）；`select` 选了一项。
    Header {
        op: HeaderOp,
        key: String,
        #[serde(default)]
        menu: Option<HeaderMenuKind>,
        #[serde(default)]
        id: Option<String>,
    },
    /// 根滚动的轨迹（iOS，`SinkFollow`）：`op` 为 start / jump / noop / busy / done / cancel（程序化开合），
    /// 或 move（不是程序化开合、也不是手指与惯性造成的位移，带 `stack` 调用栈，查卡片被谁挪走）；
    /// `x` 是当时的 contentOffset.x、`to` 是目的地（move 为位移后的位置）。只供 debug 包的事件日志（CI 端到端）。
    Sink {
        op: String,
        x: f64,
        to: f64,
        #[serde(default)]
        stack: Option<String>,
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
    const COVER: &str = include_str!("../fixtures/tab-bar-cover.json");
    const SINK_SET: &str = include_str!("../fixtures/sink-set.json");
    const SINK_SHOW: &str = include_str!("../fixtures/sink-show.json");
    const HEADER_SET: &str = include_str!("../fixtures/header-set.json");
    const HEADER_SHOW: &str = include_str!("../fixtures/header-show.json");
    const HEADER_MENU: &str = include_str!("../fixtures/header-menu.json");

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
        // 旧网页（以为底栏叠在 WebView 上面）不带 bg：解析失败，壳拒绝，它退回网页底栏
        let mut old: Value = serde_json::from_str(SET).unwrap();
        old.as_object_mut().unwrap().remove("bg");
        assert!(serde_json::from_value::<TabBarSpec>(old).is_err());
    }

    #[test]
    fn sink_and_cover_fixtures_round_trip() {
        let raw: Value = serde_json::from_str(COVER).unwrap();
        let cover: TabBarCover = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(cover, TabBarCover { covered: true });
        assert_eq!(serde_json::to_value(cover).unwrap(), raw);
        let raw: Value = serde_json::from_str(SINK_SET).unwrap();
        let set: SinkSet = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(
            set,
            SinkSet {
                on: true,
                trace: true
            }
        );
        assert_eq!(serde_json::to_value(set).unwrap(), raw);
        let raw: Value = serde_json::from_str(SINK_SHOW).unwrap();
        let show: SinkShow = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(
            show,
            SinkShow {
                open: true,
                animated: true
            }
        );
        assert_eq!(serde_json::to_value(show).unwrap(), raw);
    }

    #[test]
    fn tab_bar_event_parses() {
        let event: Event = serde_json::from_value(json!({"type": "tabbar", "id": "more"})).unwrap();
        assert_eq!(event, Event::TabBar { id: "more".into() });
    }

    #[test]
    fn header_set_fixture_round_trips() {
        let raw: Value = serde_json::from_str(HEADER_SET).unwrap();
        let spec: HeaderSpec = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(norm(serde_json::to_value(&spec).unwrap()), norm(raw));
        let menu = &spec.title.menu;
        assert_eq!(menu.quick.len(), 3);
        assert_eq!(menu.status[0].children.len(), 4);
        assert_eq!(
            menu.status[0]
                .sub
                .as_ref()
                .and_then(|s| s.icon.as_ref())
                .and_then(|i| i.orb),
            Some(HeaderOrb::Working)
        );
        assert_eq!(
            menu.status[2].sub.as_ref().and_then(|s| s.tone),
            Some(HeaderTone::Err)
        );
        assert!(menu.groups[1][1].copy.is_some());
        let trail = spec.trail.as_ref().unwrap();
        assert_eq!(trail.dot, Some(HeaderDot::Warn));
        assert_eq!(
            trail.menu.groups[0][1].checked,
            Some(false),
            "单选项未选中时 checked 显式为 false"
        );
        // 没有视图钮：`trail` 序列化为 null（不省略）
        let bare = HeaderSpec {
            trail: None,
            ..spec
        };
        assert_eq!(serde_json::to_value(&bare).unwrap()["trail"], Value::Null);
        // 撤掉：`{"spec": null}`
        let none: Option<HeaderSpec> = serde_json::from_value(Value::Null).unwrap();
        assert_eq!(json!({ "spec": none }), json!({ "spec": null }));
    }

    #[test]
    fn header_show_fixture_round_trips() {
        let raw: Value = serde_json::from_str(HEADER_SHOW).unwrap();
        let show: HeaderShow = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(show, HeaderShow { alpha: 1.0, ms: 120 });
        assert_eq!(norm(serde_json::to_value(show).unwrap()), norm(raw));
        // ms 可省
        let bare: HeaderShow = serde_json::from_value(json!({"alpha": 0})).unwrap();
        assert_eq!(bare.ms, 0);
    }

    #[test]
    fn header_menu_fixture_round_trips() {
        let raw: Value = serde_json::from_str(HEADER_MENU).unwrap();
        let menu: HeaderMenuCmd = serde_json::from_value(raw.clone()).unwrap();
        assert_eq!(
            menu,
            HeaderMenuCmd {
                open: Some(HeaderMenuKind::Title),
                page: Some("env".into())
            }
        );
        assert_eq!(serde_json::to_value(&menu).unwrap(), raw);
        // 收起：`open` 为 null 且不省略
        let close = HeaderMenuCmd {
            open: None,
            page: None,
        };
        assert_eq!(serde_json::to_value(&close).unwrap(), json!({"open": null}));
    }

    #[test]
    fn header_event_parses() {
        let select: Event = serde_json::from_value(
            json!({"type": "header", "op": "select", "key": "k", "menu": "title", "id": "env/freeze"}),
        )
        .unwrap();
        assert_eq!(
            select,
            Event::Header {
                op: HeaderOp::Select,
                key: "k".into(),
                menu: Some(HeaderMenuKind::Title),
                id: Some("env/freeze".into()),
            }
        );
        let close: Event =
            serde_json::from_value(json!({"type": "header", "op": "menu", "key": "k"})).unwrap();
        assert_eq!(
            close,
            Event::Header {
                op: HeaderOp::Menu,
                key: "k".into(),
                menu: None,
                id: None
            }
        );
    }

    #[test]
    fn header_probe_parses() {
        let probe: HeaderProbe =
            serde_json::from_value(json!({"center": false, "lead": true, "title": true})).unwrap();
        assert_eq!(
            probe,
            HeaderProbe {
                center: false,
                lead: true,
                title: true,
                trail: false
            }
        );
    }
}
