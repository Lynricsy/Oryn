package artifacts

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// consentHub 模拟观看登记，并把发给观看者的同意消息送进 sent。
type consentHub struct {
	mu      sync.Mutex
	viewers map[string][]Viewer
	fns     []func(string)
	sent    chan apigen.WsServerArtifactConsent
}

func (h *consentHub) Viewers(id string) []Viewer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.viewers[id])
}

func (h *consentHub) ToWatchers(_ string, msg any) {
	h.sent <- msg.(apigen.WsServerArtifactConsent)
}

func (h *consentHub) OnWatchChange(fn func(string)) {
	h.mu.Lock()
	h.fns = append(h.fns, fn)
	h.mu.Unlock()
}

// watch 把 id 的观看者换成 conns 并通知观看者变化。
func (h *consentHub) watch(id string, conns ...string) {
	h.mu.Lock()
	vs := make([]Viewer, len(conns))
	for i, c := range conns {
		vs[i] = Viewer{Conn: c}
	}
	h.viewers[id] = vs
	fns := slices.Clone(h.fns)
	h.mu.Unlock()
	for _, fn := range fns {
		fn(id)
	}
}

// next 取下一条同意消息（最多等 2 秒）。
func (h *consentHub) next(t *testing.T) apigen.WsServerArtifactConsent {
	t.Helper()
	select {
	case m := <-h.sent:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到 artifact.consent")
		return apigen.WsServerArtifactConsent{}
	}
}

// none 断言没有发出任何同意消息。
func (h *consentHub) none(t *testing.T) {
	t.Helper()
	select {
	case m := <-h.sent:
		t.Fatalf("不该发出 artifact.consent：%+v", m)
	default:
	}
}

// consentTimers 是可以手动到点的等待计时器。
type consentTimers struct {
	mu  sync.Mutex
	chs []chan time.Time
}

func (ft *consentTimers) timer(time.Duration) (<-chan time.Time, func() bool) {
	ch := make(chan time.Time, 1)
	ft.mu.Lock()
	ft.chs = append(ft.chs, ch)
	ft.mu.Unlock()
	return ch, func() bool { return true }
}

// fire 让目前登记的全部计时器到点。
func (ft *consentTimers) fire() {
	ft.mu.Lock()
	chs := ft.chs
	ft.chs = nil
	ft.mu.Unlock()
	for _, ch := range chs {
		ch <- time.Time{}
	}
}

type consentEnv struct {
	*rtEnv
	hub    *consentHub
	timers *consentTimers
}

// newConsentEnv 是一个声明了 files 与 sample 的页面（属于项目 prj_c）；两个能力用测试专用的实现登记，
// 只为验证同意机制，不代表真实的 files / sample。
func newConsentEnv(t *testing.T) *consentEnv {
	t.Helper()
	e := newEnv(t)
	mkProject(t, e, "prj_c")
	h := newHost(t, e, "localhost", fakeViewerAssets)
	a, err := e.s.Create(context.Background(), CreateParams{
		Title: "仓库浏览", ProjectID: "prj_c", Files: []FileChange{put("index.html", page)}, Capabilities: []string{"files", "sample"},
	})
	require.NoError(t, err)
	for _, name := range []string{"files", "sample"} {
		h.caps[name] = runtimeCap{declared: true,
			ops: map[string]runtimeOp{"list": func(context.Context, *runtimeCall) (any, error) { return "listed", nil }},
			streams: map[string]streamOp{"ask": func(_ context.Context, _ *runtimeCall, emit func(any) error) error {
				return emit("answer")
			}},
		}
	}
	r := &rtEnv{e: e, h: h, mw: h.Middleware(http.NotFoundHandler()), art: a}
	r.host = r.frame(t, 0)
	hub := &consentHub{viewers: map[string][]Viewer{}, sent: make(chan apigen.WsServerArtifactConsent, 16)}
	ft := &consentTimers{}
	e.s.consent.timer = ft.timer
	e.s.AttachConsentHub(hub)
	return &consentEnv{rtEnv: r, hub: hub, timers: ft}
}

// use 发 use cap，回 value（null 时为 nil）。
func (c *consentEnv) use(t *testing.T, capName string) any {
	t.Helper()
	return c.send(t, c.host, map[string]any{"kind": "use", "cap": capName})["value"]
}

// useAsync 在后台发 use，结果送进返回的 channel。
func (c *consentEnv) useAsync(capName string) <-chan any {
	out := make(chan any, 1)
	go func() {
		rec := postRuntime(c.mw, c.host, "/_oryn/v1", "null", `{"v":1,"id":"u","kind":"use","cap":"`+capName+`"}`)
		var rep map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &rep)
		out <- rep["value"]
	}()
	return out
}

