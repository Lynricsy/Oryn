-- +goose Up
-- M15：github_prepare 的通过记录绑定登记时项目门禁配置的规范化哈希（orbs.GitHubGatesHash）；出站门禁只认与项目
-- 当前门禁哈希相同的记录，门禁改动之后旧记录不再放行。已有记录为空串，永远不等于任何哈希，视为已过期。
ALTER TABLE github_prepare ADD COLUMN gates_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE github_prepare DROP COLUMN gates_hash;
