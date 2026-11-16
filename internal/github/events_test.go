package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	body := []byte(`{"zen":"x"}`)
	h := http.Header{}
	h.Set("X-GitHub-Delivery", "g1")
	h.Set("X-GitHub-Event", "ping")
	h.Set("X-Hub-Signature-256", e.fake.Sign(body))
	// 没有 App
	assert.Equal(t, http.StatusNotFound, e.s.HandleWebhook(e.ctx, h, body))

	e.installApp()
	bad := h.Clone()
	bad.Set("X-Hub-Signature-256", e.fake.Sign([]byte(`{"zen":"y"}`)))
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	bad.Set("X-Hub-Signature-256", "sha1=00")
	assert.Equal(t, http.StatusUnauthorized, e.s.HandleWebhook(e.ctx, bad, body))
	_, err := e.st.Q.GetGithubDelivery(e.ctx, "g1")
	assert.True(t, isNoRows(err))

	noGUID := h.Clone()
	noGUID.Del("X-GitHub-Delivery")
	assert.Equal(t, http.StatusBadRequest, e.s.HandleWebhook(e.ctx, noGUID, body))

	assert.Equal(t, http.StatusAccepted, e.s.HandleWebhook(e.ctx, h, body))
	e.drain()
	d := e.delivery("g1")
	assert.Equal(t, "ignored:ping", d.Outcome)
	assert.Equal(t, "webhook", d.Source)
	assert.Equal(t, body, d.Payload, "M15：结束后保留载荷，供 replay 重放")
	app, err := e.st.Q.GetGithubApp(e.ctx)
	require.NoError(t, err)
	require.NotNil(t, app.LastDeliverySrc)
	assert.Equal(t, "webhook", *app.LastDeliverySrc)
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
