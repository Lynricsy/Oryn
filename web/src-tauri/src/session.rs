//! 连接、配对与会话换取的状态机（连接页是它的投影）。
//!
//! 这里不直接依赖 Tauri：副作用经 [`Shell`]，网络经 [`Api`]，令牌经 [`Vault`]，计时由调用方按返回的
//! [`Next`] 安排。这样配对、换取、重试、吊销、版本过旧的全部分支都能在单测里确定性地走到。

use std::collections::VecDeque;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use serde::Serialize;
use serde_json::json;

use crate::api::{Api, ApiError, PairPoll};
use crate::config::{normalize_server_url, Config, TokenStoreKind, UrlError};
use crate::eventlog::EventLog;
use crate::i18n::Lang;
use crate::secrets::Vault;

/// 远程页面就绪看门狗。
pub const WEB_READY_TIMEOUT: Duration = Duration::from_secs(20);
/// 换取重试退避上限。
pub const MAX_BACKOFF: Duration = Duration::from_secs(30);
/// `host_reauth` 的循环判定窗口与次数。
pub const REAUTH_WINDOW_MS: i64 = 60_000;
pub const REAUTH_LIMIT: usize = 3;

/// 错误码（连接页按它取文案）。
pub mod codes {
    pub const INVALID_URL: &str = "invalid_url";
    pub const INSECURE_URL: &str = "insecure_url";
    pub const UNREACHABLE: &str = "unreachable";
    pub const NOT_ORYN: &str = "not_oryn";
    pub const SERVER_TOO_OLD: &str = "server_too_old";
    pub const PAIR_EXPIRED: &str = "pair_expired";
    pub const PAIR_FAILED: &str = "pair_failed";
    pub const REVOKED: &str = "revoked";
    pub const KEYRING: &str = "keyring";
    pub const SESSION_LOOP: &str = "session_loop";
    pub const WEB_TIMEOUT: &str = "web_timeout";
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct LauncherError {
    pub code: String,
    pub message: String,
}

impl LauncherError {
    pub fn new(code: &str, message: impl Into<String>) -> Self {
        Self {
            code: code.to_string(),
            message: message.into(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(
    tag = "step",
    rename_all = "camelCase",
    rename_all_fields = "camelCase"
)]
pub enum LauncherState {
    Connect {
        server_url: String,
        error: Option<LauncherError>,
    },
    /// 配对链接 / 二维码给出的服务器：只预填，等用户在连接页看清主机名后点「连接」（`launcher_connect`）。
    ConfirmConnect {
        server_url: String,
    },
    Pairing {
        server_url: String,
        code: String,
        verify_url: String,
        expires_at: i64,
    },
    Connecting {
        server_url: String,
    },
    /// 已配对但进不去。
    Error {
        server_url: String,
        error: LauncherError,
    },
    ConfirmChange {
        server_url: String,
    },
    /// 主窗口已在远程页面。
    Connected {
        server_url: String,
    },
}

impl LauncherState {
    fn error_code(&self) -> Option<&str> {
        match self {
            LauncherState::Connect { error, .. } => error.as_ref().map(|e| e.code.as_str()),
            LauncherState::Error { error, .. } => Some(error.code.as_str()),
            _ => None,
        }
    }
}

/// 状态机对外界的副作用。
pub trait Shell: Send + Sync + 'static {
    fn now_ms(&self) -> i64;
    /// 在系统浏览器打开地址。
    fn open_browser(&self, url: &str);
    /// 为 `origin` 开放宿主命令（每进程一次）并把主窗口导航到 `url`。
    fn open_remote(&self, origin: &str, url: &str) -> Result<(), String>;
    /// 主窗口回到连接页（已在连接页时不重载）。
    fn show_launcher(&self);
    /// 通知连接页状态变化。
    fn emit_state(&self, state: &LauncherState);
    /// 清掉 WebView 里 `origin` 的会话 Cookie 与其它站点数据（更换服务器、吊销、令牌丢失时）。
    fn clear_web_data(&self, origin: &str);
    /// 重启应用（丢掉旧 origin 的远程能力）。
    fn restart(&self);
    /// 本机已登记到服务器的推送订阅 id（移动端；桌面恒为 `None`）。
    fn push_subscription_id(&self) -> Option<String>;
    /// 停用本机推送并清掉原生侧的推送状态（移动端；桌面空操作）。删令牌之前调用。
    fn push_disable(&self);
}

/// 调用方需要安排的下一步计时。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Next {
    Idle,
    /// `after` 之后调用 [`Core::poll_tick`]（或拆开的 `poll_request` + `apply_poll`）。
    Poll {
        epoch: u64,
        after: Duration,
    },
    /// `after` 之后调用 [`Core::watchdog_fired`]。
    Watchdog {
        gen: u64,
        after: Duration,
    },
    /// `after` 之后调用 [`Core::retry_fired`]。
    Retry {
        gen: u64,
        after: Duration,
    },
}

/// 页面导航状态：与托盘 / 通知 / 深链接共享（它们在同步回调里读写）。
#[derive(Debug, Default)]
pub struct Nav {
    /// 远程页面已调用 `host_ready`。
    pub web_ready: bool,
    /// 待开路径（`host_ready` 或下一次 `enter_app` 取走）。
    pub pending: Option<String>,
}

pub type SharedNav = Arc<Mutex<Nav>>;

struct Pairing {
    server: String,
    code: String,
    verifier: String,
    verify_url: String,
    expires_at: i64,
    interval: Duration,
}

/// 已进入应用时的服务器与设备令牌（移动端编排推送订阅与分享上传用）。
#[cfg_attr(not(mobile), allow(dead_code))]
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Connection {
    pub base: String,
    pub token: String,
}

/// 轮询请求参数（锁外发起网络请求用）。
pub struct PollRequest {
    pub base: String,
    pub code: String,
    pub verifier: String,
}

pub struct Core<A: Api, S: Shell> {
    api: Arc<A>,
    shell: S,
    log: EventLog,
    vault: Vault,
    dir: PathBuf,
    config: Config,
    state: LauncherState,
    pairing: Option<Pairing>,
    epoch: u64,
    gen: u64,
    retry_attempt: u32,
    last_path: String,
    reauths: VecDeque<i64>,
    nav: SharedNav,
    device_name: String,
    platform: String,
    /// 进入配对后是否自动打开系统浏览器（桌面 true；移动端 false：配对码要先留在屏幕上）。
    auto_open_browser: bool,
}

/// 站内路径校验：以 `/` 开头、不以 `//` 开头、无反斜杠与控制字符、≤2048；不合法 → `/`。
pub fn valid_app_path(path: &str) -> String {
    let ok = path.starts_with('/')
        && !path.starts_with("//")
        && path.len() <= 2048
        && !path.chars().any(|c| c == '\\' || c.is_control());
    if ok {
        path.to_string()
    } else {
        "/".to_string()
    }
}

fn encode(s: &str) -> String {
    url::form_urlencoded::byte_serialize(s.as_bytes()).collect()
}

/// 兑换一次性码的地址：`<origin>/auth/app?code=<码>&next=<percent-encode(path)>`。
pub fn app_session_url(origin: &str, code: &str, path: &str) -> String {
    format!(
        "{origin}/auth/app?code={}&next={}",
        encode(code),
        encode(path)
    )
}

/// 系统浏览器要打开的批准页。服务器给的 `verifyUrl` 只有在 http(s)、与 `server` 同源且不带用户信息时才采用，
/// 否则改用壳自己拼的 `<server>/pair`：不把服务器给的任意 URI（`file:`、`ms-*:`、UNC…）交给系统 opener。
/// 返回 `(地址, 是否替换了服务器给的值)`。
pub fn pair_page_url(server: &str, given: &str) -> (String, bool) {
    let trusted = url::Url::parse(given).ok().filter(|u| {
        matches!(u.scheme(), "http" | "https")
            && u.username().is_empty()
            && u.password().is_none()
            && u.origin().ascii_serialization() == server
    });
    match trusted {
        Some(u) => (u.into(), false),
        None => (format!("{server}/pair"), true),
    }
}

/// 换取失败的退避：2s → 4s → … → 30s。
pub fn backoff(attempt: u32) -> Duration {
    let secs = 1u64 << attempt.clamp(1, 5);
    Duration::from_secs(secs).min(MAX_BACKOFF)
}

impl<A: Api, S: Shell> Core<A, S> {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        api: Arc<A>,
        shell: S,
        log: EventLog,
        vault: Vault,
        dir: PathBuf,
        config: Config,
        nav: SharedNav,
        device_name: String,
        platform: String,
        auto_open_browser: bool,
    ) -> Self {
        let server_url = config.server_url.clone().unwrap_or_default();
        Self {
            api,
            shell,
            log,
            vault,
            dir,
            config,
            state: LauncherState::Connect {
                server_url,
                error: None,
            },
            pairing: None,
            epoch: 0,
            gen: 0,
            retry_attempt: 0,
            last_path: "/".into(),
            reauths: VecDeque::new(),
            nav,
            device_name,
            platform,
            auto_open_browser,
        }
    }

