-- +goose Up
-- M25：桌面应用与窗口的镜像，desktop.state 里 `apps`（OrbDesktopApp[]）与 `windows`（OrbDesktopWindow[]）的 JSON。
-- NULL = orbd 未上报（旧 orbd，hello 不含 desktop-apps）。与 control 一样，orb 不在 ready 时视图不对外给出。
ALTER TABLE orb_desktops ADD COLUMN apps TEXT;
ALTER TABLE orb_desktops ADD COLUMN windows TEXT;

-- +goose Down
ALTER TABLE orb_desktops DROP COLUMN windows;
ALTER TABLE orb_desktops DROP COLUMN apps;
