-- +goose Up
-- M23：Boxes。box 是 orbs.box = 1 的常驻 orb，boxes 行持有名字、期望状态与运行时段；
-- 一台 box 跑多个 app，app 名全库唯一（地址 <app>.<portal_domain>）。时间戳均为 Unix 毫秒。
ALTER TABLE orbs ADD COLUMN box INTEGER NOT NULL DEFAULT 0 CHECK (box IN (0, 1));

CREATE TABLE boxes (
    id          TEXT    PRIMARY KEY,                   -- box_<ULID>
    name        TEXT    NOT NULL UNIQUE,
    orb_id      TEXT    NOT NULL UNIQUE REFERENCES orbs (id),
    desired     TEXT    NOT NULL DEFAULT 'running' CHECK (desired IN ('running', 'stopped')),
    desired_by  TEXT    NOT NULL DEFAULT 'owner' CHECK (desired_by IN ('owner', 'schedule')),
    desired_at  INTEGER NOT NULL,                      -- 晚于它的时段边界覆盖 desired
    sched_on    TEXT,                                  -- 'HH:MM'；三列都为 NULL = 一直运行
    sched_off   TEXT,
    sched_tz    TEXT,
    created_at  INTEGER NOT NULL
);

CREATE TABLE box_apps (
    id                 TEXT    PRIMARY KEY,            -- bxa_<ULID>
    box_id             TEXT    NOT NULL REFERENCES boxes (id) ON DELETE CASCADE,
    name               TEXT    NOT NULL UNIQUE,
    port               INTEGER NOT NULL,
    public             BOOLEAN NOT NULL DEFAULT 1,
    -- repo / root / source 描述 live release；首次激活之前是首次部署的值
    repo               TEXT    NOT NULL,               -- 规范化的 origin，或 'local:<根提交 sha>'
    root               TEXT    NOT NULL DEFAULT '',    -- app 在仓库里的目录，'' = 仓库根
    source             BOOLEAN NOT NULL DEFAULT 1,
    live_release_id    TEXT,                           -- NULL = 首次部署还在进行的临时行
    preview            BOOLEAN NOT NULL DEFAULT 0,
    preview_checked_at INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX box_apps_port ON box_apps (box_id, port);

CREATE TABLE box_releases (
    id           TEXT    PRIMARY KEY,                  -- bxr_<ULID>
    app_id       TEXT    NOT NULL REFERENCES box_apps (id) ON DELETE CASCADE,
    repo         TEXT    NOT NULL,                     -- 生成时的绑定；回滚连同 root / source 一起恢复
    commit_sha   TEXT    NOT NULL,
    branch       TEXT    NOT NULL,
    root         TEXT    NOT NULL DEFAULT '',
    source       BOOLEAN NOT NULL,
    size_bytes   INTEGER NOT NULL,
    sha256       TEXT    NOT NULL,
    services     TEXT    NOT NULL,                     -- JSON 数组：{name, command, cwd}
    thread_id    TEXT,
    state        TEXT    NOT NULL CHECK (state IN ('live', 'kept', 'pruned')),
    created_at   INTEGER NOT NULL,
    activated_at INTEGER NOT NULL
);
CREATE INDEX box_releases_app ON box_releases (app_id, created_at DESC);

CREATE TABLE box_deploys (
    id          TEXT    PRIMARY KEY,                   -- bxd_<ULID>
    box_id      TEXT    NOT NULL REFERENCES boxes (id) ON DELETE CASCADE,
    app         TEXT    NOT NULL,
    release_id  TEXT    NOT NULL,                      -- prepare 时预留
    actor       TEXT    NOT NULL CHECK (actor IN ('owner', 'agent', 'assistant')),
    thread_id   TEXT,
    src_orb_id  TEXT    NOT NULL,
    repo        TEXT    NOT NULL,                      -- 意图：激活成功时才写到 app
    root        TEXT    NOT NULL DEFAULT '',
    source      BOOLEAN NOT NULL,
    set_public  INTEGER CHECK (set_public IN (0, 1)),  -- NULL = 不改 app 的公开性
    commit_sha  TEXT    NOT NULL,
    branch      TEXT    NOT NULL,
    first       BOOLEAN NOT NULL,                      -- app 行由本次部署创建
    size_bytes  INTEGER NOT NULL DEFAULT 0,            -- size_bytes / sha256 / services 在 commit 认领时填
    sha256      TEXT    NOT NULL DEFAULT '',
    services    TEXT    NOT NULL DEFAULT '[]',
    state       TEXT    NOT NULL CHECK (state IN ('prepared', 'transferring', 'activating', 'succeeded', 'failed', 'canceled')),
    error       TEXT,
    created_at  INTEGER NOT NULL,
    finished_at INTEGER
);
-- 每个 app 至多一个未结束的部署；prepare 的 INSERT 依赖它
CREATE UNIQUE INDEX box_deploys_open ON box_deploys (box_id, app) WHERE state IN ('prepared', 'transferring', 'activating');

-- 按 (box_id, app 名) 存：首次部署前就能设，下线 app 不删
CREATE TABLE box_secrets (
    box_id     TEXT    NOT NULL REFERENCES boxes (id) ON DELETE CASCADE,
    app        TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    value_enc  BLOB    NOT NULL,                       -- AAD "box/<box_id>/<app>/<name>"
    size       INTEGER NOT NULL,                       -- len(name) + len(明文值)，用于每 app 合计
    secret     BOOLEAN NOT NULL DEFAULT 1,             -- 0 = 明文环境变量，API 可读回值
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (box_id, app, name)
);

CREATE TABLE box_activity (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    box_id TEXT    NOT NULL REFERENCES boxes (id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    kind   TEXT    NOT NULL CHECK (kind IN ('created', 'started', 'stopped', 'renamed', 'schedule',
                                            'deployed', 'deploy_failed', 'rolled_back', 'restarted', 'undeployed', 'visibility')),
    actor  TEXT    NOT NULL CHECK (actor IN ('owner', 'agent', 'assistant', 'schedule', 'system')),
    app    TEXT,
    detail TEXT    NOT NULL DEFAULT '{}'               -- JSON：commit、releaseId、threadId、error、public、from、to
);
CREATE INDEX box_activity_box ON box_activity (box_id, id DESC);

-- +goose Down
DROP TABLE box_activity;
DROP TABLE box_secrets;
DROP TABLE box_deploys;
DROP TABLE box_releases;
DROP TABLE box_apps;
DROP TABLE boxes;
ALTER TABLE orbs DROP COLUMN box;
