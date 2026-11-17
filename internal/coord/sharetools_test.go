package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeShares 是测试用的 ThreadShares：记下调用；scanHits > 0 时 Create 在未确认的情况下回 *share.ScanError。
type fakeShares struct {
	scanHits int
	created  []string
	revoked  []string
	list     []apigen.ThreadShare
}

func (f *fakeShares) Create(_ context.Context, threadID string, mode apigen.ShareMode, ack bool) (apigen.ThreadShare, error) {
	if f.scanHits > 0 && !ack {
		return apigen.ThreadShare{}, &share.ScanError{Scan: apigen.ShareScan{Findings: make([]apigen.ShareFinding, f.scanHits)}}
	}
	f.created = append(f.created, threadID+" "+string(mode))
	return apigen.ThreadShare{ID: "shr_1", ThreadID: threadID, Mode: mode, URL: "https://oryn.test/s/abc"}, nil
}

func (f *fakeShares) Revoke(_ context.Context, id string) error {
	if id == "shr_missing" {
		return share.ErrNotFound
	}
	f.revoked = append(f.revoked, id)
	return nil
}

func (f *fakeShares) List(_ context.Context, threadID string) ([]apigen.ThreadShare, error) {
	if threadID == "" {
		return f.list, nil
	}
	var out []apigen.ThreadShare
	for _, s := range f.list {
		if s.ThreadID == threadID {
			out = append(out, s)
		}
	}
	return out, nil
}

// fakePortals 是测试用的 PortalShares：记下调用。
type fakePortals struct{ calls []string }

func (f *fakePortals) SetShare(_ context.Context, orbID string, port int, d time.Duration) (apigen.OrbPortal, error) {
	f.calls = append(f.calls, "set "+orbID+" "+d.String())
	return apigen.OrbPortal{Port: port, Public: &apigen.OrbPortalPublic{ExpiresAt: 1}}, nil
}

func (f *fakePortals) EndShare(_ context.Context, orbID string, port int) error {
	f.calls = append(f.calls, "end "+orbID)
	return nil
}

// withConfirmResults 收集确认结果消息。
func (e *env) withConfirmResults() chan string {
	ch := make(chan string, 16)
	e.c.sentConfirm = func(_, text string) { ch <- text }
	return ch
}

func recv(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到确认结果消息")
		return ""
	}
}

// reject 让主人拒绝确认卡。
func (e *env) reject(conv apigen.Thread, r threads.HostToolResult) {
	e.t.Helper()
	m := details(e.t, r)
	require.Equal(e.t, "awaiting_owner", m["status"])
	id := m["confirmationId"].(string)
	e.c.UIAnswered(e.ctx, conv.ID, id, answer(id, false))
}

// 分享工具只出现在助手的工具表里；Space 协调者调用得到 unknown_tool。
func TestShareToolDefs(t *testing.T) {
	names := func(defs []apigen.OmpFrame) map[string]bool {
		out := map[string]bool{}
		for _, d := range defs {
			out[d["name"].(string)] = true
		}
		return out
	}
	space, asst := names(ToolDefs(ToolSet{InSpace: true})), names(ToolDefs(ToolSet{}))
	all := []string{ToolShareThread, ToolListShares, ToolRevokeShare, ToolSharePortal, ToolUnsharePortal, ToolPublishArtifact, ToolUnpublishArtifact}
	for _, n := range all {
		assert.True(t, asst[n], n)
		assert.False(t, space[n], n)
	}

	e := newEnv(t, true)
	e.c.SetShares(&fakeShares{}, &fakePortals{})
	chat := e.spaceChat(e.space("Alpha", ""))
	for _, n := range all {
		assert.Equal(t, "unknown_tool", errCodeOf(t, e.call(chat, n, `{}`, true)), n)
	}
}

