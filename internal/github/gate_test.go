package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPerm 设置 Acme/Widgets 上某用户的协作者权限。
func (e *testEnv) setPerm(login, perm string) {
	e.fake.Lock()
	defer e.fake.Unlock()
	for _, r := range e.fake.Repos {
		if r.ID == 42 {
			if r.Permissions == nil {
				r.Permissions = map[string]string{}
			}
			r.Permissions[login] = perm
		}
	}
}

func TestCanDrive(t *testing.T) {
	e := newEnv(t)
	prj := e.getProject(e.boundProject())
	e.fake.AddUser(20, "reader", "")
	e.fake.AddUser(21, "writer", "")
	e.setPerm("reader", "read")
	e.setPerm("writer", "write")

	ok, reason, err := e.s.canDrive(e.ctx, prj, sender{Login: "reader", Type: "User"})
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "no_write_access", reason)

	e.fake.ResetCalls()
	for range 2 {
		ok, reason, err = e.s.canDrive(e.ctx, prj, sender{Login: "Writer", Type: "User"})
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Empty(t, reason)
	}
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 1)
	e.clock.Add(permTTL + time.Second)
	_, _, err = e.s.canDrive(e.ctx, prj, sender{Login: "writer", Type: "User"})
	require.NoError(t, err)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 2)

	// 未知用户（上游 404）也算没有写权限
	ok, reason, _ = e.s.canDrive(e.ctx, prj, sender{Login: "ghost", Type: "User"})
	assert.False(t, ok)
	assert.Equal(t, "no_write_access", reason)

	ok, reason, _ = e.s.canDrive(e.ctx, prj, sender{Login: "dependabot[bot]", Type: "Bot"})
	assert.False(t, ok)
	assert.Equal(t, "bot", reason)

	// 所有者本人（user 槽位）不查上游
	e.fake.ResetCalls()
	ok, _, err = e.s.canDrive(e.ctx, prj, sender{Login: "owner", Type: "User"})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"))

	// 上游 5xx：可重试的错误，不缓存
	e.fake.AddUser(22, "flaky", "")
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /repos/acme/widgets/collaborators/flaky/permission": 500}
	e.fake.Unlock()
	_, _, err = e.s.canDrive(e.ctx, prj, sender{Login: "flaky", Type: "User"})
	require.Error(t, err)
	assert.True(t, retryable(err))
}

func TestIsSelf(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	assert.False(t, e.s.isSelf(e.ctx, sender{Login: "oryn-bot"}))
	require.NoError(t, e.connect("agent", "c2", "oryn-bot"))
	assert.True(t, e.s.isSelf(e.ctx, sender{Login: "Oryn-Bot"}))
	assert.False(t, e.s.isSelf(e.ctx, sender{Login: "owner"}))
	assert.False(t, e.s.isSelf(e.ctx, sender{}))
}
