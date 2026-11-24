-- +goose Up
-- M29：设计画布。版本记下所属回合（turn_id，由 orb 扩展生成）、回合标题（用户输入的首行）与设计检查结果；
-- artifact_designs 是设计画布的每 artifact 状态（发牌、选定方向、便签自动投递开关与已投递项、标脏时间）。
ALTER TABLE artifact_versions ADD COLUMN turn_id TEXT;
ALTER TABLE artifact_versions ADD COLUMN title TEXT;
ALTER TABLE artifact_versions ADD COLUMN check_json TEXT;   -- {status, issues, fingerprint}
CREATE INDEX artifact_versions_turn ON artifact_versions (artifact_id, turn_id);
CREATE TABLE artifact_designs (
  artifact_id TEXT PRIMARY KEY REFERENCES artifacts (id) ON DELETE CASCADE,
  surface TEXT, direction TEXT, dealt TEXT NOT NULL DEFAULT '[]',
  auto_notes INTEGER NOT NULL DEFAULT 1, delivered TEXT NOT NULL DEFAULT '[]', dirty_at INTEGER
);

-- +goose Down
DROP TABLE artifact_designs;
DROP INDEX artifact_versions_turn;
ALTER TABLE artifact_versions DROP COLUMN check_json;
ALTER TABLE artifact_versions DROP COLUMN title;
ALTER TABLE artifact_versions DROP COLUMN turn_id;
