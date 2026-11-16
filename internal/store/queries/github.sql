-- M14: GitHub integration.

-- ---------- App ----------

-- name: GetGithubApp :one
SELECT * FROM github_app WHERE id = 1;

-- name: InsertGithubApp :exec
INSERT INTO github_app (id, app_id, slug, name, client_id, owner_login, html_url, public, webhook_url,
                        private_key_enc, client_secret_enc, webhook_secret_enc, created_at)
VALUES (1, @app_id, @slug, @name, @client_id, @owner_login, @html_url, @public, @webhook_url,
        @private_key_enc, @client_secret_enc, @webhook_secret_enc, @created_at);

-- name: DeleteGithubApp :exec
DELETE FROM github_app WHERE id = 1;

-- name: SetGithubHookCursor :exec
UPDATE github_app SET hook_cursor = @hook_cursor WHERE id = 1;

-- name: TouchGithubDelivery :exec
UPDATE github_app SET last_delivery_at = @last_delivery_at, last_delivery_src = @last_delivery_src WHERE id = 1;

-- ---------- identities ----------

-- name: GetGithubIdentity :one
SELECT * FROM github_identities WHERE slot = @slot;

-- name: ListGithubIdentities :many
SELECT * FROM github_identities ORDER BY slot;

-- name: UpsertGithubIdentity :exec
INSERT INTO github_identities (slot, github_id, login, name, avatar_url, access_token_enc, access_expires_at,
                               refresh_token_enc, refresh_expires_at, status, connected_at, updated_at)
VALUES (@slot, @github_id, @login, sqlc.narg(name), @avatar_url, @access_token_enc, sqlc.narg(access_expires_at),
        sqlc.narg(refresh_token_enc), sqlc.narg(refresh_expires_at), 'ok', @connected_at, @updated_at)
ON CONFLICT (slot) DO UPDATE SET
    github_id = excluded.github_id, login = excluded.login, name = excluded.name, avatar_url = excluded.avatar_url,
    access_token_enc = excluded.access_token_enc, access_expires_at = excluded.access_expires_at,
    refresh_token_enc = excluded.refresh_token_enc, refresh_expires_at = excluded.refresh_expires_at,
    status = 'ok', connected_at = excluded.connected_at, updated_at = excluded.updated_at;

-- name: SetGithubIdentityTokens :exec
UPDATE github_identities
SET access_token_enc = @access_token_enc, access_expires_at = sqlc.narg(access_expires_at),
    refresh_token_enc = sqlc.narg(refresh_token_enc), refresh_expires_at = sqlc.narg(refresh_expires_at),
    status = 'ok', updated_at = @updated_at
WHERE slot = @slot;

-- name: SetGithubIdentityStatus :exec
UPDATE github_identities SET status = @status, updated_at = @updated_at WHERE slot = @slot;

-- name: DeleteGithubIdentity :execrows
DELETE FROM github_identities WHERE slot = @slot;

-- name: DeleteGithubIdentities :exec
DELETE FROM github_identities;

-- name: GetOwnerGithubAutomation :one
SELECT github_automation_identity FROM owner WHERE id = 1;

-- name: SetOwnerGithubAutomation :execrows
UPDATE owner SET github_automation_identity = @github_automation_identity WHERE id = 1;

-- name: SetGithubIdentityScopes :exec
UPDATE github_identities SET scopes = @scopes, updated_at = @updated_at WHERE slot = @slot;

-- ---------- OAuth App ----------

-- name: GetGithubOAuthApp :one
SELECT * FROM github_oauth_app WHERE id = 1;

-- name: UpsertGithubOAuthApp :exec
INSERT INTO github_oauth_app (id, client_id, client_secret_enc, webhook_base, created_at)
VALUES (1, @client_id, @client_secret_enc, sqlc.narg(webhook_base), @created_at)
ON CONFLICT (id) DO UPDATE SET
    client_id = excluded.client_id, client_secret_enc = excluded.client_secret_enc, webhook_base = excluded.webhook_base;

-- name: DeleteGithubOAuthApp :exec
DELETE FROM github_oauth_app WHERE id = 1;

-- ---------- one-shot states ----------

-- name: InsertGithubState :exec
INSERT INTO github_oauth_states (state_hash, kind, slot, manifest, action_url, public, webhook_url, expires_at)
VALUES (@state_hash, @kind, sqlc.narg(slot), sqlc.narg(manifest), sqlc.narg(action_url), @public, sqlc.narg(webhook_url), @expires_at);

