//! Oryn 移动端原生能力的 Rust 侧：Keystore 保险库、系统通知、UnifiedPush / FCM、分享接收、系统栏颜色。
//!
//! 命令表为空（见 `build.rs`）：远程页面拿不到任何原生命令；这里的方法只给壳的 Rust 代码调用。
//! 数据形状在 [`models`]，不依赖平台；插件句柄与注册只在 Android 上存在。

pub mod models;

pub use models::*;

#[cfg(target_os = "android")]
pub use android::{init, Error, OrynMobile, OrynMobileExt};

#[cfg(target_os = "android")]
mod android {
    use serde::de::{DeserializeOwned, IgnoredAny};
    use serde::Serialize;
    use serde_json::json;
    use tauri::ipc::{Channel, InvokeResponseBody};
    use tauri::plugin::{Builder, PluginHandle, TauriPlugin};
    use tauri::{Manager, Runtime};

    use crate::models::*;

    /// Kotlin 插件的包名与类名。
    const PLUGIN_PACKAGE: &str = "com.lynricsy.oryn.mobile";
    const PLUGIN_CLASS: &str = "OrynMobilePlugin";

    #[derive(Debug, thiserror::Error)]
    pub enum Error {
        #[error(transparent)]
        Invoke(#[from] tauri::plugin::mobile::PluginInvokeError),
        /// 进程里此刻没有 Activity（被系统销毁、还没新建）。
        #[error("no Android activity")]
        NoActivity,
    }

    pub type Result<T, E = Error> = std::result::Result<T, E>;

    /// 插件命令要经 wry 的 `dispatch` 送到某个 Activity 的 UI 线程；Activity 被系统销毁（进程保留）后、新的还没建好时，
    /// wry 那边没有 Activity 代理，`dispatch` 会 `expect("no available activity")` 直接 panic。
    /// 这里先看 tao 是否登记着 Activity（`onCreate` 时登记、非配置变更的 `onDestroy` 时移除），没有就返回错误。
    fn ensure_activity() -> Result<()> {
        match tauri::tao::platform::android::prelude::main_android_context() {
            Some(_) => Ok(()),
            None => Err(Error::NoActivity),
        }
    }

    /// 注册插件（`Builder::new("oryn-mobile")`），并把句柄放进应用状态。
    pub fn init<R: Runtime>() -> TauriPlugin<R> {
        Builder::new("oryn-mobile")
            .setup(|app, api| {
                let handle = api.register_android_plugin(PLUGIN_PACKAGE, PLUGIN_CLASS)?;
                app.manage(OrynMobile(handle));
                Ok(())
            })
            .build()
    }

    /// 插件句柄；方法与 Kotlin 命令一一对应。
    pub struct OrynMobile<R: Runtime>(PluginHandle<R>);

    impl<R: Runtime> Clone for OrynMobile<R> {
        fn clone(&self) -> Self {
            Self(self.0.clone())
        }
    }

    pub trait OrynMobileExt<R: Runtime> {
        fn oryn_mobile(&self) -> &OrynMobile<R>;
    }

    impl<R: Runtime, T: Manager<R>> OrynMobileExt<R> for T {
        fn oryn_mobile(&self) -> &OrynMobile<R> {
            self.state::<OrynMobile<R>>().inner()
        }
    }

    #[derive(Serialize)]
    struct RegisterListener {
        event: &'static str,
        handler: Channel,
    }

    #[derive(serde::Deserialize)]
    struct VaultToken {
        token: Option<String>,
    }

    impl<R: Runtime> OrynMobile<R> {
        fn run<T: DeserializeOwned>(&self, cmd: &str, args: impl Serialize) -> Result<T> {
            ensure_activity()?;
            Ok(self.0.run_mobile_plugin(cmd, args)?)
        }

        /// 无返回值的命令：忽略 Kotlin 给的任何应答体。
        fn call(&self, cmd: &str, args: impl Serialize) -> Result<()> {
            self.run::<IgnoredAny>(cmd, args).map(|_| ())
        }

        pub fn device_info(&self) -> Result<DeviceInfo> {
            self.run("deviceInfo", ())
        }

        pub fn vault_get(&self, server: &str) -> Result<Option<String>> {
            Ok(self
                .run::<VaultToken>("vaultGet", json!({"server": server}))?
                .token)
        }

        pub fn vault_set(&self, server: &str, token: &str) -> Result<()> {
            self.call("vaultSet", json!({"server": server, "token": token}))
        }

        pub fn vault_delete(&self, server: &str) -> Result<()> {
            self.call("vaultDelete", json!({"server": server}))
        }

        pub fn notify(&self, args: &NotifyArgs) -> Result<NotifyResult> {
            self.run("notify", args)
        }

        pub fn push_state(&self) -> Result<PushState> {
            self.run("pushState", ())
        }

        /// 可能先弹系统通知权限、再等分发器选择：异步等待，不占线程。
        pub async fn push_enable(&self, vapid: &str) -> Result<PushState> {
            ensure_activity()?;
            Ok(self
                .0
                .run_mobile_plugin_async("pushEnable", json!({"vapid": vapid}))
                .await?)
        }

        pub fn push_disable(&self) -> Result<PushState> {
            self.run("pushDisable", ())
        }

        /// 记下已登记的端点 / 令牌、订阅 id 与服务器（推送服务在只有它存活时据此重新登记）。
        pub fn push_mark_synced(&self, endpoint: &str, id: &str, server: &str) -> Result<()> {
            self.call(
                "pushMarkSynced",
                json!({"endpoint": endpoint, "id": id, "server": server}),
            )
        }

        pub fn open_notification_settings(&self) -> Result<()> {
            self.call("openNotificationSettings", ())
        }

        pub fn set_chrome(&self, dark: bool, color: &str) -> Result<()> {
            self.call("setChrome", json!({"dark": dark, "color": color}))
        }

        pub fn clear_history(&self) -> Result<()> {
            self.call("clearHistory", ())
        }

        pub fn share_ack(&self, id: &str) -> Result<()> {
            self.call("shareAck", json!({"id": id}))
        }

        pub fn toast(&self, text: &str) -> Result<()> {
            self.call("toast", json!({"text": text}))
        }

        /// 订阅 Kotlin 的 `event`；无法识别的事件被忽略。
        pub fn on_event(&self, f: impl Fn(Event) + Send + Sync + 'static) -> Result<()> {
            let handler = Channel::new(move |body| {
                if let InvokeResponseBody::Json(payload) = body {
                    if let Ok(event) = serde_json::from_str::<Event>(&payload) {
                        f(event);
                    }
                }
                Ok(())
            });
            self.call(
                "registerListener",
                RegisterListener {
                    event: "event",
                    handler,
                },
            )
        }
    }
}
