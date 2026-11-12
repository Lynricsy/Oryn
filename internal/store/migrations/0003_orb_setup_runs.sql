-- +goose Up
-- orb 的 setup / resume 运行记录（每次自动或手动运行一行；log_tail 为输出末尾至多 256KiB）。
CREATE TABLE orb_setup_runs (
    id                 TEXT PRIMARY KEY,
    orb_id             TEXT NOT NULL REFERENCES orbs(id),
    phase              TEXT NOT NULL CHECK (phase IN ('setup', 'resume')),
    trigger            TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
    started_at         INTEGER NOT NULL,
    finished_at        INTEGER NOT NULL,
    ok                 INTEGER NOT NULL,
    setup_ran          INTEGER NOT NULL,
    error              TEXT,
    duration_ms        INTEGER NOT NULL,
    has_repo_script    INTEGER NOT NULL,
    has_project_script INTEGER NOT NULL,
    log_tail           BLOB NOT NULL DEFAULT x''
);
CREATE INDEX orb_setup_runs_orb ON orb_setup_runs (orb_id, phase, finished_at DESC);

-- +goose Down
DROP TABLE orb_setup_runs;
