//! 下载处理：落到系统下载目录、文件名净化与去重、完成后发系统通知。

use std::path::{Path, PathBuf};

/// 文件名最大字符数。
const NAME_MAX: usize = 200;

/// 净化建议文件名：只取最后一段（去掉 `/` `\` 之前的部分）、去掉控制字符与 Windows 保留字符、
/// 去掉前导点与首尾空白（以及 Windows 不允许的结尾点）；空名用 `download`。
pub fn sanitize(name: &str) -> String {
    let last = name.rsplit(['/', '\\']).next().unwrap_or("");
    let cleaned: String = last
        .chars()
        .filter(|c| !c.is_control() && !matches!(c, '<' | '>' | ':' | '"' | '|' | '?' | '*'))
        .collect();
    let trimmed = cleaned
        .trim()
        .trim_start_matches('.')
        .trim_end_matches(['.', ' '])
        .trim();
    let limited: String = trimmed.chars().take(NAME_MAX).collect();
    if limited.is_empty() {
        "download".into()
    } else {
        limited
    }
}

/// 在 `dir` 下为 `name` 找一个不重名的路径：`a.txt` → `a (1).txt` → `a (2).txt` …
pub fn unique_path(dir: &Path, name: &str, exists: impl Fn(&Path) -> bool) -> PathBuf {
    let first = dir.join(name);
    if !exists(&first) {
        return first;
    }
    let (stem, ext) = split_ext(name);
    for i in 1..10_000 {
        let candidate = dir.join(format!("{stem} ({i}){ext}"));
        if !exists(&candidate) {
            return candidate;
        }
    }
    dir.join(format!("{stem} ({}){ext}", std::process::id()))
}

/// `a.tar.gz` 视为 `a` + `.tar.gz`（常见双扩展名），其余按最后一个点。
fn split_ext(name: &str) -> (&str, &str) {
    for double in [".tar.gz", ".tar.xz", ".tar.bz2", ".tar.zst"] {
        if name.len() > double.len() && name.to_ascii_lowercase().ends_with(double) {
            let at = name.len() - double.len();
            return (&name[..at], &name[at..]);
        }
    }
    match name.rfind('.') {
        Some(at) if at > 0 => (&name[..at], &name[at..]),
        _ => (name, ""),
    }
}

/// 建议文件名：引擎给出的目标路径的文件名，否则取 URL 最后一段。
pub fn suggested_name(destination: &Path, url: &url::Url) -> String {
    destination
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .filter(|n| !n.is_empty())
        .or_else(|| {
            url.path_segments()
                .and_then(|mut s| s.next_back().map(str::to_string))
                .filter(|n| !n.is_empty())
        })
        .unwrap_or_default()
}

pub use wiring::handler;

mod wiring {
    use std::path::PathBuf;

    use serde_json::json;
    use tauri::webview::DownloadEvent;
    use tauri::{AppHandle, Manager, Webview};

    use crate::{eventlog, host, i18n, notify};

    /// `WebviewWindowBuilder::on_download` 的回调。
    pub fn handler(
        app: AppHandle,
    ) -> impl Fn(Webview, DownloadEvent<'_>) -> bool + Send + Sync + 'static {
        move |_webview, event| {
            match event {
                DownloadEvent::Requested { url, destination } => {
                    let name = super::sanitize(&super::suggested_name(destination, &url));
                    if let Ok(dir) = app.path().download_dir() {
                        *destination = super::unique_path(&dir, &name, |p| p.exists());
                    }
                }
                DownloadEvent::Finished { url, path, success } => {
                    let path: Option<PathBuf> = path;
                    let name = path
                        .as_deref()
                        .map(|p| super::suggested_name(p, &url))
                        .unwrap_or_else(|| super::suggested_name(std::path::Path::new(""), &url));
                    eventlog::log(
                        "download",
                        json!({
                            "name": name,
                            "path": path.as_ref().map(|p| p.display().to_string()),
                            "ok": success,
                        }),
                    );
                    if success {
                        if let Some(d) = host::desktop(&app) {
                            let title = i18n::downloaded(d.lang(), &name);
                            let body = path
                                .as_ref()
                                .map(|p| p.display().to_string())
                                .unwrap_or_default();
                            notify::show_plain(&app, &d.notifier, &title, &body);
                        }
                    }
                }
                _ => {}
            }
            true
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn sanitize_names() {
        let cases = [
            ("report.pdf", "report.pdf"),
            ("../../etc/passwd", "passwd"),
            ("..\\..\\Windows\\system.ini", "system.ini"),
            (".bashrc", "bashrc"),
            ("...", "download"),
            ("..", "download"),
            ("", "download"),
            ("   ", "download"),
            ("a<b>c:d\"e|f?g*h.txt", "abcdefgh.txt"),
            ("bad\u{0}name\n.txt", "badname.txt"),
            ("trailing. . ", "trailing"),
            ("dir/", "download"),
        ];
        for (input, want) in cases {
            assert_eq!(sanitize(input), want, "{input:?}");
        }
        assert_eq!(sanitize(&"a".repeat(500)).chars().count(), NAME_MAX);
    }

    #[test]
    fn unique_paths() {
        let dir = Path::new("/dl");
        let taken: HashSet<PathBuf> = ["/dl/a.txt", "/dl/a (1).txt", "/dl/b", "/dl/x.tar.gz"]
            .into_iter()
            .map(PathBuf::from)
            .collect();
        let exists = |p: &Path| taken.contains(p);
        assert_eq!(
            unique_path(dir, "new.txt", exists),
            PathBuf::from("/dl/new.txt")
        );
        assert_eq!(
            unique_path(dir, "a.txt", exists),
            PathBuf::from("/dl/a (2).txt")
        );
        assert_eq!(unique_path(dir, "b", exists), PathBuf::from("/dl/b (1)"));
        assert_eq!(
            unique_path(dir, "x.tar.gz", exists),
            PathBuf::from("/dl/x (1).tar.gz")
        );
    }

    #[test]
    fn traversal_input_stays_in_dir() {
        let dir = Path::new("/dl");
        for evil in ["../../x", "/etc/passwd", "..", "C:\\x\\..\\y"] {
            let p = unique_path(dir, &sanitize(evil), |_| false);
            assert_eq!(p.parent(), Some(dir), "{evil}");
        }
    }

    #[test]
    fn suggested_from_url() {
        let url = url::Url::parse("https://a.example/f/abc/report.pdf?x=1").unwrap();
        assert_eq!(suggested_name(Path::new(""), &url), "report.pdf");
        assert_eq!(
            suggested_name(Path::new("/tmp/given.bin"), &url),
            "given.bin"
        );
    }
}
