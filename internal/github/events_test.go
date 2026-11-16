package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// drain 同步执行队列里的全部任务（任务里再入队的也一并执行）。
func (e *testEnv) drain() {
	e.t.Helper()
	for {
		select {
		case job := <-e.s.jobs:
			job(e.ctx)
		default:
			return
		}
	}
}

// hookURL 起一个把请求交给 HandleWebhook 的 webhook 端点。
func (e *testEnv) hookURL() string {
	e.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(e.s.HandleWebhook(r.Context(), r.Header, body))
	}))
	e.t.Cleanup(srv.Close)
	return srv.URL
}

// deliver 经假服务签名投递一次事件并处理队列，返回 guid。
func (e *testEnv) deliver(event string, payload any) string {
	e.t.Helper()
	status, guid := e.fake.Deliver(e.hookURL(), event, payload)
	require.Equal(e.t, http.StatusAccepted, status)
	e.drain()
	return guid
}

func (e *testEnv) delivery(guid string) db.GithubDelivery {
	e.t.Helper()
	d, err := e.st.Q.GetGithubDelivery(e.ctx, guid)
	require.NoError(e.t, err)
	return d
}

// hookHeaders 是仓库 hook 投递的请求头（按 hook 的 secret 签名）。
func hookHeaders(guid, event string, hookID, targetID int64, secret string, body []byte) http.Header {
	h := http.Header{}
	h.Set("X-GitHub-Delivery", guid)
	h.Set("X-GitHub-Event", event)
	h.Set("X-GitHub-Hook-ID", strconv.FormatInt(hookID, 10))
	h.Set("X-GitHub-Hook-Installation-Target-ID", strconv.FormatInt(targetID, 10))
	h.Set("X-GitHub-Hook-Installation-Target-Type", "repository")
	h.Set("X-Hub-Signature-256", githubtest.SignHook(secret, body))
	return h
}

func TestWebhookHookSignature(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"zen":"x"}`)
	// 既没有 hook 行也没有旧 App
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, hookHeaders("g0", "ping", 1, 42, "s", body), body))

	e.hookEnv()
	e.bindWidgets()
	row := e.hookRow(42)
	hookID, secret := *row.HookID, e.hookSecret(row)
	ok := hookHeaders("g1", "ping", hookID, 42, secret, body)

	// hook id 不对（查不到行，且旧 App 的 secret 也验不过）
	bad := ok.Clone()
	bad.Set("X-GitHub-Hook-ID", strconv.FormatInt(hookID+1, 10))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	bad.Set("X-GitHub-Hook-ID", "0x"+strconv.FormatInt(hookID, 16))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	// 签名不对：别的 secret、旧 App 的 secret、缺前缀
	bad = hookHeaders("g1", "ping", hookID, 42, "other", body)
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	bad.Set("X-Hub-Signature-256", e.fake.Sign(body))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	bad.Set("X-Hub-Signature-256", "sha1=00")
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	// target id 与该行的仓库不符
	bad = hookHeaders("g1", "ping", hookID, 43, secret, body)
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	bad.Del("X-GitHub-Hook-Installation-Target-ID")
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	_, err := e.st.Q.GetGithubDelivery(e.ctx, "g1")
	assert.True(t, isNoRows(err))
	assert.Nil(t, e.hookRow(42).LastWebhookAt)

	noGUID := ok.Clone()
	noGUID.Del("X-GitHub-Delivery")
	assert.Equal(t, http.StatusBadRequest, e.s.HandleWebhook(e.ctx, noGUID, body))

	assert.Equal(t, http.StatusAccepted, e.s.HandleWebhook(e.ctx, ok, body))
	e.drain()
	d := e.delivery("g1")
	assert.Equal(t, "ignored:ping", d.Outcome)
	assert.Equal(t, "webhook", d.Source)
	assert.Equal(t, body, d.Payload, "M15：结束后保留载荷，供 replay 重放")
	row = e.hookRow(42)
	require.NotNil(t, row.LastWebhookAt)
	assert.Equal(t, e.clock.Now().UnixMilli(), *row.LastWebhookAt)
	require.NotNil(t, row.LastDeliverySrc)
	assert.Equal(t, "webhook", *row.LastDeliverySrc)

	// 经假服务以仓库 hook 身份真实投递
	status, guid := e.fake.DeliverHook("Acme/Widgets", "issues", map[string]any{"action": "edited"})
	require.Equal(t, http.StatusAccepted, status)
	e.drain()
	assert.Equal(t, "webhook", e.delivery(guid).Source)
}

// 过渡：没有匹配的 hook 行、且库里还有旧 App 时，按 App 的 webhook secret 验签（步骤 1.4 删除）。
func TestWebhookLegacyApp(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"zen":"x"}`)
	h := http.Header{}
	h.Set("X-GitHub-Delivery", "g1")
	h.Set("X-GitHub-Event", "ping")
	h.Set("X-Hub-Signature-256", e.fake.Sign(body))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, h, body))

	e.installApp()
	bad := h.Clone()
	bad.Set("X-Hub-Signature-256", e.fake.Sign([]byte(`{"zen":"y"}`)))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	assert.Equal(t, http.StatusAccepted, e.s.HandleWebhook(e.ctx, h, body))
	e.drain()
	assert.Equal(t, "webhook", e.delivery("g1").Source)
}

