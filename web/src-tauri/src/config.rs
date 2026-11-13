//! 桌面配置 `<app_config_dir>/config.json` 与服务器地址规范化。

use std::io::Write;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::i18n::Lang;

/// 配置文件名。
pub const FILE_NAME: &str = "config.json";
/// 默认全局快捷键。
pub const DEFAULT_SHORTCUT: &str = "Control+Alt+Space";

/// 设备令牌的存放位置。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize, Default)]
#[serde(rename_all = "lowercase")]
pub enum TokenStoreKind {
    /// 系统凭据库（macOS Keychain、Windows Credential Manager、Linux Secret Service）。
    #[default]
    Keyring,
    /// 仅本人可读的文件（Linux 上没有 Secret Service 时的回退）。
    File,
}

impl TokenStoreKind {
    pub fn as_str(self) -> &'static str {
        match self {
            TokenStoreKind::Keyring => "keyring",
            TokenStoreKind::File => "file",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct Config {
    /// 规范化后的 origin，如 `https://oryn.example.com`。
    pub server_url: Option<String>,
    pub device_id: Option<String>,
    pub token_store: TokenStoreKind,
    /// 全局快捷键；空串 = 不注册。
    pub shortcut: String,
    pub close_to_tray: bool,
    pub auto_update: bool,
    /// `zh-CN` | `en`；首次取系统语言。
    pub lang: String,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            server_url: None,
            device_id: None,
            token_store: TokenStoreKind::Keyring,
            shortcut: DEFAULT_SHORTCUT.to_string(),
            close_to_tray: true,
            auto_update: false,
            lang: Lang::system().as_str().to_string(),
        }
    }
}

impl Config {
    pub fn lang(&self) -> Lang {
        Lang::from_tag(&self.lang)
    }

    /// 读取 `<dir>/config.json`。不存在 → 默认值；损坏 → 改名为 `config.json.bad` 后返回默认值。
    pub fn load(dir: &Path) -> Self {
        let path = dir.join(FILE_NAME);
        let bytes = match std::fs::read(&path) {
            Ok(b) => b,
            Err(_) => return Self::default(),
        };
        match serde_json::from_slice::<Config>(&bytes) {
            Ok(mut cfg) => {
                cfg.lang = cfg.lang().as_str().to_string();
                cfg
            }
            Err(_) => {
                let _ = std::fs::rename(&path, dir.join(format!("{FILE_NAME}.bad")));
                Self::default()
            }
        }
    }

    /// 原子写入 `<dir>/config.json`（先写临时文件再改名）。
    pub fn save(&self, dir: &Path) -> std::io::Result<()> {
        let mut data = serde_json::to_vec_pretty(self).map_err(std::io::Error::other)?;
        data.push(b'\n');
        write_atomic(&dir.join(FILE_NAME), &data, None)
    }
}

