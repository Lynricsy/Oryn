-- +goose Up
-- M24 阶段 ②：项目仓库的控制面裸仓库与提交↔线程关联。kind=hosted 留给阶段 ③（本阶段只写 mirror）。
CREATE TABLE project_repos (
    project_id     TEXT    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL CHECK (kind IN ('mirror', 'hosted')),
    remote_url     TEXT,              -- mirror：最近一次拉取时项目的 repo_url 原文
    github_repo_id INTEGER,           -- 同上时刻项目绑定的 GitHub 仓库 id（改名跟随判定）
    default_branch TEXT,
    synced_at      INTEGER,           -- 最近一次成功拉取；NULL = 从未成功
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    error_code     TEXT,
    error_message  TEXT,              -- 已脱敏，≤500 字符
    error_at       INTEGER,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
);

-- 出站代理观察到的 orb 推送（上游 200 应答），镜像拉取后展开成 repo_commit_threads。
CREATE TABLE repo_pushes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    thread_id   TEXT    NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    orb_id      TEXT    NOT NULL,
    ref         TEXT    NOT NULL,     -- refs/heads/<分支>
    old_sha     TEXT    NOT NULL,
    new_sha     TEXT    NOT NULL,
    pushed_at   INTEGER NOT NULL,
    expanded_at INTEGER,              -- NULL = 待展开
    expand_note TEXT                  -- NULL | truncated | missing
);
CREATE INDEX repo_pushes_pending ON repo_pushes (project_id, expanded_at);

CREATE TABLE repo_commit_threads (
    project_id TEXT    NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    sha        TEXT    NOT NULL,
    thread_id  TEXT    REFERENCES threads (id) ON DELETE CASCADE,  -- NULL = GitHub 反查过、没有线程
    source     TEXT    NOT NULL CHECK (source IN ('push', 'pull', 'none')),
    linked_at  INTEGER NOT NULL,
    PRIMARY KEY (project_id, sha)
);

-- +goose Down
DROP TABLE repo_commit_threads;
DROP INDEX repo_pushes_pending;
DROP TABLE repo_pushes;
DROP TABLE project_repos;
