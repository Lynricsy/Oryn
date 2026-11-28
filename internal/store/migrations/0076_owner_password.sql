-- +goose Up
-- 不用 Passkey 的登录方式：密码 + 强制 TOTP。三列同时为空或同时有值，由代码保证。
ALTER TABLE owner ADD COLUMN password_hash   TEXT;                         -- Argon2id PHC 串；NULL = 未启用密码登录
ALTER TABLE owner ADD COLUMN totp_secret_enc BLOB;                         -- TOTP 密钥，secrets.Box 加密（AAD "auth/owner/totp"）
ALTER TABLE owner ADD COLUMN totp_last_step  INTEGER NOT NULL DEFAULT 0;   -- 最近一次被接受的 TOTP 时间步，防止同一验证码重放
ALTER TABLE web_sessions ADD COLUMN via_password INTEGER NOT NULL DEFAULT 0; -- 1 = 由密码 + TOTP 登录签发；改密码或关闭密码登录时吊销

-- +goose Down
ALTER TABLE web_sessions DROP COLUMN via_password;
ALTER TABLE owner DROP COLUMN totp_last_step;
ALTER TABLE owner DROP COLUMN totp_secret_enc;
ALTER TABLE owner DROP COLUMN password_hash;
