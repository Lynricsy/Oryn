-- Projects.

-- snapshots / warm_orbs (M11a): NULL = column default (snapshots on, no warm orbs).
-- name: CreateProject :exec
INSERT INTO projects (id, name, repo_url, base_branch, default_provider, default_orb_type,
                      setup_script, resume_script, shallow_clone, color, icon, git_author_name, git_author_email,
                      default_browser_profile_id, created_at, updated_at, snapshots, warm_orbs, description)
VALUES (@id, @name, @repo_url, @base_branch, @default_provider, @default_orb_type,
        @setup_script, @resume_script, @shallow_clone, @color, @icon, @git_author_name, @git_author_email,
        sqlc.narg(default_browser_profile_id), @created_at, @updated_at,
        COALESCE(CAST(sqlc.narg(snapshots) AS BOOLEAN), 1), COALESCE(CAST(sqlc.narg(warm_orbs) AS INTEGER), 0),
        @description);

-- name: GetProject :one
SELECT * FROM projects WHERE id = @id;

-- name: GetProjectByName :one
SELECT * FROM projects WHERE name = @name;

-- name: ListProjects :many
SELECT * FROM projects
WHERE (CAST(sqlc.narg(archived) AS BOOLEAN) IS NULL OR (archived_at IS NOT NULL) = CAST(sqlc.narg(archived) AS BOOLEAN))
ORDER BY name;

-- snapshots / warm_orbs (M11a): NULL = unchanged.
-- name: UpdateProject :execrows
UPDATE projects SET name = @name, repo_url = @repo_url, base_branch = @base_branch,
    default_provider = @default_provider, default_orb_type = @default_orb_type,
    setup_script = @setup_script, resume_script = @resume_script, shallow_clone = @shallow_clone,
    color = @color, icon = @icon, git_author_name = @git_author_name, git_author_email = @git_author_email,
    default_browser_profile_id = sqlc.narg(default_browser_profile_id),
    snapshots = COALESCE(CAST(sqlc.narg(snapshots) AS BOOLEAN), snapshots),
    warm_orbs = COALESCE(CAST(sqlc.narg(warm_orbs) AS INTEGER), warm_orbs),
    archived_at = @archived_at, description = @description, updated_at = @updated_at
WHERE id = @id;

-- Fill the icon only while the project has none (auto-adopted from a title card; never overrides a manual pick).
-- name: AdoptProjectIcon :execrows
UPDATE projects SET icon = @icon, updated_at = @updated_at
WHERE id = @id AND (icon IS NULL OR icon = '');

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = @id;
