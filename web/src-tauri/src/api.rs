//! 与控制面的 HTTP 交互：健康检查、设备配对、App 会话换取、自我吊销。

use std::future::Future;
use std::sync::{LazyLock, OnceLock};
use std::time::Duration;

use serde::Deserialize;
use serde_json::json;

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ApiError {
    /// 连不上、超时、TLS 失败等。
    #[error("network error: {0}")]
    Network(String),
    /// 地址可达，但不是 Oryn 控制面（`/healthz` 不符）。
    #[error("not an Oryn server")]
    NotOryn,
    /// 非预期的 HTTP 状态；`code` / `message` 取自错误体（不是 JSON 时为空 / 原文）。
    #[error("HTTP {status}: {code} {message}")]
    Status {
        status: u16,
        code: String,
        message: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
pub struct Health {
    pub ok: bool,
    pub version: String,
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PairStart {
    pub code: String,
    pub verifier: String,
    pub verify_url: String,
    /// UTC 毫秒。
    pub expires_at: i64,
    /// 建议轮询间隔（秒）。
    pub interval: u64,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PairPoll {
    Pending,
    Approved {
        device_token: String,
        device_id: String,
    },
}

/// 会话状态机依赖的接口；生产实现为 [`HttpApi`]，测试用假实现。
pub trait Api: Send + Sync + 'static {
    fn healthz(&self, base: &str) -> impl Future<Output = Result<Health, ApiError>> + Send;
    fn pair_start(
        &self,
        base: &str,
        name: &str,
        platform: &str,
    ) -> impl Future<Output = Result<PairStart, ApiError>> + Send;
    fn pair_poll(
        &self,
        base: &str,
        code: &str,
        verifier: &str,
    ) -> impl Future<Output = Result<PairPoll, ApiError>> + Send;
    fn app_session(
        &self,
        base: &str,
        token: &str,
    ) -> impl Future<Output = Result<String, ApiError>> + Send;
    fn revoke_self(
        &self,
        base: &str,
        token: &str,
        device_id: &str,
    ) -> impl Future<Output = Result<(), ApiError>> + Send;
}

/// 走 [`reqwest`] 的生产实现。
#[derive(Clone, Copy, Default)]
pub struct HttpApi;

impl Api for HttpApi {
    async fn healthz(&self, base: &str) -> Result<Health, ApiError> {
        healthz(base).await
    }
    async fn pair_start(
        &self,
        base: &str,
        name: &str,
        platform: &str,
    ) -> Result<PairStart, ApiError> {
        pair_start(base, name, platform).await
    }
    async fn pair_poll(
        &self,
        base: &str,
        code: &str,
        verifier: &str,
    ) -> Result<PairPoll, ApiError> {
        pair_poll(base, code, verifier).await
    }
    async fn app_session(&self, base: &str, token: &str) -> Result<String, ApiError> {
        app_session(base, token).await
    }
    async fn revoke_self(&self, base: &str, token: &str, device_id: &str) -> Result<(), ApiError> {
        revoke_self(base, token, device_id).await
    }
}

/// 应用版本（`package_info().version`），启动时在第一个请求之前设置。
static APP_VERSION: OnceLock<String> = OnceLock::new();

/// 设置 User-Agent 里的版本；只认第一次。
pub fn set_app_version(version: String) {
    let _ = APP_VERSION.set(version);
}

/// `User-Agent: OrynDesktop/<版本> (<os>-<arch>)`；版本未设置（单测）时取 crate 版本。
pub fn user_agent() -> String {
    let version = APP_VERSION
        .get()
        .map(String::as_str)
        .unwrap_or(env!("CARGO_PKG_VERSION"));
    format!("OrynDesktop/{version} ({})", platform())
}

/// `<linux|macos|windows>-<x64|arm64>`。
pub fn platform() -> String {
    // std 的 OS 名（linux / macos / windows）与约定一致，只换架构名
    let os = std::env::consts::OS;
    let arch = match std::env::consts::ARCH {
        "x86_64" => "x64",
        "aarch64" => "arm64",
        other => other,
    };
    format!("{os}-{arch}")
}

/// 设备名：主机名；取不到时 `Oryn Desktop`。
pub fn device_name() -> String {
    let name = host_name()
        .map(|n| n.trim().to_string())
        .filter(|n| !n.is_empty())
        .unwrap_or_else(|| "Oryn Desktop".to_string());
    name.chars().take(100).collect()
}

fn host_name() -> Option<String> {
    #[cfg(windows)]
    {
        std::env::var("COMPUTERNAME").ok()
    }
    #[cfg(target_os = "macos")]
    {
        std::process::Command::new("scutil")
            .args(["--get", "ComputerName"])
            .output()
            .ok()
            .filter(|o| o.status.success())
            .and_then(|o| String::from_utf8(o.stdout).ok())
    }
    #[cfg(all(unix, not(target_os = "macos")))]
    {
        std::fs::read_to_string("/proc/sys/kernel/hostname")
            .or_else(|_| std::fs::read_to_string("/etc/hostname"))
            .ok()
    }
    #[cfg(not(any(unix, windows)))]
    {
        None
    }
}

fn build_client() -> reqwest::Client {
    let builder = reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(10))
        .timeout(Duration::from_secs(30))
        .user_agent(user_agent())
        .redirect(reqwest::redirect::Policy::none());
    // Android：不走 rustls-platform-verifier（未经 JNI 初始化即 panic），只信 Mozilla 根证书；
    // 加密后端用 ring，进程内只装一次（已装过时 install_default 返回 Err，忽略即可）
    #[cfg(target_os = "android")]
    let builder = {
        let _ = rustls::crypto::ring::default_provider().install_default();
        builder.tls_certs_only(
            webpki_root_certs::TLS_SERVER_ROOT_CERTS
                .iter()
                .filter_map(|der| reqwest::Certificate::from_der(der.as_ref()).ok()),
        )
    };
    builder.build().expect("reqwest client")
}

static CLIENT: LazyLock<reqwest::Client> = LazyLock::new(build_client);

/// 进程共享的连接池。测试里每次新建：每个 `#[tokio::test]` 各有运行时，而 wiremock 会复用端口，
/// 共享池里属于已关闭运行时的连接会让请求失败。
fn client() -> reqwest::Client {
    if cfg!(test) {
        build_client()
    } else {
        CLIENT.clone()
    }
}

fn endpoint(base: &str, path: &str) -> String {
    format!("{}{path}", base.trim_end_matches('/'))
}

fn network(e: reqwest::Error) -> ApiError {
    // 带上底层原因（连接被拒、证书不受信任等），连接页与日志才看得出问题在哪
    let e = e.without_url();
    let mut msg = e.to_string();
    let mut source = std::error::Error::source(&e);
    while let Some(s) = source {
        msg.push_str(": ");
        msg.push_str(&s.to_string());
        source = s.source();
    }
    ApiError::Network(msg)
}

#[derive(Deserialize, Default)]
struct ErrorBody {
    #[serde(default)]
    code: String,
    #[serde(default)]
    message: String,
}

async fn status_error(resp: reqwest::Response) -> ApiError {
    let status = resp.status().as_u16();
    let text = resp.text().await.unwrap_or_default();
    let body = serde_json::from_str::<ErrorBody>(&text).unwrap_or_else(|_| ErrorBody {
        code: String::new(),
        message: text.chars().take(200).collect(),
    });
    ApiError::Status {
        status,
        code: body.code,
        message: body.message,
    }
}

/// `GET /healthz`：要求 `{ok: true, version}`。
pub async fn healthz(base: &str) -> Result<Health, ApiError> {
    let resp = client()
        .get(endpoint(base, "/healthz"))
        .send()
        .await
        .map_err(network)?;
    if !resp.status().is_success() {
        return Err(ApiError::NotOryn);
    }
    let bytes = resp.bytes().await.map_err(network)?;
    match serde_json::from_slice::<Health>(&bytes) {
        Ok(h) if h.ok => Ok(h),
        _ => Err(ApiError::NotOryn),
    }
}

/// `POST /api/v1/devices/pair/start`，`kind` 固定 `desktop`。
pub async fn pair_start(base: &str, name: &str, platform: &str) -> Result<PairStart, ApiError> {
    let resp = client()
        .post(endpoint(base, "/api/v1/devices/pair/start"))
        .json(&json!({"name": name, "kind": "desktop", "platform": platform}))
        .send()
        .await
        .map_err(network)?;
    if resp.status() != reqwest::StatusCode::OK {
        return Err(status_error(resp).await);
    }
    resp.json::<PairStart>().await.map_err(network)
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct PollBody {
    device_token: String,
    device: PollDevice,
}

#[derive(Deserialize)]
struct PollDevice {
    id: String,
}

/// `POST /api/v1/devices/pair/poll`：202 → `Pending`；200 → `Approved`。
pub async fn pair_poll(base: &str, code: &str, verifier: &str) -> Result<PairPoll, ApiError> {
    let resp = client()
        .post(endpoint(base, "/api/v1/devices/pair/poll"))
        .json(&json!({"code": code, "verifier": verifier}))
        .send()
        .await
        .map_err(network)?;
    match resp.status().as_u16() {
        202 => Ok(PairPoll::Pending),
        200 => {
            let body = resp.json::<PollBody>().await.map_err(network)?;
            Ok(PairPoll::Approved {
                device_token: body.device_token,
                device_id: body.device.id,
            })
        }
        _ => Err(status_error(resp).await),
    }
}

#[derive(Deserialize)]
struct AppSessionBody {
    code: String,
}

/// `POST /api/v1/auth/app-session`（Bearer 设备令牌）→ 一次性码。
pub async fn app_session(base: &str, token: &str) -> Result<String, ApiError> {
    let resp = client()
        .post(endpoint(base, "/api/v1/auth/app-session"))
        .bearer_auth(token)
        .send()
        .await
        .map_err(network)?;
    if resp.status() != reqwest::StatusCode::OK {
        return Err(status_error(resp).await);
    }
    Ok(resp.json::<AppSessionBody>().await.map_err(network)?.code)
}

/// `DELETE /api/v1/devices/<id>`（Bearer 设备令牌）。
pub async fn revoke_self(base: &str, token: &str, device_id: &str) -> Result<(), ApiError> {
    let id: String = url::form_urlencoded::byte_serialize(device_id.as_bytes()).collect();
    let resp = client()
        .delete(endpoint(base, &format!("/api/v1/devices/{id}")))
        .bearer_auth(token)
        .send()
        .await
        .map_err(network)?;
    if !resp.status().is_success() {
        return Err(status_error(resp).await);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use wiremock::matchers::{body_json, header, method, path};
    use wiremock::{Match, Mock, MockServer, Request, ResponseTemplate};

    struct NoAuth;
    impl Match for NoAuth {
        fn matches(&self, req: &Request) -> bool {
            !req.headers.contains_key("authorization")
        }
    }

    #[tokio::test]
    async fn healthz_variants() {
        let s = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/healthz"))
            .and(NoAuth)
            .respond_with(
                ResponseTemplate::new(200).set_body_json(json!({"ok": true, "version": "v1"})),
            )
            .mount(&s)
            .await;
        assert_eq!(
            healthz(&s.uri()).await.unwrap(),
            Health {
                ok: true,
                version: "v1".into()
            }
        );

        let s = MockServer::start().await;
        Mock::given(path("/healthz"))
            .respond_with(ResponseTemplate::new(200).set_body_string("<html>hello</html>"))
            .mount(&s)
            .await;
        assert_eq!(healthz(&s.uri()).await, Err(ApiError::NotOryn));

        let s = MockServer::start().await;
        Mock::given(path("/healthz"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(json!({"ok": false, "version": "v1"})),
            )
            .mount(&s)
            .await;
        assert_eq!(healthz(&s.uri()).await, Err(ApiError::NotOryn));

        let s = MockServer::start().await;
        Mock::given(path("/healthz"))
            .respond_with(ResponseTemplate::new(404))
            .mount(&s)
            .await;
        assert_eq!(healthz(&s.uri()).await, Err(ApiError::NotOryn));
    }

    #[tokio::test]
    async fn network_error() {
        // 绑定后立即释放的端口：连接被拒绝
        let port = {
            let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            l.local_addr().unwrap().port()
        };
        let base = format!("http://127.0.0.1:{port}");
        assert!(matches!(healthz(&base).await, Err(ApiError::Network(_))));
        assert!(matches!(
            app_session(&base, "odt_x").await,
            Err(ApiError::Network(_))
        ));
    }

    #[tokio::test]
    async fn pair_start_body() {
        let s = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/api/v1/devices/pair/start"))
            .and(NoAuth)
            .and(body_json(
                json!({"name": "box", "kind": "desktop", "platform": "linux-x64"}),
            ))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "code": "ABCD-EFGH", "verifier": "v1", "verifyUrl": "https://a.example/pair",
                "expiresAt": 1_700_000_000_000_i64, "interval": 2
            })))
            .expect(1)
            .mount(&s)
            .await;
        let p = pair_start(&s.uri(), "box", "linux-x64").await.unwrap();
        assert_eq!(p.code, "ABCD-EFGH");
        assert_eq!(p.verify_url, "https://a.example/pair");
        assert_eq!(p.expires_at, 1_700_000_000_000);
        assert_eq!(p.interval, 2);
    }

    #[tokio::test]
    async fn pair_poll_pending_then_approved() {
        let s = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/api/v1/devices/pair/poll"))
            .and(NoAuth)
            .and(body_json(json!({"code": "ABCD-EFGH", "verifier": "v1"})))
            .respond_with(ResponseTemplate::new(202))
            .up_to_n_times(1)
            .mount(&s)
            .await;
        Mock::given(method("POST"))
            .and(path("/api/v1/devices/pair/poll"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "deviceToken": "odt_tok",
                "device": {"id": "dev_1", "name": "box", "kind": "desktop", "platform": "linux-x64", "createdAt": 1}
            })))
            .mount(&s)
            .await;
        assert_eq!(
            pair_poll(&s.uri(), "ABCD-EFGH", "v1").await.unwrap(),
            PairPoll::Pending
        );
        assert_eq!(
            pair_poll(&s.uri(), "ABCD-EFGH", "v1").await.unwrap(),
            PairPoll::Approved {
                device_token: "odt_tok".into(),
                device_id: "dev_1".into()
            }
        );
    }

    #[tokio::test]
    async fn pair_poll_errors() {
        for (status, code) in [(404, "not_found"), (403, "forbidden")] {
            let s = MockServer::start().await;
            Mock::given(path("/api/v1/devices/pair/poll"))
                .respond_with(
                    ResponseTemplate::new(status)
                        .set_body_json(json!({"code": code, "message": "m"})),
                )
                .mount(&s)
                .await;
            assert_eq!(
                pair_poll(&s.uri(), "c", "v").await,
                Err(ApiError::Status {
                    status,
                    code: code.into(),
                    message: "m".into()
                })
            );
        }
    }

    #[tokio::test]
    async fn app_session_variants() {
        let s = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/api/v1/auth/app-session"))
            .and(header("authorization", "Bearer odt_good"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(json!({"code": "oac_1", "expiresIn": 30})),
            )
            .mount(&s)
            .await;
        Mock::given(method("POST"))
            .and(path("/api/v1/auth/app-session"))
            .and(header("authorization", "Bearer odt_revoked"))
            .respond_with(
                ResponseTemplate::new(401)
                    .set_body_json(json!({"code": "unauthorized", "message": "no"})),
            )
            .mount(&s)
            .await;
        Mock::given(method("POST"))
            .and(path("/api/v1/auth/app-session"))
            .and(header("authorization", "Bearer odt_cli"))
            .respond_with(
                ResponseTemplate::new(403)
                    .set_body_json(json!({"code": "app_session_unsupported", "message": "cli"})),
            )
            .mount(&s)
            .await;
        assert_eq!(app_session(&s.uri(), "odt_good").await.unwrap(), "oac_1");
        assert!(matches!(
            app_session(&s.uri(), "odt_revoked").await,
            Err(ApiError::Status { status: 401, .. })
        ));
        assert_eq!(
            app_session(&s.uri(), "odt_cli").await,
            Err(ApiError::Status {
                status: 403,
                code: "app_session_unsupported".into(),
                message: "cli".into()
            })
        );

        // 早于 M6 的服务器：404 且错误体不是 JSON
        let old = MockServer::start().await;
        Mock::given(path("/api/v1/auth/app-session"))
            .respond_with(ResponseTemplate::new(404).set_body_string("404 page not found"))
            .mount(&old)
            .await;
        assert_eq!(
            app_session(&old.uri(), "odt_good").await,
            Err(ApiError::Status {
                status: 404,
                code: String::new(),
                message: "404 page not found".into()
            })
        );
    }

    #[tokio::test]
    async fn revoke_self_sends_bearer() {
        let s = MockServer::start().await;
        Mock::given(method("DELETE"))
            .and(path("/api/v1/devices/dev_1"))
            .and(header("authorization", "Bearer odt_tok"))
            .respond_with(ResponseTemplate::new(204))
            .expect(1)
            .mount(&s)
            .await;
        revoke_self(&s.uri(), "odt_tok", "dev_1").await.unwrap();
    }

    #[test]
    fn platform_shape() {
        let p = platform();
        assert!(p.contains('-'), "{p}");
        assert!(user_agent().starts_with("OrynDesktop/"));
        assert!(!device_name().is_empty());
    }
}
