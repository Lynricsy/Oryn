-- Projects.

-- name: CreateProject :exec
INSERT INTO projects (id, name, repo_url, base_branch, default_provider, default_orb_type,
                      setup_script, resume_script, shallow_clone, color, git_author_name, git_author_email,
                      created_at, updated_at)
VALUES (@id, @name, @repo_url, @base_branch, @default_provider, @default_orb_type,
        @setup_script, @resume_script, @shallow_clone, @color, @git_author_name, @git_author_email,
        @created_at, @updated_at);

-- name: GetProject :one
SELECT * FROM projects WHERE id = @id;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE name = @name;

-- name: ListProjects :many
SELECT * FROM projects
WHERE (CAST(sqlc.narg(archived) AS BOOLEAN) IS NULL OR (archived_at IS NOT NULL) = CAST(sqlc.narg(archived) AS BOOLEAN))
ORDER BY name;

-- name: UpdateProject :execrows
UPDATE projects SET name = @name, repo_url = @repo_url, base_branch = @base_branch,
    default_provider = @default_provider, default_orb_type = @default_orb_type,
    setup_script = @setup_script, resume_script = @resume_script, shallow_clone = @shallow_clone,
    color = @color, git_author_name = @git_author_name, git_author_email = @git_author_email,
    archived_at = @archived_at, updated_at = @updated_at
WHERE id = @id;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = @id;
