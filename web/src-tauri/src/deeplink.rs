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

/// `^T-[0-9a-fA-F-]{36}$`。
fn valid_thread_id(id: &str) -> bool {
    id.strip_prefix("T-").is_some_and(|rest| {
        rest.len() == 36 && rest.bytes().all(|b| b.is_ascii_hexdigit() || b == b'-')
    })
}

pub use wiring::setup;

mod wiring {
    use serde_json::json;
    use tauri::AppHandle;
    use tauri_plugin_deep_link::DeepLinkExt;

    use crate::eventlog;
    use crate::host;

    fn handle(app: &AppHandle, url: &str) {
        let path = super::parse(url);
        eventlog::log(
            "deeplink",
            json!({"url": eventlog::redact_url(url), "path": path}),
        );
        if let Some(path) = path {
            host::open_path(app, &path);
        }
    }

    /// 注册深链接回调；冷启动的链接作为待开路径；开发态与 AppImage 运行期注册协议。
    pub fn setup(app: &AppHandle, register: bool) {
        let dl = app.deep_link();
        if let Ok(Some(urls)) = dl.get_current() {
            for url in urls {
                handle(app, url.as_str());
            }
        }
        let handle_app = app.clone();
        dl.on_open_url(move |event| {
            for url in event.urls() {
                handle(&handle_app, url.as_str());
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
    use super::parse;

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
