-- Avatar identities (owner-level): agent avatar on assistant turns, user avatar on user turns.

-- name: GetOwnerAgentIdentity :one
SELECT agent_avatar_icon, agent_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerAgentIdentity :execrows
UPDATE owner SET agent_avatar_icon = @agent_avatar_icon, agent_avatar_image = @agent_avatar_image WHERE id = 1;

-- User identity (owner-level avatar shown on user turns).

-- name: GetOwnerUserIdentity :one
SELECT user_avatar_icon, user_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerUserIdentity :execrows
UPDATE owner SET user_avatar_icon = @user_avatar_icon, user_avatar_image = @user_avatar_image WHERE id = 1;
