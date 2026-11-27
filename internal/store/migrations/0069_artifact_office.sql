-- +goose Up
-- M31 Office Artifacts：artifacts.type 增加 Office 类型（docx docm xlsx xlsm pptx pptm pdf），artifacts.kind 增加 sheet。
-- 两列的 CHECK 都是列约束，SQLite 改不了 CHECK，这里不重建整表：先拷到临时列，删掉原列再按新枚举加回并拷回。
-- 两列因此移到表尾（sqlc 生成物的字段顺序随之变化，按名访问不受影响）。
ALTER TABLE artifacts ADD COLUMN type_tmp TEXT;
ALTER TABLE artifacts ADD COLUMN kind_tmp TEXT;
UPDATE artifacts SET type_tmp = type, kind_tmp = kind;
ALTER TABLE artifacts DROP COLUMN type;
ALTER TABLE artifacts DROP COLUMN kind;
ALTER TABLE artifacts ADD COLUMN type TEXT CHECK (type IN ('document', 'slides', 'docx', 'docm', 'xlsx', 'xlsm', 'pptx', 'pptm', 'pdf'));
ALTER TABLE artifacts ADD COLUMN kind TEXT NOT NULL DEFAULT 'web' CHECK (kind IN ('web', 'canvas', 'document', 'slides', 'sheet'));
UPDATE artifacts SET type = type_tmp, kind = kind_tmp;
ALTER TABLE artifacts DROP COLUMN type_tmp;
ALTER TABLE artifacts DROP COLUMN kind_tmp;

-- +goose Down
DELETE FROM artifacts WHERE type NOT IN ('document', 'slides') AND type IS NOT NULL;
ALTER TABLE artifacts ADD COLUMN type_tmp TEXT;
ALTER TABLE artifacts ADD COLUMN kind_tmp TEXT;
UPDATE artifacts SET type_tmp = type, kind_tmp = kind;
ALTER TABLE artifacts DROP COLUMN type;
ALTER TABLE artifacts DROP COLUMN kind;
ALTER TABLE artifacts ADD COLUMN type TEXT CHECK (type IN ('document', 'slides'));
ALTER TABLE artifacts ADD COLUMN kind TEXT NOT NULL DEFAULT 'web' CHECK (kind IN ('web', 'canvas', 'document', 'slides'));
UPDATE artifacts SET type = type_tmp, kind = kind_tmp;
ALTER TABLE artifacts DROP COLUMN type_tmp;
ALTER TABLE artifacts DROP COLUMN kind_tmp;
