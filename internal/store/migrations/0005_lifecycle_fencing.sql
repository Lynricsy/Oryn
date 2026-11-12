-- +goose Up
-- orb 令牌代数：令牌 = HMAC(key, orbID ":" gen)（gen 0 沿用旧格式）；每次入队 spawn 时 gen+1 并更新 token_hash。
ALTER TABLE orbs ADD COLUMN token_gen INTEGER NOT NULL DEFAULT 0;
-- 任务认领代数：每次认领 +1，随认领应答下发并由完成上报回传；完成 / 重排 / 租约巡检都按它比较并交换。
ALTER TABLE jobs ADD COLUMN claim_gen INTEGER NOT NULL DEFAULT 0;
CREATE INDEX orb_setup_runs_orb_finished ON orb_setup_runs (orb_id, finished_at DESC, id DESC);

-- +goose Down
DROP INDEX orb_setup_runs_orb_finished;
ALTER TABLE jobs DROP COLUMN claim_gen;
ALTER TABLE orbs DROP COLUMN token_gen;
