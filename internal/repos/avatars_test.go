package repos

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestCommitAvatarsCache(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	fox := apigen.ProjectRepoAvatar{Email: "fox@ling.plus", Login: "WineFoxDev", AvatarURL: "https://avatars.githubusercontent.com/u/2?v=4"}
	calls := &atomic.Int32{}
	e := newTestEnv(t, nil, nil, fakeGitHub{avatars: map[string][]apigen.ProjectRepoAvatar{sha: {fox}}, avatarCalls: calls})
	pid := e.project(t, widgetsURL)
	ctx := context.Background()

	got, err := e.svc.CommitAvatars(ctx, pid, sha)
	require.NoError(t, err)
	assert.Equal(t, []apigen.ProjectRepoAvatar{fox}, got.Avatars)
	_, err = e.svc.CommitAvatars(ctx, pid, sha)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load(), "缓存命中不再查 GitHub")
	e.clock.Advance(avatarTTL + time.Minute)
	_, err = e.svc.CommitAvatars(ctx, pid, sha)
	require.NoError(t, err)
	assert.EqualValues(t, 2, calls.Load(), "过期后重查")

	_, err = e.svc.CommitAvatars(ctx, pid, "0123456")
	assert.Equal(t, "invalid_request", errCode(err), "短 sha 拒绝")
	_, err = e.svc.CommitAvatars(ctx, "prj_missing", sha)
	assert.Equal(t, "not_found", errCode(err))
}

func TestCommitAvatarsFailureIsEmptyAndCachedBriefly(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	calls := &atomic.Int32{}
	e := newTestEnv(t, nil, nil, fakeGitHub{avatarErr: errors.New("没有连接 GitHub 身份"), avatarCalls: calls})
	pid := e.project(t, widgetsURL)
	ctx := context.Background()

	got, err := e.svc.CommitAvatars(ctx, pid, sha)
	require.NoError(t, err, "查询失败不报错")
	assert.Empty(t, got.Avatars)
	assert.NotNil(t, got.Avatars, "JSON 里是 []")
	_, _ = e.svc.CommitAvatars(ctx, pid, sha)
	assert.EqualValues(t, 1, calls.Load(), "失败结果短时缓存")
	e.clock.Advance(avatarFailTTL + time.Minute)
	_, _ = e.svc.CommitAvatars(ctx, pid, sha)
	assert.EqualValues(t, 2, calls.Load())
}
