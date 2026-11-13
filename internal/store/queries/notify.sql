-- M5: notification channels, push subscriptions, notifications and the delivery queue.

-- name: InsertNotificationChannel :exec
INSERT INTO notification_channels (id, kind, name, enabled, events, locale, config_enc, state, created_at, updated_at)
VALUES (@id, @kind, @name, @enabled, @events, @locale, @config_enc, @state, @created_at, @updated_at);

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE id = @id;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels ORDER BY created_at, id;

-- name: UpdateNotificationChannel :exec
UPDATE notification_channels
SET name = @name, enabled = @enabled, events = @events, locale = @locale, config_enc = @config_enc, updated_at = @updated_at
WHERE id = @id;

-- Non-secret runtime state (telegram: botUsername, chatId, chatTitle, offset, linkCode, linkExpires).
-- name: SetNotificationChannelState :exec
UPDATE notification_channels SET state = @state WHERE id = @id;

-- NULL clears the error (last delivery or test succeeded).
-- name: SetNotificationChannelError :exec
UPDATE notification_channels SET last_error = @last_error WHERE id = @id;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE id = @id;

-- name: CountNotificationChannelsByKind :one
SELECT COUNT(*) FROM notification_channels WHERE kind = @kind;

-- Upsert by endpoint: an existing row keeps its id and created_at, takes the new keys and owner, clears last_error.
-- name: UpsertPushSubscription :one
INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, web_session_hash, device_id, user_agent, created_at)
VALUES (@id, @kind, @endpoint, @p256dh, @auth, @web_session_hash, @device_id, @user_agent, @created_at)
ON CONFLICT (endpoint) DO UPDATE SET
    p256dh           = excluded.p256dh,
    auth             = excluded.auth,
    web_session_hash = excluded.web_session_hash,
    device_id        = excluded.device_id,
    user_agent       = excluded.user_agent,
    last_error       = NULL
RETURNING *;

-- name: GetPushSubscriptionByEndpoint :one
SELECT * FROM push_subscriptions WHERE endpoint = @endpoint;

-- name: ListPushSubscriptions :many
SELECT * FROM push_subscriptions ORDER BY created_at, id;

-- Subscriptions whose owning device (if any) is not revoked.
-- name: ListDeliverablePushSubscriptions :many
SELECT s.* FROM push_subscriptions s
LEFT JOIN devices d ON d.id = s.device_id
WHERE s.device_id IS NULL OR d.revoked_at IS NULL
ORDER BY s.created_at, s.id;

-- One subscription, only if its owning device (if any) is not revoked (re-checked right before each delivery).
-- name: GetDeliverablePushSubscription :one
SELECT s.* FROM push_subscriptions s
LEFT JOIN devices d ON d.id = s.device_id
WHERE s.id = @id AND (s.device_id IS NULL OR d.revoked_at IS NULL);

-- name: DeletePushSubscription :execrows
DELETE FROM push_subscriptions WHERE id = @id;

-- Success: last_success_at = now, last_error = NULL. Failure: last_success_at = NULL (kept), last_error = message.
-- name: SetPushSubscriptionResult :exec
UPDATE push_subscriptions
SET last_success_at = COALESCE(CAST(sqlc.narg(last_success_at) AS INTEGER), last_success_at),
    last_error      = CAST(sqlc.narg(last_error) AS TEXT)
WHERE id = @id;

-- Returns 0 rows affected when an open notification for the same (thread_id, request_id) exists.
-- name: InsertNotification :execrows
INSERT INTO notifications (id, thread_id, kind, title, body, request_id, request, created_at)
VALUES (@id, @thread_id, @kind, @title, @body, @request_id, @request, @created_at)
ON CONFLICT DO NOTHING;

-- name: GetNotification :one
SELECT * FROM notifications WHERE id = @id;

-- name: ListNotifications :many
SELECT * FROM notifications WHERE created_at < @before ORDER BY created_at DESC, id DESC LIMIT @limit;

-- name: CountUnreadNotifications :one
SELECT COUNT(*) FROM notifications WHERE read_at IS NULL;

-- name: ListUnreadNotificationThreads :many
SELECT DISTINCT CAST(thread_id AS TEXT) AS thread_id FROM notifications
WHERE read_at IS NULL AND thread_id IS NOT NULL
ORDER BY thread_id;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications SET read_at = CAST(@now AS INTEGER) WHERE read_at IS NULL;

-- name: MarkThreadNotificationsRead :execrows
UPDATE notifications SET read_at = CAST(@now AS INTEGER) WHERE thread_id = CAST(@thread_id AS TEXT) AND read_at IS NULL;

-- name: ResolveNotificationRequest :many
UPDATE notifications SET resolved_at = CAST(@now AS INTEGER)
WHERE thread_id = CAST(@thread_id AS TEXT) AND request_id = CAST(@request_id AS TEXT) AND resolved_at IS NULL
RETURNING id;

-- name: DeleteNotificationsBefore :execrows
DELETE FROM notifications WHERE created_at < @before;

-- Newest main-session message entries first (notification body source).
-- name: ListRecentMainMessages :many
SELECT seq, payload FROM entries
WHERE thread_id = @thread_id AND type = 'message' AND instr(file, '/') = 0
ORDER BY seq DESC
LIMIT @limit;

-- name: InsertNotificationDelivery :exec
INSERT INTO notification_deliveries (id, notification_id, channel_id, subscription_id, target, status, attempts, next_attempt_at, created_at)
VALUES (@id, @notification_id, @channel_id, @subscription_id, @target, 'pending', 0, CAST(@next_attempt_at AS INTEGER), @created_at);

-- name: GetNotificationDelivery :one
SELECT * FROM notification_deliveries WHERE id = @id;

-- name: ListDueNotificationDeliveries :many
SELECT * FROM notification_deliveries
WHERE status = 'pending' AND next_attempt_at <= CAST(@now AS INTEGER)
ORDER BY next_attempt_at, id
LIMIT 50;

-- Earliest next_attempt_at among pending deliveries; 0 when none (delivery loop wake-up).
-- name: GetNextNotificationDeliveryAt :one
SELECT CAST(COALESCE(MIN(next_attempt_at), 0) AS INTEGER) AS next_at FROM notification_deliveries WHERE status = 'pending';

-- name: RescheduleNotificationDelivery :exec
UPDATE notification_deliveries
SET attempts = @attempts, next_attempt_at = CAST(@next_attempt_at AS INTEGER), last_error = @last_error
WHERE id = @id;

-- status is 'sent' or 'failed'.
-- name: FinishNotificationDelivery :exec
UPDATE notification_deliveries
SET status = @status, attempts = @attempts, last_error = @last_error, external_ref = @external_ref,
    finished_at = CAST(@finished_at AS INTEGER), next_attempt_at = NULL
WHERE id = @id;

-- name: ListDeliveriesByNotifications :many
SELECT * FROM notification_deliveries
WHERE notification_id IN (sqlc.slice('ids'))
ORDER BY created_at, id;

-- name: GetNotificationDeliveryByRef :one
SELECT * FROM notification_deliveries WHERE target = @target AND external_ref = CAST(@ref AS TEXT) ORDER BY created_at DESC LIMIT 1;

-- name: ListDeliveryRefsByNotification :many
SELECT * FROM notification_deliveries
WHERE notification_id = @notification_id AND external_ref IS NOT NULL
ORDER BY created_at, id;

-- name: GetLastDeliveryByChannel :one
SELECT * FROM notification_deliveries WHERE channel_id = CAST(@channel_id AS TEXT) ORDER BY created_at DESC, id DESC LIMIT 1;
