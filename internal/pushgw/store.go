package pushgw

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go）

	"github.com/Lynricsy/Oryn/internal/secrets"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DBFile 是 data_dir 下的数据库文件名。
const DBFile = "gateway.db"

// 保留期限。
const (
	// touchEvery：last_used_at 至多每小时写一次。
	touchEvery = time.Hour
	// idleRetention：连续 90 天未用的授权删除。
	idleRetention = 90 * 24 * time.Hour
	// revokedRetention：已吊销的授权 7 天后删除。
	revokedRetention = 7 * 24 * time.Hour
)

// Grant 是一条授权；Token 是解密后的推送令牌。
type Grant struct {
	ID, DeviceKey, HostKey string
	Channel, AppID         string
	// Environment 是 APNs 环境；FCM 为空串。
	Environment string
	Token       string
	CreatedAt   int64
	LastUsedAt  int64
	// RevokedAt 为 0 表示有效。
	RevokedAt int64
}

// Store 是授权表。连接池上限 1 条（单写者，见 internal/store 的并发说明）。
type Store struct {
	db  *sql.DB
	box *secrets.Box
}

// OpenStore 打开 <dataDir>/gateway.db 并执行全部未应用的迁移；推送令牌用 box 静态加密。
func OpenStore(ctx context.Context, dataDir string, box *secrets.Box, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, DBFile)
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	conn.SetConnMaxLifetime(0)
	conn.SetConnMaxIdleTime(0)
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	if err := migrate(ctx, conn, log); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Store{db: conn, box: box}, nil
}

// dsn 与控制面同一套参数：WAL、busy_timeout、BEGIN IMMEDIATE。
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

func migrate(ctx context.Context, conn *sql.DB, log *slog.Logger) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(database.DialectSQLite3, conn, sub)
	if err != nil {
		return fmt.Errorf("初始化迁移: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("执行迁移: %w", err)
	}
	for _, r := range results {
		log.Info("已应用数据库迁移", "version", r.Source.Version, "file", filepath.Base(r.Source.Path), "duration", r.Duration)
	}
	return nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// newID 是 128 位随机数的 base64url（22 字符）。
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sealName(id string) string { return "grant/" + id }

const grantColumns = `id, device_key, host_key, channel, app_id, environment, token_enc, created_at, last_used_at, revoked_at`

// Get 读取授权（含已吊销的）；不存在时返回 nil。
func (s *Store) Get(ctx context.Context, id string) (*Grant, error) {
	var (
		g       Grant
		enc     []byte
		revoked sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `SELECT `+grantColumns+` FROM grants WHERE id = ?`, id).
		Scan(&g.ID, &g.DeviceKey, &g.HostKey, &g.Channel, &g.AppID, &g.Environment, &enc, &g.CreatedAt, &g.LastUsedAt, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tok, err := s.box.Open(sealName(g.ID), enc)
	if err != nil {
		return nil, fmt.Errorf("解密授权 %s 的推送令牌: %w", g.ID, err)
	}
	g.Token, g.RevokedAt = string(tok), revoked.Int64
	return &g, nil
}

// Put 在一个事务里登记授权：吊销该设备密钥指向其它主机的有效授权；同一对（设备，主机）已有有效授权时
// 用新令牌覆盖并返回原 id 与原创建时间；否则新建。g 的 ID / CreatedAt / LastUsedAt 由本函数填写。
func (s *Store) Put(ctx context.Context, g Grant, now time.Time) (Grant, error) {
	ms := now.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return g, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`UPDATE grants SET revoked_at = ? WHERE device_key = ? AND host_key <> ? AND revoked_at IS NULL`,
		ms, g.DeviceKey, g.HostKey); err != nil {
		return g, err
	}
	err = tx.QueryRowContext(ctx,
		`SELECT id, created_at FROM grants WHERE device_key = ? AND host_key = ? AND revoked_at IS NULL`,
		g.DeviceKey, g.HostKey).Scan(&g.ID, &g.CreatedAt)
	existing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return g, err
	}
	if !existing {
		g.ID, g.CreatedAt = newID(), ms
	}
	g.LastUsedAt = ms
	enc, err := s.box.Seal(sealName(g.ID), []byte(g.Token))
	if err != nil {
		return g, err
	}
	if existing {
		_, err = tx.ExecContext(ctx,
			`UPDATE grants SET channel = ?, app_id = ?, environment = ?, token_enc = ?, last_used_at = ? WHERE id = ?`,
			g.Channel, g.AppID, g.Environment, enc, ms, g.ID)
	} else {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO grants (`+grantColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
			g.ID, g.DeviceKey, g.HostKey, g.Channel, g.AppID, g.Environment, enc, g.CreatedAt, ms)
	}
	if err != nil {
		return g, err
	}
	return g, tx.Commit()
}

// Revoke 吊销有效授权（已吊销的不变）。
func (s *Store) Revoke(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE grants SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now.UnixMilli(), id)
	return err
}

// Touch 在距上次记录满一小时时更新 last_used_at。
func (s *Store) Touch(ctx context.Context, g *Grant, now time.Time) error {
	if now.UnixMilli()-g.LastUsedAt < touchEvery.Milliseconds() {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE grants SET last_used_at = ? WHERE id = ?`, now.UnixMilli(), g.ID)
	return err
}

// Cleanup 删除连续 90 天未用的授权与吊销满 7 天的授权，返回删除条数。
func (s *Store) Cleanup(ctx context.Context, now time.Time) (int64, error) {
	ms := now.UnixMilli()
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM grants WHERE last_used_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`,
		ms-idleRetention.Milliseconds(), ms-revokedRetention.Milliseconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
