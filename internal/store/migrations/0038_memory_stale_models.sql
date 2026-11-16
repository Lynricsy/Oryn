-- +goose Up
-- M16：遗忘后等待 mental model 全量刷新的模型。Hindsight 删除 document 后只把受影响的 mental model 标 is_stale，
-- 旧摘要照常返回；在这里的模型，网关对 orb 隐藏其内容并在 reflect 里排除，直到 orynd 在删除之后提交的那次
-- 全量刷新（refresh_op）完成，或模型已被删除。删除之前已开始的刷新可能读到被删的事实，所以不按刷新时间判断。
CREATE TABLE memory_stale_models (
    model_id        TEXT    PRIMARY KEY,
    project_id      TEXT,                      -- NULL = 全局模型（user-preferences 等）或无法确定项目
    refresh_op      TEXT,                      -- 删除之后提交的全量刷新的 Hindsight operation id；NULL = 尚未提交成功
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    last_error      TEXT,
    created_at      INTEGER NOT NULL
);

-- +goose Down
DROP TABLE memory_stale_models;
