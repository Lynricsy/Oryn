package store

import (
	"context"
	"io/fs"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sec F1：0018 的库里已有入库校验之前写下的越界事实行（同一汇总键下两行 input / ttft = 5e18：SUM 会抛
// integer overflow）。升级到 0019 及之后不失败：回填用 TOTAL，过大的整数和饱和到 MaxInt64。
func TestM10RollupMigrationOverflow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t, t.TempDir())
	sub, err := fs.Sub(migrationsFS, "migrations")
	require.NoError(t, err)
	p, err := goose.NewProvider(database.DialectSQLite3, s.DB, sub)
	require.NoError(t, err)
	_, err = p.DownTo(ctx, 18)
	require.NoError(t, err)

	require.NoError(t, s.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: "T-1", TitleSource: "auto", State: "idle", SessionDir: "/a", CreatedAt: 1, UpdatedAt: 1}))
	_, err = s.DB.ExecContext(ctx, `INSERT INTO usage_records (thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey,
		input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent) VALUES
		('T-1', 1, 1000, 'p', 'm', 'x', 'stop', 'a@1', 5000000000000000000, 5000000000000000000, 0, 0, 1.7e308, 5000000000000000000, 5000000000000000000, 1),
		('T-1', 2, 2000, 'p', 'm', 'x', 'stop', 'a@2', 5000000000000000000, 5000000000000000000, 0, 0, 1.7e308, 5000000000000000000, 5000000000000000000, 1)`)
	require.NoError(t, err)
	var n int64
	require.ErrorContains(t, s.DB.QueryRowContext(ctx, `SELECT SUM(input_tokens) FROM usage_records`).Scan(&n), "integer overflow",
		"前提：这组数据让 SUM 抛 integer overflow")

	_, err = p.Up(ctx)
	require.NoError(t, err, "0019 回填不因越界行失败")
	v, err := p.GetDBVersion(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 20, v)
	// q, thread, project, space, provider, model, purpose, requests, errors, in, out, cacheR, cacheW, cost, subCost,
	// ttftSum, ttftN, timedOut, durMs
	assert.Equal(t, []string{"0|T-1|||p|m|x|2|0|9223372036854775807|9223372036854775807|0|0|+Inf|+Inf|9223372036854775807|2|9223372036854775807|9223372036854775807"},
		dumpRows(t, s.DB, `SELECT * FROM usage_q`))
}
