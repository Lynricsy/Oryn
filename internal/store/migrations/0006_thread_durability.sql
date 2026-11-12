-- +goose Up
-- 线程的持久化续作与转录版本（M2.6 审查修复 CP5 / CP11）：
--   startup_frames：已受理、尚未被 agent 确认的首批命令帧（JSON 数组，不带 id；首条 prompt 及其图片、set_session_name）；
--   fork_op：fork 的未完成阶段（JSON：stage = snapshot | create_orb | copy | fork）；控制面重启后据此续作；
--   transcript_rev：转录版本；会话文件被重写（entries.reset_done）或条目被整体移除时加一，客户端据此整线程重拉。
ALTER TABLE threads ADD COLUMN startup_frames BLOB;
ALTER TABLE threads ADD COLUMN fork_op BLOB;
ALTER TABLE threads ADD COLUMN transcript_rev INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE threads DROP COLUMN transcript_rev;
ALTER TABLE threads DROP COLUMN fork_op;
ALTER TABLE threads DROP COLUMN startup_frames;
