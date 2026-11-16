package store

import (
	"context"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

// downTo 回滚比 version 新的全部迁移（version 本身保留）。各里程碑的迁移测试从「当时的最新版本」起逐个回滚，
// 之后新增的迁移先由它退掉，那些测试就不必随每个新迁移改写；新迁移自己的 Down / Up 在自己的测试里覆盖。
func downTo(ctx context.Context, t *testing.T, p *goose.Provider, version int64) {
	t.Helper()
	_, err := p.DownTo(ctx, version)
	require.NoError(t, err)
}

// latestVersion 是迁移目录里的最大版本号。
func latestVersion(t *testing.T, p *goose.Provider) int64 {
	t.Helper()
	srcs := p.ListSources()
	require.NotEmpty(t, srcs)
	return srcs[len(srcs)-1].Version
}
