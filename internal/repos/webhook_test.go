package repos

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// hookReceiver 是记录型的 Webhook 接收端；handle 为 nil 时回 204。
type hookReceiver struct {
	*httptest.Server
	mu       sync.Mutex
	reqs     []hookReq
	inFlight atomic.Int32
	maxIn    atomic.Int32
	handle   func(w http.ResponseWriter, r *http.Request)
}

type hookReq struct {
	header http.Header
	body   []byte
}

func newHookReceiver(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *hookReceiver {
	t.Helper()
	h := &hookReceiver{handle: handle}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := h.inFlight.Add(1)
		defer h.inFlight.Add(-1)
		for {
			m := h.maxIn.Load()
			if n <= m || h.maxIn.CompareAndSwap(m, n) {
				break
			}
		}
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, hookReq{header: r.Header.Clone(), body: body})
		h.mu.Unlock()
		if h.handle != nil {
			h.handle(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookReceiver) got() []hookReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookReq(nil), h.reqs...)
}

// hookEnv 是带密钥盒的托管项目环境。
type hookEnv struct {
	*testEnv
	prj db.Project
}

func newHookEnv(t *testing.T, timeout time.Duration) *hookEnv {
	t.Helper()
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	e := newTestEnv(t, func(c *Config) {
		c.Box = box
		c.WebhookTimeout = timeout
		c.PublicURL = "https://oryn.test"
	}, nil, nil)
	ctx := context.Background()
	id := e.hostedProject(t, "main")
	_, err = e.svc.InitHosted(ctx, id, "main", true, testIdentity)
	require.NoError(t, err)
	require.NoError(t, e.svc.RecordHosted(ctx, id, "main"))
	prj, err := e.st.Q.GetProject(ctx, id)
	require.NoError(t, err)
	return &hookEnv{testEnv: e, prj: prj}
}

func (e *hookEnv) setHook(t *testing.T, url, secret string) {
	t.Helper()
	var sealed []byte
	if secret != "" {
		var err error
		sealed, err = e.svc.SealWebhookSecret(e.prj.ID, secret)
		require.NoError(t, err)
	}
	require.NoError(t, e.st.Q.UpsertProjectWebhook(context.Background(), db.UpsertProjectWebhookParams{
		ProjectID: e.prj.ID, Url: url, SecretEnc: sealed, UpdatedAt: time.Now().UnixMilli(),
	}))
}

func (e *hookEnv) hook(t *testing.T) db.ProjectWebhook {
	t.Helper()
	h, err := e.st.Q.GetProjectWebhook(context.Background(), e.prj.ID)
	require.NoError(t, err)
	return h
}

// waitDelivered 等到最近投递被记下（last_delivery_at 非空）。
func (e *hookEnv) waitDelivered(t *testing.T) db.ProjectWebhook {
	t.Helper()
	var h db.ProjectWebhook
	require.Eventually(t, func() bool {
		h = e.hook(t)
		return h.LastDeliveryAt != nil
	}, 10*time.Second, 20*time.Millisecond)
	return h
}

var (
	sha1 = strings.Repeat("a", 40)
	sha2 = strings.Repeat("b", 40)
	zero = strings.Repeat("0", 40)
)

func TestWebhookSignedThreadPush(t *testing.T) {
	e := newHookEnv(t, 0)
	recv := newHookReceiver(t, nil)
	e.setHook(t, recv.URL+"/hook", "s3cret")
	updates := []egress.RefUpdate{{Ref: "refs/heads/feat", Old: sha1, New: zero}, {Ref: "refs/heads/main", Old: sha1, New: sha2}}
	at := time.UnixMilli(1795000000000)
	e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{OrbID: "orb-1", ThreadID: "T-1"}, updates, at)

	h := e.waitDelivered(t)
	require.Len(t, recv.got(), 1)
	assert.Equal(t, int64(204), *h.LastStatus)
	assert.Nil(t, h.LastError)
	req := recv.got()[0]
	assert.Equal(t, "application/json", req.header.Get("Content-Type"))
	assert.Equal(t, "push", req.header.Get("X-Oryn-Event"))
	assert.True(t, strings.HasPrefix(req.header.Get("User-Agent"), "Oryn-Webhook/"))
	assert.Equal(t, "sha256="+WebhookSignature([]byte("s3cret"), req.body), req.header.Get("X-Oryn-Signature-256"))

	var p webhookPayload
	require.NoError(t, json.Unmarshal(req.body, &p))
	assert.Equal(t, req.header.Get("X-Oryn-Delivery"), p.DeliveryID)
	assert.True(t, strings.HasPrefix(p.DeliveryID, "whd_"))
	assert.Equal(t, "push", p.Event)
	assert.Equal(t, int64(1795000000000), p.PushedAt)
	assert.Equal(t, webhookProject{ID: e.prj.ID, Name: e.prj.Name, CloneURL: "https://oryn.test/git/" + e.prj.ID + ".git"}, p.Project)
	assert.Equal(t, webhookPusher{Kind: "thread", ThreadID: "T-1"}, p.Pusher)
	assert.Equal(t, []webhookRefJSON{{"refs/heads/feat", sha1, zero}, {"refs/heads/main", sha1, sha2}}, p.Refs)
}

