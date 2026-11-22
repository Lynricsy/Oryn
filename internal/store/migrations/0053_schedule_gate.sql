-- +goose Up
-- 定时的运行前闸门：每次运行前控制面经 SSH 网关在 gate_host 上执行 gate_command，输出 {"wakeAgent": false} 时跳过本次运行。
-- gate_host 为 NULL = 不设闸门；gate_command / gate_timeout 只在 gate_host 非空时有意义。
ALTER TABLE schedules ADD COLUMN gate_host TEXT;
ALTER TABLE schedules ADD COLUMN gate_command TEXT;
ALTER TABLE schedules ADD COLUMN gate_timeout INTEGER;

-- +goose Down
ALTER TABLE schedules DROP COLUMN gate_timeout;
ALTER TABLE schedules DROP COLUMN gate_command;
ALTER TABLE schedules DROP COLUMN gate_host;