-- name: GetGithubState :one
SELECT * FROM github_oauth_states WHERE state_hash = @state_hash;

-- name: DeleteGithubState :execrows
DELETE FROM github_oauth_states WHERE state_hash = @state_hash;

-- name: DeleteExpiredGithubStates :exec
DELETE FROM github_oauth_states WHERE expires_at < @now;

-- ---------- installations ----------

-- name: ListGithubInstallations :many
SELECT * FROM github_installations ORDER BY account_login, id;

-- name: UpsertGithubInstallation :exec
INSERT INTO github_installations (id, account_login, account_type, repository_selection, suspended, updated_at)
VALUES (@id, @account_login, @account_type, @repository_selection, @suspended, @updated_at)
ON CONFLICT (id) DO UPDATE SET
    account_login = excluded.account_login, account_type = excluded.account_type,
    repository_selection = excluded.repository_selection, suspended = excluded.suspended, updated_at = excluded.updated_at;

-- name: DeleteGithubInstallationsExcept :exec
DELETE FROM github_installations WHERE id NOT IN (sqlc.slice('ids'));

-- name: DeleteGithubInstallations :exec
DELETE FROM github_installations;

-- ---------- project binding ----------

-- name: SetProjectGithub :exec
UPDATE projects SET github_repo_id = @github_repo_id, github_owner_id = @github_owner_id,
                    github_installation_id = @github_installation_id
WHERE id = @id;

-- name: ClearProjectGithub :exec
UPDATE projects SET github_repo_id = NULL, github_owner_id = NULL, github_installation_id = NULL WHERE id = @id;

-- name: ClearProjectGithubInstallation :exec
UPDATE projects SET github_installation_id = NULL WHERE id = @id;

-- name: ClearAllProjectGithubInstallations :exec
UPDATE projects SET github_installation_id = NULL WHERE github_installation_id IS NOT NULL;

-- name: SetProjectRepoURL :exec
UPDATE projects SET repo_url = @repo_url, github_prev_urls = @github_prev_urls, updated_at = @updated_at WHERE id = @id;

-- name: SetProjectGithubListen :exec
UPDATE projects SET github_listen_reviews = @github_listen_reviews, github_listen_comments = @github_listen_comments,
                    github_listen_ci = @github_listen_ci, updated_at = @updated_at
WHERE id = @id;

-- name: SetProjectGithubBindStatus :exec
UPDATE projects SET github_bind_status = sqlc.narg(github_bind_status) WHERE id = @id;

-- name: SetProjectGithubGates :exec
UPDATE projects SET github_gates = sqlc.narg(github_gates), updated_at = @updated_at WHERE id = @id;

-- Bound (installation reachable), unarchived projects.
-- name: ListProjectsByGithubRepo :many
SELECT * FROM projects
WHERE github_repo_id = @repo_id AND github_installation_id IS NOT NULL AND archived_at IS NULL
ORDER BY created_at;

-- Every project that ever recorded this repo id (rename following).
-- name: ListProjectsByGithubRepoAny :many
SELECT * FROM projects WHERE github_repo_id = @repo_id ORDER BY created_at;

-- name: ListGithubCandidateProjects :many
SELECT * FROM projects WHERE archived_at IS NULL AND repo_url LIKE 'https://github.com/%' ORDER BY created_at;

-- ---------- orb ----------

-- GitHub login of an orb whose project is bound and whose identity slot is connected.
-- name: GetOrbGithubLogin :one
SELECT gi.login FROM orbs o
JOIN projects p ON p.id = o.project_id
JOIN github_identities gi ON gi.slot = o.github_identity
WHERE o.id = @id AND p.github_repo_id IS NOT NULL AND p.github_installation_id IS NOT NULL;

-- ---------- deliveries ----------

-- name: InsertGithubDelivery :execrows
INSERT OR IGNORE INTO github_deliveries (guid, event, source, payload, received_at)
VALUES (@guid, @event, @source, @payload, @received_at);

-- name: FinishGithubDelivery :exec
UPDATE github_deliveries SET outcome = @outcome WHERE guid = @guid;

-- Replay: only finished deliveries received since @since whose payload is still kept go back to pending.
-- name: ReplayGithubDelivery :execrows
UPDATE github_deliveries SET outcome = 'pending', attempts = 0
WHERE guid = @guid AND outcome <> 'pending' AND received_at >= @since AND payload IS NOT NULL;

-- name: BumpGithubDeliveryAttempts :one
UPDATE github_deliveries SET attempts = attempts + 1 WHERE guid = @guid RETURNING attempts;

