-- Agent identity (owner-level avatar shown on assistant turns).

-- name: GetOwnerAgentIdentity :one
SELECT agent_avatar_icon, agent_avatar_image FROM owner WHERE id = 1;

-- name: SetOwnerAgentIdentity :execrows
UPDATE owner SET agent_avatar_icon = @agent_avatar_icon, agent_avatar_image = @agent_avatar_image WHERE id = 1;
