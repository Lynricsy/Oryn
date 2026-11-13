//! 设备令牌存储。默认系统凭据库（`keyring` crate）；Linux 上没有 Secret Service 时回退为 0600 文件。
//!
//! 令牌只存在于 Rust 进程与这里的存储后端，永不经过 IPC。

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use crate::config::TokenStoreKind;

/// 凭据库服务名（= 应用标识）。
pub const SERVICE: &str = "com.lynricsy.oryn";
/// 文件回退的文件名。
pub const FILE_NAME: &str = "credentials.json";

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum StoreError {
    /// 存储后端不可用（没有 Secret Service、被锁、平台失败）；Linux 上据此回退到文件。
    #[error("credential store unavailable: {0}")]
    Unavailable(String),
    #[error("credential store error: {0}")]
    Failed(String),
}

pub trait TokenStore: Send + Sync {
    fn get(&self, server: &str) -> Result<Option<String>, StoreError>;
    fn set(&self, server: &str, token: &str) -> Result<(), StoreError>;
    fn delete(&self, server: &str) -> Result<(), StoreError>;
}

/// 系统凭据库：`keyring::Entry::new("com.lynricsy.oryn", <server>)`。
#[cfg(desktop)]
pub struct KeyringStore;

#[cfg(desktop)]
impl KeyringStore {
    fn entry(server: &str) -> Result<keyring::Entry, StoreError> {
        keyring::Entry::new(SERVICE, server).map_err(map_keyring)
    }

    /// 凭据库能否初始化（不读写任何条目）。
    pub fn available() -> bool {
        keyring::Entry::store_status().is_ok()
    }
}

#[cfg(desktop)]
fn map_keyring(e: keyring::Error) -> StoreError {
    match e {
        keyring::Error::NoDefaultStore
        | keyring::Error::NoStorageAccess(_)
        | keyring::Error::PlatformFailure(_) => StoreError::Unavailable(e.to_string()),
        other => StoreError::Failed(other.to_string()),
    }
}

#[cfg(desktop)]
impl TokenStore for KeyringStore {
    fn get(&self, server: &str) -> Result<Option<String>, StoreError> {
        match Self::entry(server)?.get_password() {
            Ok(v) => Ok(Some(v)),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(map_keyring(e)),
        }
    }

    fn set(&self, server: &str, token: &str) -> Result<(), StoreError> {
        Self::entry(server)?
            .set_password(token)
            .map_err(map_keyring)
    }

    fn delete(&self, server: &str) -> Result<(), StoreError> {
        match Self::entry(server)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(map_keyring(e)),
        }
    }
}

/// 文件回退：`<app_config_dir>/credentials.json`，权限 0600，内容 `{server: token}`。
pub struct FileStore {
    path: PathBuf,
}

impl FileStore {
    pub fn new(dir: &Path) -> Self {
        Self {
            path: dir.join(FILE_NAME),
        }
    }

    fn read(&self) -> Result<BTreeMap<String, String>, StoreError> {
        match std::fs::read(&self.path) {
            Ok(b) => serde_json::from_slice(&b).map_err(|e| StoreError::Failed(e.to_string())),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(BTreeMap::new()),
            Err(e) => Err(StoreError::Failed(e.to_string())),
        }
    }

    fn write(&self, map: &BTreeMap<String, String>) -> Result<(), StoreError> {
        let data = serde_json::to_vec(map).map_err(|e| StoreError::Failed(e.to_string()))?;
        crate::config::write_atomic(&self.path, &data, Some(0o600))
            .map_err(|e| StoreError::Failed(e.to_string()))
    }
}

impl TokenStore for FileStore {
    fn get(&self, server: &str) -> Result<Option<String>, StoreError> {
        Ok(self.read()?.remove(server))
    }

    fn set(&self, server: &str, token: &str) -> Result<(), StoreError> {
        let mut map = self.read()?;
        map.insert(server.to_string(), token.to_string());
        self.write(&map)
    }

    fn delete(&self, server: &str) -> Result<(), StoreError> {
        let mut map = self.read()?;
        if map.remove(server).is_some() {
            self.write(&map)?;
        }
        Ok(())
    }
}