-- name: ListPendingGithubDeliveries :many
SELECT guid, event, payload FROM github_deliveries WHERE outcome = 'pending' ORDER BY received_at, guid;

-- name: GetGithubDelivery :one
SELECT * FROM github_deliveries WHERE guid = @guid;

-- name: GithubDeliveryExists :one
SELECT EXISTS (SELECT 1 FROM github_deliveries WHERE guid = @guid);

-- name: DeleteGithubDeliveriesBefore :exec
DELETE FROM github_deliveries WHERE received_at < @before;

-- ---------- repository hooks ----------

-- name: GetGithubHook :one
SELECT * FROM github_hooks WHERE repo_id = @repo_id;

-- name: GetGithubHookByHookID :one
SELECT * FROM github_hooks WHERE hook_id = @hook_id;

-- name: ListGithubHooks :many
SELECT * FROM github_hooks ORDER BY full_name, repo_id;

-- name: ListActiveGithubHooks :many
SELECT * FROM github_hooks WHERE status = 'active' AND hook_id IS NOT NULL ORDER BY repo_id;

-- A different hook id starts a new delivery log: drop the cursor and ETag.
-- name: UpsertGithubHook :exec
INSERT INTO github_hooks (repo_id, full_name, hook_id, secret_enc, status, error, updated_at)
VALUES (@repo_id, @full_name, sqlc.narg(hook_id), sqlc.narg(secret_enc), @status, sqlc.narg(error), @updated_at)
ON CONFLICT (repo_id) DO UPDATE SET
    full_name = excluded.full_name, hook_id = excluded.hook_id, secret_enc = excluded.secret_enc,
    status = excluded.status, error = excluded.error, updated_at = excluded.updated_at,
    delivery_cursor = CASE WHEN github_hooks.hook_id IS excluded.hook_id THEN github_hooks.delivery_cursor ELSE 0 END,
    deliveries_etag = CASE WHEN github_hooks.hook_id IS excluded.hook_id THEN github_hooks.deliveries_etag ELSE NULL END;

-- name: SetGithubHookStatus :exec
UPDATE github_hooks SET status = @status, error = sqlc.narg(error), updated_at = @updated_at WHERE repo_id = @repo_id;

-- name: SetGithubHookDeliveryCursor :exec
UPDATE github_hooks SET delivery_cursor = @delivery_cursor, deliveries_etag = sqlc.narg(deliveries_etag)
WHERE repo_id = @repo_id;

-- name: TouchGithubHookDelivery :exec
UPDATE github_hooks SET last_delivery_at = @last_delivery_at, last_delivery_src = @last_delivery_src WHERE repo_id = @repo_id;

-- name: TouchGithubHookWebhook :exec
UPDATE github_hooks SET last_webhook_at = @last_webhook_at WHERE repo_id = @repo_id;

-- name: SetGithubHooksRepoName :exec
UPDATE github_hooks SET full_name = @full_name WHERE repo_id = @repo_id;

-- name: DeleteGithubHook :execrows
DELETE FROM github_hooks WHERE repo_id = @repo_id;

-- name: DeleteGithubHooks :exec
DELETE FROM github_hooks;

-- ---------- REST watches ----------

-- Re-registering keeps the ETags and the since cursor.
-- name: UpsertGithubRepoWatch :exec
INSERT INTO github_repo_watches (repo_id, full_name, updated_at) VALUES (@repo_id, @full_name, @updated_at)
ON CONFLICT (repo_id) DO UPDATE SET full_name = excluded.full_name, updated_at = excluded.updated_at;

-- name: GetGithubRepoWatch :one
SELECT * FROM github_repo_watches WHERE repo_id = @repo_id;

-- name: ListGithubRepoWatches :many
SELECT * FROM github_repo_watches ORDER BY repo_id;

-- name: SetGithubRepoWatchState :exec
UPDATE github_repo_watches
SET etag_issues = sqlc.narg(etag_issues), etag_comments = sqlc.narg(etag_comments), etag_pulls = sqlc.narg(etag_pulls),
    etag_runs = sqlc.narg(etag_runs), since_at = sqlc.narg(since_at), updated_at = @updated_at
WHERE repo_id = @repo_id;

-- name: DeleteGithubRepoWatch :execrows
DELETE FROM github_repo_watches WHERE repo_id = @repo_id;