    pub fn state(&self) -> &LauncherState {
        &self.state
    }

    pub fn config(&self) -> &Config {
        &self.config
    }

    /// 网络接口（运行期在锁外发轮询请求用）。
    pub fn api(&self) -> Arc<A> {
        self.api.clone()
    }

    pub fn token_store(&self) -> TokenStoreKind {
        self.vault.kind()
    }

    /// 修改配置并落盘。
    pub fn update_config(&mut self, f: impl FnOnce(&mut Config)) {
        f(&mut self.config);
        self.save_config();
    }

    fn save_config(&self) {
        if let Err(e) = self.config.save(&self.dir) {
            self.log
                .log("config.error", json!({"error": e.to_string()}));
        }
    }

    fn set_state(&mut self, state: LauncherState) {
        self.state = state;
        self.shell.emit_state(&self.state);
    }

    fn server(&self) -> String {
        self.config.server_url.clone().unwrap_or_default()
    }

    fn nav(&self) -> std::sync::MutexGuard<'_, Nav> {
        self.nav.lock().unwrap_or_else(|e| e.into_inner())
    }

    /// 启动：已配对 → 进入应用；否则连接页（命令行给了地址就直接连接）。
    pub async fn start(&mut self, cli_server: Option<&str>) -> Next {
        if self.config.server_url.is_some() {
            let server = self.server();
            match self.vault.get(&server) {
                Ok(Some(_)) => return self.enter_app("/").await,
                Ok(None) => {}
                // 读不了凭据库 ≠ 没配对：报 keyring 错误并保留配置，「重试」重新走 enter_app 读令牌，
                // 而不是落到连接页另起一轮配对
                Err(e) => {
                    self.fail(codes::KEYRING, e.to_string());
                    return Next::Idle;
                }
            }
        }
        match cli_server {
            Some(url) => self.connect(url).await,
            // 只动初始的连接页状态：冷启动的配对链接（深链接）可能已先一步发起了连接
            None => {
                if matches!(self.state, LauncherState::Connect { error: None, .. }) {
                    let server_url = self.server();
                    self.set_state(LauncherState::Connect {
                        server_url,
                        error: None,
                    });
                }
                Next::Idle
            }
        }
    }

    fn connect_error(
        &mut self,
        server_url: String,
        code: &str,
        message: impl Into<String>,
    ) -> Next {
        self.set_state(LauncherState::Connect {
            server_url,
            error: Some(LauncherError::new(code, message)),
        });
        Next::Idle
    }

    /// `launcher_connect`：校验地址 → 健康检查 → 发起配对 →（桌面）系统浏览器打开批准页 → 轮询。
    /// 地址也可以是配对二维码的内容 `oryn://pair?server=…`（用户在连接页里亲手提交的）。
    /// 只有用户的这一步会发起连接：深链接进来的地址先经 [`Self::propose`] 停在 `ConfirmConnect`。
    pub async fn connect(&mut self, input: &str) -> Next {
        if !matches!(
            self.state,
            LauncherState::Connect { .. } | LauncherState::ConfirmConnect { .. }
        ) {
            return Next::Idle;
        }
        let pair = crate::deeplink::parse_pair(input);
        if let Some(server) = &pair {
            self.log
                .log("pair.link", json!({"server": server, "source": "connect"}));
        }
        let input = pair.as_deref().unwrap_or(input);
        let server = match normalize_server_url(input) {
            Ok(s) => s,
            Err(UrlError::Insecure) => {
                return self.connect_error(input.trim().into(), codes::INSECURE_URL, "")
            }
            Err(UrlError::Invalid) => {
                return self.connect_error(input.trim().into(), codes::INVALID_URL, "")
            }
        };
        self.set_state(LauncherState::Connecting {
            server_url: server.clone(),
        });
        match self.api.healthz(&server).await {
            Ok(_) => {}
            Err(ApiError::Network(m)) => return self.connect_error(server, codes::UNREACHABLE, m),
            Err(e) => return self.connect_error(server, codes::NOT_ORYN, e.to_string()),
        }
        let start = match self
            .api
            .pair_start(&server, &self.device_name, &self.platform)
            .await
        {
            Ok(s) => s,
            Err(ApiError::Network(m)) => return self.connect_error(server, codes::UNREACHABLE, m),
            Err(e) => return self.connect_error(server, codes::PAIR_FAILED, e.to_string()),
        };
        let (verify_url, replaced) = pair_page_url(&server, &start.verify_url);
        if replaced {
            self.log.log(
                "pair.verify_url_replaced",
                json!({"given": crate::eventlog::redact_url(&start.verify_url), "used": verify_url}),
            );
        }
        self.log.log(
            "pair.started",
            json!({"server": server, "code": start.code, "verifyUrl": verify_url}),
        );
        if self.auto_open_browser {
            self.shell.open_browser(&verify_url);
            self.log.log("browser.open", json!({"url": verify_url}));
        }
        self.epoch += 1;
        let interval = Duration::from_secs(start.interval.clamp(1, 30));
        self.pairing = Some(Pairing {
            server: server.clone(),
            code: start.code.clone(),
            verifier: start.verifier,
            verify_url: verify_url.clone(),
            expires_at: start.expires_at,
            interval,
        });
        self.set_state(LauncherState::Pairing {
            server_url: server,
            code: start.code,
            verify_url,
            expires_at: start.expires_at,
        });
        Next::Poll {
            epoch: self.epoch,
            after: interval,
        }
    }

    /// 当前轮询的参数；没有进行中的配对时 `None`。
    pub fn poll_request(&self, epoch: u64) -> Option<PollRequest> {
        let p = self.pairing.as_ref().filter(|_| epoch == self.epoch)?;
        Some(PollRequest {
            base: p.server.clone(),
            code: p.code.clone(),
            verifier: p.verifier.clone(),
        })
    }

    /// 处理一次轮询结果。
    pub async fn apply_poll(&mut self, epoch: u64, result: Result<PairPoll, ApiError>) -> Next {
        let Some(p) = self.pairing.as_ref().filter(|_| epoch == self.epoch) else {
            return Next::Idle;
        };
        let (server, interval, expires_at) = (p.server.clone(), p.interval, p.expires_at);
        match result {
            Ok(PairPoll::Approved {
                device_token,
                device_id,
            }) => self.approve(server, device_token, device_id).await,
            Ok(PairPoll::Pending) | Err(ApiError::Network(_)) => {
                if self.shell.now_ms() >= expires_at {
                    self.pairing = None;
                    return self.connect_error(server, codes::PAIR_EXPIRED, "");
                }
                Next::Poll {
                    epoch,
                    after: interval,
                }
            }
            Err(ApiError::Status { status: 404, .. })
            | Err(ApiError::Status { status: 410, .. }) => {
                self.pairing = None;
                self.connect_error(server, codes::PAIR_EXPIRED, "")
            }
            Err(e) => {
                self.pairing = None;
                self.connect_error(server, codes::PAIR_FAILED, e.to_string())
            }
        }
    }

    /// 轮询一次（请求与处理连在一起；运行期为了不在请求期间占锁，拆成 `poll_request` + `apply_poll`）。
    #[cfg(test)]
    pub async fn poll_tick(&mut self, epoch: u64) -> Next {
        let Some(req) = self.poll_request(epoch) else {
            return Next::Idle;
        };
        let result = self
            .api
            .pair_poll(&req.base, &req.code, &req.verifier)
            .await;
        self.apply_poll(epoch, result).await
    }

    async fn approve(&mut self, server: String, token: String, device_id: String) -> Next {
        self.pairing = None;
        let kind = match self.vault.set(&server, &token) {
            Ok(kind) => kind,
            Err(e) => return self.connect_error(server, codes::KEYRING, e.to_string()),
        };
        self.config.server_url = Some(server);
        self.config.device_id = Some(device_id.clone());
        self.config.token_store = kind;
        self.save_config();
        self.log
            .log("pair.approved", json!({"deviceId": device_id}));
        self.log
            .log("token.stored", json!({"store": kind.as_str()}));
        self.enter_app("/").await
    }

    /// `launcher_cancel`：终止轮询回到连接页；或放弃链接给出的服务器（不留预填，免得误点）。
    pub fn cancel(&mut self) {
        if let Some(p) = self.pairing.take() {
            self.epoch += 1;
            self.set_state(LauncherState::Connect {
                server_url: p.server,
                error: None,
            });
        } else if matches!(self.state, LauncherState::ConfirmConnect { .. }) {
            self.log.log("pair.link_dismissed", json!({}));
            self.set_state(LauncherState::Connect {
                server_url: String::new(),
                error: None,
            });
        }
    }

