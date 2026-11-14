-- +goose Up
-- M9 fixes: thread_reports.gen tags a report registration (also the delivery tag, rep_<ULID>);
-- schedules.rev is the definition version used for optimistic concurrency (ifRev).
ALTER TABLE thread_reports ADD COLUMN gen TEXT NOT NULL DEFAULT '';
ALTER TABLE schedules ADD COLUMN rev INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE schedules DROP COLUMN rev;
ALTER TABLE thread_reports DROP COLUMN gen;