-- Re-registering keeps the ETags, the cursor and done_at.
-- name: UpsertGithubWatch :exec
INSERT INTO github_watches (kind, repo_id, full_name, number, thread_id, slot)
VALUES (@kind, @repo_id, @full_name, @number, @thread_id, @slot)
ON CONFLICT (kind, full_name, number) DO UPDATE SET
    repo_id = excluded.repo_id, thread_id = excluded.thread_id, slot = excluded.slot;

-- name: GetGithubWatch :one
SELECT * FROM github_watches WHERE kind = @kind AND full_name = @full_name AND number = @number;

-- name: ListGithubWatches :many
SELECT * FROM github_watches ORDER BY kind, full_name, number;

-- name: ListGithubWatchesByRepo :many
SELECT * FROM github_watches WHERE repo_id = @repo_id ORDER BY kind, number;

-- name: ListGithubWatchesByThread :many
SELECT * FROM github_watches WHERE thread_id = @thread_id ORDER BY kind, full_name, number;

-- name: SetGithubWatchState :exec
UPDATE github_watches
SET etag_issue = sqlc.narg(etag_issue), etag_comments = sqlc.narg(etag_comments), etag_reviews = sqlc.narg(etag_reviews),
    etag_review_comments = sqlc.narg(etag_review_comments), etag_checks = sqlc.narg(etag_checks),
    last_seen_at = sqlc.narg(last_seen_at)
WHERE kind = @kind AND full_name = @full_name AND number = @number;

-- name: SetGithubWatchDone :exec
UPDATE github_watches SET done_at = sqlc.narg(done_at) WHERE kind = @kind AND full_name = @full_name AND number = @number;

-- name: SetGithubWatchesRepoName :exec
UPDATE github_watches SET full_name = @full_name WHERE repo_id = @repo_id;

-- name: DeleteGithubWatch :execrows
DELETE FROM github_watches WHERE kind = @kind AND full_name = @full_name AND number = @number;

-- Retirement: terminal for long enough, or the thread is archived.
-- name: DeleteRetiredGithubWatches :exec
DELETE FROM github_watches
WHERE (done_at IS NOT NULL AND done_at < @done_before)
   OR thread_id IN (SELECT id FROM threads WHERE archived_at IS NOT NULL);

-- ---------- self writes ----------

-- name: InsertGithubSelfWrite :exec
INSERT INTO github_self_writes (slot, category, target, body_sha256, created_at)
VALUES (@slot, @category, @target, sqlc.narg(body_sha256), @created_at);

-- name: ListGithubSelfWrites :many
SELECT * FROM github_self_writes
WHERE target = @target AND category = @category AND created_at >= @since
ORDER BY id;

-- name: DeleteGithubSelfWritesBefore :exec
DELETE FROM github_self_writes WHERE created_at < @before;

-- name: InsertGithubSelfKey :exec
INSERT OR IGNORE INTO github_self_keys (key, created_at) VALUES (@key, @created_at);

-- name: GithubSelfKeyExists :one
SELECT EXISTS (SELECT 1 FROM github_self_keys WHERE key IN (sqlc.slice('keys')));

-- name: DeleteGithubSelfKeysBefore :exec
DELETE FROM github_self_keys WHERE created_at < @before;

-- ---------- audit ----------

-- name: InsertGithubAudit :exec
INSERT INTO github_audit (at, thread_id, orb_id, slot, method, op, repo, status, gate, body_sha256)
VALUES (@at, sqlc.narg(thread_id), @orb_id, @slot, @method, @op, @repo, @status, @gate, sqlc.narg(body_sha256));

-- thread_id / repo NULL = no filter. Newest first; page with @before (exclusive).
-- name: ListGithubAudit :many
SELECT * FROM github_audit
WHERE (CAST(sqlc.narg(thread_id) AS TEXT) IS NULL OR thread_id = CAST(sqlc.narg(thread_id) AS TEXT))
  AND (CAST(sqlc.narg(repo) AS TEXT) IS NULL OR repo = CAST(sqlc.narg(repo) AS TEXT))
  AND at < @before
ORDER BY at DESC, id DESC
LIMIT @limit;

-- name: DeleteGithubAuditBefore :exec
DELETE FROM github_audit WHERE at < @before;

-- ---------- prepare ----------

-- name: UpsertGithubPrepare :exec
INSERT INTO github_prepare (sha, orb_id, level, skipped, reason, at)
VALUES (@sha, @orb_id, @level, @skipped, sqlc.narg(reason), @at)
ON CONFLICT (sha, orb_id, level) DO UPDATE SET skipped = excluded.skipped, reason = excluded.reason, at = excluded.at;

