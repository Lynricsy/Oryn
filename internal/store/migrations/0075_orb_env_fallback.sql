-- +goose Up
-- orb 上可能持有的环境变量降级模型密钥（AI 数据同意，App Store 5.1.2(i)）：以环境变量注入 orbd setup 环境、omp 拿去
-- 直连上游的 provider 键（JSON 字符串数组）。是「orbd 当前环境 ∪ 还没停掉的 agent 启动时的环境」的上界：新的 setup /
-- resume 只并入、停掉 orb 上全部 agent 之后才收窄为本次下发的那组。撤回同意时按这里的 provider 直接查同意，不从当前
-- models.yml 或密钥库反推（密钥被删、配置被改之后，已经下发的密钥仍在进程里）。
-- NULL = 不知道（本列出现之前已在跑的 orb，旧版本不看同意就注入）：按可能持有任何密钥处理，停掉 agent 后才变为已知。
-- 新建的 orb 还没有进程，缺省 '[]'。
ALTER TABLE orbs ADD COLUMN env_fallback TEXT DEFAULT '[]';
UPDATE orbs SET env_fallback = NULL;

-- +goose Down
ALTER TABLE orbs DROP COLUMN env_fallback;
