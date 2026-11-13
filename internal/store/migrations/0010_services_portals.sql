-- +goose Up
-- M4：受管服务镜像、portal 限时公开与 portal 主机上的所有者会话。

-- portal 主机名里的线程短码（线程 id 末 12 位十六进制）。
ALTER TABLE threads ADD COLUMN short_code TEXT;
UPDATE threads SET short_code = substr(id, -12) WHERE id GLOB 'T-????????-????-????-????-????????????';
CREATE UNIQUE INDEX threads_short_code ON threads (short_code);

-- orbd 上报的服务快照（镜像；真相在 orbd）。
CREATE TABLE orb_services (
    orb_id     TEXT    NOT NULL REFERENCES orbs(id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    port       INTEGER,
    has_portal INTEGER NOT NULL DEFAULT 0,
    doc        TEXT    NOT NULL,          -- apigen.OrbService 的 JSON
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (orb_id, name)
);
CREATE INDEX orb_services_port ON orb_services (orb_id, port);

-- 限时公开；一行 = 一个 (orb, 端口)。
CREATE TABLE portal_shares (
    orb_id     TEXT    NOT NULL REFERENCES orbs(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (orb_id, port)
);

-- portal 主机上的所有者会话（Cookie __Host-oryn_portal），随网页会话级联删除。
CREATE TABLE portal_sessions (
    id_hash          TEXT    PRIMARY KEY,  -- SHA-256(Cookie 值 ops_…)
    web_session_hash TEXT    NOT NULL REFERENCES web_sessions(id_hash) ON DELETE CASCADE,
    label            TEXT    NOT NULL,     -- t-<code>-p<port>
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL
);
CREATE INDEX portal_sessions_web ON portal_sessions (web_session_hash);

-- +goose Down
DROP INDEX portal_sessions_web;
DROP TABLE portal_sessions;
DROP TABLE portal_shares;
DROP INDEX orb_services_port;
DROP TABLE orb_services;
DROP INDEX threads_short_code;
ALTER TABLE threads DROP COLUMN short_code;
