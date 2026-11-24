package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestM28MigrationDownUp 覆盖 0058：Up 删掉 fcm 订阅连同其投递、只留 webpush，kind 只接受 webpush / gateway；
// Down 删掉 gateway 订阅后回到 0057 的 CHECK，再 Up 不丢 webpush。
func TestM28MigrationDownUp(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	exec := func(q string) {
		t.Helper()
		_, err := s.DB.ExecContext(ctx, q)
		require.NoError(t, err)
	}
	count := func(q string) int { return queryInt(ctx, t, s, q) }

	downTo(ctx, t, p, 57)
	exec(`INSERT INTO notifications (id, kind, title, created_at) VALUES ('ntf_1', 'done', 't', 1)`)
	exec(`INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, created_at) VALUES ('psub_w', 'webpush', 'https://push.example/w', 'p', 'a', 1)`)
	exec(`INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, created_at) VALUES ('psub_f', 'fcm', 'fcm-token', 'p', 'a', 1)`)
	exec(`INSERT INTO notification_deliveries (id, notification_id, subscription_id, target, status, created_at) VALUES ('ndl_w', 'ntf_1', 'psub_w', 'webpush', 'sent', 1)`)
	exec(`INSERT INTO notification_deliveries (id, notification_id, subscription_id, target, status, created_at) VALUES ('ndl_f', 'ntf_1', 'psub_f', 'fcm', 'pending', 1)`)

	_, err = p.UpTo(ctx, 58)
	require.NoError(t, err)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM push_subscriptions WHERE id = 'psub_w' AND kind = 'webpush'`))
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM push_subscriptions`), "fcm 订阅已删除")
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM notification_deliveries WHERE id = 'ndl_w' AND subscription_id = 'psub_w'`))
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM notification_deliveries`), "fcm 订阅的投递随之删除")
	assert.Equal(t, 4, count(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'notification_deliveries' AND name LIKE 'notification_deliveries_%'`))

	exec(`INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, created_at) VALUES ('psub_g', 'gateway', 'https://push.oryn.best/v1/grants/AAAAAAAAAAAAAAAAAAAAAA', 'p', 'a', 2)`)
	exec(`INSERT INTO notification_deliveries (id, notification_id, subscription_id, target, status, created_at) VALUES ('ndl_g', 'ntf_1', 'psub_g', 'gateway', 'sent', 2)`)
	_, err = s.DB.ExecContext(ctx, `INSERT INTO push_subscriptions (id, kind, endpoint, created_at) VALUES ('psub_f2', 'fcm', 'fcm-token-2', 2)`)
	assert.ErrorContains(t, err, "CHECK", "fcm 不再是合法的 kind")
	// 外键仍在：删除订阅级联删除投递
	exec(`DELETE FROM push_subscriptions WHERE id = 'psub_g'`)
	assert.Zero(t, count(`SELECT COUNT(*) FROM notification_deliveries WHERE id = 'ndl_g'`))
	exec(`INSERT INTO push_subscriptions (id, kind, endpoint, p256dh, auth, created_at) VALUES ('psub_g', 'gateway', 'https://push.oryn.best/v1/grants/AAAAAAAAAAAAAAAAAAAAAA', 'p', 'a', 2)`)
	exec(`INSERT INTO notification_deliveries (id, notification_id, subscription_id, target, status, created_at) VALUES ('ndl_g', 'ntf_1', 'psub_g', 'gateway', 'sent', 2)`)

	downTo(ctx, t, p, 57)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM push_subscriptions`), "Down 只保留 webpush")
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM push_subscriptions WHERE id = 'psub_w'`))
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM notification_deliveries`))
	exec(`INSERT INTO push_subscriptions (id, kind, endpoint, created_at) VALUES ('psub_f3', 'fcm', 'fcm-token-3', 3)`)
	exec(`DELETE FROM push_subscriptions WHERE id = 'psub_f3'`)

	_, err = p.Up(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM push_subscriptions WHERE id = 'psub_w' AND kind = 'webpush'`), "Down 再 Up 不丢 webpush")
	assert.Equal(t, 1, count(`SELECT COUNT(*) FROM notification_deliveries WHERE id = 'ndl_w'`))
}
