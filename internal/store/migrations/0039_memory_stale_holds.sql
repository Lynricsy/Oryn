-- +goose Up
-- M16：遗忘后待刷新队列的并发保护。
-- token 是每次标记新生成、从不复用的随机令牌（16 字节十六进制）。重试循环与立即刷新的每次写回、清除都带
-- token 相等条件：令牌变了说明期间又有一次遗忘（或记录清除后重新插入），这次的结果作废，旧刷新不能解除新遗忘
-- 的隐藏。只比较相等，不比较大小；已有记录为空串，永远不等于新令牌。
-- memory_stale_holds 记录进行中的删除：每次遗忘对每个受影响的模型持有一条 hold，删除结束（成功或失败）后按 hold_id
-- 精确释放。模型还有任何 hold 时不许提交刷新（刷新会读到尚未删除的数据），也不许写回或清除；多个遗忘的删除重叠、
-- 乱序结束时，只有最后一个释放后才提交刷新。进程退出时进行中的删除随之结束，启动时清空全部 hold。
ALTER TABLE memory_stale_models ADD COLUMN token TEXT NOT NULL DEFAULT '';

CREATE TABLE memory_stale_holds (
    hold_id    TEXT    PRIMARY KEY,
    model_id   TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX memory_stale_holds_model ON memory_stale_holds (model_id);

-- +goose Down
DROP TABLE memory_stale_holds;
ALTER TABLE memory_stale_models DROP COLUMN token;
