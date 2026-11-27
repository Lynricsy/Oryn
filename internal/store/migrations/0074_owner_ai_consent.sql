-- +goose Up
-- 主人对「把个人数据发给第三方 AI 服务」的同意记录（App Store 审核指南 5.1.2(i)）。
-- ai_consent_at：最近一次同意的时间（Unix 毫秒）；NULL = 未同意或已撤回。
-- ai_consent_recipients：同意时列出的接收方 id（JSON 字符串数组，如 ["model:anthropic","web:public"]）。
ALTER TABLE owner ADD COLUMN ai_consent_at INTEGER;
ALTER TABLE owner ADD COLUMN ai_consent_recipients TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE owner DROP COLUMN ai_consent_recipients;
ALTER TABLE owner DROP COLUMN ai_consent_at;
