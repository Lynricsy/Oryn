-- +goose Up
-- M14：artifact 页面运行时的数据与能力授权。
--   artifact_data：`oryn.use("db")` 的文档存储。数据不属于任何版本；回收站里的 artifact 数据保留但不可读写。
--     version 取自该 artifact 的递增序号 artifact_data_totals.seq（删了重建不会复用旧版本号）。
--   artifact_data_totals：每个 artifact 的数据总字节与序号。
--   artifact_consents：用户对 `files` / `sample` 能力的授权（按 artifact × 能力持久化）；拒绝只记在内存里。
CREATE TABLE artifact_data (
    artifact_id TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    collection  TEXT    NOT NULL,
    doc_id      TEXT    NOT NULL,
    json        TEXT    NOT NULL,
    size        INTEGER NOT NULL,
    version     INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, collection, doc_id)
) WITHOUT ROWID;

CREATE TABLE artifact_data_totals (
    artifact_id TEXT    PRIMARY KEY REFERENCES artifacts (id) ON DELETE CASCADE,
    bytes       INTEGER NOT NULL,
    seq         INTEGER NOT NULL
);

CREATE TABLE artifact_consents (
    artifact_id TEXT    NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    capability  TEXT    NOT NULL CHECK (capability IN ('files', 'sample')),
    granted_at  INTEGER NOT NULL,
    PRIMARY KEY (artifact_id, capability)
);

-- +goose Down
DROP TABLE artifact_consents;
DROP TABLE artifact_data_totals;
DROP TABLE artifact_data;