/// 选择存储后端：已记录 `file` → 文件（不再碰凭据库）；否则凭据库可用 → 凭据库；
/// 不可用时只有允许回退的平台（Linux）改用文件，其它平台仍返回凭据库（读写时报 `keyring` 错误）。
pub fn select(
    kind: Option<TokenStoreKind>,
    keyring_available: impl FnOnce() -> bool,
    fallback_allowed: bool,
) -> TokenStoreKind {
    if kind == Some(TokenStoreKind::File) {
        return TokenStoreKind::File;
    }
    if keyring_available() || !fallback_allowed {
        TokenStoreKind::Keyring
    } else {
        TokenStoreKind::File
    }
}

/// 本平台是否允许回退到文件。
pub const FALLBACK_ALLOWED: bool = cfg!(target_os = "linux");

/// 打开存储后端，返回后端与其种类。
pub fn open(kind: Option<TokenStoreKind>, dir: &Path) -> (Box<dyn TokenStore>, TokenStoreKind) {
    #[cfg(desktop)]
    {
        match select(kind, KeyringStore::available, FALLBACK_ALLOWED) {
            TokenStoreKind::Keyring => (Box::new(KeyringStore), TokenStoreKind::Keyring),
            TokenStoreKind::File => (Box::new(FileStore::new(dir)), TokenStoreKind::File),
        }
    }
    #[cfg(mobile)]
    {
        // M7 增加 Keychain / Keystore 实现前，移动端先用文件。
        let _ = kind;
        (Box::new(FileStore::new(dir)), TokenStoreKind::File)
    }
}

/// 令牌保管处：在 [`TokenStore`] 之上实现「写入时凭据库不可用 → 回退文件」。
pub struct Vault {
    store: Box<dyn TokenStore>,
    kind: TokenStoreKind,
    dir: PathBuf,
    fallback_allowed: bool,
}

impl Vault {
    /// 按配置里记录的种类打开。
    pub fn open(kind: TokenStoreKind, dir: &Path) -> Self {
        let (store, kind) = open(Some(kind), dir);
        Self::with_store(store, kind, dir, FALLBACK_ALLOWED)
    }

    /// 只用文件（冒烟模式：不碰凭据库）。
    pub fn file_only(dir: &Path) -> Self {
        Self::with_store(
            Box::new(FileStore::new(dir)),
            TokenStoreKind::File,
            dir,
            false,
        )
    }

    pub fn with_store(
        store: Box<dyn TokenStore>,
        kind: TokenStoreKind,
        dir: &Path,
        fallback_allowed: bool,
    ) -> Self {
        Self {
            store,
            kind,
            dir: dir.to_path_buf(),
            fallback_allowed,
        }
    }

    pub fn kind(&self) -> TokenStoreKind {
        self.kind
    }

    pub fn get(&self, server: &str) -> Result<Option<String>, StoreError> {
        self.store.get(server)
    }

    /// 写入令牌；凭据库不可用且允许回退时改存文件（之后 `kind()` 为 `file`）。
    pub fn set(&mut self, server: &str, token: &str) -> Result<TokenStoreKind, StoreError> {
        match self.store.set(server, token) {
            Ok(()) => Ok(self.kind),
            Err(StoreError::Unavailable(reason))
                if self.fallback_allowed && self.kind == TokenStoreKind::Keyring =>
            {
                // 回退原因只进事件日志（不含令牌），便于排查凭据库为何不可用
                crate::eventlog::log("token.fallback", serde_json::json!({ "reason": reason }));
                let file = FileStore::new(&self.dir);
                file.set(server, token)?;
                self.store = Box::new(file);
                self.kind = TokenStoreKind::File;
                Ok(self.kind)
            }
            Err(e) => Err(e),
        }
    }

    pub fn delete(&self, server: &str) -> Result<(), StoreError> {
        self.store.delete(server)
    }
}

#[cfg(test)]
pub(crate) mod testing {
    use super::*;
    use std::sync::{Arc, Mutex};

    /// 假凭据库：可设置为不可用。
    #[derive(Clone, Default)]
    pub struct FakeStore {
        pub map: Arc<Mutex<BTreeMap<String, String>>>,
        pub unavailable: bool,
    }

