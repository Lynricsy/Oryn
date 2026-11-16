package artifacts

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/share"
)

const secretPage = `<script>const key = "sk-test-abcdefghijklmnopqrstuvwx0123";</script>`

// newPublishEnv 是打开了发布（主域回退模式、真实的密钥扫描规则）的测试环境。
func newPublishEnv(t *testing.T, domain string) *env {
	t.Helper()
	e := newEnv(t)
	base, err := portalhost.NewBase("http://localhost:7600", domain)
	require.NoError(t, err)
	e.s.ConfigurePublishing(PublishConfig{
		Base: base, GrantKey: DeriveGrantKey([]byte("master")), Scan: (&share.Service{}).ScanContent,
		Visitors: func(slug string) int { return len(slug) },
	})
	return e
}

func pubPage(t *testing.T, e *env, html string) Artifact {
	t.Helper()
	a, err := e.s.Create(context.Background(), CreateParams{Title: "页面", Files: []FileChange{put("index.html", html)}})
	require.NoError(t, err)
	return a
}

func TestPublishScanSecrets(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, secretPage)

	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	var se *ScanError
	require.ErrorAs(t, err, &se)
	require.NotEmpty(t, se.Scan.Findings)
	assert.Equal(t, apigen.ShareFindingRuleAPIKeySk, se.Scan.Findings[0].Rule)
	assert.Equal(t, "index.html", se.Scan.Findings[0].EntryID)
	p, err := e.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, p, "被拒的发布不落库")

	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode})
	require.ErrorAs(t, err, &se, "code 同样扫描")

	pub, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic, AcknowledgeFindings: true})
	require.NoError(t, err)
	assert.Equal(t, AccessPublic, pub.Access)
	assert.Equal(t, 1, pub.Version)
	assert.True(t, pub.Follow)
	assert.Regexp(t, `^http://localhost:7600/p/[a-z2-7]{26}/$`, pub.URL)
	assert.Equal(t, 26, pub.Visitors, "访客数来自注入的函数")
	assert.Equal(t, "public", string(*publishedOf(t, e, a.ID)))

	// 同一版本在 code / public 之间切换不再扫描；改回 private 再公开要重新扫描
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode})
	require.NoError(t, err)
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	require.ErrorAs(t, err, &se)
}

func TestPublishPrivateSkipsScan(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, secretPage)
	pub, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)
	assert.Equal(t, AccessPrivate, pub.Access)
	assert.Empty(t, pub.Code)
	assert.Nil(t, pub.API().Code)
	usage, err := e.s.PublicationUsage(ctx)
	require.NoError(t, err)
	assert.Equal(t, a.Size, usage)
}

func TestPublishVersionFollowAndURL(t *testing.T) {
	e := newPublishEnv(t, "localhost")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>v1</h1>")
	_, err := e.s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: 1, Files: []FileChange{put("index.html", "<h1>v2</h1>")}})
	require.NoError(t, err)

	one := 1
	no := false
	pub, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic, Version: &one, Follow: &no})
	require.NoError(t, err)
	assert.Equal(t, 1, pub.Version)
	assert.False(t, pub.Follow)
	assert.Regexp(t, `^http://p-[a-z2-7]{26}\.localhost:7600/$`, pub.URL)

	// 不给的字段保持不变；slug 不变
	again, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode})
	require.NoError(t, err)
	assert.Equal(t, 1, again.Version)
	assert.False(t, again.Follow)
	assert.Equal(t, pub.Slug, again.Slug)
	assert.Equal(t, pub.CodeGen+1, again.CodeGen, "改访问级别时代数加一")
	assert.Regexp(t, `^[23456789abcdefghjkmnpqrstuvwxyz]{4}$`, again.Code)
	require.NotNil(t, again.API().Code)

	nine := 9
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode, Version: &nine})
	assert.Equal(t, CodeNotFound, code(err))
	zero := 0
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode, Version: &zero})
	assert.Equal(t, CodeInvalid, code(err))
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: "secret"})
	assert.Equal(t, CodeInvalid, code(err))
}