/// 先写同目录临时文件、`fsync` 后改名；`mode` 仅在 Unix 上生效。
pub(crate) fn write_atomic(path: &Path, data: &[u8], mode: Option<u32>) -> std::io::Result<()> {
    let dir = path.parent().unwrap_or_else(|| Path::new("."));
    std::fs::create_dir_all(dir)?;
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let tmp: PathBuf = dir.join(format!(".{name}.tmp-{}", std::process::id()));
    {
        let mut opts = std::fs::OpenOptions::new();
        opts.write(true).create(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            opts.mode(mode.unwrap_or(0o644));
        }
        #[cfg(not(unix))]
        let _ = mode;
        let mut f = opts.open(&tmp)?;
        f.write_all(data)?;
        f.sync_all()?;
    }
    std::fs::rename(&tmp, path).inspect_err(|_| {
        let _ = std::fs::remove_file(&tmp);
    })
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
pub enum UrlError {
    #[error("invalid server address")]
    Invalid,
    #[error("server address must use https")]
    Insecure,
}

/// 规范化服务器地址：去首尾空白；无协议补 `https://`；只保留 origin；
/// 只接受 `https`，`http` 仅限 `localhost` / `127.0.0.1` / `[::1]`。
pub fn normalize_server_url(input: &str) -> Result<String, UrlError> {
    let s = input.trim();
    if s.is_empty() || s.chars().any(|c| c.is_whitespace() || c.is_control()) {
        return Err(UrlError::Invalid);
    }
    let with_scheme = if s.contains("://") {
        s.to_string()
    } else {
        format!("https://{s}")
    };
    let url = url::Url::parse(&with_scheme).map_err(|_| UrlError::Invalid)?;
    let host = match url.host() {
        Some(h) => h,
        None => return Err(UrlError::Invalid),
    };
    match url.scheme() {
        "https" => {}
        "http" => {
            let loopback = match &host {
                url::Host::Domain(d) => d.eq_ignore_ascii_case("localhost"),
                url::Host::Ipv4(ip) => *ip == std::net::Ipv4Addr::LOCALHOST,
                url::Host::Ipv6(ip) => *ip == std::net::Ipv6Addr::LOCALHOST,
            };
            if !loopback {
                return Err(UrlError::Insecure);
            }
        }
        _ => return Err(UrlError::Invalid),
    }
    let origin = url.origin();
    if !origin.is_tuple() {
        return Err(UrlError::Invalid);
    }
    Ok(origin.ascii_serialization())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalize_table() {
        let ok = [
            ("oryn.example.com", "https://oryn.example.com"),
            ("  https://oryn.example.com/  ", "https://oryn.example.com"),
            (
                "https://Oryn.Example.com/t/x?y=1#z",
                "https://oryn.example.com",
            ),
            (
                "https://user:pw@oryn.example.com/path",
                "https://oryn.example.com",
            ),
            (
                "https://oryn.example.com:8443/a",
                "https://oryn.example.com:8443",
            ),
            ("https://oryn.example.com:443", "https://oryn.example.com"),
            ("http://localhost:7600", "http://localhost:7600"),
            ("http://127.0.0.1:7600/x", "http://127.0.0.1:7600"),
            ("http://[::1]:7600", "http://[::1]:7600"),
            ("localhost:7643", "https://localhost:7643"),
        ];
        for (input, want) in ok {
            assert_eq!(normalize_server_url(input).as_deref(), Ok(want), "{input}");
        }
        let bad = [
            ("http://example.com", UrlError::Insecure),
            ("http://10.0.0.1:7600", UrlError::Insecure),
            ("http://localhost.evil.com", UrlError::Insecure),
            ("", UrlError::Invalid),
            ("   ", UrlError::Invalid),
            ("ftp://example.com", UrlError::Invalid),
            ("https://", UrlError::Invalid),
            ("exa mple.com", UrlError::Invalid),
            ("file:///etc/passwd", UrlError::Invalid),
        ];
        for (input, want) in bad {
            assert_eq!(normalize_server_url(input), Err(want), "{input}");
        }
    }

    #[test]
    fn roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config {
            server_url: Some("https://oryn.example.com".into()),
            device_id: Some("dev_01".into()),
            token_store: TokenStoreKind::File,
            shortcut: "Control+Alt+K".into(),
            close_to_tray: false,
            auto_update: true,
            lang: "en".into(),
        };
        cfg.save(dir.path()).unwrap();
        assert_eq!(Config::load(dir.path()), cfg);
        let raw: serde_json::Value =
            serde_json::from_slice(&std::fs::read(dir.path().join(FILE_NAME)).unwrap()).unwrap();
        assert_eq!(raw["tokenStore"], "file");
        assert_eq!(raw["serverUrl"], "https://oryn.example.com");
        assert_eq!(raw["closeToTray"], false);
    }

    #[test]
    fn missing_file_is_default() {
        let dir = tempfile::tempdir().unwrap();
        let cfg = Config::load(dir.path());
        assert_eq!(cfg.server_url, None);
        assert_eq!(cfg.shortcut, DEFAULT_SHORTCUT);
        assert!(cfg.close_to_tray);
        assert!(!cfg.auto_update);
        assert!(cfg.lang == "zh-CN" || cfg.lang == "en");
    }

    #[test]
    fn partial_file_fills_defaults() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(
            dir.path().join(FILE_NAME),
            r#"{"serverUrl":"https://a.example","lang":"zh-TW"}"#,
        )
        .unwrap();
        let cfg = Config::load(dir.path());
        assert_eq!(cfg.server_url.as_deref(), Some("https://a.example"));
        assert_eq!(cfg.lang, "zh-CN");
        assert_eq!(cfg.shortcut, DEFAULT_SHORTCUT);
        assert_eq!(cfg.token_store, TokenStoreKind::Keyring);
    }

    #[test]
    fn corrupt_file_moved_aside() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join(FILE_NAME), b"{not json").unwrap();
        let cfg = Config::load(dir.path());
        assert_eq!(cfg.server_url, None);
        assert!(!dir.path().join(FILE_NAME).exists());
        assert_eq!(
            std::fs::read(dir.path().join("config.json.bad")).unwrap(),
            b"{not json"
        );
    }
}
