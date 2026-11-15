-- +goose Up
-- M11a 审查修复：orb 建立时 provider 上报的基础镜像身份。配置哈希用这台 orb 实际所用镜像的身份，
-- 而不是阶段执行时 provider 的当前值（provider 换了基础镜像，旧 orb 拍出的快照不能盖上新身份）。
ALTER TABLE orbs ADD COLUMN base_image_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE orbs DROP COLUMN base_image_id;
