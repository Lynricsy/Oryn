-- +goose Up
-- 定时的运行前闸门：每次运行前控制面执行 gate_command，输出 {"wakeAgent": false} 时跳过本次运行。
-- gate_command 为 NULL = 不设闸门；gate_host 为 NULL = 在控制面本机沙箱里执行，否则经 SSH 网关在该主机上执行。
ALTER TABLE schedules ADD COLUMN gate_host TEXT;
ALTER TABLE schedules ADD COLUMN gate_command TEXT;
ALTER TABLE schedules ADD COLUMN gate_timeout INTEGER;

-- +goose Down
ALTER TABLE schedules DROP COLUMN gate_timeout;
ALTER TABLE schedules DROP COLUMN gate_command;
ALTER TABLE schedules DROP COLUMN gate_host;