func TestWebhookDedupAndUnknown(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	var calls atomic.Int32
	e.s.handlers["test_event"] = func(context.Context, string, []byte) (string, error) {
		calls.Add(1)
		return "handled", nil
	}
	hook := e.hookURL()
	_, guid := e.fake.Deliver(hook, "test_event", map[string]any{"action": "x"})
	status, _ := e.fake.DeliverGUID(hook, "test_event", map[string]any{"action": "x"}, guid)
	assert.Equal(t, http.StatusAccepted, status)
	e.drain()
	_, _ = e.fake.DeliverGUID(hook, "test_event", map[string]any{"action": "x"}, guid)
	e.drain()
	assert.EqualValues(t, 1, calls.Load())
	assert.Equal(t, "handled", e.delivery(guid).Outcome)

	g := e.deliver("no_such_event", map[string]any{})
	assert.Equal(t, "ignored:event", e.delivery(g).Outcome)
}

func TestRepositoryRenamedEvent(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.widgets()
	p := e.project("https://github.com/acme/widgets")
	require.NoError(t, e.s.BindProject(e.ctx, p))
	e.fake.Rename("Acme/Widgets", "acme/gadgets")
	g := e.deliver("repository", map[string]any{
		"action": "renamed", "repository": map[string]any{"id": 42, "full_name": "acme/gadgets"},
	})
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, "https://github.com/acme/gadgets", *e.getProject(p).RepoUrl)

	g = e.deliver("repository", map[string]any{"action": "edited", "repository": map[string]any{"id": 42}})
	assert.Equal(t, "ignored:action", e.delivery(g).Outcome)
}

func TestAppAuthorizationRevoked(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.fake.AddUser(7, "owner", "")
	require.NoError(t, e.connect(slotUser, "c1", "owner"))
	g := e.deliver("github_app_authorization", map[string]any{"action": "revoked", "sender": map[string]any{"login": "Owner"}})
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	row, err := e.st.Q.GetGithubIdentity(e.ctx, slotUser)
	require.NoError(t, err)
	assert.Equal(t, "reauth", row.Status)
}

func TestRunRecoversPending(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	e.s.cfg.PollInterval = 20 * time.Millisecond
	_, err := e.st.Q.InsertGithubDelivery(e.ctx, db.InsertGithubDeliveryParams{
		Guid: "pending-1", Event: "ping", Source: "webhook", Payload: []byte(`{}`), ReceivedAt: e.clock.Now().UnixMilli(),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { e.s.Run(ctx); close(done) }()
	assert.Eventually(t, func() bool {
		d, err := e.st.Q.GetGithubDelivery(e.ctx, "pending-1")
		return err == nil && d.Outcome == "ignored:ping"
	}, 5*time.Second, 10*time.Millisecond)
	cancel()
	<-done
}

func TestDeliveryRetry(t *testing.T) {
	e := newEnv(t)
	e.installApp()
	var n atomic.Int32
	fails := int32(2)
	e.s.handlers["flaky"] = func(context.Context, string, []byte) (string, error) {
		if n.Add(1) <= fails {
			return "", &apiError{Status: 502, Message: "bad gateway"}
		}
		return "handled", nil
	}
	g := e.deliver("flaky", map[string]any{"action": "x"})
	for range 2 {
		assert.NotEqual(t, "handled", e.delivery(g).Outcome)
		e.s.recoverPending(e.ctx)
		e.drain()
	}
	d := e.delivery(g)
	assert.Equal(t, "handled", d.Outcome)
	assert.EqualValues(t, 2, d.Attempts)
	assert.EqualValues(t, 3, n.Load())

	// 连续三次可重试错误 → error
	n.Store(0)
	fails = 100
	g = e.deliver("flaky", map[string]any{"action": "x"})
	for range 5 {
		e.s.recoverPending(e.ctx)
		e.drain()
	}
	d = e.delivery(g)
	assert.Regexp(t, `^error:`, d.Outcome)
	assert.EqualValues(t, 3, d.Attempts)
	assert.EqualValues(t, 3, n.Load())

	// 不可重试的错误直接失败
	e.s.handlers["broken"] = func(context.Context, string, []byte) (string, error) { return "", errf("not_found", "x") }
	g = e.deliver("broken", map[string]any{})
	assert.Equal(t, "error:github: not_found: x", e.delivery(g).Outcome)
}

func TestEnqueueFullQueue(t *testing.T) {
	e := newEnv(t)
	for range jobQueueSize {
		require.True(t, e.s.enqueue(func(context.Context) {}))
	}
	e.s.enqueueDelivery("g-full", "ping", []byte(`{}`))
	e.s.qmu.Lock()
	assert.False(t, e.s.inflight["g-full"])
	e.s.qmu.Unlock()
	e.drain()
}
