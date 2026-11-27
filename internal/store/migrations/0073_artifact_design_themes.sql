-- +goose Up
-- M34：画布的方案主题。themes 是主题名到方向牌（完整 Direction 对象）的 JSON 对象，键按写入先后排列（最早在前，
-- 重写同名主题移到末尾），最多 12 项，超出时淘汰最早写入的；'{}' 表示没有方案主题。
ALTER TABLE artifact_designs ADD COLUMN themes TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE artifact_designs DROP COLUMN themes;
