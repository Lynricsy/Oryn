-- +goose Up
-- M14：Artifacts（agent 产出的版本化交付物）。内容按 sha256 寻址存在 <data_dir>/artifacts/content/<前 2 位>/<sha256>，
-- 这里只存行。时间一律为毫秒整数。
--   artifacts：一个 artifact。head 是最新版本号；size / pages 是最新版本的值（列表用，不必再查版本表）。
--     project_id / thread_id：所属项目与创建线程；线程是硬删除，删除后置 NULL，artifact 留着。
--     origin：agent（agent 工具创建）| import（用户导入）。
--     type：类型化 artifact 的类型（document | slides），plain 为 NULL；一经确定不能变。
--     kind：web | canvas | document | slides（由 type 与文件决定，列表筛选用）。
--     opened_at：用户最近一次打开（总览页「最近」排序，存服务端换设备一致）。
--     deleted_at：在「最近删除」里时为删除时间，保留 30 天后由服务清理。
CREATE TABLE artifacts (
    id         TEXT    PRIMARY KEY,                                         -- art_<ULID>
    project_id TEXT    REFERENCES projects (id) ON DELETE SET NULL,
    thread_id  TEXT    REFERENCES threads (id) ON DELETE SET NULL,
    origin     TEXT    NOT NULL CHECK (origin IN ('agent', 'import')),
    type       TEXT    CHECK (type IN ('document', 'slides')),
    kind       TEXT    NOT NULL CHECK (kind IN ('web', 'canvas', 'document', 'slides')),
    title      TEXT    NOT NULL,
    head       INTEGER NOT NULL,
    size       INTEGER NOT NULL,
    pages      INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    opened_at  INTEGER,
    deleted_at INTEGER
);
CREATE INDEX artifacts_project ON artifacts (project_id, updated_at);
CREATE INDEX artifacts_thread ON artifacts (thread_id);

-- 版本：每次写入一个新版本（n 从 1 递增）。
--   author_thread_id：写入的线程，不设外键（线程删除后仍要显示作者）；NULL = 用户。
--   author_label：写入时的线程标题快照。
--   capabilities：页面运行时能力声明的 JSON 数组（["db","files","sample"] 的子集）。
--   format：类型化内容的格式版本；plain 为 NULL。
--   files_key：文件清单（路径 + sha256，类型化时连同 type 与 format）的摘要，缩略图等按它缓存。
CREATE TABLE artifact_versions (
    artifact_id      TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    n                INTEGER NOT NULL,
    size             INTEGER NOT NULL,
    pages            INTEGER NOT NULL,
    author_thread_id TEXT,
    author_label     TEXT    NOT NULL DEFAULT '',
    note             TEXT,
    capabilities     TEXT    NOT NULL DEFAULT '[]',
    format           INTEGER,
    files_key        TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, n)
) WITHOUT ROWID;

-- 每个版本的完整文件清单（不是增量）；内容文件只要还有一行引用就不删。
CREATE TABLE artifact_files (
    artifact_id TEXT    NOT NULL,
    n           INTEGER NOT NULL,
    path        TEXT    NOT NULL,
    sha256      TEXT    NOT NULL,
    size        INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, n, path),
    FOREIGN KEY (artifact_id, n) REFERENCES artifact_versions (artifact_id, n) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX artifact_files_sha ON artifact_files (sha256);

-- +goose Down
DROP TABLE artifact_files;
DROP TABLE artifact_versions;
DROP TABLE artifacts;
