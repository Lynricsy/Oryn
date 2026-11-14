//! 移动端的纯逻辑：导航判定、推送状态归并与订阅同步、分享接收流程、`MobileInfo`。
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
    CornerRadii, Insets, Permission, PushChannel, PushState, ShareIntake,
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
}