    /// 配对链接（深链接、主框架里点到的 `oryn://pair`）：链接的来源不可信，这里绝不发起连接——
    /// 只在尚未配对、停在连接页（含上一个待确认的链接）时把地址预填进 `ConfirmConnect`，由用户确认后走 [`Self::connect`]。
    /// 地址不合法 → 连接页显示对应错误；配对、连接进行中或已配对时忽略。
    pub fn propose(&mut self, input: &str) {
        if self.config.server_url.is_some()
            || !matches!(
                self.state,
                LauncherState::Connect { .. } | LauncherState::ConfirmConnect { .. }
            )
        {
            self.log
                .log("pair.link_ignored", json!({"reason": "busy_or_paired"}));
            return;
        }
        match normalize_server_url(input) {
            Ok(server_url) => self.set_state(LauncherState::ConfirmConnect { server_url }),
            Err(UrlError::Insecure) => {
                self.connect_error(input.trim().into(), codes::INSECURE_URL, "");
            }
            Err(UrlError::Invalid) => {
                self.connect_error(input.trim().into(), codes::INVALID_URL, "");
            }
        }
    }

    /// `launcher_open_browser`：重新打开批准页。
    pub fn reopen_browser(&self) {
        if let Some(p) = &self.pairing {
            self.shell.open_browser(&p.verify_url);
            self.log.log("browser.open", json!({"url": p.verify_url}));
        }
    }

    fn fail(&mut self, code: &str, message: impl Into<String>) {
        let server_url = self.server();
        self.shell.show_launcher();
        self.set_state(LauncherState::Error {
            server_url,
            error: LauncherError::new(code, message),
        });
    }

    fn revoked(&mut self, server: String) -> Next {
        self.shell.push_disable();
        let _ = self.vault.delete(&server);
        self.shell.clear_web_data(&server);
        self.config.device_id = None;
        self.save_config();
        self.log.log("disconnect", json!({"reason": "revoked"}));
        self.shell.show_launcher();
        self.connect_error(server, codes::REVOKED, "")
    }

    /// 用设备令牌换一次性码，把主窗口导航到兑换地址，并启动看门狗。
    pub async fn enter_app(&mut self, path: &str) -> Next {
        let Some(server) = self.config.server_url.clone() else {
            self.set_state(LauncherState::Connect {
                server_url: String::new(),
                error: None,
            });
            return Next::Idle;
        };
        self.gen += 1;
        let gen = self.gen;
        self.last_path = valid_app_path(path);
        self.set_state(LauncherState::Connecting {
            server_url: server.clone(),
        });
        let token = match self.vault.get(&server) {
            Ok(Some(t)) => t,
            Ok(None) => {
                self.shell.show_launcher();
                self.shell.clear_web_data(&server);
                return self.connect_error(server, codes::REVOKED, "");
            }
            Err(e) => {
                self.fail(codes::KEYRING, e.to_string());
                return Next::Idle;
            }
        };
        match self.api.app_session(&server, &token).await {
            Ok(code) => {
                // 待开路径在请求返回后才取：等待期间点的深链接 / 通知 / 托盘线程要进这次兑换地址
                let consumed = self.nav().pending.clone();
                let target = consumed.clone().unwrap_or_else(|| self.last_path.clone());
                let url = app_session_url(&server, &code, &target);
                if let Err(e) = self.shell.open_remote(&server, &url) {
                    self.fail(codes::WEB_TIMEOUT, e);
                    return Next::Idle;
                }
                // 只清掉本次带走的那一个：导航发出后才到的路径留给新页面的 host_ready
                {
                    let mut nav = self.nav();
                    if nav.pending == consumed {
                        nav.pending = None;
                    }
                }
                self.retry_attempt = 0;
                self.log.log("session.minted", json!({"server": server}));
                Next::Watchdog {
                    gen,
                    after: WEB_READY_TIMEOUT,
                }
            }
            Err(ApiError::Status { status: 401, .. }) => self.revoked(server),
            Err(ApiError::Status {
                status: 403, code, ..
            }) if code == "app_session_unsupported" => self.revoked(server),
            Err(ApiError::Status {
                status: 404 | 405, ..
            }) => {
                self.fail(codes::SERVER_TOO_OLD, "");
                Next::Idle
            }
            Err(ApiError::NotOryn) => {
                self.fail(codes::NOT_ORYN, "");
                Next::Idle
            }
            Err(e) => {
                self.fail(codes::UNREACHABLE, e.to_string());
                self.retry_attempt += 1;
                Next::Retry {
                    gen,
                    after: backoff(self.retry_attempt),
                }
            }
        }
    }

    /// 自动重连计时到点。
    pub async fn retry_fired(&mut self, gen: u64) -> Next {
        if gen != self.gen || self.state.error_code() != Some(codes::UNREACHABLE) {
            return Next::Idle;
        }
        let path = self.last_path.clone();
        self.enter_app(&path).await
    }

    /// `launcher_retry`：错误态下立即重试。
    pub async fn retry(&mut self) -> Next {
        if !matches!(self.state, LauncherState::Error { .. }) {
            return Next::Idle;
        }
        let path = self.last_path.clone();
        self.enter_app(&path).await
    }

    /// 看门狗到点：远程页面仍未就绪 → 回连接页 `web_timeout`。
    pub fn watchdog_fired(&mut self, gen: u64) {
        if gen != self.gen || self.nav().web_ready {
            return;
        }
        self.gen += 1;
        self.fail(codes::WEB_TIMEOUT, "");
    }

    /// 远程页面就绪：停看门狗、状态 `Connected`、保存语言；返回并清空待开路径。
    pub fn host_ready(&mut self, origin: &str, bridge: u32, lang: &str) -> Option<String> {
        self.gen += 1;
        let lang = Lang::from_tag(lang);
        if self.config.lang != lang.as_str() {
            self.config.lang = lang.as_str().to_string();
            self.save_config();
        }
        let pending = {
            let mut nav = self.nav();
            nav.web_ready = true;
            nav.pending.take()
        };
        let server_url = self.server();
        self.set_state(LauncherState::Connected { server_url });
        self.log.log(
            "web.ready",
            json!({"origin": origin, "bridge": bridge, "lang": lang.as_str()}),
        );
        pending
    }

    /// 远程页面收到 401：60 秒内第 3 次判为循环，否则重新换取。
    pub async fn reauth(&mut self, path: &str) -> Next {
        let path = valid_app_path(path);
        self.log.log("reauth", json!({"path": path}));
        let now = self.shell.now_ms();
        self.reauths.push_back(now);
        while self
            .reauths
            .front()
            .is_some_and(|t| now - *t > REAUTH_WINDOW_MS)
        {
            self.reauths.pop_front();
        }
        self.nav().web_ready = false;
        if self.reauths.len() >= REAUTH_LIMIT {
            self.reauths.clear();
            self.gen += 1;
            self.fail(codes::SESSION_LOOP, "");
            return Next::Idle;
        }
        self.enter_app(&path).await
    }

    /// `host_change_server` / 托盘「更换服务器…」：回连接页等待确认。
    pub fn request_change(&mut self) {
        self.gen += 1;
        if self.pairing.take().is_some() {
            self.epoch += 1;
        }
        self.nav().web_ready = false;
        self.shell.show_launcher();
        let server_url = self.server();
        if self.config.server_url.is_some() {
            self.set_state(LauncherState::ConfirmChange { server_url });
        } else {
            self.set_state(LauncherState::Connect {
                server_url,
                error: None,
            });
        }
    }

    /// `launcher_change_server{confirm}`：确认 → 尽力自我吊销、删令牌、清配置、重启；取消 → 回到应用。
    pub async fn change_server(&mut self, confirm: bool) -> Next {
        let Some(server) = self.config.server_url.clone() else {
            self.set_state(LauncherState::Connect {
                server_url: String::new(),
                error: None,
            });
            return Next::Idle;
        };
        if !confirm {
            return self.enter_app("/").await;
        }
        if let Ok(Some(token)) = self.vault.get(&server) {
            if let Some(id) = self.shell.push_subscription_id() {
                let _ = self.api.push_unsubscribe(&server, &token, &id).await;
            }
            if let Some(device_id) = self.config.device_id.clone() {
                let _ = self.api.revoke_self(&server, &token, &device_id).await;
            }
        }
        self.shell.push_disable();
        let _ = self.vault.delete(&server);
        // 自我吊销可能失败（离线）：旧服务器的 App 会话 Cookie 不能留在 WebView 存储里
        self.shell.clear_web_data(&server);
        self.config.server_url = None;
        self.config.device_id = None;
        self.save_config();
        self.log.log("disconnect", json!({"reason": "user"}));
        self.set_state(LauncherState::Connect {
            server_url: String::new(),
            error: None,
        });
        self.shell.restart();
        Next::Idle
    }