func TestWebhookUnsignedOwnerPush(t *testing.T) {
	e := newHookEnv(t, 0)
	recv := newHookReceiver(t, nil)
	e.setHook(t, recv.URL, "")
	e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, []egress.RefUpdate{{Ref: "refs/heads/main", Old: sha1, New: sha2}}, time.Now())
	e.waitDelivered(t)
	req := recv.got()[0]
	assert.Empty(t, req.header.Values("X-Oryn-Signature-256"))
	var raw map[string]any
	require.NoError(t, json.Unmarshal(req.body, &raw))
	assert.Equal(t, map[string]any{"kind": "owner"}, raw["pusher"])

	// orb 推送但没有唯一线程：kind=thread，不带 threadId
	e.exec(t, `UPDATE project_webhooks SET last_delivery_at = NULL WHERE project_id = ?`, e.prj.ID)
	e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{OrbID: "orb-1"}, []egress.RefUpdate{{Ref: "refs/heads/main", Old: sha1, New: sha2}}, time.Now())
	e.waitDelivered(t)
	require.Len(t, recv.got(), 2)
	require.NoError(t, json.Unmarshal(recv.got()[1].body, &raw))
	assert.Equal(t, map[string]any{"kind": "thread"}, raw["pusher"])
}

func TestWebhookNoHookNoDelivery(t *testing.T) {
	e := newHookEnv(t, 0)
	e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, []egress.RefUpdate{{Ref: "refs/heads/main", Old: sha1, New: sha2}}, time.Now())
	e.svc.hooks.mu.Lock()
	defer e.svc.hooks.mu.Unlock()
	assert.Empty(t, e.svc.hooks.q)
}

func TestWebhookFailures(t *testing.T) {
	push := []egress.RefUpdate{{Ref: "refs/heads/main", Old: sha1, New: sha2}}
	t.Run("http 500", func(t *testing.T) {
		e := newHookEnv(t, 0)
		recv := newHookReceiver(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) })
		e.setHook(t, recv.URL, "")
		e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, push, time.Now())
		h := e.waitDelivered(t)
		assert.Equal(t, int64(500), *h.LastStatus)
		assert.Equal(t, "HTTP 500", *h.LastError)
	})
	t.Run("redirect not followed", func(t *testing.T) {
		e := newHookEnv(t, 0)
		recv := newHookReceiver(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		})
		e.setHook(t, recv.URL, "")
		e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, push, time.Now())
		h := e.waitDelivered(t)
		assert.Equal(t, int64(302), *h.LastStatus)
		assert.Len(t, recv.got(), 1)
	})
	t.Run("timeout", func(t *testing.T) {
		e := newHookEnv(t, 200*time.Millisecond)
		recv := newHookReceiver(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
		})
		e.setHook(t, recv.URL, "")
		e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, push, time.Now())
		h := e.waitDelivered(t)
		assert.Equal(t, int64(0), *h.LastStatus)
		assert.NotEmpty(t, *h.LastError)
	})
	t.Run("secret undecryptable", func(t *testing.T) {
		e := newHookEnv(t, 0)
		recv := newHookReceiver(t, nil)
		e.setHook(t, recv.URL, "s3cret")
		e.exec(t, `UPDATE project_webhooks SET secret_enc = x'00112233' WHERE project_id = ?`, e.prj.ID)
		e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, push, time.Now())
		h := e.waitDelivered(t)
		assert.Equal(t, int64(0), *h.LastStatus)
		assert.Equal(t, webhookBadSecret, *h.LastError)
		assert.Empty(t, recv.got())
	})
}

func TestWebhookQueueFull(t *testing.T) {
	e := newHookEnv(t, 0)
	var changed atomic.Int32
	e.svc.cfg.Changed = func(string) { changed.Add(1) }
	recv := newHookReceiver(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	e.setHook(t, recv.URL, "")
	push := []egress.RefUpdate{{Ref: "refs/heads/main", Old: sha1, New: sha2}}
	for range 12 {
		e.svc.queueWebhook(context.Background(), e.prj, egress.HostedPusher{}, push, time.Now())
	}
	// 丢弃是同步记下的：此时首个投递还在途（接收端要睡 300 ms）
	h := e.hook(t)
	require.NotNil(t, h.LastError)
	assert.Equal(t, webhookQueueFull, *h.LastError)
	assert.Equal(t, int64(0), *h.LastStatus)

	require.Eventually(t, func() bool {
		e.svc.hooks.mu.Lock()
		defer e.svc.hooks.mu.Unlock()
		return len(e.svc.hooks.q) == 0
	}, 10*time.Second, 20*time.Millisecond)
	assert.LessOrEqual(t, len(recv.got()), 9)
	assert.GreaterOrEqual(t, len(recv.got()), 1)
	assert.Equal(t, int32(1), recv.maxIn.Load())
	h = e.hook(t)
	assert.Equal(t, int64(204), *h.LastStatus)
	assert.Nil(t, h.LastError)
}

// TestWebhookHostedPush 走真实的托管推送：主人入口推送 main 后接收端收到一次 owner 事件。
func TestWebhookHostedPush(t *testing.T) {
	e := newHookEnv(t, 0)
	recv := newHookReceiver(t, nil)
	e.setHook(t, recv.URL, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.svc.ServeHostedGit(w, r, e.prj.ID, strings.TrimPrefix(r.URL.Path, "/r.git/"), egress.HostedPusher{})
	}))
	t.Cleanup(srv.Close)
	work := t.TempDir()
	gitCmd(t, "", "clone", "-q", srv.URL+"/r.git", work)
	gitCmd(t, work, "-c", "user.name=A", "-c", "user.email=a@x", "commit", "-q", "--allow-empty", "-m", "second")
	gitCmd(t, work, "push", "-q", "origin", "main")

	e.waitDelivered(t)
	require.Len(t, recv.got(), 1)
	var p webhookPayload
	require.NoError(t, json.Unmarshal(recv.got()[0].body, &p))
	assert.Equal(t, "owner", p.Pusher.Kind)
	require.Len(t, p.Refs, 1)
	assert.Equal(t, "refs/heads/main", p.Refs[0].Ref)
	assert.Equal(t, strings.TrimSpace(gitCmd(t, work, "rev-parse", "HEAD")), p.Refs[0].NewSha)
}