-- Best pass record among the accepted levels: an unskipped one wins.
-- name: FindGithubPrepare :one
SELECT * FROM github_prepare
WHERE sha = @sha AND orb_id = @orb_id AND level IN (sqlc.slice('levels'))
ORDER BY skipped, at DESC
LIMIT 1;

-- name: DeleteGithubPrepareBefore :exec
DELETE FROM github_prepare WHERE at < @before;

-- ---------- autoclose ----------

-- Re-scheduling restarts the entry.
-- name: UpsertGithubAutoclose :exec
INSERT INTO github_autoclose (repo_id, number, thread_id, comment_id, author, due_at)
VALUES (@repo_id, @number, @thread_id, @comment_id, @author, @due_at)
ON CONFLICT (repo_id, number) DO UPDATE SET
    thread_id = excluded.thread_id, comment_id = excluded.comment_id, author = excluded.author,
    due_at = excluded.due_at, state = 'pending', outcome = NULL;

-- name: GetGithubAutoclose :one
SELECT * FROM github_autoclose WHERE repo_id = @repo_id AND number = @number;

-- name: ListGithubAutoclosesByThread :many
SELECT * FROM github_autoclose WHERE thread_id = @thread_id ORDER BY due_at;

-- Atomic claim: each entry runs at most once.
-- name: ClaimDueGithubAutocloses :many
UPDATE github_autoclose SET state = 'claimed' WHERE state = 'pending' AND due_at <= @now RETURNING *;

-- name: FinishGithubAutoclose :exec
UPDATE github_autoclose SET state = @state, outcome = sqlc.narg(outcome)
WHERE repo_id = @repo_id AND number = @number AND state = 'claimed';

-- name: CancelGithubAutoclose :execrows
UPDATE github_autoclose SET state = 'cancelled', outcome = sqlc.narg(outcome)
WHERE repo_id = @repo_id AND number = @number AND state = 'pending';

-- name: DeleteGithubAutoclosesBefore :exec
DELETE FROM github_autoclose WHERE state IN ('done','cancelled') AND due_at < @before;

-- ---------- releases ----------

-- name: GetGithubRelease :one
SELECT * FROM github_releases WHERE repo_id = @repo_id AND version = @version;

-- name: GetGithubReleaseByThread :one
SELECT * FROM github_releases WHERE thread_id = @thread_id ORDER BY updated_at DESC LIMIT 1;

-- name: ListGithubReleasesByRepo :many
SELECT * FROM github_releases WHERE repo_id = @repo_id ORDER BY updated_at DESC, version;

-- name: ListGithubReleases :many
SELECT * FROM github_releases ORDER BY updated_at DESC, repo_id, version;

-- Seeing a version again refreshes the run snapshot but keeps the thread and the round counter.
-- name: UpsertGithubRelease :exec
INSERT INTO github_releases (repo_id, version, sha, tag_oid, state, updated_at)
VALUES (@repo_id, @version, @sha, sqlc.narg(tag_oid), @state, @updated_at)
ON CONFLICT (repo_id, version) DO UPDATE SET
    sha = excluded.sha, tag_oid = excluded.tag_oid, state = excluded.state, updated_at = excluded.updated_at;

-- name: SetGithubReleaseThread :exec
UPDATE github_releases SET thread_id = sqlc.narg(thread_id), updated_at = @updated_at
WHERE repo_id = @repo_id AND version = @version;

-- name: SetGithubReleaseState :exec
UPDATE github_releases SET state = @state, rounds = @rounds, updated_at = @updated_at
WHERE repo_id = @repo_id AND version = @version;

-- A newer version of the same repo supersedes every unfinished older one.
-- name: SupersedeGithubReleases :many
UPDATE github_releases SET state = 'superseded', updated_at = @updated_at
WHERE repo_id = @repo_id AND version <> @version AND state IN ('awaiting_ci','fixing')
RETURNING *;

-- name: DeleteGithubReleasesBefore :exec
DELETE FROM github_releases WHERE state IN ('green','failed','superseded') AND updated_at < @before;

-- ---------- sender rate limit ----------

-- name: InsertGithubSenderRun :exec
INSERT INTO github_sender_runs (rule_id, sender, at) VALUES (@rule_id, @sender, @at);

-- name: CountGithubSenderRunsSince :one
SELECT COUNT(*) FROM github_sender_runs WHERE rule_id = @rule_id AND sender = @sender AND at >= @since;

