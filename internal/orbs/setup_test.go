package orbs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// TestPlanGitIdentityPrecedence：项目覆盖（两项齐全）> 所有者默认 > 不下发；半套项目覆盖被忽略。
func TestPlanGitIdentityPrecedence(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	d := &DefaultSetup{Store: st}

	project := func(name string, gitName, gitEmail *string) *string {
		id := ids.New("prj")
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{
			ID: id, Name: name, GitAuthorName: gitName, GitAuthorEmail: gitEmail, CreatedAt: 1, UpdatedAt: 1,
		}))
		return &id
	}
	full := project("full", new("Project Bot"), new("bot@project.example"))
	half := project("half", new("Only Name"), nil)
	plain := project("plain", nil, nil)
	plan := func(projectID *string) *apigen.GitIdentity {
		t.Helper()
		p, err := d.Plan(ctx, db.Orb{ID: "orb-x", ProjectID: projectID})
		require.NoError(t, err)
		return p.Params.GitIdentity
	}

	// 尚无所有者：只有项目覆盖生效。
	assert.Nil(t, plan(nil))
	assert.Nil(t, plan(plain))
	assert.Nil(t, plan(half))
	assert.Equal(t, &apigen.GitIdentity{Name: "Project Bot", Email: "bot@project.example"}, plan(full))

	require.NoError(t, st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	assert.Nil(t, plan(nil), "所有者未设置默认值")

	n, err := st.Q.SetOwnerGitIdentity(ctx, db.SetOwnerGitIdentityParams{GitName: new("Owner"), GitEmail: new("owner@example.com")})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	owner := &apigen.GitIdentity{Name: "Owner", Email: "owner@example.com"}
	assert.Equal(t, owner, plan(nil))
	assert.Equal(t, owner, plan(plain))
	assert.Equal(t, owner, plan(half), "半套项目覆盖不生效，回落到所有者默认")
	assert.Equal(t, &apigen.GitIdentity{Name: "Project Bot", Email: "bot@project.example"}, plan(full))
}
