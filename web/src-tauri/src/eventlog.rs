//! 事件日志：验收与排障的观察面。
//!
//! 环境变量 `ORYN_DESKTOP_EVENT_LOG=<文件>` 存在时，每个事件向该文件追加一行 JSON
//! `{"ts": <毫秒>, "event": "...", ...字段}`；未设置时为空操作。Android 没有环境变量可设：
//! 启动时由 [`init_file`] 指向插件给的 `cache/events.jsonl`（仅 debug 包），Kotlin 写同一个文件。
//! 永不写入设备令牌、verifier、一次性码——URL 一律先经 [`redact_url`]。

use std::fs::OpenOptions;
use std::io::Write;
use std::path::Path;
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

use serde_json::{Map, Value};

/// 环境变量名：事件日志文件路径。
pub const ENV: &str = "ORYN_DESKTOP_EVENT_LOG";

type Sink = Arc<Mutex<Box<dyn Write + Send>>>;

/// 事件日志句柄；无输出目标时 `log` 为空操作。可克隆，克隆共享同一输出。
#[derive(Clone, Default)]
pub struct EventLog(Option<Sink>);

impl EventLog {
    /// 按 [`ENV`] 打开；未设置或打开失败时返回空操作句柄。
    pub fn from_env() -> Self {
        match std::env::var_os(ENV) {
            Some(path) if !path.is_empty() => Self::to_file(Path::new(&path)).unwrap_or_default(),
            _ => Self::default(),
        }
    }

    /// 以追加方式写入指定文件。
    pub fn to_file(path: &Path) -> std::io::Result<Self> {
        let file = OpenOptions::new().create(true).append(true).open(path)?;
        Ok(Self::to_writer(Box::new(file)))
    }

    /// 写入任意输出（测试用内存缓冲）。
    pub fn to_writer(w: Box<dyn Write + Send>) -> Self {
        Self(Some(Arc::new(Mutex::new(w))))
    }

    /// 追加一条事件。`fields` 须为对象（其它类型被忽略）；`ts` 与 `event` 由这里填写。
    pub fn log(&self, event: &str, fields: Value) {
        let Some(sink) = &self.0 else { return };
        let line = render(now_ms(), event, fields);
        let mut w = sink.lock().unwrap_or_else(|e| e.into_inner());
        // 日志写失败不影响应用本身
        let _ = w.write_all(line.as_bytes());
        let _ = w.flush();
    }
}

static GLOBAL: OnceLock<EventLog> = OnceLock::new();

fn global() -> &'static EventLog {
    GLOBAL.get_or_init(EventLog::from_env)
}

/// 把进程级事件日志指向 `path`（须在第一条事件之前调用；之后调用无效）。
#[cfg(target_os = "android")]
pub fn init_file(path: &Path) {
    if let Ok(log) = EventLog::to_file(path) {
        let _ = GLOBAL.set(log);
    }
}

/// 向进程级事件日志（[`ENV`] 指定的文件）追加一条事件。
pub fn log(event: &str, fields: Value) {
    global().log(event, fields);
}

/// 进程级事件日志句柄（供需要注入日志的模块持有）。
pub fn handle() -> EventLog {
    global().clone()
}

fn render(ts: i64, event: &str, fields: Value) -> String {
    let mut obj = Map::new();
    obj.insert("ts".into(), Value::from(ts));
    obj.insert("event".into(), Value::from(event));
    if let Value::Object(extra) = fields {
        for (k, v) in extra {
            if k != "ts" && k != "event" {
                obj.insert(k, v);
            }
        }
    }
    let mut line = Value::Object(obj).to_string();
    line.push('\n');
    line
}

/// 当前 UTC 毫秒。
pub fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

/// 去掉 URL 查询串里的 `code` 参数（一次性码）；无法解析的输入原样返回。
pub fn redact_url(input: &str) -> String {
    let Ok(mut url) = url::Url::parse(input) else {
        return input.to_string();
    };
    if url.query().is_none() {
        return url.into();
    }
    let kept: Vec<(String, String)> = url
        .query_pairs()
        .filter(|(k, _)| k != "code")
        .map(|(k, v)| (k.into_owned(), v.into_owned()))
        .collect();
    if kept.is_empty() {
        url.set_query(None);
    } else {
        url.query_pairs_mut().clear().extend_pairs(kept);
    }
    url.into()
}

#[cfg(test)]
pub(crate) mod testing {
    use super::*;

    /// 内存缓冲：`EventLog::to_writer` 的测试输出。
    #[derive(Clone, Default)]
    pub struct Buffer(pub Arc<Mutex<Vec<u8>>>);

    impl Write for Buffer {
        fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    impl Buffer {
        pub fn text(&self) -> String {
            String::from_utf8(self.0.lock().unwrap().clone()).unwrap()
        }
        pub fn events(&self) -> Vec<Value> {
            self.text()
                .lines()
                .map(|l| serde_json::from_str(l).unwrap())
                .collect()
        }
    }

    pub fn memory() -> (EventLog, Buffer) {
        let buf = Buffer::default();
        (EventLog::to_writer(Box::new(buf.clone())), buf)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn writes_jsonl_to_file() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("events.jsonl");
        let log = EventLog::to_file(&path).unwrap();
        log.log(
            "app.start",
            json!({"version": "0.1.0", "args": ["--smoke"]}),
        );
        log.log("window", json!({"visible": false, "event": "覆盖无效"}));
        let text = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<Value> = text
            .lines()
            .map(|l| serde_json::from_str(l).unwrap())
            .collect();
        assert_eq!(lines.len(), 2);
        assert_eq!(lines[0]["event"], "app.start");
        assert_eq!(lines[0]["version"], "0.1.0");
        assert!(lines[0]["ts"].as_i64().unwrap() > 0);
        assert_eq!(lines[1]["event"], "window");
        assert_eq!(lines[1]["visible"], false);
    }

    #[test]
    fn disabled_log_is_noop() {
        EventLog::default().log("x", json!({}));
    }

    #[test]
    fn redact_url_drops_code() {
        assert_eq!(
            redact_url("https://a.example/auth/app?code=oac_secret&next=%2Ft%2Fx"),
            "https://a.example/auth/app?next=%2Ft%2Fx"
        );
        assert_eq!(
            redact_url("https://a.example/auth/app?code=oac_secret"),
            "https://a.example/auth/app"
        );
        assert_eq!(
            redact_url("https://a.example/pair"),
            "https://a.example/pair"
        );
        assert_eq!(redact_url("not a url"), "not a url");
    }
}
