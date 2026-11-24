//! 官方推送网关（M28，`docs/protocol.md`「官方推送网关」）在壳这一侧的基础件：受信 origin 列表、签名输入、
//! P-256 设备密钥与本地授权账本。登记编排在 [`crate::mobile_logic`]。
//!
//! 设备密钥是软件 P-256（用户决定 D2），32 字节标量经 [`TokenStore`] 存放（Android Keystore 加密 / iOS Keychain），
//! 账户名 [`DEVICE_KEY_ACCOUNT`]。账本 `<app_config_dir>/push.json` 只存授权 id、主机公钥与令牌哈希，不存令牌本身。

use std::path::{Path, PathBuf};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use p256::ecdsa::signature::Signer as _;
use p256::ecdsa::{Signature, SigningKey};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::secrets::TokenStore;

/// 官方网关。
pub const OFFICIAL_GATEWAY: &str = "https://push.oryn.best";
/// 设备密钥在 [`TokenStore`] 里的账户名。
pub const DEVICE_KEY_ACCOUNT: &str = "oryn:push-device-key";
/// 账本文件名（`<app_config_dir>/push.json`）。
pub const LEDGER_FILE: &str = "push.json";
/// 待吊销队列的上限：超出时丢最旧的（网关上 90 天未用的授权自然过期）。
pub const MAX_PENDING_REVOKES: usize = 8;

pub const HEADER_DEVICE_KEY: &str = "Oryn-Device-Key";
pub const HEADER_TIMESTAMP: &str = "Oryn-Timestamp";
pub const HEADER_SIGNATURE: &str = "Oryn-Signature";

pub const PATH_INFO: &str = "/v1/info";
pub const PATH_CHALLENGES: &str = "/v1/challenges";
pub const PATH_GRANTS: &str = "/v1/grants";

/// 本构建信任的网关 origin：编译期环境变量 `ORYN_PUSH_GATEWAYS`（逗号分隔），缺省只有官方网关。
/// 只接受 https 或回环主机的 http；不合规的项丢弃。
pub fn trusted_origins() -> Vec<String> {
    parse_origins(option_env!("ORYN_PUSH_GATEWAYS"))
}

fn parse_origins(raw: Option<&str>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for item in raw.unwrap_or(OFFICIAL_GATEWAY).split(',') {
        if let Some(o) = gateway_origin(item.trim()) {
            if !out.contains(&o) {
                out.push(o);
            }
        }
    }
    out
}

/// 规范化的网关 origin：不带路径（`/` 除外）、查询、片段与用户信息；https，或回环主机（`localhost`、`127.0.0.0/8`、`::1`）的 http。
pub fn gateway_origin(raw: &str) -> Option<String> {
    let u = url::Url::parse(raw).ok()?;
    let loopback = match u.host()? {
        url::Host::Domain(d) => d.eq_ignore_ascii_case("localhost"),
        url::Host::Ipv4(ip) => ip.is_loopback(),
        url::Host::Ipv6(ip) => ip.is_loopback(),
    };
    let scheme_ok = u.scheme() == "https" || (u.scheme() == "http" && loopback);
    let bare = matches!(u.path(), "" | "/")
        && u.query().is_none()
        && u.fragment().is_none()
        && u.username().is_empty()
        && u.password().is_none();
    (scheme_ok && bare).then(|| u.origin().ascii_serialization())
}

/// 签名输入：`oryn-push-gw/1\n<METHOD>\n<origin><path>\n<ts_ms>\n<hex(sha256(body))>`。
pub fn signing_input(method: &str, origin: &str, path: &str, ts_ms: i64, body: &[u8]) -> String {
    let digest = Sha256::digest(body);
    let mut hex = String::with_capacity(64);
    for b in digest {
        hex.push_str(&format!("{b:02x}"));
    }
    format!("oryn-push-gw/1\n{method}\n{origin}{path}\n{ts_ms}\n{hex}")
}

/// 推送令牌的指纹：SHA-256 的 base64url 前 22 位（账本据此判断令牌是否变了，不存令牌本身）。
pub fn token_hash(token: &str) -> String {
    let mut s = URL_SAFE_NO_PAD.encode(Sha256::digest(token.as_bytes()));
    s.truncate(22);
    s
}