    impl TokenStore for FakeStore {
        fn get(&self, server: &str) -> Result<Option<String>, StoreError> {
            if self.unavailable {
                return Err(StoreError::Unavailable("no secret service".into()));
            }
            Ok(self.map.lock().unwrap().get(server).cloned())
        }
        fn set(&self, server: &str, token: &str) -> Result<(), StoreError> {
            if self.unavailable {
                return Err(StoreError::Unavailable("no secret service".into()));
            }
            self.map.lock().unwrap().insert(server.into(), token.into());
            Ok(())
        }
        fn delete(&self, server: &str) -> Result<(), StoreError> {
            if self.unavailable {
                return Err(StoreError::Unavailable("no secret service".into()));
            }
            self.map.lock().unwrap().remove(server);
            Ok(())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::testing::FakeStore;
    use super::*;

    #[test]
    fn file_store_crud() {
        let dir = tempfile::tempdir().unwrap();
        let s = FileStore::new(dir.path());
        assert_eq!(s.get("https://a.example").unwrap(), None);
        s.set("https://a.example", "odt_a").unwrap();
        s.set("https://b.example", "odt_b").unwrap();
        assert_eq!(
            s.get("https://a.example").unwrap().as_deref(),
            Some("odt_a")
        );
        s.delete("https://a.example").unwrap();
        assert_eq!(s.get("https://a.example").unwrap(), None);
        assert_eq!(
            s.get("https://b.example").unwrap().as_deref(),
            Some("odt_b")
        );
        s.delete("https://missing.example").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(dir.path().join(FILE_NAME))
                .unwrap()
                .permissions()
                .mode();
            assert_eq!(mode & 0o777, 0o600);
        }
    }

    #[test]
    fn select_logic() {
        // 凭据库可用 → keyring
        assert_eq!(select(None, || true, true), TokenStoreKind::Keyring);
        // Linux 上不可用 → file
        assert_eq!(select(None, || false, true), TokenStoreKind::File);
        assert_eq!(
            select(Some(TokenStoreKind::Keyring), || false, true),
            TokenStoreKind::File
        );
        // 其它平台不回退：仍是 keyring（读写时报错）
        assert_eq!(select(None, || false, false), TokenStoreKind::Keyring);
        // 已记录 file：不再探测凭据库
        let probed = std::cell::Cell::new(false);
        assert_eq!(
            select(
                Some(TokenStoreKind::File),
                || {
                    probed.set(true);
                    true
                },
                true
            ),
            TokenStoreKind::File
        );
        assert!(!probed.get());
    }

    #[test]
    fn vault_falls_back_on_linux() {
        let dir = tempfile::tempdir().unwrap();
        let fake = FakeStore {
            unavailable: true,
            ..Default::default()
        };
        let mut v = Vault::with_store(Box::new(fake), TokenStoreKind::Keyring, dir.path(), true);
        assert_eq!(
            v.set("https://a.example", "odt_x").unwrap(),
            TokenStoreKind::File
        );
        assert_eq!(v.kind(), TokenStoreKind::File);
        assert_eq!(
            v.get("https://a.example").unwrap().as_deref(),
            Some("odt_x")
        );
        assert!(dir.path().join(FILE_NAME).exists());
    }

    #[test]
    fn vault_hard_error_elsewhere() {
        let dir = tempfile::tempdir().unwrap();
        let fake = FakeStore {
            unavailable: true,
            ..Default::default()
        };
        let mut v = Vault::with_store(Box::new(fake), TokenStoreKind::Keyring, dir.path(), false);
        assert!(matches!(
            v.set("https://a.example", "odt_x"),
            Err(StoreError::Unavailable(_))
        ));
        assert_eq!(v.kind(), TokenStoreKind::Keyring);
        assert!(!dir.path().join(FILE_NAME).exists());
    }

    #[test]
    fn vault_keyring_ok() {
        let dir = tempfile::tempdir().unwrap();
        let fake = FakeStore::default();
        let mut v = Vault::with_store(
            Box::new(fake.clone()),
            TokenStoreKind::Keyring,
            dir.path(),
            true,
        );
        assert_eq!(
            v.set("https://a.example", "odt_x").unwrap(),
            TokenStoreKind::Keyring
        );
        assert_eq!(
            fake.map
                .lock()
                .unwrap()
                .get("https://a.example")
                .map(String::as_str),
            Some("odt_x")
        );
        v.delete("https://a.example").unwrap();
        assert!(fake.map.lock().unwrap().is_empty());
    }
}
