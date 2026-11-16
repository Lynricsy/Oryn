-- +goose Up
-- M15：仓库级 REST 轮询改由 issue 列表（含 PR）发现新 PR 与 PR 状态变化，不再读 pulls 列表，删除它的 ETag 列。
ALTER TABLE github_repo_watches DROP COLUMN etag_pulls;

-- +goose Down
-- 只恢复列；值是缓存，空着下一轮重新取。
ALTER TABLE github_repo_watches ADD COLUMN etag_pulls TEXT;