-- name: DeleteGithubSenderRunsBefore :exec
DELETE FROM github_sender_runs WHERE at < @before;

-- ---------- PR ----------

-- A new head_sha means new commits: drop the stale CI rollup until the next one.
-- name: UpsertGithubPull :exec
INSERT INTO github_pulls (repo_id, number, repo_full_name, title, state, draft, same_repo, head_ref, head_sha,
                          base_ref, author_login, html_url, merged_at, updated_at)
VALUES (@repo_id, @number, @repo_full_name, @title, @state, @draft, @same_repo, @head_ref, @head_sha,
        @base_ref, @author_login, @html_url, sqlc.narg(merged_at), @updated_at)
ON CONFLICT (repo_id, number) DO UPDATE SET
    repo_full_name = excluded.repo_full_name, title = excluded.title, state = excluded.state, draft = excluded.draft,
    same_repo = excluded.same_repo, head_ref = excluded.head_ref, head_sha = excluded.head_sha,
    base_ref = excluded.base_ref, author_login = excluded.author_login, html_url = excluded.html_url,
    merged_at = excluded.merged_at, updated_at = excluded.updated_at,
    checks = CASE WHEN github_pulls.head_sha = excluded.head_sha THEN github_pulls.checks ELSE NULL END,
    checks_sha = CASE WHEN github_pulls.head_sha = excluded.head_sha THEN github_pulls.checks_sha ELSE NULL END;

-- name: GetGithubPull :one
SELECT * FROM github_pulls WHERE repo_id = @repo_id AND number = @number;

-- name: SetGithubPullChecks :exec
UPDATE github_pulls SET checks = sqlc.narg(checks), checks_sha = sqlc.narg(checks_sha)
WHERE repo_id = @repo_id AND number = @number;

-- name: ListOpenGithubPullsByHead :many
SELECT * FROM github_pulls WHERE repo_id = @repo_id AND head_sha = @head_sha AND state = 'open' ORDER BY number;

-- name: SetGithubPullsRepoName :exec
UPDATE github_pulls SET repo_full_name = @repo_full_name WHERE repo_id = @repo_id;

-- ---------- thread <-> pull request ----------

-- name: LinkThreadPull :execrows
INSERT OR IGNORE INTO thread_pulls (thread_id, repo_id, number, linked_at)
VALUES (@thread_id, @repo_id, @number, @linked_at);

-- A PR this thread created through egress: link it and mark the origin (an unlinked tombstone stays unlinked).
-- name: LinkThreadPullViaEgress :execrows
INSERT INTO thread_pulls (thread_id, repo_id, number, linked_at, created_via_egress)
VALUES (@thread_id, @repo_id, @number, @linked_at, 1)
ON CONFLICT (thread_id, repo_id, number) DO UPDATE SET created_via_egress = 1;

-- Turning delivery back on also resets the CI round counter.
-- name: SetThreadPullListening :execrows
UPDATE thread_pulls
SET listening = CAST(@listening AS BOOLEAN),
    ci_rounds = CASE WHEN CAST(@listening AS BOOLEAN) THEN 0 ELSE ci_rounds END
WHERE thread_id = @thread_id AND repo_id = @repo_id AND number = @number AND unlinked = 0;

-- name: UnlinkThreadPull :execrows
UPDATE thread_pulls SET unlinked = 1
WHERE thread_id = @thread_id AND repo_id = @repo_id AND number = @number AND unlinked = 0;

-- name: SetThreadPullCIRounds :exec
UPDATE thread_pulls SET ci_rounds = @ci_rounds WHERE thread_id = @thread_id AND repo_id = @repo_id AND number = @number;

-- name: ResetGithubPullCIRounds :exec
UPDATE thread_pulls SET ci_rounds = 0 WHERE repo_id = @repo_id AND number = @number AND ci_rounds <> 0;

-- PR summaries of one thread (not unlinked). Same column set as ListThreadPullSummaries.
-- name: ListThreadPulls :many
SELECT tp.thread_id, tp.repo_id, tp.number, tp.listening, gp.repo_full_name, gp.title, gp.state, gp.draft, gp.html_url, gp.checks
FROM thread_pulls tp
JOIN github_pulls gp ON gp.repo_id = tp.repo_id AND gp.number = tp.number
WHERE tp.thread_id = @thread_id AND tp.unlinked = 0
ORDER BY tp.number;