func TestPublishCodeLockAndRotate(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>hi</h1>")
	pub, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode})
	require.NoError(t, err)

	g, err := e.s.UnlockCode(ctx, pub.Slug, " "+strings.ToUpper(pub.Code)+" ")
	require.NoError(t, err)
	assert.True(t, e.s.GrantValid(pub, g))

	wrong := "zzzz"
	if pub.Code == wrong {
		wrong = "yyyy"
	}
	for i := 1; i < MaxCodeFailures; i++ {
		_, err := e.s.UnlockCode(ctx, pub.Slug, wrong)
		require.ErrorIs(t, err, ErrCodeWrong, "第 %d 次", i)
	}
	p, err := e.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, p.CodeLocked)
	_, err = e.s.UnlockCode(ctx, pub.Slug, wrong)
	require.ErrorIs(t, err, ErrCodeLocked, "第 10 次锁定")
	p, err = e.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, p.CodeLocked)
	assert.True(t, p.API().CodeLocked)
	_, err = e.s.UnlockCode(ctx, pub.Slug, pub.Code)
	require.ErrorIs(t, err, ErrCodeLocked, "锁定后码对也不行")

	rot, err := e.s.RotateCode(ctx, a.ID)
	require.NoError(t, err)
	assert.False(t, rot.CodeLocked)
	assert.Equal(t, pub.CodeGen+1, rot.CodeGen)
	assert.False(t, e.s.GrantValid(rot, g), "换码后旧授权失效")
	g2, err := e.s.UnlockCode(ctx, rot.Slug, rot.Code)
	require.NoError(t, err)
	assert.True(t, e.s.GrantValid(rot, g2))

	// 一次输错后码对会清零失败计数
	_, _ = e.s.UnlockCode(ctx, rot.Slug, wrong)
	_, err = e.s.UnlockCode(ctx, rot.Slug, rot.Code)
	require.NoError(t, err)
	row, err := e.st.Q.GetArtifactPublication(ctx, a.ID)
	require.NoError(t, err)
	assert.Zero(t, row.CodeFailures)
}

func TestGrantURLAndExpiry(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>hi</h1>")
	pub, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)
	u, err := e.s.Grant(ctx, pub.Slug)
	require.NoError(t, err)
	m := regexp.MustCompile(`^` + regexp.QuoteMeta(pub.URL) + `~([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/$`).FindStringSubmatch(u)
	require.NotNil(t, m, u)
	assert.True(t, e.s.GrantValid(pub, m[1]))
	e.clk.add(GrantTTL)
	assert.False(t, e.s.GrantValid(pub, m[1]), "过期")

	_, err = e.s.Grant(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaa")
	assert.Equal(t, CodeNotFound, code(err))
}

func TestUnpublishAndDelete(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>hi</h1>")
	b := pubPage(t, e, "<h1>b</h1>")
	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)
	_, err = e.s.Publish(ctx, b.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)

	list, err := e.s.Publications(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	arts, err := e.s.List(ctx, Filter{})
	require.NoError(t, err)
	for _, x := range arts {
		assert.Equal(t, AccessPrivate, x.Published)
	}

	// 普通软删除保留发布；带取消发布的删除在同一事务里删掉发布记录
	_, err = e.s.Delete(ctx, a.ID)
	require.NoError(t, err)
	p, err := e.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.NotNil(t, p)
	got, err := e.s.DeleteAndUnpublish(ctx, a.ID)
	require.NoError(t, err)
	assert.True(t, got.Deleted())
	assert.Empty(t, got.Published)
	p, err = e.s.Publication(ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, p)

	// 回收站里的不能新发布
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	assert.Equal(t, CodeDeleted, code(err))

	require.NoError(t, e.s.Unpublish(ctx, b.ID))
	require.NoError(t, e.s.Unpublish(ctx, b.ID), "没有发布时什么都不做")
	got, err = e.s.Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Published)
	_, err = e.s.RotateCode(ctx, b.ID)
	assert.Equal(t, CodeNotFound, code(err))
}

func TestPublishNotConfigured(t *testing.T) {
	e := newEnv(t)
	a := pubPage(t, e, "<h1>hi</h1>")
	_, err := e.s.Publish(context.Background(), a.ID, PublishParams{Access: AccessPrivate})
	require.Error(t, err)
	assert.False(t, errors.As(err, new(*OpError)))
}

func publishedOf(t *testing.T, e *env, id string) *apigen.ArtifactAccess {
	t.Helper()
	a, err := e.s.Get(context.Background(), id)
	require.NoError(t, err)
	api := a.API()
	if api.Published == nil {
		return nil
	}
	return &api.Published.Access
}
