-- M8: browser profiles, per-orb desktop mirror and profile binding, profile audit events.
-- Lease = browser_profiles.holder_orb_id/held_at; fence is bumped every time the lease changes hands
-- (NULL -> orb, or takeover). Saves must present the fence obtained with the lease.

-- name: CreateBrowserProfile :exec
INSERT INTO browser_profiles (id, name, created_at, updated_at) VALUES (@id, @name, @now, @now);

-- name: GetBrowserProfile :one
SELECT * FROM browser_profiles WHERE id = @id;

-- name: GetBrowserProfileByName :one
SELECT * FROM browser_profiles WHERE name = @name;

-- name: GetBrowserProfileByHolder :one
SELECT * FROM browser_profiles WHERE holder_orb_id = CAST(@orb_id AS TEXT);

-- name: ListBrowserProfiles :many
SELECT * FROM browser_profiles ORDER BY name, id;

-- name: CountBrowserProfiles :one
SELECT COUNT(*) FROM browser_profiles;

-- name: RenameBrowserProfile :execrows
UPDATE browser_profiles SET name = @name, updated_at = @now WHERE id = @id;

-- Only when nobody holds the exclusive lease (0 rows + profile exists = profile_in_use).
-- name: DeleteBrowserProfile :execrows
DELETE FROM browser_profiles WHERE id = @id AND holder_orb_id IS NULL;

-- Take the exclusive lease (idempotent for the current holder). Returns the fence the holder must
-- present when saving; sql.ErrNoRows = held by another orb. A new holder bumps the fence.
-- name: AcquireBrowserProfile :one
UPDATE browser_profiles
SET fence         = CASE WHEN holder_orb_id = CAST(@orb_id AS TEXT) THEN fence ELSE fence + 1 END,
    held_at       = CASE WHEN holder_orb_id = CAST(@orb_id AS TEXT) THEN held_at ELSE CAST(@now AS INTEGER) END,
    holder_orb_id = CAST(@orb_id AS TEXT)
WHERE id = @id AND (holder_orb_id IS NULL OR holder_orb_id = CAST(@orb_id AS TEXT))
RETURNING fence;

-- Takeover: move the lease to orb_id unconditionally, compare-and-swap on the fence read before the
-- pre-takeover save. Returns the new fence; sql.ErrNoRows = the lease changed meanwhile (re-read and retry).
-- name: StealBrowserProfile :one
UPDATE browser_profiles
SET holder_orb_id = CAST(@orb_id AS TEXT), held_at = CAST(@now AS INTEGER), fence = fence + 1,
    updated_at = CAST(@now AS INTEGER)
WHERE id = @id AND fence = @expected_fence
RETURNING fence;

-- Release whatever lease this orb holds (fence is kept; the next holder bumps it).
-- name: ReleaseBrowserProfileByOrb :execrows
UPDATE browser_profiles SET holder_orb_id = NULL, held_at = NULL WHERE holder_orb_id = CAST(@orb_id AS TEXT);

-- Commit a new revision; only the holder with the current fence may write.
-- sql.ErrNoRows = fenced (lease moved or re-acquired since): drop the new file, record profile.save_fenced.
-- name: SaveBrowserProfileRevision :one
UPDATE browser_profiles
SET revision = revision + 1, size_bytes = @size_bytes, content_sha256 = CAST(@content_sha256 AS TEXT),
    saved_at = CAST(@now AS INTEGER), save_error = NULL, updated_at = CAST(@now AS INTEGER)
WHERE id = @id AND holder_orb_id = CAST(@orb_id AS TEXT) AND fence = @fence
RETURNING revision;

-- name: SetBrowserProfileSaveError :exec
UPDATE browser_profiles SET save_error = sqlc.narg(save_error), updated_at = @now WHERE id = @id;

-- name: ClearProjectsBrowserProfile :exec
UPDATE projects SET default_browser_profile_id = NULL WHERE default_browser_profile_id = CAST(@profile_id AS TEXT);