-- name: ListThreadPullSummaries :many
SELECT tp.thread_id, tp.repo_id, tp.number, tp.listening, gp.repo_full_name, gp.title, gp.state, gp.draft, gp.html_url, gp.checks
FROM thread_pulls tp
JOIN github_pulls gp ON gp.repo_id = tp.repo_id AND gp.number = tp.number
WHERE tp.unlinked = 0
ORDER BY tp.thread_id, tp.number;

-- Live links of a PR: not unlinked, thread not archived.
-- name: ListLivePullThreads :many
SELECT tp.thread_id, tp.listening, tp.ci_rounds FROM thread_pulls tp JOIN threads t ON t.id = tp.thread_id
WHERE tp.repo_id = @repo_id AND tp.number = @number AND tp.unlinked = 0 AND t.archived_at IS NULL
ORDER BY tp.linked_at;

-- name: CountLivePullLinks :one
SELECT COUNT(*) FROM thread_pulls tp JOIN threads t ON t.id = tp.thread_id
WHERE tp.repo_id = @repo_id AND tp.number = @number AND tp.unlinked = 0 AND t.archived_at IS NULL;

-- name: PullTargetThread :one
SELECT tp.thread_id, tp.listening, tp.ci_rounds FROM thread_pulls tp JOIN threads t ON t.id = tp.thread_id
WHERE tp.repo_id = @repo_id AND tp.number = @number AND tp.unlinked = 0 AND t.archived_at IS NULL
ORDER BY COALESCE(t.last_message_at, t.created_at) DESC LIMIT 1;

-- When several threads sampled the same commit, only the most recently active one counts.
-- name: ThreadByCommit :one
SELECT tc.thread_id FROM thread_commits tc
JOIN threads t ON t.id = tc.thread_id JOIN projects p ON p.id = t.project_id
WHERE tc.sha = @sha AND p.github_repo_id = @repo_id AND t.archived_at IS NULL
ORDER BY COALESCE(t.last_message_at, t.created_at) DESC LIMIT 1;

-- name: PullsForThreadCommits :many
SELECT gp.repo_id, gp.number FROM github_pulls gp
JOIN thread_commits tc ON tc.sha = gp.head_sha
JOIN threads t ON t.id = tc.thread_id JOIN projects p ON p.id = t.project_id AND p.github_repo_id = gp.repo_id
WHERE tc.thread_id = @thread_id AND gp.state IN ('open','merged') AND gp.same_repo = 1;

-- name: ListSamplingCandidates :many
SELECT t.id FROM threads t JOIN projects p ON p.id = t.project_id
WHERE p.github_repo_id = @repo_id AND p.github_installation_id IS NOT NULL
  AND t.kind = 'work' AND t.archived_at IS NULL AND t.orb_id IS NOT NULL
  AND COALESCE(t.last_message_at, t.created_at) >= @since
ORDER BY COALESCE(t.last_message_at, t.created_at) DESC LIMIT 10;

-- ---------- triggers ----------

-- name: ListGithubTriggers :many
SELECT * FROM github_triggers WHERE project_id = @project_id ORDER BY created_at, id;

-- name: ListEnabledGithubTriggersByRepo :many
SELECT gt.* FROM github_triggers gt JOIN projects p ON p.id = gt.project_id
WHERE p.github_repo_id = @repo_id AND p.github_installation_id IS NOT NULL AND p.archived_at IS NULL
  AND gt.enabled = 1 AND gt.event = @event
ORDER BY gt.created_at, gt.id;

-- name: GetGithubTrigger :one
SELECT * FROM github_triggers WHERE id = @id;

-- name: InsertGithubTrigger :exec
INSERT INTO github_triggers (id, project_id, name, event, label, prompt, enabled, announce, created_at, updated_at)
VALUES (@id, @project_id, @name, @event, sqlc.narg(label), @prompt, @enabled, @announce, @created_at, @updated_at);

-- name: UpdateGithubTrigger :exec
UPDATE github_triggers
SET name = @name, event = @event, label = sqlc.narg(label), prompt = @prompt, enabled = @enabled, announce = @announce,
    updated_at = @updated_at
WHERE id = @id;

-- name: DeleteGithubTrigger :execrows
DELETE FROM github_triggers WHERE id = @id;

-- name: BumpGithubTrigger :exec
UPDATE github_triggers SET run_count = run_count + 1, last_run_at = @last_run_at WHERE id = @id;

