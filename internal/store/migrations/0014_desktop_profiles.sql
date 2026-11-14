-- +goose Up
-- M8：命名浏览器 Profile、每台 orb 的桌面镜像与 Profile 绑定、Profile 审计事件。

-- 命名浏览器 Profile（归档文件在 data_dir/browser-profiles/<id>/<revision>.oss）。
-- 独占租约 = holder_orb_id / held_at；fence 是围栏代数：租约每换一次持有者（从无到有或被接管）+1，
-- 保存时必须带上取得租约时的代数，不符即丢弃（记 profile.save_fenced）。
CREATE TABLE browser_profiles (
    id             TEXT    PRIMARY KEY,                       -- bpf_<ULID>
    name           TEXT    NOT NULL UNIQUE,
    revision       INTEGER NOT NULL DEFAULT 0,                -- 0 = 空，没有归档文件
    size_bytes     INTEGER NOT NULL DEFAULT 0,                -- 当前修订归档的密文字节数
    content_sha256 TEXT,                                      -- 当前修订明文归档的摘要（内容未变时不产生新修订）
    holder_orb_id  TEXT    REFERENCES orbs(id) ON DELETE SET NULL,  -- 独占租约
    held_at        INTEGER,
    fence          INTEGER NOT NULL DEFAULT 0,                -- 围栏代数
    save_error     TEXT,                                      -- too_large | export_failed | takeover_unsaved
    saved_at       INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX browser_profiles_holder ON browser_profiles (holder_orb_id) WHERE holder_orb_id IS NOT NULL;

-- 每台 orb 的桌面镜像与 Profile 绑定；只读使用者（profile_mode = 'readonly'）也记录在这里。
CREATE TABLE orb_desktops (
    orb_id                TEXT    PRIMARY KEY REFERENCES orbs(id) ON DELETE CASCADE,
    profile_id            TEXT    REFERENCES browser_profiles(id) ON DELETE SET NULL,  -- 选用的 Profile
    profile_mode          TEXT    NOT NULL DEFAULT 'exclusive',  -- exclusive | readonly
    takeover              INTEGER NOT NULL DEFAULT 0,            -- 下次同步时接管（用后清零）
    fence                 INTEGER NOT NULL DEFAULT 0,            -- 本 orb 取得租约时的围栏代数（只读时为 0）
    readonly_reason       TEXT,                                  -- requested | taken_over
    readonly_by           TEXT,                                  -- 接管方线程标题
    readonly_by_thread_id TEXT,                                  -- 接管方的代表线程
    readonly_at           INTEGER,                               -- 成为只读使用者的时间
    local_rev             INTEGER NOT NULL DEFAULT -1,           -- orb 本地目录对应的修订；-1 = 未装载
    profile_error         TEXT,
    reported              INTEGER NOT NULL DEFAULT 0,            -- orbd 上报过 desktop.state
    display_ready         INTEGER NOT NULL DEFAULT 0,
    browser               TEXT    NOT NULL DEFAULT 'stopped',    -- stopped | starting | running
    dirty                 INTEGER NOT NULL DEFAULT 0,
    updated_at            INTEGER NOT NULL
);
CREATE INDEX orb_desktops_profile ON orb_desktops (profile_id) WHERE profile_id IS NOT NULL;

-- Profile 审计事件（profile.saved / profile.save_failed / profile.save_fenced / profile.taken_over）。
CREATE TABLE browser_profile_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id TEXT    NOT NULL REFERENCES browser_profiles(id) ON DELETE CASCADE,
    orb_id     TEXT,                                          -- 相关 orb（不建外键：审计记录不随 orb 变化）
    ts         INTEGER NOT NULL,
    event      TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '{}'                  -- JSON
);
CREATE INDEX browser_profile_events_profile ON browser_profile_events (profile_id, ts);

-- 不建外键（便于回滚）；删除 Profile 时由代码清空。
ALTER TABLE projects ADD COLUMN default_browser_profile_id TEXT;

-- +goose Down
ALTER TABLE projects DROP COLUMN default_browser_profile_id;
DROP INDEX browser_profile_events_profile;
DROP TABLE browser_profile_events;
DROP INDEX orb_desktops_profile;
DROP TABLE orb_desktops;
DROP INDEX browser_profiles_holder;
DROP TABLE browser_profiles;