func recv(t *testing.T, ch <-chan any) any {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("use 没有返回")
		return nil
	}
}

func (c *consentEnv) callCap(t *testing.T, capName string) map[string]any {
	t.Helper()
	return c.send(t, c.host, map[string]any{"kind": "call", "cap": capName, "op": "list", "args": []any{}})
}

func (c *consentEnv) answer(t *testing.T, requestID string, allow bool) {
	t.Helper()
	require.NoError(t, c.e.s.AnswerConsent(context.Background(), requestID, allow))
}

// 没有观看者：use 立刻回 null 且不发事件；call 与 stream 回 not_allowed。不需要同意的能力不受影响。
func TestConsentNoViewers(t *testing.T) {
	c := newConsentEnv(t)
	assert.Nil(t, c.use(t, "files"))
	c.hub.none(t)
	assert.Equal(t, codeNotAllowed, replyCode(t, c.callCap(t, "files")))
	rec := postRuntime(c.mw, c.host, "/_oryn/v1", "null", `{"v":1,"id":"s","kind":"stream","cap":"sample","op":"ask"}`)
	assert.Equal(t, codeNotAllowed, replyCode(t, replyOf(t, rec)))
	c.hub.none(t)
	assert.Len(t, c.e.s.consent.open, 0)
}

// 有观看者：发 asked（带标题、项目名与能力），允许后这次 use 成功并写库；之后的 use 与 call 直接通过。
func TestConsentAllow(t *testing.T) {
	ctx := context.Background()
	c := newConsentEnv(t)
	c.hub.watch(c.art.ID, "c1")
	done := c.useAsync("files")
	asked := c.hub.next(t)
	assert.Equal(t, apigen.ArtifactConsentAsked, asked.Kind)
	assert.Equal(t, c.art.ID, asked.ArtifactID)
	assert.Equal(t, "仓库浏览", *asked.Title)
	assert.Equal(t, "prj_c", *asked.Project)
	assert.Equal(t, apigen.ArtifactConsentFiles, *asked.Capability)
	// 同一 artifact × 能力的第二个 use 共用这个请求，不再发 asked
	second := c.useAsync("files")
	require.Eventually(t, func() bool {
		c.e.s.consent.mu.Lock()
		defer c.e.s.consent.mu.Unlock()
		return c.e.s.consent.open[asked.RequestID] != nil && c.e.s.consent.open[asked.RequestID].waiting == 2
	}, 2*time.Second, time.Millisecond)

	c.answer(t, asked.RequestID, true)
	assert.NotNil(t, recv(t, done))
	assert.NotNil(t, recv(t, second))
	settled := c.hub.next(t)
	assert.Equal(t, apigen.ArtifactConsentSettled, settled.Kind)
	assert.Equal(t, asked.RequestID, settled.RequestID)
	assert.True(t, *settled.Allowed)
	assert.False(t, *settled.Reload, "页面还在等，不必重载")
	c.hub.none(t)

	_, err := c.e.st.Q.GetArtifactConsent(ctx, db.GetArtifactConsentParams{ArtifactID: c.art.ID, Capability: "files"})
	require.NoError(t, err)
	assert.NotNil(t, c.use(t, "files"))
	assert.Equal(t, "listed", c.ok2(t, "files"))
	c.hub.none(t)
	// 另一个能力单独询问
	assert.Equal(t, codeNotAllowed, replyCode(t, c.callCap(t, "sample")))

	// 第一个回答生效：再回答同一个请求回 not_found
	err = c.e.s.AnswerConsent(ctx, asked.RequestID, false)
	oe, ok := AsOpError(err)
	require.True(t, ok)
	assert.Equal(t, CodeNotFound, oe.Code)

	// 收回之后 call 回 not_allowed
	list, err := c.e.s.Consents(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, []apigen.ArtifactConsentCapability{apigen.ArtifactConsentFiles}, list[0].Capabilities)
	require.NoError(t, c.e.s.RevokeConsents(ctx, c.art.ID))
	assert.Equal(t, codeNotAllowed, replyCode(t, c.callCap(t, "files")))
	list, err = c.e.s.Consents(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func (c *consentEnv) ok2(t *testing.T, capName string) any {
	t.Helper()
	rep := c.callCap(t, capName)
	require.Equal(t, true, rep["ok"], rep)
	return rep["value"]
}

// 等待超时：这次 use 回 null，问题保持打开；之后允许时 settled 带 reload，页面重载后的 use 成功。
func TestConsentTimeout(t *testing.T) {
	c := newConsentEnv(t)
	c.hub.watch(c.art.ID, "c1")
	done := c.useAsync("sample")
	asked := c.hub.next(t)
	require.Eventually(t, func() bool {
		c.timers.mu.Lock()
		defer c.timers.mu.Unlock()
		return len(c.timers.chs) == 1
	}, 2*time.Second, time.Millisecond)
	c.timers.fire()
	assert.Nil(t, recv(t, done))
	c.e.s.consent.mu.Lock()
	q := c.e.s.consent.open[asked.RequestID]
	c.e.s.consent.mu.Unlock()
	require.NotNil(t, q, "超时后问题保持打开")
	assert.Equal(t, 0, q.waiting)

	c.answer(t, asked.RequestID, true)
	settled := c.hub.next(t)
	assert.True(t, *settled.Allowed)
	assert.True(t, *settled.Reload, "页面已经不在等，查看器要重载帧")
	assert.NotNil(t, c.use(t, "sample"))
}

// 拒绝：这次 use 回 null；同一观看期内不再询问；观看者全部离开后再打开会重新询问。
func TestConsentDeny(t *testing.T) {
	c := newConsentEnv(t)
	c.hub.watch(c.art.ID, "c1")
	done := c.useAsync("files")
	asked := c.hub.next(t)
	c.answer(t, asked.RequestID, false)
	assert.Nil(t, recv(t, done))
	settled := c.hub.next(t)
	assert.False(t, *settled.Allowed)

	assert.Nil(t, c.use(t, "files"))
	c.hub.none(t)
	assert.Equal(t, codeNotAllowed, replyCode(t, c.callCap(t, "files")))
	_, err := c.e.st.Q.GetArtifactConsent(context.Background(), db.GetArtifactConsentParams{ArtifactID: c.art.ID, Capability: "files"})
	assert.Error(t, err)

	// 另一个连接加入不清除拒绝
	c.hub.watch(c.art.ID, "c1", "c2")
	assert.Nil(t, c.use(t, "files"))
	c.hub.none(t)

	c.hub.watch(c.art.ID)
	c.hub.watch(c.art.ID, "c3")
	again := c.useAsync("files")
	reasked := c.hub.next(t)
	assert.NotEqual(t, asked.RequestID, reasked.RequestID)
	c.answer(t, reasked.RequestID, true)
	assert.NotNil(t, recv(t, again))
}

// 观看者变化：新加入的连接会再收到打开着的 asked；最后一个观看者离开时请求按拒绝结束、等待中的 use 回 null。
func TestConsentViewersLeave(t *testing.T) {
	c := newConsentEnv(t)
	c.hub.watch(c.art.ID, "c1")
	done := c.useAsync("files")
	asked := c.hub.next(t)

	c.hub.watch(c.art.ID, "c1") // page 变化之类：没有新连接，不重发
	c.hub.none(t)
	c.hub.watch(c.art.ID, "c1", "c2")
	assert.Equal(t, asked.RequestID, c.hub.next(t).RequestID)

	c.hub.watch(c.art.ID)
	assert.Nil(t, recv(t, done))
	c.hub.none(t)
	err := c.e.s.AnswerConsent(context.Background(), asked.RequestID, true)
	_, ok := AsOpError(err)
	assert.True(t, ok, "请求已经结束")
}

// 回收站里的 artifact 不出现在授权列表里。
func TestConsentListSkipsTrash(t *testing.T) {
	ctx := context.Background()
	c := newConsentEnv(t)
	for _, capName := range []string{"sample", "files"} {
		require.NoError(t, c.e.st.Q.UpsertArtifactConsent(ctx, db.UpsertArtifactConsentParams{ArtifactID: c.art.ID, Capability: capName, GrantedAt: 5}))
	}
	list, err := c.e.s.Consents(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, []apigen.ArtifactConsentCapability{apigen.ArtifactConsentFiles, apigen.ArtifactConsentSample}, list[0].Capabilities)
	assert.Equal(t, "prj_c", *list[0].ProjectID)
	_, err = c.e.s.Delete(ctx, c.art.ID)
	require.NoError(t, err)
	list, err = c.e.s.Consents(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	oe, ok := AsOpError(c.e.s.RevokeConsents(ctx, "bogus"))
	require.True(t, ok)
	assert.Equal(t, CodeInvalid, oe.Code)
}
