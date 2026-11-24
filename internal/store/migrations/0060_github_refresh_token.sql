-- +goose Up
-- GitHub OAuth App 短命令牌支持：2026-08-14 起新建 OAuth App 默认启用 8 小时过期访问令牌与 6 个月刷新令牌。
-- 补回过期时间与加密后的刷新令牌，供控制面到期前及 401 时自动轮换；未启用过期的旧 App 三列保持 NULL。
ALTER TABLE github_identities ADD COLUMN access_expires_at  INTEGER; -- Unix 毫秒；NULL = 不过期
ALTER TABLE github_identities ADD COLUMN refresh_token_enc  BLOB;    -- AAD "github/identity/<slot>/refresh"
ALTER TABLE github_identities ADD COLUMN refresh_expires_at INTEGER; -- Unix 毫秒；NULL = 无刷新令牌或无过期时间

-- +goose Down
ALTER TABLE github_identities DROP COLUMN refresh_expires_at;
ALTER TABLE github_identities DROP COLUMN refresh_token_enc;
ALTER TABLE github_identities DROP COLUMN access_expires_at;
