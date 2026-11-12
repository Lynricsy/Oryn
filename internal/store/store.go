// Package store 管理控制面的 SQLite 数据库：打开、迁移与事务。
//
// 查询由 sqlc 从 queries/*.sql 生成到 db 包；迁移文件在 migrations/（goose 格式），
// 启动时自动执行。
//
// 注意：queries/*.sql 中只能写 ASCII（注释用英文）。sqlc v1.31 的 sqlite 引擎在
// 改写命名参数（@name / sqlc.arg）时按字节与字符混算偏移，文件中出现多字节字符
// 会让生成的 SQL 错位。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go）

	"github.com/Lynricsy/Oryn/internal/store/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// FileName 是 data_dir 下数据库文件的名称。
const FileName = "oryn.db"

// Store 持有数据库连接与生成的查询。
//
// 并发模型：连接池上限为 1 条连接（单写者）。SQLite 同一时刻只允许一个写事务，
// 进程内多连接并发写会互相等锁甚至在「读升级为写」时直接返回 SQLITE_BUSY；
// 把进程内全部访问串行到一条连接上即可彻底避免，而本控制面的查询都很短，
// 串行化的代价可忽略。跨进程（如 `orynd admin create-device`、sqlite3 CLI）
// 的竞争由 WAL + busy_timeout=5000 + `BEGIN IMMEDIATE`（_txlock=immediate）处理。
//
// 由此带来的约束：在 Tx 回调内必须只使用回调传入的 *db.Queries，
// 不得再调用 s.Q 或 s.DB，否则会因等待唯一的连接而死锁。
type Store struct {
	DB *sql.DB
	Q  *db.Queries
}

// Open 创建 dataDir（0700），打开 <dataDir>/oryn.db 并执行全部未应用的迁移。
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, FileName)
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
	if err := migrate(ctx, conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Store{DB: conn, Q: db.New(conn)}, nil
}

// dsn 拼出 modernc sqlite 的连接串；_pragma 在每条新连接上执行。
func dsn(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

func migrate(ctx context.Context, conn *sql.DB) error {
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
		slog.Info("已应用数据库迁移", "version", r.Source.Version, "file", filepath.Base(r.Source.Path), "duration", r.Duration)
	}
	return nil
}

// Tx 在一个写事务（BEGIN IMMEDIATE）中执行 fn；fn 返回错误或 panic 时回滚。
// fn 内只能使用传入的 q（见 Store 的并发说明）。
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.DB.Close() }
