-- Identities (owner-level): agent name + avatar on assistant turns, assistant identity, user avatar on user turns,
-- and per-role avatars for omp roles.

-- name: GetOwnerAgentIdentity :one
SELECT agent_name, agent_avatar_icon, agent_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerAgentIdentity :execrows
UPDATE owner SET agent_name = @agent_name, agent_avatar_icon = @agent_avatar_icon, agent_avatar_image = @agent_avatar_image
WHERE id = 1;

-- Global assistant identity: raw values; NULL name / avatar = follow the agent identity (resolved by clients).

-- name: GetOwnerAssistantIdentity :one
SELECT assistant_name, assistant_avatar_icon, assistant_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerAssistantIdentity :execrows
UPDATE owner SET assistant_name = @assistant_name, assistant_avatar_icon = @assistant_avatar_icon,
    assistant_avatar_image = @assistant_avatar_image
WHERE id = 1;

-- User identity (owner-level avatar shown on user turns).

-- name: GetOwnerUserIdentity :one
SELECT user_avatar_icon, user_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerUserIdentity :execrows
UPDATE owner SET user_avatar_icon = @user_avatar_icon, user_avatar_image = @user_avatar_image WHERE id = 1;

-- Role avatars: a row exists iff the role has an avatar; clearing deletes the row.

-- name: ListRoleAvatars :many
SELECT role, avatar_icon, avatar_image FROM role_avatars ORDER BY role;

-- name: GetRoleAvatar :one
SELECT role, avatar_icon, avatar_image FROM role_avatars WHERE role = @role;

-- name: UpsertRoleAvatar :exec
INSERT INTO role_avatars (role, avatar_icon, avatar_image, updated_at)
VALUES (@role, @avatar_icon, @avatar_image, @updated_at)
ON CONFLICT (role) DO UPDATE SET avatar_icon = excluded.avatar_icon, avatar_image = excluded.avatar_image,
    updated_at = excluded.updated_at;

-- name: DeleteRoleAvatar :exec
DELETE FROM role_avatars WHERE role = @role;

-- Agent avatars (omp subagent definitions, keyed by frontmatter name): a row exists iff the agent has an avatar.

-- name: ListAgentAvatars :many
SELECT name, avatar_icon, avatar_image FROM agent_avatars ORDER BY name;

-- name: GetAgentAvatar :one
SELECT name, avatar_icon, avatar_image FROM agent_avatars WHERE name = @name;

-- name: UpsertAgentAvatar :exec
INSERT INTO agent_avatars (name, avatar_icon, avatar_image, updated_at)
VALUES (@name, @avatar_icon, @avatar_image, @updated_at)
ON CONFLICT (name) DO UPDATE SET avatar_icon = excluded.avatar_icon, avatar_image = excluded.avatar_image,
    updated_at = excluded.updated_at;

-- name: DeleteAgentAvatar :exec
DELETE FROM agent_avatars WHERE name = @name;

-- Composer preference: default target for new threads ('role' | 'assistant').

-- name: GetOwnerNewThreadTarget :one
SELECT new_thread_target FROM owner WHERE id = 1;

-- name: SetOwnerNewThreadTarget :execrows
UPDATE owner SET new_thread_target = @new_thread_target WHERE id = 1;
