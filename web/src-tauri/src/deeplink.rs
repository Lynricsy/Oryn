//! `oryn://` 深链接解析与接线。

/// 解析深链接：`oryn://thread/<T-…>` → `/t/<id>`；`oryn://`、`oryn://open` → `/`；其余 `None`。
/// scheme 不分大小写，忽略查询与片段。
pub fn parse(url: &str) -> Option<String> {
    let url = url.trim();
    let rest = url
        .get(..7)
        .filter(|p| p.eq_ignore_ascii_case("oryn://"))
        .map(|_| &url[7..])?;
    let rest = rest.split(['?', '#']).next().unwrap_or("");
    let rest = rest.strip_suffix('/').unwrap_or(rest);
    if rest.is_empty() || rest.eq_ignore_ascii_case("open") {
        return Some("/".into());
    }
    let (host, id) = rest.split_once('/')?;
    if !host.eq_ignore_ascii_case("thread") || !valid_thread_id(id) {
        return None;
    }
    Some(format!("/t/{id}"))
}

/// 配对二维码 / 链接：`oryn://pair?server=<encodeURIComponent(origin)>` → 服务器地址原文（只认 http / https，
/// 规范化与回环限制交给 [`crate::config::normalize_server_url`]）；其余 `None`。
pub fn parse_pair(url: &str) -> Option<String> {
    let u = url::Url::parse(url.trim()).ok()?;
    if !u.scheme().eq_ignore_ascii_case("oryn")
        || !u.host_str().is_some_and(|h| h.eq_ignore_ascii_case("pair"))
        || !matches!(u.path(), "" | "/")
    {
        return None;
    }
    let server = u
        .query_pairs()
        .find(|(k, _)| k == "server")
        .map(|(_, v)| v.trim().to_string())?;
    let scheme = server.split_once("://").map(|(s, _)| s)?;
    (scheme.eq_ignore_ascii_case("http") || scheme.eq_ignore_ascii_case("https")).then_some(server)
}

/// `^T-[0-9a-fA-F-]{36}$`。
fn valid_thread_id(id: &str) -> bool {
    id.strip_prefix("T-").is_some_and(|rest| {
        rest.len() == 36 && rest.bytes().all(|b| b.is_ascii_hexdigit() || b == b'-')
    })
}

#[cfg(mobile)]
pub use wiring::open_url;
pub use wiring::setup;

mod wiring {
    use serde_json::json;
    use tauri::AppHandle;
    use tauri_plugin_deep_link::DeepLinkExt;

    use crate::eventlog;
    use crate::host;

    /// 处理一个 `oryn:` 链接：配对链接 → 发起连接；站内链接 → 打开路径。
    pub fn open_url(app: &AppHandle, url: &str) {
        if let Some(server) = super::parse_pair(url) {
            eventlog::log("pair.link", json!({"server": server, "source": "deeplink"}));
            host::connect_from_link(app, server);
            return;
        }
        let path = super::parse(url);
        eventlog::log(
            "deeplink",
            json!({"url": eventlog::redact_url(url), "path": path}),
        );
        if let Some(path) = path {
            host::open_path(app, &path);
        }
    }

    /// 注册深链接回调；冷启动的链接作为待开路径；开发态与 AppImage 运行期注册协议（移动端协议是静态声明的）。
    pub fn setup(app: &AppHandle, register: bool) {
        let dl = app.deep_link();
        if let Ok(Some(urls)) = dl.get_current() {
            for url in urls {
                open_url(app, url.as_str());
            }
        }
        let handle_app = app.clone();
        dl.on_open_url(move |event| {
            for url in event.urls() {
                open_url(&handle_app, url.as_str());
            }
        });
        #[cfg(any(windows, target_os = "linux"))]
        if register {
            if let Err(e) = dl.register_all() {
                eventlog::log("deeplink.register", json!({"error": e.to_string()}));
            }
        }
        #[cfg(not(any(windows, target_os = "linux")))]
        let _ = register;
    }
}

#[cfg(test)]
mod tests {
    use super::{parse, parse_pair};

    #[test]
    fn pair_links() {
        assert_eq!(
            parse_pair("oryn://pair?server=https%3A%2F%2Foryn.example.com").as_deref(),
            Some("https://oryn.example.com")
        );
        assert_eq!(
            parse_pair("ORYN://Pair/?server=http%3A%2F%2Flocalhost%3A7600&x=1").as_deref(),
            Some("http://localhost:7600")
        );
        for bad in [
            "oryn://pair",
            "oryn://pair?server=",
            "oryn://pair?server=oryn.example.com",
            "oryn://pair?server=javascript%3Aalert(1)",
            "oryn://pair?server=file%3A%2F%2F%2Fetc%2Fpasswd",
            "oryn://pair/extra?server=https%3A%2F%2Fa.example",
            "oryn://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab",
            "https://pair?server=https%3A%2F%2Fa.example",
            "not a url",
        ] {
            assert_eq!(parse_pair(bad), None, "{bad}");
        }
        // 配对链接不是站内路径
        assert_eq!(parse("oryn://pair?server=https%3A%2F%2Fa.example"), None);
    }

    const ID: &str = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab";

    #[test]
    fn parse_table() {
        let want = format!("/t/{ID}");
        assert_eq!(parse(&format!("oryn://thread/{ID}")), Some(want.clone()));
        assert_eq!(parse(&format!("ORYN://thread/{ID}")), Some(want.clone()));
        assert_eq!(parse(&format!("Oryn://Thread/{ID}/")), Some(want.clone()));
        assert_eq!(
            parse(&format!("oryn://thread/{ID}?x=1#frag")),
            Some(want.clone())
        );
        assert_eq!(
            parse(&format!("oryn://thread/{}", ID.to_uppercase())),
            Some(format!("/t/{}", ID.to_uppercase()))
        );
        assert_eq!(parse("oryn://"), Some("/".into()));
        assert_eq!(parse("oryn://open"), Some("/".into()));
        assert_eq!(parse("oryn://open/"), Some("/".into()));
    }

    #[test]
    fn parse_rejects() {
        for bad in [
            "oryn://thread/T-123",
            "oryn://thread/../x",
            "oryn://thread/../../settings",
            "oryn://thread/",
            "oryn://settings",
            "oryn://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789zz",
            "oryn://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab/extra",
            "https://thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab",
            "oryn:thread/T-01932f6e-8d1a-7c3b-9f2e-0123456789ab",
            "",
            "oryn:/",
        ] {
            assert_eq!(parse(bad), None, "{bad}");
        }
    }
}
