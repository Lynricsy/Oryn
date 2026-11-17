package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/coord"
	"github.com/Lynricsy/Oryn/internal/store"
)

// ProjectOps：全局助手的项目操作走 REST 同一套校验，带状态的错误转成同码的 *coord.Error。
func TestProjectOps(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ops := api.ProjectOps{S: &api.Server{Deps: api.Deps{Store: st}}}
	code := func(err error) string {
		t.Helper()
		var ce *coord.Error
		require.True(t, errors.As(err, &ce), "%v", err)
		return ce.Code
	}

	p, err := ops.Create(ctx, apigen.ProjectCreate{Name: " Beta ", RepoURL: new("https://example.com/b.git"), WarmOrbs: new(1)})
	require.NoError(t, err)
	assert.Equal(t, "Beta", p.Name)
	assert.Equal(t, 1, p.WarmOrbs)
	_, err = ops.Create(ctx, apigen.ProjectCreate{Name: "Beta"})
	assert.Equal(t, "conflict", code(err))
	_, err = ops.Create(ctx, apigen.ProjectCreate{Name: " "})
	assert.Equal(t, "invalid_request", code(err))
	_, err = ops.Create(ctx, apigen.ProjectCreate{Name: "Gamma", WarmOrbs: new(9)})
	assert.Equal(t, "invalid_request", code(err))

	p, err = ops.Update(ctx, p.ID, apigen.ProjectPatch{Archived: new(true), SetupScript: new("make")})
	require.NoError(t, err)
	assert.NotNil(t, p.ArchivedAt)
	assert.Equal(t, "make", *p.SetupScript)
	_, err = ops.Update(ctx, "prj_nope", apigen.ProjectPatch{Name: new("x")})
	assert.Equal(t, "not_found", code(err))

	require.NoError(t, ops.Delete(ctx, p.ID))
	assert.Equal(t, "not_found", code(ops.Delete(ctx, p.ID)))
}