/// 网关的 `grant` / `challengeId` / `nonce`：`^[A-Za-z0-9_-]{22}$`。
pub fn valid_id(s: &str) -> bool {
    s.len() == 22
        && s.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// 网关订阅在 orynd 上的 endpoint：`<origin>/v1/grants/<grant>`（R9）。
pub fn grant_endpoint(origin: &str, grant: &str) -> String {
    format!("{origin}{PATH_GRANTS}/{grant}")
}

/// 是否形如 [`grant_endpoint`]（用来认出 UnifiedPush 时代留下的旧订阅）。
pub fn is_grant_endpoint(endpoint: &str) -> bool {
    let Ok(u) = url::Url::parse(endpoint) else {
        return false;
    };
    u.path()
        .strip_prefix(PATH_GRANTS)
        .and_then(|rest| rest.strip_prefix('/'))
        .is_some_and(valid_id)
        && gateway_origin(&u.origin().ascii_serialization()).is_some()
}

/// 当前 Unix 毫秒。
pub fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// 设备密钥：P-256 / ECDSA，签名用 SHA-256、DER 编码。
pub struct DeviceKey(SigningKey);

impl DeviceKey {
    /// 读出存储里的标量；没有（或已损坏）则新生成并存回。存储读写失败时报错（不在内存里另起一把，
    /// 否则每次启动都是新设备，旧授权全部作废）。
    pub fn load_or_create(store: &dyn TokenStore) -> Result<DeviceKey, String> {
        let stored = store
            .get(DEVICE_KEY_ACCOUNT)
            .map_err(|e| format!("device key: {e}"))?;
        if let Some(key) = stored.as_deref().and_then(Self::from_stored) {
            return Ok(key);
        }
        let key = DeviceKey(SigningKey::random(&mut rand_core::OsRng));
        store
            .set(
                DEVICE_KEY_ACCOUNT,
                &URL_SAFE_NO_PAD.encode(key.0.to_bytes()),
            )
            .map_err(|e| format!("device key: {e}"))?;
        Ok(key)
    }

    fn from_stored(s: &str) -> Option<DeviceKey> {
        let bytes = URL_SAFE_NO_PAD.decode(s.trim()).ok()?;
        Self::from_scalar(&bytes)
    }

    /// 32 字节大端标量。
    pub fn from_scalar(bytes: &[u8]) -> Option<DeviceKey> {
        if bytes.len() != 32 {
            return None;
        }
        SigningKey::from_slice(bytes).ok().map(DeviceKey)
    }

    /// `Oryn-Device-Key`：base64url 的 65 字节未压缩点。
    pub fn public(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.0.verifying_key().to_encoded_point(false).as_bytes())
    }

    /// `Oryn-Signature`：对 `input` 的 ECDSA P-256 / SHA-256 签名，DER，base64url。
    pub fn sign(&self, input: &[u8]) -> String {
        let sig: Signature = self.0.sign(input);
        URL_SAFE_NO_PAD.encode(sig.to_der().as_bytes())
    }

    /// 设备签名的三个请求头。
    pub fn headers(
        &self,
        method: &str,
        origin: &str,
        path: &str,
        ts_ms: i64,
        body: &[u8],
    ) -> Vec<(String, String)> {
        let input = signing_input(method, origin, path, ts_ms, body);
        vec![
            (HEADER_DEVICE_KEY.into(), self.public()),
            (HEADER_TIMESTAMP.into(), ts_ms.to_string()),
            (HEADER_SIGNATURE.into(), self.sign(input.as_bytes())),
        ]
    }
}

/// 本机持有的网关授权。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Grant {
    pub origin: String,
    pub host_key: String,
    pub grant: String,
    /// 授权时推送令牌的 [`token_hash`]。
    pub token_hash: String,
}

impl Grant {
    pub fn endpoint(&self) -> String {
        grant_endpoint(&self.origin, &self.grant)
    }
}

/// 待吊销的授权（关闭推送或解除配对时网关不可达）。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Revoke {
    pub origin: String,
    pub grant: String,
}

