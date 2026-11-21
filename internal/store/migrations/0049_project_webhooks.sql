-- +goose Up
-- M24 阶段 ③：托管仓库的推送后 Webhook，每项目至多一条。
-- secret_enc = secrets.Box.Seal("project-webhook:" || project_id, 密钥)；NULL = 不签名。
CREATE TABLE project_webhooks (
    project_id       TEXT    PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,
    url              TEXT    NOT NULL,
    secret_enc       BLOB,
    last_delivery_at INTEGER,
    last_status      INTEGER,   -- HTTP 状态码；0 = 没拿到响应（连接失败、超时、未发出）
    last_error       TEXT,      -- 非 2xx 或传输错误的简短原因（≤500 字符）；成功为 NULL
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

-- +goose Down
DROP TABLE project_webhooks;
