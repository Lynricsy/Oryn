-- Git commit identity (owner default; project overrides live in projects.sql).

-- name: GetOwnerGitIdentity :one
SELECT git_name, git_email FROM owner WHERE id = 1;

-- name: SetOwnerGitIdentity :execrows
UPDATE owner SET git_name = @git_name, git_email = @git_email WHERE id = 1;
