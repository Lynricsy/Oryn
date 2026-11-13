-- +goose Up
-- M5 修复（F5）：快照分享在创建时固化公开范围——活动分支上的条目集合（按分支顺序）与每行原始 payload 的
-- SHA-256，以及当时的标题。之后被改写或删除的行不再公开，新内容永远进不了快照。

ALTER TABLE thread_shares ADD COLUMN title TEXT;                -- snapshot：创建时的线程标题；live：NULL

CREATE TABLE thread_share_entries (
    share_id       TEXT    NOT NULL REFERENCES thread_shares (id) ON DELETE CASCADE,
    pos            INTEGER NOT NULL,                            -- 活动分支上的顺序（0 起）
    seq            INTEGER NOT NULL,                            -- entries.seq
    payload_sha256 BLOB    NOT NULL,                            -- 创建时该行原始 payload 的 SHA-256（32 字节）
    PRIMARY KEY (share_id, pos)
);

-- 迁移前创建的快照没有固化的条目集合，无法保证内容不变：一律撤销（所有者可重新分享）。
UPDATE thread_shares SET revoked_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000
WHERE mode = 'snapshot' AND revoked_at IS NULL;

-- +goose Down
DROP TABLE thread_share_entries;
ALTER TABLE thread_shares DROP COLUMN title;
