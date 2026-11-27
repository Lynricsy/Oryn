-- +goose Up
-- 控制面运维参数的覆盖层：设置页里改过的值压在 orynd.yaml 之上。键是 YAML 点路径，白名单见 internal/opsconf。
CREATE TABLE config_overrides (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,   -- JSON 标量：时长为毫秒整数，容量为 GB 整数，布尔为 true / false
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE config_overrides;
