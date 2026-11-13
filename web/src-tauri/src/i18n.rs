//! 壳内 Rust 侧文字（托盘菜单、系统通知）。两种语言用枚举键 + `match` 保证齐全。

use serde::{Deserialize, Serialize};

/// 界面语言。
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Lang {
    #[serde(rename = "zh-CN")]
    ZhCn,
    #[serde(rename = "en")]
    En,
}

impl Lang {
    /// 解析 BCP 47 语言标签：`zh*` → 简体中文，其余 → 英文。
    pub fn from_tag(tag: &str) -> Self {
        if tag.trim().to_ascii_lowercase().starts_with("zh") {
            Lang::ZhCn
        } else {
            Lang::En
        }
    }

    /// 系统语言（取不到时英文）。
    pub fn system() -> Self {
        sys_locale::get_locale()
            .map(|l| Self::from_tag(&l))
            .unwrap_or(Lang::En)
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Lang::ZhCn => "zh-CN",
            Lang::En => "en",
        }
    }
}

/// 固定文案的键。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Key {
    OpenOryn,
    NewThread,
    CheckUpdates,
    ChangeServer,
    Quit,
    UntitledThread,
}

/// 固定文案。
pub fn t(lang: Lang, key: Key) -> &'static str {
    match (lang, key) {
        (Lang::ZhCn, Key::OpenOryn) => "打开 Oryn",
        (Lang::En, Key::OpenOryn) => "Open Oryn",
        (Lang::ZhCn, Key::NewThread) => "新建线程",
        (Lang::En, Key::NewThread) => "New thread",
        (Lang::ZhCn, Key::CheckUpdates) => "检查更新…",
        (Lang::En, Key::CheckUpdates) => "Check for updates…",
        (Lang::ZhCn, Key::ChangeServer) => "更换服务器…",
        (Lang::En, Key::ChangeServer) => "Change server…",
        (Lang::ZhCn, Key::Quit) => "退出 Oryn",
        (Lang::En, Key::Quit) => "Quit Oryn",
        (Lang::ZhCn, Key::UntitledThread) => "未命名线程",
        (Lang::En, Key::UntitledThread) => "Untitled thread",
    }
}

/// 托盘状态行：「N 个待你处理 · M 个未读」，为 0 的部分省略；都为 0 时 `None`。
pub fn status_line(lang: Lang, awaiting: u32, unread: u32) -> Option<String> {
    let mut parts = Vec::with_capacity(2);
    if awaiting > 0 {
        parts.push(match lang {
            Lang::ZhCn => format!("{awaiting} 个待你处理"),
            Lang::En => format!("{awaiting} awaiting you"),
        });
    }
    if unread > 0 {
        parts.push(match lang {
            Lang::ZhCn => format!("{unread} 个未读"),
            Lang::En => format!("{unread} unread"),
        });
    }
    (!parts.is_empty()).then(|| parts.join(" · "))
}

/// 托盘里已下载更新的菜单项。
pub fn restart_to_update(lang: Lang, version: &str) -> String {
    match lang {
        Lang::ZhCn => format!("重启以更新到 v{version}"),
        Lang::En => format!("Restart to update to v{version}"),
    }
}

/// 下载完成的系统通知标题。
pub fn downloaded(lang: Lang, name: &str) -> String {
    match lang {
        Lang::ZhCn => format!("已下载 {name}"),
        Lang::En => format!("Downloaded {name}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn from_tag() {
        assert_eq!(Lang::from_tag("zh-CN"), Lang::ZhCn);
        assert_eq!(Lang::from_tag("zh_TW"), Lang::ZhCn);
        assert_eq!(Lang::from_tag("ZH"), Lang::ZhCn);
        assert_eq!(Lang::from_tag("en-US"), Lang::En);
        assert_eq!(Lang::from_tag("de"), Lang::En);
        assert_eq!(Lang::from_tag(""), Lang::En);
    }

    #[test]
    fn status_line_parts() {
        assert_eq!(status_line(Lang::ZhCn, 0, 0), None);
        assert_eq!(
            status_line(Lang::ZhCn, 2, 3).as_deref(),
            Some("2 个待你处理 · 3 个未读")
        );
        assert_eq!(
            status_line(Lang::En, 1, 0).as_deref(),
            Some("1 awaiting you")
        );
        assert_eq!(status_line(Lang::En, 0, 4).as_deref(), Some("4 unread"));
    }

    #[test]
    fn serde_tags() {
        assert_eq!(serde_json::to_string(&Lang::ZhCn).unwrap(), "\"zh-CN\"");
        assert_eq!(serde_json::from_str::<Lang>("\"en\"").unwrap(), Lang::En);
    }
}
