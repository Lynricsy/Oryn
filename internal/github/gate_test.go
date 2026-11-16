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

func tier(drivers string) driveRule { return driveRule{drivers: drivers} }

func TestCanDrive(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	e.fake.AddUser(20, "reader", "")
	e.fake.AddUser(21, "writer", "")
	e.setPerm("owner", "admin")
	e.setPerm("reader", "read")
	e.setPerm("writer", "write")
	const repo = "acme/widgets"
	can := func(login, typ, author, assoc string, r driveRule) (drive, error) {
		return e.s.canDrive(e.ctx, repo, sender{Login: login, Type: typ}, author, assoc, r)
	}

	d, err := can("reader", "User", "", "", tier(driversWriters))
	require.NoError(t, err)
	assert.False(t, d.ok)
	assert.Equal(t, "no_write_access", d.reason)

	e.fake.ResetCalls()
	for range 2 {
		d, err = can("Writer", "User", "", "NONE", tier(driversWriters))
		require.NoError(t, err)
		assert.True(t, d.ok)
		assert.Empty(t, d.reason)
	}
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 1, "权限结果缓存 permTTL")
	e.clock.Add(permTTL + time.Second)
	_, err = can("writer", "User", "", "", tier(driversWriters))
	require.NoError(t, err)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"), 2)

	// author_association 是 OWNER / MEMBER / COLLABORATOR 时直接放行，不查上游
	e.fake.ResetCalls()
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		d, err = can("stranger", "User", "", assoc, tier(driversWriters))
		require.NoError(t, err)
		assert.True(t, d.ok, assoc)
	}
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))

	// 未知用户（上游 404）也算没有写权限
	d, _ = can("ghost", "User", "", "", tier(driversWriters))
	assert.False(t, d.ok)
	assert.Equal(t, "no_write_access", d.reason)

	d, _ = can("dependabot[bot]", "Bot", "", "OWNER", tier(driversWriters))
	assert.False(t, d.ok)
	assert.Equal(t, "bot", d.reason)

	// 所有者本人（user 槽位）不查上游
	e.fake.ResetCalls()
	d, err = can("owner", "User", "", "", tier(driversWriters))
	require.NoError(t, err)
	assert.True(t, d.ok)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))

	// 规则维护者不查上游、不限流
	d, err = can("maint", "User", "", "NONE", driveRule{drivers: driversAnyone, maintainers: []string{"Maint"}})
	require.NoError(t, err)
	assert.Equal(t, drive{ok: true}, d)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/*"))

	// 上游 5xx：可重试的错误，不缓存
	e.fake.AddUser(22, "flaky", "")
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /repos/acme/widgets/collaborators/flaky/permission": 500}
	e.fake.Unlock()
	_, err = can("flaky", "User", "", "", tier(driversWriters))
	require.Error(t, err)
	assert.True(t, retryable(err))
}

// R13 矩阵：三档 × 发送者类别（OWNER / CONTRIBUTOR / NONE / 作者 / 评审 bot / 名单外的 bot）。
// contributor 与 stranger 在仓库上只有读权限（权限接口回 read），作者 author 的 association 是 NONE。
func TestCanDriveMatrix(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	for i, login := range []string{"boss", "contributor", "stranger", "author"} {
		e.fake.AddUser(int64(40+i), login, "")
	}
	e.setPerm("owner", "admin")
	e.setPerm("contributor", "read")
	e.setPerm("stranger", "read")
	e.setPerm("author", "read")
	bots := []string{"coderabbitai"}
	type who struct {
		name, login, typ, assoc string
	}
	senders := []who{
		{"OWNER", "boss", "User", "OWNER"},
		{"CONTRIBUTOR", "contributor", "User", "CONTRIBUTOR"},
		{"NONE", "stranger", "User", "NONE"},
		{"author", "author", "User", "NONE"},
		{"review bot", "coderabbitai[bot]", "Bot", "NONE"},
		{"other bot", "dependabot[bot]", "Bot", "NONE"},
	}
	// 期望：放行与否；anyone 档的每小时上限
	want := map[string]map[string]drive{
		driversWriters: {
			"OWNER": {ok: true}, "CONTRIBUTOR": {reason: "no_write_access"}, "NONE": {reason: "no_write_access"},
			"author": {reason: "no_write_access"}, "review bot": {ok: true}, "other bot": {reason: "bot"},
		},
		driversWritersAndAuthor: {
			"OWNER": {ok: true}, "CONTRIBUTOR": {reason: "no_write_access"}, "NONE": {reason: "no_write_access"},
			"author": {ok: true}, "review bot": {ok: true}, "other bot": {reason: "bot"},
		},
		driversAnyone: {
			"OWNER": {ok: true}, "CONTRIBUTOR": {ok: true, limit: contributorMaxPerHour}, "NONE": {ok: true, limit: anyoneMaxPerHour},
			"author": {ok: true, limit: anyoneMaxPerHour}, "review bot": {ok: true}, "other bot": {reason: "bot"},
		},
	}
	for _, drivers := range []string{driversWriters, driversWritersAndAuthor, driversAnyone} {
		r := driveRule{drivers: drivers, reviewerBots: bots}
		for _, w := range senders {
			d, err := e.s.canDrive(e.ctx, "acme/widgets", sender{Login: w.login, Type: w.typ}, "author", w.assoc, r)
			require.NoError(t, err)
			assert.Equal(t, want[drivers][w.name], d, "%s × %s", drivers, w.name)
		}
	}
	// 与评审 bot 同名（去掉 [bot]）的人类账号不算评审 bot
	d, err := e.s.canDrive(e.ctx, "acme/widgets", sender{Login: "coderabbitai[bot]", Type: "User"}, "", "NONE",
		driveRule{drivers: driversWriters, reviewerBots: []string{"coderabbitai"}})
	require.NoError(t, err)
	assert.False(t, d.ok)
}

// issues.labeled 的发送者判定与档位无关：仓库 owner、规则维护者、我的账号、权限接口返回 write / admin 的人。
func TestCanLabel(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	e.fake.AddUser(21, "writer", "")
	e.fake.AddUser(22, "reader", "")
	e.setPerm("owner", "admin")
	e.setPerm("writer", "write")
	e.setPerm("reader", "read")
	r := driveRule{drivers: driversAnyone, maintainers: []string{"maint"}}
	for login, ok := range map[string]bool{"acme": true, "maint": true, "owner": true, "writer": true, "reader": false} {
		d, err := e.s.canLabel(e.ctx, "acme/widgets", sender{Login: login, Type: "User"}, r)
		require.NoError(t, err)
		assert.Equal(t, ok, d.ok, login)
		assert.Zero(t, d.limit, login)
	}
	d, err := e.s.canLabel(e.ctx, "acme/widgets", sender{Login: "maint", Type: "Bot"}, r)
	require.NoError(t, err)
	assert.Equal(t, "bot", d.reason)
}

// 我的账号没有 push 权限时不调权限接口（它必回 403），一律判为非写权限者。
func TestCanDriveWithoutPush(t *testing.T) {
	e := newEnv(t)
	e.boundProject()
	e.fake.AddUser(21, "writer", "")
	e.setPerm("owner", "read")
	e.setPerm("writer", "write")
	e.fake.ResetCalls()
	d, err := e.s.canDrive(e.ctx, "acme/widgets", sender{Login: "writer", Type: "User"}, "", "", tier(driversWriters))
	require.NoError(t, err)
	assert.False(t, d.ok)
	assert.Equal(t, "no_write_access", d.reason)
	assert.Len(t, e.fake.CallsTo("GET", "/repos/acme/widgets"), 1)
	assert.Empty(t, e.fake.CallsTo("GET", "/repos/acme/widgets/collaborators/*"))

	// 权限接口回 403（push 判断过期之后权限被收回）：判否、不报错
	e.setPerm("owner", "write")
	e.clock.Add(permTTL + time.Second)
	e.fake.Lock()
	e.fake.Fail = map[string]int{"GET /repos/acme/widgets/collaborators/writer/permission": 403}
	e.fake.Unlock()
	d, err = e.s.canDrive(e.ctx, "acme/widgets", sender{Login: "writer", Type: "User"}, "", "", tier(driversWriters))
	require.NoError(t, err)
	assert.False(t, d.ok)
}
