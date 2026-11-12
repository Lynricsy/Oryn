-- 0002_pairing_meta：配对请求记录发起方 IP 与 User-Agent，供网页批准前核对（防钓鱼）。

-- +goose Up
ALTER TABLE device_pairings ADD COLUMN requester_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE device_pairings ADD COLUMN user_agent   TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE device_pairings DROP COLUMN user_agent;
ALTER TABLE device_pairings DROP COLUMN requester_ip;
