-- +goose Up
-- M19：全局助手的三层执行与主线对话。

-- 全局助手的设置（单行）：联网总开关、关掉的 MCP 服务器、助手的电脑、主线备忘。
CREATE TABLE assistant_settings (
    id                     INTEGER PRIMARY KEY CHECK (id = 1),
    web_access             INTEGER NOT NULL DEFAULT 1 CHECK (web_access IN (0,1)),
    disabled_mcp           TEXT    NOT NULL DEFAULT '[]',   -- JSON 数组：助手不用的全局 mcp.json 服务器名
    computer_provider      TEXT,                             -- 助手的电脑下一次创建时用的 provider 名；NULL = 默认解析
    computer_orb_type      TEXT,
    computer_tools         TEXT,                             -- JSON：电脑里 omp 的工具定义缓存（OmpFrame[]）
    computer_tools_version TEXT,                             -- 生成缓存时电脑里 omp 的版本
    main_notes             TEXT    NOT NULL DEFAULT '',      -- 主线备忘（上限 1500 字，由服务层校验）
    main_notes_updated_at  INTEGER,
    updated_at             INTEGER NOT NULL
);

-- 助手发起、等主人确认的动作（删除、对外公开）。
CREATE TABLE assistant_confirmations (
    id         TEXT    PRIMARY KEY,                          -- cfm_<ULID>，同时是确认卡帧的 id
    thread_id  TEXT    NOT NULL REFERENCES threads ON DELETE CASCADE,
    action     TEXT    NOT NULL,                             -- 工具名（delete_thread、share_thread …）
    target     TEXT    NOT NULL,
    summary    TEXT    NOT NULL,
    params     TEXT    NOT NULL,                             -- JSON：执行所需参数
    state      TEXT    NOT NULL CHECK (state IN ('pending','approved','rejected','expired','failed')),
    error      TEXT,
    created_at INTEGER NOT NULL,
    decided_at INTEGER
);
CREATE INDEX assistant_confirmations_state ON assistant_confirmations (state, created_at);

-- 交给助手的运行结果，等待合并投递进主线。
CREATE TABLE assistant_inbox (
    id           TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL CHECK (kind IN ('schedule','trigger')),
    source_id    TEXT    NOT NULL,
    source_name  TEXT    NOT NULL,
    thread_id    TEXT    NOT NULL,                           -- 不设外键：线程删除后结果仍可投递
    outcome      TEXT    NOT NULL,
    body         TEXT    NOT NULL DEFAULT '',
    error        TEXT,
    created_at   INTEGER NOT NULL,
    delivered_at INTEGER
);
CREATE INDEX assistant_inbox_pending ON assistant_inbox (created_at) WHERE delivered_at IS NULL;

-- 主线：全库至多一条 main = 1 的助手对话。
ALTER TABLE threads ADD COLUMN main INTEGER NOT NULL DEFAULT 0 CHECK (main IN (0,1));
CREATE UNIQUE INDEX threads_main ON threads (main) WHERE main = 1;

-- 助手的电脑：全库至多一台 computer = 1 的 orb（由服务层单飞保证）。
ALTER TABLE orbs ADD COLUMN computer INTEGER NOT NULL DEFAULT 0 CHECK (computer IN (0,1));

-- 运行结果交给助手的开关。
ALTER TABLE schedules ADD COLUMN hand_to_assistant INTEGER NOT NULL DEFAULT 0;
ALTER TABLE github_triggers ADD COLUMN hand_to_assistant INTEGER NOT NULL DEFAULT 0;

-- 主线可以有多条线程内定时：线程上的唯一索引改为普通索引（普通线程的一条上限由服务层保证）。
DROP INDEX schedules_thread;
CREATE INDEX schedules_thread ON schedules (thread_id) WHERE thread_id IS NOT NULL;

-- +goose Down
-- 恢复唯一索引前，每个线程只保留最早的一条线程内定时。
DELETE FROM schedules
WHERE thread_id IS NOT NULL
  AND id NOT IN (
    SELECT s.id FROM schedules s
    WHERE s.thread_id IS NOT NULL
      AND s.id = (SELECT s2.id FROM schedules s2 WHERE s2.thread_id = s.thread_id ORDER BY s2.created_at, s2.id LIMIT 1)
  );
DROP INDEX schedules_thread;
CREATE UNIQUE INDEX schedules_thread ON schedules (thread_id) WHERE thread_id IS NOT NULL;
ALTER TABLE github_triggers DROP COLUMN hand_to_assistant;
ALTER TABLE schedules DROP COLUMN hand_to_assistant;
ALTER TABLE orbs DROP COLUMN computer;
DROP INDEX threads_main;
ALTER TABLE threads DROP COLUMN main;
DROP INDEX assistant_inbox_pending;
DROP TABLE assistant_inbox;
DROP INDEX assistant_confirmations_state;
DROP TABLE assistant_confirmations;
DROP TABLE assistant_settings;