/// `<app_config_dir>/push.json`。
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct Ledger {
    /// 下次进入应用时自动启用一次推送（新装、或解除配对之后）。
    pub auto_pending: bool,
    pub grant: Option<Grant>,
    pub pending_revokes: Vec<Revoke>,
}

impl Default for Ledger {
    /// 文件不存在 = 从未配对过：等价于 `auto_pending = true`。
    fn default() -> Self {
        Self {
            auto_pending: true,
            grant: None,
            pending_revokes: Vec::new(),
        }
    }
}

impl Ledger {
    pub fn path(dir: &Path) -> PathBuf {
        dir.join(LEDGER_FILE)
    }

    /// 读账本；不存在或解析失败按新装处理。
    pub fn load(dir: &Path) -> Ledger {
        std::fs::read(Self::path(dir))
            .ok()
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or_default()
    }

    pub fn save(&self, dir: &Path) -> std::io::Result<()> {
        let data = serde_json::to_vec_pretty(self).map_err(std::io::Error::other)?;
        crate::config::write_atomic(&Self::path(dir), &data, Some(0o600))
    }

    /// 授权挪进待吊销队列（去重，超出上限丢最旧的）。
    pub fn queue_revoke(&mut self, r: Revoke) {
        if self.pending_revokes.contains(&r) {
            return;
        }
        self.pending_revokes.push(r);
        let over = self
            .pending_revokes
            .len()
            .saturating_sub(MAX_PENDING_REVOKES);
        self.pending_revokes.drain(..over);
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use crate::secrets::testing::FakeStore;
    use p256::ecdsa::signature::Verifier as _;
    use p256::ecdsa::VerifyingKey;
    use serde_json::Value;

    fn vectors() -> Value {
        serde_json::from_str(include_str!("../../../schema/pushgw/sign-vectors.json")).unwrap()
    }

    fn hex(s: &str) -> Vec<u8> {
        (0..s.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
            .collect()
    }

    fn vector_key(v: &Value) -> DeviceKey {
        DeviceKey::from_scalar(&hex(v["deviceScalar"].as_str().unwrap())).unwrap()
    }

    /// 校验设备签名（与网关 `pushproto.Verify` 同一算法）。
    pub(crate) fn verify(device_key: &str, input: &[u8], sig: &str) -> bool {
        let point = URL_SAFE_NO_PAD.decode(device_key).unwrap();
        let vk = VerifyingKey::from_sec1_bytes(&point).unwrap();
        let Ok(der) = URL_SAFE_NO_PAD.decode(sig) else {
            return false;
        };
        let Ok(sig) = Signature::from_der(&der) else {
            return false;
        };
        vk.verify(input, &sig).is_ok()
    }

    #[test]
    fn signing_input_matches_vectors() {
        let v = vectors();
        for c in v["cases"].as_array().unwrap() {
            let got = signing_input(
                c["method"].as_str().unwrap(),
                c["origin"].as_str().unwrap(),
                c["path"].as_str().unwrap(),
                c["timestamp"].as_i64().unwrap(),
                c["body"].as_str().unwrap().as_bytes(),
            );
            assert_eq!(got, c["input"].as_str().unwrap());
        }
    }

    #[test]
    fn device_key_matches_vectors() {
        let v = vectors();
        let key = vector_key(&v);
        assert_eq!(key.public(), v["deviceKey"].as_str().unwrap());
        for c in v["cases"].as_array().unwrap() {
            let input = c["input"].as_str().unwrap().as_bytes();
            let sig = key.sign(input);
            assert!(verify(&key.public(), input, &sig));
            // 换一个字节就验不过
            let mut bad = input.to_vec();
            bad[0] ^= 1;
            assert!(!verify(&key.public(), &bad, &sig));
        }
    }

    #[test]
    fn go_device_signature_verifies() {
        let v = vectors();
        let key = v["deviceKey"].as_str().unwrap();
        for c in v["cases"].as_array().unwrap() {
            assert!(verify(
                key,
                c["input"].as_str().unwrap().as_bytes(),
                c["deviceSignature"].as_str().unwrap()
            ));
        }
    }

    #[test]
    fn device_key_persists_in_store() {
        let store = FakeStore::default();
        let a = DeviceKey::load_or_create(&store).unwrap();
        let stored = store
            .map
            .lock()
            .unwrap()
            .get(DEVICE_KEY_ACCOUNT)
            .cloned()
            .unwrap();
        assert_eq!(URL_SAFE_NO_PAD.decode(&stored).unwrap().len(), 32);
        let b = DeviceKey::load_or_create(&store).unwrap();
        assert_eq!(a.public(), b.public());
        // 损坏的值换一把新的
        store
            .map
            .lock()
            .unwrap()
            .insert(DEVICE_KEY_ACCOUNT.into(), "!!".into());
        let c = DeviceKey::load_or_create(&store).unwrap();
        assert_ne!(c.public(), a.public());
        // 存储不可用：报错，不在内存里另起一把
        let down = FakeStore {
            unavailable: true,
            ..FakeStore::default()
        };
        assert!(DeviceKey::load_or_create(&down).is_err());
    }

    #[test]
    fn origins_and_endpoints() {
        assert_eq!(parse_origins(None), [OFFICIAL_GATEWAY]);
        assert_eq!(
            parse_origins(Some(
                " https://gw.example.com/ ,http://localhost:7700,http://127.0.0.1:1,http://evil.example,https://x.example/p,https://gw.example.com"
            )),
            [
                "https://gw.example.com",
                "http://localhost:7700",
                "http://127.0.0.1:1"
            ]
        );
        assert!(trusted_origins()
            .iter()
            .all(|o| gateway_origin(o).is_some()));
        assert_eq!(
            gateway_origin("http://[::1]:7700").as_deref(),
            Some("http://[::1]:7700")
        );
        for bad in [
            "ftp://a",
            "https://u:p@a.example",
            "https://a.example?x=1",
            "",
        ] {
            assert_eq!(gateway_origin(bad), None, "{bad}");
        }
        let e = grant_endpoint(OFFICIAL_GATEWAY, "AAAAAAAAAAAAAAAAAAAAAA");
        assert_eq!(e, "https://push.oryn.best/v1/grants/AAAAAAAAAAAAAAAAAAAAAA");
        assert!(is_grant_endpoint(&e));
        for not in [
            "https://ntfy.example/upAbc?up=1",
            "https://push.oryn.best/v1/grants/short",
            "fcm_token_value",
        ] {
            assert!(!is_grant_endpoint(not), "{not}");
        }
        assert_eq!(token_hash("tok").len(), 22);
        assert_ne!(token_hash("tok"), token_hash("tok2"));
        assert!(valid_id("Zz_-Zz_-Zz_-Zz_-Zz_-Zz"));
        assert!(!valid_id("Zz_-Zz_-Zz_-Zz_-Zz_-Z="));
    }

    #[test]
    fn ledger_round_trip_and_queue_cap() {
        let dir = tempfile::tempdir().unwrap();
        assert!(
            Ledger::load(dir.path()).auto_pending,
            "文件不存在 = 待自动启用"
        );
        let mut l = Ledger {
            auto_pending: false,
            grant: Some(Grant {
                origin: OFFICIAL_GATEWAY.into(),
                host_key: "hk".into(),
                grant: "AAAAAAAAAAAAAAAAAAAAAA".into(),
                token_hash: token_hash("tok"),
            }),
            pending_revokes: vec![],
        };
        for i in 0..10 {
            l.queue_revoke(Revoke {
                origin: OFFICIAL_GATEWAY.into(),
                grant: format!("g{i}"),
            });
        }
        l.queue_revoke(Revoke {
            origin: OFFICIAL_GATEWAY.into(),
            grant: "g9".into(),
        });
        assert_eq!(l.pending_revokes.len(), MAX_PENDING_REVOKES);
        assert_eq!(l.pending_revokes[0].grant, "g2");
        l.save(dir.path()).unwrap();
        assert_eq!(Ledger::load(dir.path()), l);
        let raw: Value =
            serde_json::from_slice(&std::fs::read(Ledger::path(dir.path())).unwrap()).unwrap();
        assert_eq!(raw["autoPending"], false);
        assert_eq!(raw["grant"]["hostKey"], "hk");
        assert!(raw["grant"].get("tokenHash").is_some());
    }
}
