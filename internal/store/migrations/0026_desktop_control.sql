-- +goose Up
-- 桌面控制状态（idle / agent / user / ask，orbd 权威）的镜像：desktop.state 里 `control` 的 JSON（OrbDesktopControl）。
-- NULL = orbd 未上报（旧 orbd），按 idle 处理。
ALTER TABLE orb_desktops ADD COLUMN control TEXT;

-- +goose Down
ALTER TABLE orb_desktops DROP COLUMN control;