-- name: SetGithubTriggerOptions :exec
UPDATE github_triggers
SET drivers = @drivers, maintainers = @maintainers, reviewer_bots = @reviewer_bots, models = @models,
    thinking = sqlc.narg(thinking), impl_auth = @impl_auth, comment_only_reviews = @comment_only_reviews,
    autoclose_hours = sqlc.narg(autoclose_hours), max_duration_sec = sqlc.narg(max_duration_sec),
    completion_reminders = @completion_reminders, release_prefix = sqlc.narg(release_prefix), updated_at = @updated_at
WHERE id = @id;

-- name: CountTriggerRunsSince :one
SELECT COUNT(*) FROM thread_issues WHERE trigger_id = @trigger_id AND created_at >= @since;

-- ---------- issue <-> thread ----------

-- name: GetThreadIssue :one
SELECT ti.repo_id, ti.number, ti.thread_id, ti.trigger_id, ti.created_at, ti.state, ti.classification, ti.impl_authorized,
       ti.review_only, ti.rule_id, ti.deadline_at, ti.reminders, t.archived_at, t.project_id
FROM thread_issues ti JOIN threads t ON t.id = ti.thread_id
WHERE ti.repo_id = @repo_id AND ti.number = @number;

-- name: UpsertThreadIssue :exec
INSERT INTO thread_issues (repo_id, number, thread_id, trigger_id, created_at)
VALUES (@repo_id, @number, @thread_id, sqlc.narg(trigger_id), @created_at)
ON CONFLICT (repo_id, number) DO UPDATE SET
    thread_id = excluded.thread_id, trigger_id = excluded.trigger_id, created_at = excluded.created_at;

-- Latest issue task of a thread.
-- name: GetThreadIssueByThread :one
SELECT * FROM thread_issues WHERE thread_id = @thread_id ORDER BY created_at DESC LIMIT 1;

-- name: ListThreadIssues :many
SELECT * FROM thread_issues ORDER BY created_at DESC, repo_id, number;

-- Task options fixed when a rule creates the thread.
-- name: SetThreadIssueTask :exec
UPDATE thread_issues SET rule_id = sqlc.narg(rule_id), review_only = @review_only, deadline_at = sqlc.narg(deadline_at)
WHERE repo_id = @repo_id AND number = @number;

-- name: SetThreadIssueState :execrows
UPDATE thread_issues SET state = @state WHERE thread_id = @thread_id;

-- name: SetThreadIssueClassification :execrows
UPDATE thread_issues SET classification = sqlc.narg(classification) WHERE thread_id = @thread_id;

-- name: SetThreadIssueImplAuthorized :execrows
UPDATE thread_issues SET impl_authorized = @impl_authorized WHERE repo_id = @repo_id AND number = @number;

-- name: SetThreadIssueDeadline :exec
UPDATE thread_issues SET deadline_at = sqlc.narg(deadline_at) WHERE thread_id = @thread_id;

-- name: BumpThreadIssueReminders :one
UPDATE thread_issues SET reminders = reminders + 1 WHERE thread_id = @thread_id RETURNING reminders;

-- Due deadlines of non-terminal tasks.
-- name: ListDueThreadIssueDeadlines :many
SELECT * FROM thread_issues
WHERE deadline_at IS NOT NULL AND deadline_at <= @now AND state NOT IN ('merged','closed','abandoned')
ORDER BY deadline_at;

-- ---------- outbox ----------

-- name: InsertGithubOutbox :exec
INSERT INTO github_outbox (id, thread_id, repo_id, number, is_pull, title, url, kind, item, created_at, deliver_after)
VALUES (@id, @thread_id, @repo_id, @number, @is_pull, @title, @url, @kind, @item, @created_at, @deliver_after);

-- name: ListDueGithubOutbox :many
SELECT * FROM github_outbox
WHERE delivered_at IS NULL AND deliver_after <= @now
ORDER BY thread_id, repo_id, number, created_at;

-- name: ListPendingGithubOutboxByKey :many
SELECT * FROM github_outbox
WHERE delivered_at IS NULL AND thread_id = @thread_id AND repo_id = @repo_id AND number = @number
ORDER BY created_at, id;

-- name: MarkGithubOutboxDelivered :exec
UPDATE github_outbox SET delivered_at = @delivered_at WHERE id IN (sqlc.slice('ids'));

-- name: CountGithubOutboxSince :one
SELECT COUNT(*) FROM github_outbox
WHERE thread_id = @thread_id AND repo_id = @repo_id AND number = @number AND created_at >= @since;

-- name: DeleteGithubOutboxBefore :exec
DELETE FROM github_outbox WHERE delivered_at IS NOT NULL AND delivered_at < @before;