    #[cfg_attr(not(mobile), allow(dead_code))]
    /// 连接页的脚本跑起来了（移动端 `launcher_state`）。此时状态仍是 `Connected` = 远程页面已就绪过、
    /// 主框架却回到了连接页：只有 WebView 被重建才会这样（Android 改字体大小等未声明的配置变更会重建 Activity，
    /// wry 按创建时的地址重建 WebView）。用设备令牌重新换取会话回到应用，路径取待开路径（重建前所在页面）或上次的路径。
    /// 冷启动时连接页脚本可能晚于兑换跳转才执行，那时状态是 `Connecting`，不重复换取。
    pub async fn launcher_loaded(&mut self) -> Next {
        if !matches!(self.state, LauncherState::Connected { .. }) {
            return Next::Idle;
        }
        self.nav().web_ready = false;
        let pending = self.nav().pending.clone();
        self.log.log("session.restore", json!({"path": pending}));
        let path = self.last_path.clone();
        self.enter_app(&path).await
    }

    #[cfg_attr(not(mobile), allow(dead_code))]
    /// 回到前台（移动端）：配对中 → 立即轮询一次（换一代轮询，旧计时作废）；`unreachable` 错误 → 立即重试；
    /// 其余不动。
    pub fn resumed(&mut self) -> Next {
        match &self.state {
            LauncherState::Pairing { .. } if self.pairing.is_some() => {
                self.epoch += 1;
                Next::Poll {
                    epoch: self.epoch,
                    after: Duration::ZERO,
                }
            }
            LauncherState::Error { error, .. } if error.code == codes::UNREACHABLE => Next::Retry {
                gen: self.gen,
                after: Duration::ZERO,
            },
            _ => Next::Idle,
        }
    }

    #[cfg_attr(not(mobile), allow(dead_code))]
    /// 进入后台（移动端）：取消 `unreachable` 的退避重试（回到前台时由 [`Self::resumed`] 重试）。
    pub fn stopped(&mut self) {
        if self.state.error_code() == Some(codes::UNREACHABLE) {
            self.gen += 1;
        }
    }

