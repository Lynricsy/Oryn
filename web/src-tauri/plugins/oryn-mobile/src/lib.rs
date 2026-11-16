//! Oryn 移动端原生能力的 Rust 侧：Android 上是 Keystore 保险库、系统通知、UnifiedPush / FCM、分享接收、系统栏颜色；
//! iOS 上是下沉侧栏的原生底栏（iOS 26+，Swift 实现在 `ios/`）。
//!
//! 命令表为空（见 `build.rs`）：远程页面拿不到任何原生命令；这里的方法只给壳的 Rust 代码调用。
//! 数据形状在 [`models`]，不依赖平台；插件句柄与注册只在 Android / iOS 上存在。

pub mod models;

pub use models::*;

#[cfg(any(target_os = "android", target_os = "ios"))]
pub use native::{init, Error, OrynMobile, OrynMobileExt};

#[cfg(any(target_os = "android", target_os = "ios"))]
mod native {
    use serde::de::{DeserializeOwned, IgnoredAny};
    use serde::Serialize;
    use serde_json::json;
    use tauri::ipc::{Channel, InvokeResponseBody};
    use tauri::plugin::{Builder, PluginHandle, TauriPlugin};
    use tauri::{Manager, Runtime};

    use crate::models::*;

    /// Kotlin 插件的包名与类名。
    #[cfg(target_os = "android")]
    const PLUGIN_PACKAGE: &str = "com.lynricsy.oryn.mobile";
    #[cfg(target_os = "android")]
    const PLUGIN_CLASS: &str = "OrynMobilePlugin";

    // Swift 侧的入口 `@_cdecl("init_plugin_oryn_mobile")`（`ios/Sources/OrynMobilePlugin.swift`）。
    #[cfg(target_os = "ios")]
    tauri::ios_plugin_binding!(init_plugin_oryn_mobile);

    #[derive(Debug, thiserror::Error)]
    pub enum Error {
        #[error(transparent)]
        Invoke(#[from] tauri::plugin::mobile::PluginInvokeError),
        /// 进程里此刻没有 Activity（被系统销毁、还没新建）。
        #[cfg(target_os = "android")]
        #[error("no Android activity")]
        NoActivity,
    }

    pub type Result<T, E = Error> = std::result::Result<T, E>;

    /// 插件命令要经 wry 的 `dispatch` 送到某个 Activity 的 UI 线程；Activity 被系统销毁（进程保留）后、新的还没建好时，
    /// wry 那边没有 Activity 代理，`dispatch` 会 `expect("no available activity")` 直接 panic。
    /// 这里先看 tao 是否登记着 Activity（`onCreate` 时登记、非配置变更的 `onDestroy` 时移除），没有就返回错误。
    #[cfg(target_os = "android")]
    fn ensure_activity() -> Result<()> {
        match tauri::tao::platform::android::prelude::main_android_context() {
            Some(_) => Ok(()),
            None => Err(Error::NoActivity),
        }
    }

    /// iOS 的视图控制器与进程同寿命，没有「此刻没有 Activity」的窗口期。
    #[cfg(target_os = "ios")]
    fn ensure_activity() -> Result<()> {
        Ok(())
    }

    /// 注册插件（`Builder::new("oryn-mobile")`），并把句柄放进应用状态。
    pub fn init<R: Runtime>() -> TauriPlugin<R> {
        Builder::new("oryn-mobile")
            .setup(|app, api| {
                #[cfg(target_os = "android")]
                let handle = api.register_android_plugin(PLUGIN_PACKAGE, PLUGIN_CLASS)?;
                #[cfg(target_os = "ios")]
                let handle = api.register_ios_plugin(init_plugin_oryn_mobile)?;
                app.manage(OrynMobile(handle));
                Ok(())
            })
            .build()
    }

    /// 插件句柄；方法与 Kotlin / Swift 命令一一对应。
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

    #[cfg(target_os = "android")]
    #[derive(serde::Deserialize)]
    struct VaultToken {
        token: Option<String>,
    }

    /// 两个平台共用：命令调用与事件订阅。
    impl<R: Runtime> OrynMobile<R> {
        fn run<T: DeserializeOwned>(&self, cmd: &str, args: impl Serialize) -> Result<T> {
            ensure_activity()?;
            Ok(self.0.run_mobile_plugin(cmd, args)?)
        }

        /// 无返回值的命令：忽略原生侧给的任何应答体。
        fn call(&self, cmd: &str, args: impl Serialize) -> Result<()> {
            self.run::<IgnoredAny>(cmd, args).map(|_| ())
        }

        /// 订阅原生侧的 `event`；无法识别的事件被忽略。
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

    #[cfg(target_os = "android")]
    impl<R: Runtime> OrynMobile<R> {
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
    }

    /// 原生底栏（Swift 在 iOS 26+ 才真正实现）。Swift 只把界面操作派到主线程就应答，不等它画完。
    #[cfg(target_os = "ios")]
    impl<R: Runtime> OrynMobile<R> {
        /// 整份状态；`None` = 撤掉原生底栏（任何系统版本都成功）。iOS < 26 且 `spec` 非空时 Swift 以 `unsupported` 拒绝。
        pub fn tab_bar_set(&self, spec: Option<&TabBarSpec>) -> Result<()> {
            self.call("tabBarSet", json!({ "spec": spec }))
        }

        /// 揭示边与透明度；还没 set 过时是空操作。
        pub fn tab_bar_reveal(&self, reveal: &TabBarReveal) -> Result<()> {
            self.call("tabBarReveal", reveal)
        }
    }
}
