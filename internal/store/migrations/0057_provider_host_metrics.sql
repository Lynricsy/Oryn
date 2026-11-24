-- +goose Up
-- Provider 最近一次心跳上报的宿主机资源快照（JSON；空串 = 尚未上报）。
ALTER TABLE providers ADD COLUMN host_metrics TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE providers DROP COLUMN host_metrics;
