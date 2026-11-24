-- M19: global assistant settings, confirmations, the main conversation inbox and the assistant's computer.

-- ---------- settings ----------

-- name: GetAssistantSettings :one
SELECT * FROM assistant_settings WHERE id = 1;

-- name: UpsertAssistantSettings :exec
INSERT INTO assistant_settings (id, web_access, disabled_mcp, computer_provider, computer_orb_type, model, thinking_level, updated_at)
VALUES (1, @web_access, @disabled_mcp, sqlc.narg(computer_provider), sqlc.narg(computer_orb_type), sqlc.narg(model), sqlc.narg(thinking_level), @updated_at)
ON CONFLICT (id) DO UPDATE SET
    web_access = excluded.web_access, disabled_mcp = excluded.disabled_mcp,
    computer_provider = excluded.computer_provider, computer_orb_type = excluded.computer_orb_type,
    model = excluded.model, thinking_level = excluded.thinking_level,
    updated_at = excluded.updated_at;
-- name: SetAssistantMainNotes :exec
INSERT INTO assistant_settings (id, main_notes, main_notes_updated_at, updated_at)
VALUES (1, @main_notes, @main_notes_updated_at, @main_notes_updated_at)
ON CONFLICT (id) DO UPDATE SET
    main_notes = excluded.main_notes, main_notes_updated_at = excluded.main_notes_updated_at;

-- name: SetAssistantComputerTools :exec
INSERT INTO assistant_settings (id, computer_tools, computer_tools_version, updated_at)
VALUES (1, sqlc.narg(computer_tools), sqlc.narg(computer_tools_version), @updated_at)
ON CONFLICT (id) DO UPDATE SET
    computer_tools = excluded.computer_tools, computer_tools_version = excluded.computer_tools_version;

-- ---------- confirmations ----------

-- name: InsertAssistantConfirmation :exec
INSERT INTO assistant_confirmations (id, thread_id, action, target, summary, params, state, created_at)
VALUES (@id, @thread_id, @action, @target, @summary, @params, 'pending', @created_at);

-- name: GetAssistantConfirmation :one
SELECT * FROM assistant_confirmations WHERE id = @id;

-- name: CountPendingAssistantConfirmations :one
SELECT COUNT(*) FROM assistant_confirmations WHERE thread_id = @thread_id AND state = 'pending';

-- name: ListPendingAssistantConfirmations :many
SELECT * FROM assistant_confirmations WHERE state = 'pending' ORDER BY created_at, id;

-- name: ListExpiredAssistantConfirmations :many
SELECT * FROM assistant_confirmations WHERE state = 'pending' AND created_at < @before ORDER BY created_at, id;

-- Compare-and-set from pending; 0 rows = already decided elsewhere.
-- name: DecideAssistantConfirmation :execrows
UPDATE assistant_confirmations SET state = @state, error = sqlc.narg(error), decided_at = @decided_at
WHERE id = @id AND state = 'pending';

-- Records the outcome after an approved action ran (approved -> failed keeps decided_at).
-- name: SetAssistantConfirmationResult :exec
UPDATE assistant_confirmations SET state = @state, error = sqlc.narg(error) WHERE id = @id;

-- ---------- inbox ----------

-- restricted = 1: the finding came from outside content and only goes to the restricted main agent (M26).
-- name: InsertAssistantInbox :exec
INSERT INTO assistant_inbox (id, kind, source_id, source_name, thread_id, outcome, body, error, created_at, restricted)
VALUES (@id, @kind, @source_id, @source_name, @thread_id, @outcome, @body, sqlc.narg(error), @created_at, @restricted);

-- name: ListPendingAssistantInbox :many
SELECT * FROM assistant_inbox WHERE delivered_at IS NULL ORDER BY created_at, id LIMIT @lim;

-- name: MarkAssistantInboxDelivered :exec
UPDATE assistant_inbox SET delivered_at = @delivered_at WHERE id = @id;

-- Puts a row back in the queue (restricted delivery withdrawn before it reached the agent).
-- name: UnmarkAssistantInboxDelivered :exec
UPDATE assistant_inbox SET delivered_at = NULL WHERE id = @id;

-- name: DeleteStaleAssistantInbox :many
DELETE FROM assistant_inbox WHERE delivered_at IS NULL AND created_at < @before RETURNING *;

-- name: DeleteDeliveredAssistantInbox :execrows
DELETE FROM assistant_inbox WHERE delivered_at IS NOT NULL AND delivered_at < @before;

-- ---------- singletons ----------

-- name: GetMainThread :one
SELECT * FROM threads WHERE main = 1;

-- Newest main-session message entries with their time (main conversation brief).
-- name: ListRecentMainMessagesTs :many
SELECT seq, ts, payload FROM entries
WHERE thread_id = @thread_id AND type = 'message' AND instr(file, '/') = 0
ORDER BY seq DESC
LIMIT @limit;

-- name: GetComputerOrb :one
SELECT * FROM orbs WHERE computer = 1 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1;

-- True when the run thread was created by a schedule or GitHub trigger that hands its results to the assistant.
-- name: IsThreadHandedToAssistant :one
SELECT CAST(EXISTS (
    SELECT 1 FROM threads t JOIN schedules s ON s.id = t.schedule_id
    WHERE t.id = @thread_id AND s.hand_to_assistant = 1
) OR EXISTS (
    SELECT 1 FROM thread_issues ti JOIN github_triggers g ON g.id = COALESCE(ti.rule_id, ti.trigger_id)
    WHERE ti.thread_id = @thread_id AND g.hand_to_assistant = 1
) AS BOOLEAN) AS handed;