    /// 已进入应用时的服务器与令牌；连接页阶段、读不到令牌时 `None`。
    #[cfg_attr(not(mobile), allow(dead_code))]
    pub fn connection(&self) -> Option<Connection> {
        let LauncherState::Connected { server_url } = &self.state else {
            return None;
        };
        let token = self.vault.get(server_url).ok().flatten()?;
        Some(Connection {
            base: server_url.clone(),
            token,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{Health, PairStart};
    use crate::eventlog::testing::{memory, Buffer};
    use crate::secrets::testing::FakeStore;
    use crate::secrets::{StoreError, TokenStore};
    use std::collections::BTreeMap;
    use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
    use tokio::sync::{oneshot, Notify};

    const SERVER: &str = "https://oryn.example.com";

    #[derive(Default)]
    struct FakeApi {
        healthz: Mutex<VecDeque<Result<Health, ApiError>>>,
        pair_start: Mutex<VecDeque<Result<PairStart, ApiError>>>,
        pair_poll: Mutex<VecDeque<Result<PairPoll, ApiError>>>,
        app_session: Mutex<VecDeque<Result<String, ApiError>>>,
        tokens_seen: Mutex<Vec<String>>,
        revoked: Mutex<Vec<(String, String)>>,
        unsubscribed: Mutex<Vec<(String, String)>>,
        /// 下一次 `app_session` 进来时先 `notify` 第一个、再等第二个放行（模拟挂起中的网络请求）。
        gate: Mutex<Option<(Arc<Notify>, oneshot::Receiver<()>)>>,
    }

    fn pop<T>(q: &Mutex<VecDeque<Result<T, ApiError>>>) -> Result<T, ApiError> {
        q.lock()
            .unwrap()
            .pop_front()
            .unwrap_or(Err(ApiError::Network("unscripted".into())))
    }

    impl Api for FakeApi {
        async fn healthz(&self, _base: &str) -> Result<Health, ApiError> {
            pop(&self.healthz)
        }
        async fn pair_start(
            &self,
            _: &str,
            name: &str,
            platform: &str,
        ) -> Result<PairStart, ApiError> {
            assert_eq!(name, "box");
            assert_eq!(platform, "linux-x64");
            pop(&self.pair_start)
        }
        async fn pair_poll(
            &self,
            _: &str,
            code: &str,
            verifier: &str,
        ) -> Result<PairPoll, ApiError> {
            assert_eq!(code, "ABCD-EFGH");
            assert_eq!(verifier, "ver");
            pop(&self.pair_poll)
        }
        async fn app_session(&self, _: &str, token: &str) -> Result<String, ApiError> {
            self.tokens_seen.lock().unwrap().push(token.into());
            let gate = self.gate.lock().unwrap().take();
            if let Some((entered, release)) = gate {
                entered.notify_one();
                let _ = release.await;
            }
            pop(&self.app_session)
        }
        async fn revoke_self(&self, _: &str, token: &str, device_id: &str) -> Result<(), ApiError> {
            self.revoked
                .lock()
                .unwrap()
                .push((token.into(), device_id.into()));
            Ok(())
        }
        async fn vapid_key(&self, _: &str, _: &str) -> Result<String, ApiError> {
            unreachable!("session does not fetch VAPID keys")
        }
        async fn push_subscribe(
            &self,
            _: &str,
            _: &str,
            _: &crate::api::PushSubscription,
        ) -> Result<String, ApiError> {
            unreachable!("session does not subscribe")
        }
        async fn push_unsubscribe(&self, _: &str, token: &str, id: &str) -> Result<(), ApiError> {
            self.unsubscribed
                .lock()
                .unwrap()
                .push((token.into(), id.into()));
            Ok(())
        }
        async fn upload_attachment(
            &self,
            _: &str,
            _: &str,
            _: &str,
            _: &std::path::Path,
        ) -> Result<serde_json::Value, ApiError> {
            unreachable!("session does not upload")
        }
    }

    #[derive(Debug, Clone, PartialEq, Eq)]
    enum Call {
        Browser(String),
        Remote(String, String),
        Launcher,
        ClearWebData(String),
        Restart,
        PushDisable,
    }

    #[derive(Clone, Default)]
    struct FakeShell {
        now: Arc<AtomicI64>,
        calls: Arc<Mutex<Vec<Call>>>,
        states: Arc<Mutex<Vec<LauncherState>>>,
        /// `open_remote` 时把这条路径写进待开槽（模拟导航发出后才到的深链接）。
        pending_on_remote: Arc<Mutex<Option<(SharedNav, String)>>>,
        /// 本机推送订阅 id。
        push_id: Arc<Mutex<Option<String>>>,
    }

    impl Shell for FakeShell {
        fn now_ms(&self) -> i64 {
            self.now.load(Ordering::SeqCst)
        }
        fn open_browser(&self, url: &str) {
            self.calls.lock().unwrap().push(Call::Browser(url.into()));
        }
        fn open_remote(&self, origin: &str, url: &str) -> Result<(), String> {
            self.calls
                .lock()
                .unwrap()
                .push(Call::Remote(origin.into(), url.into()));
            if let Some((nav, path)) = self.pending_on_remote.lock().unwrap().take() {
                nav.lock().unwrap().pending = Some(path);
            }
            Ok(())
        }
        fn show_launcher(&self) {
            self.calls.lock().unwrap().push(Call::Launcher);
        }
        fn emit_state(&self, state: &LauncherState) {
            self.states.lock().unwrap().push(state.clone());
        }
        fn clear_web_data(&self, origin: &str) {
            self.calls
                .lock()
                .unwrap()
                .push(Call::ClearWebData(origin.into()));
        }
        fn restart(&self) {
            self.calls.lock().unwrap().push(Call::Restart);
        }
        fn push_subscription_id(&self) -> Option<String> {
            self.push_id.lock().unwrap().clone()
        }
        fn push_disable(&self) {
            *self.push_id.lock().unwrap() = None;
            self.calls.lock().unwrap().push(Call::PushDisable);
        }
    }

    /// 读令牌可切换为失败的凭据库（模拟 Keychain 被拒、Secret Service 暂时不可用）。
    #[derive(Clone, Default)]
    struct FlakyStore {
        map: Arc<Mutex<BTreeMap<String, String>>>,
        fail: Arc<AtomicBool>,
    }

    impl TokenStore for FlakyStore {
        fn get(&self, server: &str) -> Result<Option<String>, StoreError> {
            if self.fail.load(Ordering::SeqCst) {
                return Err(StoreError::Failed("access denied".into()));
            }
            Ok(self.map.lock().unwrap().get(server).cloned())
        }
        fn set(&self, server: &str, token: &str) -> Result<(), StoreError> {
            self.map.lock().unwrap().insert(server.into(), token.into());
            Ok(())
        }
        fn delete(&self, server: &str) -> Result<(), StoreError> {
            self.map.lock().unwrap().remove(server);
            Ok(())
        }
    }

    struct Rig {
        core: Core<FakeApi, FakeShell>,
        api: Arc<FakeApi>,
        shell: FakeShell,
        store: FakeStore,
        log: Buffer,
        nav: SharedNav,
        dir: tempfile::TempDir,
    }

    fn rig(config: Config, store: FakeStore) -> Rig {
        rig_with(config, store, true)
    }

    fn rig_with(config: Config, store: FakeStore, auto_open_browser: bool) -> Rig {
        let dir = tempfile::tempdir().unwrap();
        let api = Arc::new(FakeApi::default());
        let shell = FakeShell::default();
        shell.now.store(1_000, Ordering::SeqCst);
        let (log, buf) = memory();
        let nav = SharedNav::default();
        let vault =
            Vault::with_fallback(Box::new(store.clone()), TokenStoreKind::Keyring, dir.path());
        let core = Core::new(
            api.clone(),
            shell.clone(),
            log,
            vault,
            dir.path().to_path_buf(),
            config,
            nav.clone(),
            "box".into(),
            "linux-x64".into(),
            auto_open_browser,
        );
        Rig {
            core,
            api,
            shell,
            store,
            log: buf,
            nav,
            dir,
        }
    }

    fn fresh() -> Rig {
        rig(Config::default(), FakeStore::default())
    }

    fn paired() -> Rig {
        let store = FakeStore::default();
        store
            .map
            .lock()
            .unwrap()
            .insert(SERVER.into(), "odt_secret".into());
        let config = Config {
            server_url: Some(SERVER.into()),
            device_id: Some("dev_1".into()),
            ..Config::default()
        };
        rig(config, store)
    }

    fn healthy(r: &Rig) {
        r.api.healthz.lock().unwrap().push_back(Ok(Health {
            ok: true,
            version: "v".into(),
        }));
    }

    fn start_ok(r: &Rig, expires_at: i64) {
        r.api.pair_start.lock().unwrap().push_back(Ok(PairStart {
            code: "ABCD-EFGH".into(),
            verifier: "ver".into(),
            verify_url: format!("{SERVER}/pair"),
            expires_at,
            interval: 2,
        }));
    }

    fn err_code(state: &LauncherState) -> Option<&str> {
        state.error_code()
    }

    /// 事件日志里的事件名（按序）。
    fn events(b: &Buffer) -> Vec<String> {
        b.events()
            .iter()
            .map(|e| e["event"].as_str().unwrap_or_default().to_string())
            .collect()
    }

    impl Drop for Rig {
        fn drop(&mut self) {
            // 任何测试结束时，事件日志里都不能出现设备令牌、verifier 与一次性码
            let text = self.log.text();
            assert!(!text.contains("odt_"), "event log leaked a token: {text}");
            assert!(!text.contains("oac_"), "event log leaked a code: {text}");
            assert!(
                !text.contains("\"ver\""),
                "event log leaked verifier: {text}"
            );
        }
    }

    #[tokio::test]
    async fn connect_pair_approve_enter() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        let next = r.core.connect("oryn.example.com/some/path").await;
        let epoch = match next {
            Next::Poll { epoch, after } => {
                assert_eq!(after, Duration::from_secs(2));
                epoch
            }
            other => panic!("{other:?}"),
        };
        assert_eq!(
            r.core.state(),
            &LauncherState::Pairing {
                server_url: SERVER.into(),
                code: "ABCD-EFGH".into(),
                verify_url: format!("{SERVER}/pair"),
                expires_at: 600_000,
            }
        );
        assert_eq!(
            r.shell.calls.lock().unwrap()[0],
            Call::Browser(format!("{SERVER}/pair"))
        );

        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Ok(PairPoll::Pending));
        assert!(matches!(r.core.poll_tick(epoch).await, Next::Poll { .. }));

        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Ok(PairPoll::Approved {
                device_token: "odt_new".into(),
                device_id: "dev_9".into(),
            }));
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_code".into()));
        let next = r.core.poll_tick(epoch).await;
        assert!(matches!(
            next,
            Next::Watchdog {
                after: WEB_READY_TIMEOUT,
                ..
            }
        ));
        assert_eq!(
            r.core.state(),
            &LauncherState::Connecting {
                server_url: SERVER.into()
            }
        );
        assert_eq!(
            r.store.map.lock().unwrap().get(SERVER).map(String::as_str),
            Some("odt_new")
        );
        assert_eq!(r.api.tokens_seen.lock().unwrap().as_slice(), ["odt_new"]);
        let saved = Config::load(r.dir.path());
        assert_eq!(saved.server_url.as_deref(), Some(SERVER));
        assert_eq!(saved.device_id.as_deref(), Some("dev_9"));
        assert_eq!(saved.token_store, TokenStoreKind::Keyring);
        assert_eq!(
            r.shell.calls.lock().unwrap().last().unwrap(),
            &Call::Remote(
                SERVER.into(),
                format!("{SERVER}/auth/app?code=oac_code&next=%2F")
            )
        );
        let events: Vec<String> = r
            .log
            .events()
            .iter()
            .map(|e| e["event"].as_str().unwrap().to_string())
            .collect();
        assert_eq!(
            events,
            [
                "pair.started",
                "browser.open",
                "pair.approved",
                "token.stored",
                "session.minted"
            ]
        );
        let started = &r.log.events()[0];
        assert_eq!(started["code"], "ABCD-EFGH");
        assert_eq!(started["verifyUrl"], format!("{SERVER}/pair"));

        // 远程页面就绪
        r.nav.lock().unwrap().pending = Some("/t/T-1".into());
        let pending = r.core.host_ready(SERVER, 1, "en");
        assert_eq!(pending.as_deref(), Some("/t/T-1"));
        assert_eq!(
            r.core.state(),
            &LauncherState::Connected {
                server_url: SERVER.into()
            }
        );
        assert!(r.nav.lock().unwrap().web_ready);
        assert_eq!(Config::load(r.dir.path()).lang, "en");
    }

