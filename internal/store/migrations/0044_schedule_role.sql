-- +goose Up
-- 「每次新建线程」的定时可指定新线程开场的 omp 模型 role（与 model 互斥；NULL = 未指定）。
ALTER TABLE schedules ADD COLUMN role TEXT;

-- +goose Down
ALTER TABLE schedules DROP COLUMN role;
