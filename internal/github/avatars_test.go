package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
)

func TestCommitAvatars(t *testing.T) {
	e := newEnv(t)
	p := e.boundProject()
	e.fake.SetCommitAuthors(shaA, []githubtest.CommitAuthor{
		{Email: "fox@ling.plus", Name: "Wine Fox", Login: "WineFoxDev", AvatarURL: "https://avatars.githubusercontent.com/u/2?v=4"},
		// 同一人换了大小写（作者与 Co-authored-by 都写了）只留第一个
		{Email: "FOX@ling.plus", Name: "Wine Fox", Login: "WineFoxDev", AvatarURL: "https://avatars.githubusercontent.com/u/2?v=4"},
		// 对不上账号、头像不是 https 的跳过
		{Email: "nobody@example.com", Name: "Nobody"},
		{Email: "odd@example.com", Name: "Odd", Login: "odd", AvatarURL: "http://evil.test/a.png"},
	})

	got, err := e.s.CommitAvatars(e.ctx, p, shaA)
	require.NoError(t, err)
	assert.Equal(t, []apigen.ProjectRepoAvatar{{Email: "fox@ling.plus", Login: "WineFoxDev", AvatarURL: "https://avatars.githubusercontent.com/u/2?v=4"}}, got)

	// GitHub 上没有这个提交：空，不报错
	got, err = e.s.CommitAvatars(e.ctx, p, shaB)
	require.NoError(t, err)
	assert.Empty(t, got)

	// 仓库不在 github.com：不发请求
	before := len(e.fake.CallsTo("POST", "/graphql"))
	got, err = e.s.CommitAvatars(e.ctx, e.project("https://gitlab.com/acme/widgets"), shaA)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Len(t, e.fake.CallsTo("POST", "/graphql"), before)
}
