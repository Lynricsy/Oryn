-- M24 phase 2: control-plane bare repositories of projects, observed pushes and commit <-> thread links.

-- ---------- project_repos ----------

-- name: GetProjectRepo :one
SELECT * FROM project_repos WHERE project_id = @project_id;

-- name: ListProjectRepos :many
SELECT * FROM project_repos ORDER BY project_id;

-- name: DeleteProjectRepo :exec
DELETE FROM project_repos WHERE project_id = @project_id;

-- Insert or overwrite every column; created_at keeps the first value.
-- name: UpsertProjectRepo :exec
INSERT INTO project_repos (project_id, kind, remote_url, github_repo_id, default_branch, synced_at, size_bytes,
                           error_code, error_message, error_at, created_at, updated_at)
VALUES (@project_id, @kind, sqlc.narg(remote_url), sqlc.narg(github_repo_id), sqlc.narg(default_branch), sqlc.narg(synced_at),
        @size_bytes, sqlc.narg(error_code), sqlc.narg(error_message), sqlc.narg(error_at), @created_at, @updated_at)
ON CONFLICT (project_id) DO UPDATE SET
    kind = excluded.kind,
    remote_url = excluded.remote_url,
    github_repo_id = excluded.github_repo_id,
    default_branch = excluded.default_branch,
    synced_at = excluded.synced_at,
    size_bytes = excluded.size_bytes,
    error_code = excluded.error_code,
    error_message = excluded.error_message,
    error_at = excluded.error_at,
    updated_at = excluded.updated_at;

-- ---------- repo_pushes ----------

-- name: InsertRepoPush :exec
INSERT INTO repo_pushes (project_id, thread_id, orb_id, ref, old_sha, new_sha, pushed_at)
VALUES (@project_id, @thread_id, @orb_id, @ref, @old_sha, @new_sha, @pushed_at);

-- name: ListPendingRepoPushes :many
SELECT * FROM repo_pushes WHERE project_id = @project_id AND expanded_at IS NULL ORDER BY id;

-- name: FinishRepoPush :exec
UPDATE repo_pushes SET expanded_at = CAST(@expanded_at AS INTEGER), expand_note = sqlc.narg(expand_note) WHERE id = @id;

-- Pushes whose new commit never showed up in the mirror.
-- name: ExpireRepoPushes :exec
UPDATE repo_pushes SET expanded_at = CAST(@now AS INTEGER), expand_note = 'missing'
WHERE expanded_at IS NULL AND pushed_at < @before;

-- name: DeleteRepoPushesBefore :exec
DELETE FROM repo_pushes WHERE expanded_at IS NOT NULL AND expanded_at < CAST(@before AS INTEGER);

-- ---------- repo_commit_threads ----------

-- A positive link only replaces a negative ('none') row: the first positive result wins.
-- name: LinkRepoCommitThread :exec
INSERT INTO repo_commit_threads (project_id, sha, thread_id, source, linked_at)
VALUES (@project_id, @sha, @thread_id, @source, @linked_at)
ON CONFLICT (project_id, sha) DO UPDATE SET
    thread_id = excluded.thread_id, source = excluded.source, linked_at = excluded.linked_at
WHERE repo_commit_threads.source = 'none';

-- Negative cache of the GitHub lookup; never overwrites a positive link.
-- name: MarkRepoCommitNoThread :exec
INSERT OR REPLACE INTO repo_commit_threads (project_id, sha, thread_id, source, linked_at)
SELECT CAST(@project_id AS TEXT), CAST(@sha AS TEXT), NULL, 'none', CAST(@linked_at AS INTEGER)
WHERE NOT EXISTS (
    SELECT 1 FROM repo_commit_threads r
    WHERE r.project_id = CAST(@project_id AS TEXT) AND r.sha = CAST(@sha AS TEXT) AND r.source <> 'none'
);

-- Stored links (including 'none' rows) of a page of commits, with the thread title.
-- name: ListRepoCommitThreads :many
SELECT rct.sha, rct.thread_id, rct.source, rct.linked_at, t.title
FROM repo_commit_threads rct LEFT JOIN threads t ON t.id = rct.thread_id
WHERE rct.project_id = @project_id AND rct.sha IN (sqlc.slice('shas'));

-- Commits sampled from threads of the project (M10 thread_commits), most recently active thread first.
-- name: ListSampledCommitThreads :many
SELECT tc.sha, tc.thread_id, t.title
FROM thread_commits tc JOIN threads t ON t.id = tc.thread_id
WHERE t.project_id = CAST(@project_id AS TEXT) AND tc.sha IN (sqlc.slice('shas'))
ORDER BY COALESCE(t.last_message_at, t.created_at) DESC;
