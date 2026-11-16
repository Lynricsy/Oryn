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
	e.boundProject()
	e.fake.AddUser(20, "reader", "")
	e.fake.AddUser(21, "writer", "")
	e.setPerm("owner", "admin")
	e.setPerm("reader", "read")
	e.setPerm("writer", "write")
	const repo = "acme/widgets"
	can := func(login, typ, author, assoc, drivers string) (bool, string, error) {
		return e.s.canDrive(e.ctx, repo, sender{Login: login, Type: typ}, author, assoc, drivers)
	}

	ok, reason, err := can("reader", "User", "", "", driversWriters)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "no_write_access", reason)

	e.fake.ResetCalls()
	for range 2 {
		ok, reason, err = can("Writer", "User", "", "NONE", driversWriters)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Empty(t, reason)
	}
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 1, "权限结果缓存 permTTL")
	e.clock.Add(permTTL + time.Second)
	_, _, err = can("writer", "User", "", "", driversWriters)
	require.NoError(t, err)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 2)

	// author_association 是 OWNER / MEMBER / COLLABORATOR 时直接放行，不查上游
	e.fake.ResetCalls()
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		ok, _, err = can("stranger", "User", "", assoc, driversWriters)
		require.NoError(t, err)
		assert.True(t, ok, assoc)
	}
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))

	// 作者：writers 档不放行，writers_and_author 档放行
	ok, _, _ = can("reader", "User", "Reader", "CONTRIBUTOR", driversWriters)
	assert.False(t, ok)
	ok, _, _ = can("reader", "User", "Reader", "CONTRIBUTOR", driversWritersAndAuthor)
	assert.True(t, ok)

	// 未知用户（上游 404）也算没有写权限
	ok, reason, _ = can("ghost", "User", "", "", driversWriters)
	assert.False(t, ok)
	assert.Equal(t, "no_write_access", reason)

	ok, reason, _ = can("dependabot[bot]", "Bot", "", "OWNER", driversWriters)
	assert.False(t, ok)
	assert.Equal(t, "bot", reason)

	// 所有者本人（user 槽位）不查上游
	e.fake.ResetCalls()
	ok, _, err = can("owner", "User", "", "", driversWriters)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))

	// 上游 5xx：可重试的错误，不缓存
	e.fake.AddUser(22, "flaky", "")
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /repos/acme/widgets/collaborators/flaky/permission": 500}
	e.fake.Unlock()
	_, _, err = can("flaky", "User", "", "", driversWriters)
	require.Error(t, err)
	assert.True(t, retryable(err))
}

// 我的账号没有 push 权限时不调权限接口（它必回 403），一律判为非写权限者。
func TestCanDriveWithoutPush(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	e.fake.AddUser(21, "writer", "")
	e.setPerm("owner", "read")
	e.setPerm("writer", "write")
	e.fake.ResetCalls()
	ok, reason, err := e.s.canDrive(e.ctx, "acme/widgets", sender{Login: "writer", Type: "User"}, "", "", driversWriters)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "no_write_access", reason)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets"), 1)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"))

	// 权限接口回 403（push 判断过期之后权限被收回）：判否、不报错
	e.setPerm("owner", "write")
	e.clock.Add(permTTL + time.Second)
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /repos/acme/widgets/collaborators/writer/permission": 403}
	e.fake.Unlock()
	ok, _, err = e.s.canDrive(e.ctx, "acme/widgets", sender{Login: "writer", Type: "User"}, "", "", driversWriters)
	require.NoError(t, err)
	assert.False(t, ok)
}
