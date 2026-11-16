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
	"github.com/Lynricsy/Oryn/internal/store/db"
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
	t.Cleanup(e.s.waitFollow) // 在关闭存储之前等后台的跟随推进结束（Cleanup 后进先出）
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

func pubOf(t *testing.T, e *env, id string) *Publication {
	t.Helper()
	p, err := e.s.Publication(context.Background(), id)
	require.NoError(t, err)
	return p
}

func writeHTML(t *testing.T, e *env, id string, base int, html string) {
	t.Helper()
	_, err := e.s.Write(context.Background(), WriteParams{ID: id, BaseVersion: base, Files: []FileChange{put("index.html", html)}})
	require.NoError(t, err)
}

func TestFollowPrivateAdvances(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>v1</h1>")
	b := pubPage(t, e, "<h1>v1</h1>")
	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPrivate})
	require.NoError(t, err)
	no := false
	_, err = e.s.Publish(ctx, b.ID, PublishParams{Access: AccessPublic, Follow: &no})
	require.NoError(t, err)

	writeHTML(t, e, a.ID, 1, secretPage) // private 不扫描
	writeHTML(t, e, b.ID, 1, "<h1>v2</h1>")
	e.s.waitFollow()
	assert.Equal(t, 2, pubOf(t, e, a.ID).Version, "跟随的 private 页推到最新")
	assert.Zero(t, pubOf(t, e, a.ID).PendingVersion)
	assert.Equal(t, 1, pubOf(t, e, b.ID).Version, "没开跟随的不动")

	// 连续两个版本：最终停在最新
	writeHTML(t, e, a.ID, 2, "<h1>v3</h1>")
	writeHTML(t, e, a.ID, 3, "<h1>v4</h1>")
	e.s.waitFollow()
	assert.Equal(t, 4, pubOf(t, e, a.ID).Version)
}

func TestFollowPublicPendsOnSecret(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>v1</h1>")
	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)

	e.hub.mu.Lock()
	before := e.hub.changed
	e.hub.mu.Unlock()
	writeHTML(t, e, a.ID, 1, secretPage)
	e.s.waitFollow()
	p := pubOf(t, e, a.ID)
	assert.Equal(t, 1, p.Version, "有疑似密钥时不推进")
	assert.Equal(t, 2, p.PendingVersion)
	require.NotNil(t, p.API().PendingVersion)
	e.hub.mu.Lock()
	assert.Equal(t, before+2, e.hub.changed, "写入本身发一次，挂起等待确认又发一次")
	e.hub.mu.Unlock()

	// 用户确认后更新到等待的版本，等待清空
	two := 2
	p2, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic, Version: &two, AcknowledgeFindings: true})
	require.NoError(t, err)
	assert.Equal(t, 2, p2.Version)
	assert.Zero(t, p2.PendingVersion)
	assert.True(t, p2.Follow)

	// 再来一个含密钥的版本被挂起，随后干净的版本推进并清掉等待
	writeHTML(t, e, a.ID, 2, secretPage+"<p>v3</p>")
	e.s.waitFollow()
	assert.Equal(t, 3, pubOf(t, e, a.ID).PendingVersion)
	writeHTML(t, e, a.ID, 3, "<h1>v4</h1>")
	e.s.waitFollow()
	p = pubOf(t, e, a.ID)
	assert.Equal(t, 4, p.Version)
	assert.Zero(t, p.PendingVersion)
}

// 扫描期间来的新版本合并成一轮，用户在这期间手动换到更新的版本时不被倒退。
func TestFollowCoalescesAndKeepsManualChange(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>v1</h1>")
	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode})
	require.NoError(t, err)

	scanned := make(chan struct{})
	release := make(chan struct{})
	var scans int
	e.s.follow.afterScan = func() {
		scans++
		if scans == 1 {
			close(scanned)
			<-release
		}
	}
	writeHTML(t, e, a.ID, 1, "<h1>v2</h1>")
	<-scanned // worker 扫完 v2，停在落库之前
	writeHTML(t, e, a.ID, 2, "<h1>v3</h1>")
	writeHTML(t, e, a.ID, 3, "<h1>v4</h1>")
	four := 4
	_, err = e.s.Publish(ctx, a.ID, PublishParams{Access: AccessCode, Version: &four})
	require.NoError(t, err)
	close(release)
	e.s.waitFollow()

	p := pubOf(t, e, a.ID)
	assert.Equal(t, 4, p.Version, "不倒退到 worker 扫过的 v2")
	assert.True(t, p.Follow)
	assert.Equal(t, 1, scans, "v3、v4 合并成一轮，且那一轮发现已是最新、不再扫描")
}

// 扫描期间被关掉跟随的发布记录不被推进。
func TestFollowRespectsFollowTurnedOff(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	a := pubPage(t, e, "<h1>v1</h1>")
	_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic})
	require.NoError(t, err)
	e.s.follow.afterScan = func() {
		no := false
		_, err := e.s.Publish(ctx, a.ID, PublishParams{Access: AccessPublic, Follow: &no})
		assert.NoError(t, err)
	}
	writeHTML(t, e, a.ID, 1, "<h1>v2</h1>")
	e.s.waitFollow()
	p := pubOf(t, e, a.ID)
	assert.Equal(t, 1, p.Version)
	assert.False(t, p.Follow)
}

func TestAutoPublish(t *testing.T) {
	e := newPublishEnv(t, "")
	ctx := context.Background()
	require.NoError(t, e.st.Q.CreateOwner(ctx, db.CreateOwnerParams{DisplayName: "主人", UserHandle: []byte{1}, CreatedAt: 1}))

	off := pubPage(t, e, "<h1>off</h1>")
	assert.Nil(t, pubOf(t, e, off.ID), "缺省关闭")

	n, err := e.st.Q.SetArtifactAutoPublish(ctx, 1)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	created := pubPage(t, e, "<h1>agent</h1>")
	assert.Equal(t, AccessPrivate, created.Published, "返回的 artifact 已带发布状态")
	imported, err := e.s.Import(ctx, multipartOf(t, "index.html", "<p>hi</p>"), ImportParams{Name: "report.html"})
	require.NoError(t, err)
	for _, id := range []string{created.ID, imported.ID} {
		p := pubOf(t, e, id)
		require.NotNil(t, p)
		assert.Equal(t, AccessPrivate, p.Access)
		assert.True(t, p.Follow)
		assert.Equal(t, 1, p.Version)
	}

	// 自动发布的页面随后跟随新版本
	writeHTML(t, e, created.ID, 1, "<h1>v2</h1>")
	e.s.waitFollow()
	assert.Equal(t, 2, pubOf(t, e, created.ID).Version)

	_, err = e.st.Q.SetArtifactAutoPublish(ctx, 0)
	require.NoError(t, err)
	assert.Nil(t, pubOf(t, e, pubPage(t, e, "<h1>off again</h1>").ID), "关闭后不再自动发布")
}