-- name: InsertBrowserProfileEvent :exec
INSERT INTO browser_profile_events (profile_id, orb_id, ts, event, detail)
VALUES (@profile_id, sqlc.narg(orb_id), @ts, @event, @detail);

-- name: ListBrowserProfileEvents :many
SELECT * FROM browser_profile_events WHERE profile_id = @profile_id ORDER BY id DESC LIMIT @lim;

-- Representative thread of an orb (earliest created) for holder display.
-- name: GetOrbRepresentativeThread :one
SELECT id, title, created_at FROM threads WHERE orb_id = CAST(@orb_id AS TEXT) ORDER BY created_at, id LIMIT 1;

-- name: GetOrbDesktop :one
SELECT * FROM orb_desktops WHERE orb_id = @orb_id;

-- Bind a profile (or none) to an orb: resets local state, fence and any takeover record.
-- readonly_reason/readonly_at are set only for profile_mode = 'readonly' (reason 'requested').
-- name: UpsertOrbDesktopProfile :exec
INSERT INTO orb_desktops (orb_id, profile_id, profile_mode, takeover, readonly_reason, readonly_at, updated_at)
VALUES (@orb_id, sqlc.narg(profile_id), @profile_mode, @takeover, sqlc.narg(readonly_reason), sqlc.narg(readonly_at), @now)
ON CONFLICT (orb_id) DO UPDATE SET
    profile_id = excluded.profile_id, profile_mode = excluded.profile_mode, takeover = excluded.takeover,
    fence = 0, readonly_reason = excluded.readonly_reason, readonly_by = NULL, readonly_by_thread_id = NULL,
    readonly_at = excluded.readonly_at, local_rev = -1, profile_error = NULL, updated_at = excluded.updated_at;

-- Mirror of orbd's desktop.state (only the reported fields).
-- name: UpsertOrbDesktopState :exec
INSERT INTO orb_desktops (orb_id, reported, display_ready, browser, dirty, updated_at)
VALUES (@orb_id, 1, @display_ready, @browser, @dirty, @now)
ON CONFLICT (orb_id) DO UPDATE SET
    reported = 1, display_ready = excluded.display_ready, browser = excluded.browser,
    dirty = excluded.dirty, updated_at = excluded.updated_at;

-- Result of a sync/checkin: local revision, load error, fence held (0 when not holding); clears takeover.
-- name: SetOrbDesktopLocal :exec
UPDATE orb_desktops
SET local_rev = @local_rev, profile_error = sqlc.narg(profile_error), fence = @fence, takeover = 0, updated_at = @now
WHERE orb_id = @orb_id;

-- Demote the previous holder to a read-only user after a takeover (same transaction as StealBrowserProfile).
-- name: DemoteOrbDesktopReadonly :execrows
UPDATE orb_desktops
SET profile_mode = 'readonly', takeover = 0, fence = 0, readonly_reason = 'taken_over',
    readonly_by = sqlc.narg(readonly_by), readonly_by_thread_id = sqlc.narg(readonly_by_thread_id),
    readonly_at = CAST(@now AS INTEGER), updated_at = CAST(@now AS INTEGER)
WHERE orb_id = @orb_id AND profile_id = CAST(@profile_id AS TEXT);

-- Read-only users of profiles (all profiles when profile_id is NULL), excluding deleted orbs.
-- name: ListBrowserProfileReaders :many
SELECT d.orb_id, d.profile_id, d.readonly_reason, d.readonly_at
FROM orb_desktops d JOIN orbs o ON o.id = d.orb_id
WHERE d.profile_mode = 'readonly' AND d.profile_id IS NOT NULL
  AND (CAST(sqlc.narg(profile_id) AS TEXT) IS NULL OR d.profile_id = CAST(sqlc.narg(profile_id) AS TEXT))
  AND o.state NOT IN ('deleting', 'deleted')
ORDER BY d.profile_id, d.readonly_at, d.orb_id;

-- name: DeleteOrbDesktop :exec
DELETE FROM orb_desktops WHERE orb_id = @orb_id;