// share_thread：先挂确认卡；拒绝后没有任何分享；批准后创建并把链接投递回对话（审计 origin=owner）；
// 密钥扫描命中记 failed（secrets_found），结果消息带命中条数；目标必须是工作线程。
func TestShareThreadConfirm(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	w := e.threadOn(e.newOrb(), "", "", "")
	results := e.withConfirmResults()

	assert.Equal(t, CodeCoordinatorUnavailable, errCodeOf(t, e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"live"}`, true)))
	fs := &fakeShares{}
	e.c.SetShares(fs, nil)
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"forever"}`, true)))
	assert.Equal(t, CodeInvalidTarget, errCodeOf(t, e.call(asst, ToolShareThread, `{"threadId":"`+e.assistant().ID+`","mode":"live"}`, true)))

	n := len(e.actions())
	r := e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"live"}`, true)
	assert.Empty(t, fs.created, "确认之前不创建")
	assert.Len(t, e.actions(), n, "挂卡成功时不写审计")
	e.reject(asst, r)
	assert.Contains(t, recv(t, results), "share_thread "+w.ID+": rejected by the owner.")
	assert.Empty(t, fs.created, "拒绝后没有分享")
	assert.Len(t, e.actions(), n, "拒绝不写审计")

	e.approve(asst, e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"snapshot"}`, true))
	text := recv(t, results)
	assert.Contains(t, text, "approved and done.")
	assert.Contains(t, text, "https://oryn.test/s/abc")
	assert.Equal(t, []string{w.ID + " snapshot"}, fs.created)
	a := e.latest()
	assert.Equal(t, ToolShareThread, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)
	assert.Equal(t, w.ID, deref(a.TargetThreadID))

	// 密钥扫描命中：什么都没公开，记 failed
	fs.scanHits = 3
	r = e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"live"}`, true)
	id := details(t, r)["confirmationId"].(string)
	e.approve(asst, r)
	text = recv(t, results)
	assert.Contains(t, text, "approved but failed: "+CodeSecretsFound+".")
	assert.Contains(t, text, "found 3 secret-like string(s)")
	assert.Len(t, fs.created, 1)
	row, err := e.st.Q.GetAssistantConfirmation(e.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, confirmFailed, row.State)
	assert.Equal(t, CodeSecretsFound, deref(row.Error))
	a = e.latest()
	assert.False(t, a.Ok)
	assert.Equal(t, CodeSecretsFound, deref(a.Error))

	// 主人同意带上确认后照常分享
	e.approve(asst, e.call(asst, ToolShareThread, `{"threadId":"`+w.ID+`","mode":"live","acknowledgeFindings":true}`, true))
	assert.Contains(t, recv(t, results), "approved and done.")
	assert.Equal(t, w.ID+" live", fs.created[1])
}

// list_shares 带不可信包裹；revoke_share 直接执行并写审计；不存在的分享回 not_found。
func TestListRevokeShares(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	fs := &fakeShares{list: []apigen.ThreadShare{
		{ID: "shr_a", ThreadID: "T-a", ThreadTitle: "A", Mode: apigen.ShareModeLive, URL: "u1"},
		{ID: "shr_b", ThreadID: "T-b", ThreadTitle: "B", Mode: apigen.ShareModeSnapshot, URL: "u2"},
	}}
	e.c.SetShares(fs, nil)

	r := e.call(asst, ToolListShares, `{}`, true)
	assert.Contains(t, r.Text, "untrusted")
	assert.Len(t, details(t, r)["shares"], 2)
	assert.Len(t, details(t, e.call(asst, ToolListShares, `{"threadId":"T-b"}`, true))["shares"], 1)

	details(t, e.call(asst, ToolRevokeShare, `{"id":"shr_a"}`, false))
	assert.Equal(t, []string{"shr_a"}, fs.revoked)
	a := e.latest()
	assert.Equal(t, ToolRevokeShare, a.Action)
	assert.Equal(t, "auto", a.Origin)
	assert.True(t, a.Ok)
	assert.Equal(t, "shr_a", a.Summary)
	assert.Equal(t, CodeNotFound, errCodeOf(t, e.call(asst, ToolRevokeShare, `{"id":"shr_missing"}`, true)))
	assert.False(t, e.latest().Ok)
}

// share_portal 先挂确认卡，拒绝后不公开、批准后公开挂卡时线程所在 orb 的端口；unshare_portal 直接执行；
// 没有配置 Portal 时两者都回 portal_disabled；线程没有 orb 回 no_orb。
func TestSharePortalConfirm(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	orbID := e.newOrb()
	w := e.threadOn(orbID, "", "", "")
	results := e.withConfirmResults()
	e.c.SetShares(&fakeShares{}, nil)

	assert.Equal(t, CodePortalDisabled, errCodeOf(t, e.call(asst, ToolSharePortal, `{"threadId":"`+w.ID+`","port":3000,"duration":"1d"}`, true)))
	assert.Equal(t, CodePortalDisabled, errCodeOf(t, e.call(asst, ToolUnsharePortal, `{"threadId":"`+w.ID+`","port":3000}`, true)))

	fp := &fakePortals{}
	e.c.SetShares(&fakeShares{}, fp)
	noOrb := e.work("p")
	_, err := e.st.DB.ExecContext(e.ctx, `UPDATE threads SET orb_id = NULL WHERE id = ?`, noOrb.ID)
	require.NoError(t, err)
	assert.Equal(t, CodeNoOrb, errCodeOf(t, e.call(asst, ToolSharePortal, `{"threadId":"`+noOrb.ID+`","port":3000,"duration":"1d"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSharePortal, `{"threadId":"`+w.ID+`","port":0,"duration":"1d"}`, true)))
	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolSharePortal, `{"threadId":"`+w.ID+`","port":3000,"duration":"2d"}`, true)))

	e.reject(asst, e.call(asst, ToolSharePortal, `{"threadId":"`+w.ID+`","port":3000,"duration":"1d"}`, true))
	assert.Contains(t, recv(t, results), "rejected by the owner.")
	assert.Empty(t, fp.calls, "拒绝后不公开")

	e.approve(asst, e.call(asst, ToolSharePortal, `{"threadId":"`+w.ID+`","port":3000,"duration":"1d"}`, true))
	assert.Contains(t, recv(t, results), "share_portal "+w.ID+": approved and done.")
	assert.Equal(t, []string{"set " + orbID + " 24h0m0s"}, fp.calls)
	a := e.latest()
	assert.Equal(t, ToolSharePortal, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.Equal(t, w.ID, deref(a.TargetThreadID))

	details(t, e.call(asst, ToolUnsharePortal, `{"threadId":"`+w.ID+`","port":3000}`, true))
	assert.Equal(t, "end "+orbID, fp.calls[1])
	a = e.latest()
	assert.Equal(t, ToolUnsharePortal, a.Action)
	assert.True(t, a.Ok)
}

