-- +goose Up
-- M11a：项目快照、预热池与快照相关的任务结果。
-- 项目快照：镜像在 provider 宿主上，名字 = id；键 = (项目, provider, 类型)，同键至多一行 building、一行 ready。
CREATE TABLE project_snapshots (
    id            TEXT    PRIMARY KEY,                                      -- oryn-snap-<ulid>
    project_id    TEXT    REFERENCES projects (id) ON DELETE SET NULL,      -- 项目删除后留给回收器
    provider_id   TEXT    NOT NULL REFERENCES providers (id),
    type          TEXT    NOT NULL,
    state         TEXT    NOT NULL CHECK (state IN ('building', 'ready', 'retired')),
    config_hash   TEXT    NOT NULL,
    base_commit   TEXT    NOT NULL DEFAULT '',
    depth         INTEGER NOT NULL DEFAULT 0,                               -- 血统内第几次刷新（0 = 从基础镜像全新 setup）
    lineage_at    INTEGER NOT NULL,                                         -- 血统起点；刷新不重置
    source_orb_id TEXT,
    size_bytes    INTEGER,
    use_count     INTEGER NOT NULL DEFAULT 0,
    last_used_at  INTEGER,
    created_at    INTEGER NOT NULL,
    ready_at      INTEGER,
    retired_at    INTEGER,
    retire_reason TEXT,
    gc_job_id     TEXT,
    gc_attempt_at INTEGER
);
CREATE UNIQUE INDEX project_snapshots_building ON project_snapshots (project_id, provider_id, type) WHERE state = 'building';
CREATE UNIQUE INDEX project_snapshots_ready    ON project_snapshots (project_id, provider_id, type) WHERE state = 'ready';
CREATE INDEX project_snapshots_state ON project_snapshots (state);

ALTER TABLE projects  ADD COLUMN snapshots BOOLEAN NOT NULL DEFAULT 1;
ALTER TABLE projects  ADD COLUMN warm_orbs INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orbs      ADD COLUMN pool_state  TEXT;      -- NULL | warming | idle | claimed | retiring
ALTER TABLE orbs      ADD COLUMN config_hash TEXT;      -- 最近一次生成 setup 参数时的项目配置哈希
ALTER TABLE orbs      ADD COLUMN claimed_at  INTEGER;
CREATE INDEX orbs_pool ON orbs (pool_state) WHERE pool_state IS NOT NULL;
ALTER TABLE providers ADD COLUMN base_image_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs      ADD COLUMN result TEXT;           -- JobResult 的 JSON
ALTER TABLE orb_setup_runs ADD COLUMN prepared BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE orb_setup_runs ADD COLUMN base_commit TEXT;

-- +goose Down
ALTER TABLE orb_setup_runs DROP COLUMN base_commit;
ALTER TABLE orb_setup_runs DROP COLUMN prepared;
ALTER TABLE jobs      DROP COLUMN result;
ALTER TABLE providers DROP COLUMN base_image_id;
DROP INDEX orbs_pool;
ALTER TABLE orbs      DROP COLUMN claimed_at;
ALTER TABLE orbs      DROP COLUMN config_hash;
ALTER TABLE orbs      DROP COLUMN pool_state;
ALTER TABLE projects  DROP COLUMN warm_orbs;
ALTER TABLE projects  DROP COLUMN snapshots;
DROP INDEX project_snapshots_state;
DROP INDEX project_snapshots_ready;
DROP INDEX project_snapshots_building;
DROP TABLE project_snapshots;