    #[tokio::test]
    async fn connect_rejects_bad_urls() {
        let mut r = fresh();
        r.core.connect("http://example.com").await;
        assert_eq!(err_code(r.core.state()), Some(codes::INSECURE_URL));
        r.core.connect("ftp://x").await;
        assert_eq!(err_code(r.core.state()), Some(codes::INVALID_URL));
        r.api
            .healthz
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("refused".into())));
        r.core.connect(SERVER).await;
        assert_eq!(err_code(r.core.state()), Some(codes::UNREACHABLE));
        r.api
            .healthz
            .lock()
            .unwrap()
            .push_back(Err(ApiError::NotOryn));
        r.core.connect(SERVER).await;
        assert_eq!(err_code(r.core.state()), Some(codes::NOT_ORYN));
        healthy(&r);
        r.api
            .pair_start
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 500,
                code: "internal".into(),
                message: "x".into(),
            }));
        r.core.connect(SERVER).await;
        assert_eq!(err_code(r.core.state()), Some(codes::PAIR_FAILED));
        assert!(r.shell.calls.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn cancel_stops_polling() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.core.reopen_browser();
        assert_eq!(r.shell.calls.lock().unwrap().len(), 2);
        r.core.cancel();
        assert_eq!(
            r.core.state(),
            &LauncherState::Connect {
                server_url: SERVER.into(),
                error: None
            }
        );
        // 旧轮询到点：不再请求
        assert_eq!(r.core.poll_tick(epoch).await, Next::Idle);
        assert!(r.api.pair_poll.lock().unwrap().is_empty());
    }

    #[tokio::test]
    async fn pairing_expires() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 5_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.shell.now.store(5_000, Ordering::SeqCst);
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Ok(PairPoll::Pending));
        assert_eq!(r.core.poll_tick(epoch).await, Next::Idle);
        assert_eq!(err_code(r.core.state()), Some(codes::PAIR_EXPIRED));

        // 服务端 404 同样视为过期
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 404,
                code: "not_found".into(),
                message: String::new(),
            }));
        r.core.poll_tick(epoch).await;
        assert_eq!(err_code(r.core.state()), Some(codes::PAIR_EXPIRED));

        // 其它状态码 → pair_failed；网络错误继续轮询
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("blip".into())));
        assert!(matches!(r.core.poll_tick(epoch).await, Next::Poll { .. }));
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 403,
                code: "forbidden".into(),
                message: String::new(),
            }));
        r.core.poll_tick(epoch).await;
        assert_eq!(err_code(r.core.state()), Some(codes::PAIR_FAILED));
    }

    #[tokio::test]
    async fn keyring_failure_is_reported() {
        let store = FakeStore {
            unavailable: true,
            ..Default::default()
        };
        let mut r = rig(Config::default(), store.clone());
        // 不允许回退的平台（macOS / Windows）
        r.core.vault = Vault::with_store(Box::new(store), TokenStoreKind::Keyring);
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Ok(PairPoll::Approved {
                device_token: "odt_new".into(),
                device_id: "dev_9".into(),
            }));
        assert_eq!(r.core.poll_tick(epoch).await, Next::Idle);
        assert_eq!(err_code(r.core.state()), Some(codes::KEYRING));
        assert_eq!(r.core.config().server_url, None);
    }

    #[tokio::test]
    async fn linux_falls_back_to_file_store() {
        let store = FakeStore {
            unavailable: true,
            ..Default::default()
        };
        let mut r = rig(Config::default(), store);
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        r.api
            .pair_poll
            .lock()
            .unwrap()
            .push_back(Ok(PairPoll::Approved {
                device_token: "odt_new".into(),
                device_id: "dev_9".into(),
            }));
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_x".into()));
        r.core.poll_tick(epoch).await;
        assert_eq!(r.core.token_store(), TokenStoreKind::File);
        assert_eq!(Config::load(r.dir.path()).token_store, TokenStoreKind::File);
        let stored = r
            .log
            .events()
            .into_iter()
            .find(|e| e["event"] == "token.stored")
            .unwrap();
        assert_eq!(stored["store"], "file");
    }

    #[tokio::test]
    async fn start_when_paired_enters_app() {
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        r.nav.lock().unwrap().pending = Some("/t/T-abc".into());
        let next = r.core.start(None).await;
        assert!(matches!(next, Next::Watchdog { .. }));
        assert_eq!(
            r.shell.calls.lock().unwrap().as_slice(),
            [Call::Remote(
                SERVER.into(),
                format!("{SERVER}/auth/app?code=oac_1&next=%2Ft%2FT-abc")
            )]
        );
        // 待开路径被兑换地址带走
        assert_eq!(r.nav.lock().unwrap().pending, None);
        assert_eq!(r.api.tokens_seen.lock().unwrap().as_slice(), ["odt_secret"]);
    }

    #[tokio::test]
    async fn start_unpaired_with_cli_server_connects() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        assert!(matches!(
            r.core.start(Some("https://oryn.example.com")).await,
            Next::Poll { .. }
        ));
        let mut r = fresh();
        assert_eq!(r.core.start(None).await, Next::Idle);
        assert!(matches!(
            r.core.state(),
            LauncherState::Connect { error: None, .. }
        ));
    }

    #[tokio::test]
    async fn revoked_deletes_token() {
        for err in [
            ApiError::Status {
                status: 401,
                code: "unauthorized".into(),
                message: String::new(),
            },
            ApiError::Status {
                status: 403,
                code: "app_session_unsupported".into(),
                message: String::new(),
            },
        ] {
            let mut r = paired();
            r.api.app_session.lock().unwrap().push_back(Err(err));
            assert_eq!(r.core.start(None).await, Next::Idle);
            assert_eq!(
                r.core.state(),
                &LauncherState::Connect {
                    server_url: SERVER.into(),
                    error: Some(LauncherError::new(codes::REVOKED, ""))
                }
            );
            assert!(r.store.map.lock().unwrap().is_empty());
            let cfg = Config::load(r.dir.path());
            assert_eq!(cfg.device_id, None);
            assert_eq!(cfg.server_url.as_deref(), Some(SERVER));
            let ev = r.log.events();
            assert_eq!(ev.last().unwrap()["event"], "disconnect");
            assert_eq!(ev.last().unwrap()["reason"], "revoked");
            assert!(r.shell.calls.lock().unwrap().contains(&Call::Launcher));
        }
    }

    #[tokio::test]
    async fn server_too_old() {
        for status in [404, 405] {
            let mut r = paired();
            r.api
                .app_session
                .lock()
                .unwrap()
                .push_back(Err(ApiError::Status {
                    status,
                    code: String::new(),
                    message: String::new(),
                }));
            assert_eq!(r.core.start(None).await, Next::Idle);
            assert_eq!(err_code(r.core.state()), Some(codes::SERVER_TOO_OLD));
            assert!(matches!(r.core.state(), LauncherState::Error { .. }));
            // 令牌保留
            assert_eq!(r.store.map.lock().unwrap().len(), 1);
        }
    }

    #[tokio::test]
    async fn unreachable_retries_with_backoff() {
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("down".into())));
        let Next::Retry { gen, after } = r.core.start(None).await else {
            panic!()
        };
        assert_eq!(after, Duration::from_secs(2));
        assert_eq!(err_code(r.core.state()), Some(codes::UNREACHABLE));
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("down".into())));
        let Next::Retry { gen: gen2, after } = r.core.retry_fired(gen).await else {
            panic!()
        };
        assert_eq!(after, Duration::from_secs(4));
        // 过期的计时器不起作用
        assert_eq!(r.core.retry_fired(gen).await, Next::Idle);
        // 手动重试立即发起并成功
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_ok".into()));
        assert!(matches!(r.core.retry().await, Next::Watchdog { .. }));
        assert_eq!(r.core.retry_fired(gen2).await, Next::Idle);
    }

    #[test]
    fn backoff_sequence() {
        let secs: Vec<u64> = (1..=7).map(|a| backoff(a).as_secs()).collect();
        assert_eq!(secs, [2, 4, 8, 16, 30, 30, 30]);
    }

    #[tokio::test]
    async fn watchdog_times_out() {
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        let Next::Watchdog { gen, .. } = r.core.start(None).await else {
            panic!()
        };
        r.core.watchdog_fired(gen);
        assert_eq!(err_code(r.core.state()), Some(codes::WEB_TIMEOUT));
        assert_eq!(r.shell.calls.lock().unwrap().last(), Some(&Call::Launcher));

        // 就绪后看门狗失效
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        let Next::Watchdog { gen, .. } = r.core.start(None).await else {
            panic!()
        };
        r.core.host_ready(SERVER, 1, "zh-CN");
        r.core.watchdog_fired(gen);
        assert!(matches!(r.core.state(), LauncherState::Connected { .. }));
    }

    #[tokio::test]
    async fn reauth_loop_detected() {
        let mut r = paired();
        for _ in 0..3 {
            r.api
                .app_session
                .lock()
                .unwrap()
                .push_back(Ok("oac_1".into()));
        }
        r.core.start(None).await;
        r.core.host_ready(SERVER, 1, "en");
        r.shell.now.store(10_000, Ordering::SeqCst);
        assert!(matches!(r.core.reauth("/t/x").await, Next::Watchdog { .. }));
        assert!(!r.nav.lock().unwrap().web_ready);
        r.core.host_ready(SERVER, 1, "en");
        r.shell.now.store(20_000, Ordering::SeqCst);
        assert!(matches!(
            r.core.reauth("//evil").await,
            Next::Watchdog { .. }
        ));
        // 非法路径改用 /
        assert!(matches!(
            r.shell.calls.lock().unwrap().last(),
            Some(Call::Remote(_, u)) if u.ends_with("&next=%2F")
        ));
        r.shell.now.store(30_000, Ordering::SeqCst);
        assert_eq!(r.core.reauth("/").await, Next::Idle);
        assert_eq!(err_code(r.core.state()), Some(codes::SESSION_LOOP));

        // 窗口外的旧记录被丢弃
        let mut r = paired();
        for _ in 0..3 {
            r.api
                .app_session
                .lock()
                .unwrap()
                .push_back(Ok("oac_1".into()));
        }
        for t in [0, 30_000, 100_000] {
            r.shell.now.store(t, Ordering::SeqCst);
            assert!(matches!(r.core.reauth("/").await, Next::Watchdog { .. }));
        }
    }

    #[tokio::test]
    async fn change_server_confirm_and_cancel() {
        let mut r = paired();
        r.core.request_change();
        assert_eq!(
            r.core.state(),
            &LauncherState::ConfirmChange {
                server_url: SERVER.into()
            }
        );
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        assert!(matches!(
            r.core.change_server(false).await,
            Next::Watchdog { .. }
        ));
        assert_eq!(r.store.map.lock().unwrap().len(), 1);

        r.core.request_change();
        assert_eq!(r.core.change_server(true).await, Next::Idle);
        assert_eq!(
            r.api.revoked.lock().unwrap().as_slice(),
            [("odt_secret".to_string(), "dev_1".to_string())]
        );
        assert!(r.store.map.lock().unwrap().is_empty());
        let cfg = Config::load(r.dir.path());
        assert_eq!(cfg.server_url, None);
        assert_eq!(cfg.device_id, None);
        assert_eq!(r.shell.calls.lock().unwrap().last(), Some(&Call::Restart));
        let ev = r.log.events();
        assert_eq!(ev.last().unwrap()["reason"], "user");
    }

    #[tokio::test]
    async fn pending_written_during_session_request_is_kept() {
        let mut r = paired();
        let entered = Arc::new(Notify::new());
        let (release, rx) = oneshot::channel();
        *r.api.gate.lock().unwrap() = Some((entered.clone(), rx));
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        let nav = r.nav.clone();
        // 换码请求挂起期间，深链接 / 通知把线程 B 写进待开槽
        let click = async move {
            entered.notified().await;
            nav.lock().unwrap().pending = Some("/t/T-B".into());
            release.send(()).unwrap();
        };
        let (next, ()) = tokio::join!(r.core.start(None), click);
        assert!(matches!(next, Next::Watchdog { .. }));
        assert_eq!(
            r.shell.calls.lock().unwrap().as_slice(),
            [Call::Remote(
                SERVER.into(),
                format!("{SERVER}/auth/app?code=oac_1&next=%2Ft%2FT-B")
            )]
        );
        assert_eq!(r.nav.lock().unwrap().pending, None);
    }

    #[tokio::test]
    async fn pending_arriving_after_navigation_survives_for_host_ready() {
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        r.nav.lock().unwrap().pending = Some("/t/T-A".into());
        *r.shell.pending_on_remote.lock().unwrap() = Some((r.nav.clone(), "/t/T-C".into()));
        assert!(matches!(r.core.start(None).await, Next::Watchdog { .. }));
        assert_eq!(
            r.shell.calls.lock().unwrap().as_slice(),
            [Call::Remote(
                SERVER.into(),
                format!("{SERVER}/auth/app?code=oac_1&next=%2Ft%2FT-A")
            )]
        );
        // A 已随兑换地址带走；导航发出后才到的 C 留给新页面
        assert_eq!(
            r.core.host_ready(SERVER, 1, "en").as_deref(),
            Some("/t/T-C")
        );
    }

    #[tokio::test]
    async fn start_keyring_read_error_is_retryable() {
        let store = FlakyStore::default();
        store
            .map
            .lock()
            .unwrap()
            .insert(SERVER.into(), "odt_secret".into());
        store.fail.store(true, Ordering::SeqCst);
        let config = Config {
            server_url: Some(SERVER.into()),
            device_id: Some("dev_1".into()),
            ..Config::default()
        };
        let mut r = rig(config, FakeStore::default());
        r.core.vault = Vault::with_store(Box::new(store.clone()), TokenStoreKind::Keyring);
        // 命令行给了地址也不能另起配对
        assert_eq!(r.core.start(Some(SERVER)).await, Next::Idle);
        assert!(matches!(r.core.state(), LauncherState::Error { .. }));
        assert_eq!(err_code(r.core.state()), Some(codes::KEYRING));
        assert!(r.api.tokens_seen.lock().unwrap().is_empty());
        assert!(!r
            .shell
            .calls
            .lock()
            .unwrap()
            .iter()
            .any(|c| matches!(c, Call::Browser(_) | Call::Remote(..))));
        assert_eq!(r.core.config().device_id.as_deref(), Some("dev_1"));
        assert_eq!(r.core.config().server_url.as_deref(), Some(SERVER));

        // 凭据库恢复后「重试」直接用原令牌进入
        store.fail.store(false, Ordering::SeqCst);
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        assert!(matches!(r.core.retry().await, Next::Watchdog { .. }));
        assert_eq!(r.api.tokens_seen.lock().unwrap().as_slice(), ["odt_secret"]);
        assert_eq!(store.map.lock().unwrap().len(), 1);
    }

    #[tokio::test]
    async fn untrusted_verify_url_is_never_opened() {
        for bad in [
            "file:///etc/passwd",
            "https://other.example/pair",
            "search-ms:query=x",
            "https://user:pw@oryn.example.com/pair",
            "http://oryn.example.com/pair",
            "not a url",
        ] {
            let mut r = fresh();
            healthy(&r);
            r.api.pair_start.lock().unwrap().push_back(Ok(PairStart {
                code: "ABCD-EFGH".into(),
                verifier: "ver".into(),
                verify_url: bad.into(),
                expires_at: 600_000,
                interval: 2,
            }));
            assert!(matches!(r.core.connect(SERVER).await, Next::Poll { .. }));
            let want = format!("{SERVER}/pair");
            assert_eq!(
                r.shell.calls.lock().unwrap().as_slice(),
                [Call::Browser(want.clone())],
                "{bad}"
            );
            assert!(matches!(
                r.core.state(),
                LauncherState::Pairing { verify_url, .. } if *verify_url == want
            ));
            r.core.reopen_browser();
            assert_eq!(
                r.shell.calls.lock().unwrap().last(),
                Some(&Call::Browser(want.clone()))
            );
            let replaced = r
                .log
                .events()
                .into_iter()
                .find(|e| e["event"] == "pair.verify_url_replaced")
                .expect(bad);
            assert_eq!(replaced["used"], want);
        }
        // 同源的 http(s) 地址照用（含路径与查询）
        assert_eq!(
            pair_page_url(SERVER, "https://oryn.example.com:443/pair?x=1"),
            (format!("{SERVER}/pair?x=1"), false)
        );
    }

    #[tokio::test]
    async fn disconnects_clear_web_data() {
        // 更换服务器：清理先于重启
        let mut r = paired();
        r.core.request_change();
        assert_eq!(r.core.change_server(true).await, Next::Idle);
        let calls = r.shell.calls.lock().unwrap().clone();
        assert_eq!(
            calls[calls.len() - 2..],
            [Call::ClearWebData(SERVER.into()), Call::Restart]
        );

        // 吊销
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 401,
                code: "unauthorized".into(),
                message: String::new(),
            }));
        r.core.start(None).await;
        assert!(r
            .shell
            .calls
            .lock()
            .unwrap()
            .contains(&Call::ClearWebData(SERVER.into())));

        // 已配对但令牌不见了
        let config = Config {
            server_url: Some(SERVER.into()),
            device_id: Some("dev_1".into()),
            ..Config::default()
        };
        let mut r = rig(config, FakeStore::default());
        r.core.enter_app("/").await;
        assert_eq!(err_code(r.core.state()), Some(codes::REVOKED));
        assert!(r
            .shell
            .calls
            .lock()
            .unwrap()
            .contains(&Call::ClearWebData(SERVER.into())));
    }

    #[tokio::test]
    async fn mobile_pairing_does_not_open_browser() {
        let mut r = rig_with(Config::default(), FakeStore::default(), false);
        healthy(&r);
        start_ok(&r, 600_000);
        assert!(matches!(r.core.connect(SERVER).await, Next::Poll { .. }));
        assert!(matches!(r.core.state(), LauncherState::Pairing { .. }));
        assert!(r.shell.calls.lock().unwrap().is_empty());
        let names: Vec<String> = r
            .log
            .events()
            .iter()
            .map(|e| e["event"].as_str().unwrap().to_string())
            .collect();
        assert_eq!(names, ["pair.started"]);
        // 「复制配对码并打开浏览器」照常打开
        r.core.reopen_browser();
        assert_eq!(
            r.shell.calls.lock().unwrap().as_slice(),
            [Call::Browser(format!("{SERVER}/pair"))]
        );
    }

    #[tokio::test]
    async fn connect_accepts_pair_link() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        r.core
            .connect("oryn://pair?server=https%3A%2F%2Foryn.example.com%2F")
            .await;
        assert!(matches!(
            r.core.state(),
            LauncherState::Pairing { server_url, .. } if server_url == SERVER
        ));
        let ev = r.log.events();
        assert_eq!(ev[0]["event"], "pair.link");
        assert_eq!(ev[0]["source"], "connect");

        // 链接里的地址同样受 https / 回环限制；其它 oryn 链接是非法地址
        let mut r = fresh();
        r.core
            .connect("oryn://pair?server=http%3A%2F%2Fevil.example")
            .await;
        assert_eq!(err_code(r.core.state()), Some(codes::INSECURE_URL));
        r.core.connect("oryn://open").await;
        assert_eq!(err_code(r.core.state()), Some(codes::INVALID_URL));
    }

    /// 安全审查第 1 条：深链接给的地址只预填，绝不自己发起连接；用户确认后才走 connect。
    #[tokio::test]
    async fn link_only_proposes_until_user_confirms() {
        let mut r = fresh();
        r.core.propose("https://oryn.example.com/");
        assert_eq!(
            r.core.state(),
            &LauncherState::ConfirmConnect {
                server_url: SERVER.into()
            }
        );
        // 没有任何网络请求与外部动作
        assert!(r.api.healthz.lock().unwrap().is_empty());
        assert!(r.shell.calls.lock().unwrap().is_empty());
        // 冷启动的链接先到：start 不覆盖待确认的地址
        assert_eq!(r.core.start(None).await, Next::Idle);
        assert!(matches!(
            r.core.state(),
            LauncherState::ConfirmConnect { .. }
        ));
        // 新链接替换旧的；不合法 / 不安全的地址在连接页报错
        r.core.propose("http://evil.example");
        assert_eq!(err_code(r.core.state()), Some(codes::INSECURE_URL));
        r.core.propose("https://evil.example");
        r.core.cancel();
        assert_eq!(
            r.core.state(),
            &LauncherState::Connect {
                server_url: String::new(),
                error: None
            }
        );
        // 用户确认（launcher_connect 带上页面显示的地址）才发起配对
        r.core.propose(SERVER);
        healthy(&r);
        start_ok(&r, 600_000);
        assert!(matches!(r.core.connect(SERVER).await, Next::Poll { .. }));
        assert!(matches!(r.core.state(), LauncherState::Pairing { .. }));
        // 配对进行中再来链接：忽略
        r.core.propose("https://evil.example");
        assert!(matches!(r.core.state(), LauncherState::Pairing { .. }));
        assert_eq!(
            events(&r.log),
            [
                "pair.link_dismissed",
                "pair.started",
                // 测试 rig 是桌面形态（配对后自动打开批准页）
                "browser.open",
                "pair.link_ignored"
            ]
        );

        // 已配对：链接一律忽略
        let mut r = paired();
        r.core.propose("https://evil.example");
        assert!(matches!(
            r.core.state(),
            LauncherState::Connect { error: None, .. }
        ));
    }

    /// 代码审查第 2 条：远程页面就绪后连接页重新加载（WebView 被重建）→ 重新换取会话回到原页面；
    /// 冷启动兑换途中连接页脚本晚到不重复换取。
    #[tokio::test]
    async fn launcher_reload_after_ready_restores_session() {
        let mut r = paired();
        for code in ["oac_1", "oac_2"] {
            r.api.app_session.lock().unwrap().push_back(Ok(code.into()));
        }
        r.core.start(None).await;
        // 冷启动：兑换跳转已发出、远程页面尚未就绪时连接页脚本才跑
        assert_eq!(r.core.launcher_loaded().await, Next::Idle);
        assert_eq!(r.shell.calls.lock().unwrap().len(), 1);
        r.core.host_ready(SERVER, 3, "en");
        // Activity 重建：插件把重建前的页面写进待开路径，新连接页加载
        r.nav.lock().unwrap().pending = Some("/t/T-abc?x=1".into());
        assert!(matches!(
            r.core.launcher_loaded().await,
            Next::Watchdog { .. }
        ));
        assert_eq!(
            r.shell.calls.lock().unwrap().last(),
            Some(&Call::Remote(
                SERVER.into(),
                format!("{SERVER}/auth/app?code=oac_2&next=%2Ft%2FT-abc%3Fx%3D1")
            ))
        );
        assert!(!r.nav.lock().unwrap().web_ready);
        assert!(matches!(r.core.state(), LauncherState::Connecting { .. }));
        assert!(events(&r.log).contains(&"session.restore".to_string()));
    }

    #[tokio::test]
    async fn start_keeps_pairing_started_by_link() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        r.core
            .connect("oryn://pair?server=https%3A%2F%2Foryn.example.com")
            .await;
        assert_eq!(r.core.start(None).await, Next::Idle);
        assert!(matches!(r.core.state(), LauncherState::Pairing { .. }));
    }

    #[tokio::test]
    async fn resumed_polls_now_and_retries_unreachable() {
        let mut r = fresh();
        healthy(&r);
        start_ok(&r, 600_000);
        let Next::Poll { epoch: old, .. } = r.core.connect(SERVER).await else {
            panic!()
        };
        let Next::Poll { epoch, after } = r.core.resumed() else {
            panic!()
        };
        assert_eq!(after, Duration::ZERO);
        assert!(epoch > old);
        // 旧的轮询计时作废，新的一代照常
        assert!(r.core.poll_request(old).is_none());
        assert!(r.core.poll_request(epoch).is_some());

        // 已配对、服务器暂时不可达：后台取消退避，回前台立即重试
        let mut r = paired();
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Network("down".into())));
        let Next::Retry {
            gen: backoff_gen, ..
        } = r.core.start(None).await
        else {
            panic!()
        };
        r.core.stopped();
        assert_eq!(r.core.retry_fired(backoff_gen).await, Next::Idle);
        let Next::Retry { gen, after } = r.core.resumed() else {
            panic!()
        };
        assert_eq!(after, Duration::ZERO);
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_2".into()));
        assert!(matches!(
            r.core.retry_fired(gen).await,
            Next::Watchdog { .. }
        ));
        // 其余状态不动
        assert_eq!(r.core.resumed(), Next::Idle);
        assert_eq!(fresh().core.resumed(), Next::Idle);
    }

    #[tokio::test]
    async fn connection_only_when_connected() {
        let mut r = paired();
        assert_eq!(r.core.connection(), None);
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Ok("oac_1".into()));
        r.core.start(None).await;
        assert_eq!(r.core.connection(), None);
        r.core.host_ready(SERVER, 2, "en");
        assert_eq!(
            r.core.connection(),
            Some(Connection {
                base: SERVER.into(),
                token: "odt_secret".into()
            })
        );
    }

    #[tokio::test]
    async fn disconnects_tear_down_push() {
        // 更换服务器：先用令牌删订阅，再停用本机推送，最后删令牌
        let mut r = paired();
        *r.shell.push_id.lock().unwrap() = Some("ps_1".into());
        r.core.request_change();
        r.core.change_server(true).await;
        assert_eq!(
            r.api.unsubscribed.lock().unwrap().as_slice(),
            [("odt_secret".to_string(), "ps_1".to_string())]
        );
        assert!(r.shell.calls.lock().unwrap().contains(&Call::PushDisable));
        assert!(r.store.map.lock().unwrap().is_empty());

        // 被吊销：令牌已失效，只停用本机推送
        let mut r = paired();
        *r.shell.push_id.lock().unwrap() = Some("ps_1".into());
        r.api
            .app_session
            .lock()
            .unwrap()
            .push_back(Err(ApiError::Status {
                status: 401,
                code: "unauthorized".into(),
                message: String::new(),
            }));
        r.core.start(None).await;
        assert!(r.api.unsubscribed.lock().unwrap().is_empty());
        assert!(r.shell.calls.lock().unwrap().contains(&Call::PushDisable));
        assert_eq!(*r.shell.push_id.lock().unwrap(), None);
    }

    #[test]
    fn app_path_validation() {
        for ok in ["/", "/t/T-1", "/settings/devices?x=1", "/a b"] {
            assert_eq!(valid_app_path(ok), ok);
        }
        for bad in [
            "",
            "t/x",
            "//evil.example",
            "/\\evil",
            "https://evil.example",
            "/a\nb",
            "/a\u{0}",
        ] {
            assert_eq!(valid_app_path(bad), "/", "{bad:?}");
        }
        assert_eq!(valid_app_path(&format!("/{}", "a".repeat(2048))), "/");
        assert_eq!(
            app_session_url(SERVER, "oac_a+b", "/t/x?y=1&z"),
            format!("{SERVER}/auth/app?code=oac_a%2Bb&next=%2Ft%2Fx%3Fy%3D1%26z")
        );
    }

    #[test]
    fn state_serialization() {
        let s = LauncherState::Pairing {
            server_url: SERVER.into(),
            code: "C".into(),
            verify_url: "V".into(),
            expires_at: 5,
        };
        assert_eq!(
            serde_json::to_value(&s).unwrap(),
            json!({"step": "pairing", "serverUrl": SERVER, "code": "C", "verifyUrl": "V", "expiresAt": 5})
        );
        let s = LauncherState::ConfirmChange {
            server_url: SERVER.into(),
        };
        assert_eq!(serde_json::to_value(&s).unwrap()["step"], "confirmChange");
        let s = LauncherState::Connect {
            server_url: String::new(),
            error: Some(LauncherError::new(codes::REVOKED, "")),
        };
        assert_eq!(
            serde_json::to_value(&s).unwrap(),
            json!({"step": "connect", "serverUrl": "", "error": {"code": "revoked", "message": ""}})
        );
    }
}