// publish_artifact：private 直接发布（不经确认）；code / public 先挂确认卡，拒绝后发布状态不变，批准后才发布；
// 扫描命中记 failed（secrets_found）；unpublish_artifact 直接撤下。
func TestPublishArtifactTools(t *testing.T) {
	e := newEnv(t, true)
	asst := e.assistant()
	arts := e.withArtifacts()
	base, err := portalhost.NewBase("https://oryn.test", "")
	require.NoError(t, err)
	hits := 0
	arts.ConfigurePublishing(artifacts.PublishConfig{Base: base, GrantKey: artifacts.DeriveGrantKey([]byte("master")),
		Scan: func(context.Context, map[string][]byte) apigen.ShareScan {
			return apigen.ShareScan{Findings: make([]apigen.ShareFinding, hits)}
		}})
	results := e.withConfirmResults()
	id := details(t, e.call(asst, ToolCreateArtifact, `{"title":"报告","html":"<h1>hi</h1>"}`, true))["id"].(string)
	access := func() string {
		p, err := arts.Publication(e.ctx, id)
		require.NoError(t, err)
		if p == nil {
			return ""
		}
		return p.Access
	}

	assert.Equal(t, CodeInvalid, errCodeOf(t, e.call(asst, ToolPublishArtifact, `{"id":"`+id+`","access":"world"}`, true)))
	n := len(e.actions())
	m := details(t, e.call(asst, ToolPublishArtifact, `{"id":"`+id+`","access":"private"}`, true))
	assert.Equal(t, "private", m["access"])
	assert.Equal(t, "private", access(), "private 不经确认直接发布")
	assert.Len(t, e.actions(), n+1)
	assert.Equal(t, ToolPublishArtifact, e.latest().Action)

	for _, acc := range []string{"public", "code"} {
		e.reject(asst, e.call(asst, ToolPublishArtifact, `{"id":"`+id+`","access":"`+acc+`"}`, true))
		assert.Contains(t, recv(t, results), "rejected by the owner.")
		assert.Equal(t, "private", access(), "拒绝后发布状态不变")
	}

	e.approve(asst, e.call(asst, ToolPublishArtifact, `{"id":"`+id+`","access":"code"}`, true))
	text := recv(t, results)
	assert.Contains(t, text, "publish_artifact "+id+": approved and done.")
	assert.Contains(t, text, "Access code: ")
	assert.Equal(t, "code", access())
	a := e.latest()
	assert.Equal(t, ToolPublishArtifact, a.Action)
	assert.Equal(t, "owner", a.Origin)
	assert.True(t, a.Ok)

	// 扫描命中：新的 artifact 发布为 public 失败
	hits = 2
	id2 := details(t, e.call(asst, ToolCreateArtifact, `{"title":"密钥","html":"<p>x</p>"}`, true))["id"].(string)
	e.approve(asst, e.call(asst, ToolPublishArtifact, `{"id":"`+id2+`","access":"public"}`, true))
	text = recv(t, results)
	assert.Contains(t, text, "approved but failed: "+CodeSecretsFound+".")
	assert.Contains(t, text, "found 2 secret-like string(s)")
	p, err := arts.Publication(e.ctx, id2)
	require.NoError(t, err)
	assert.Nil(t, p, "命中后没有发布")

	details(t, e.call(asst, ToolUnpublishArtifact, `{"id":"`+id+`"}`, true))
	assert.Equal(t, "", access())
	assert.Equal(t, ToolUnpublishArtifact, e.latest().Action)
}
