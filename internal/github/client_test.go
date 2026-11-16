package github

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/github/githubtest"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/version"
)

const testPublicURL = "https://oryn.test"

// testClock 是可拨动的时钟。
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stubThreads 是线程服务桩。
type stubThreads struct {
	mu        sync.Mutex
	creates   []threads.CreateParams
	delivered []string
	published []string
	shipped   map[string]int64
	aborted   []string
}

func (t *stubThreads) Create(_ context.Context, p threads.CreateParams) (apigen.Thread, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.creates = append(t.creates, p)
	return apigen.Thread{}, nil
}

func (t *stubThreads) Deliver(_ context.Context, threadID string, m threads.AutoMessage, _ func(error)) (func() bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.delivered = append(t.delivered, threadID+"\n"+m.Text)
	return func() bool { return false }, nil
}

func (t *stubThreads) Publish(_ context.Context, threadID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.published = append(t.published, threadID)
}

func (t *stubThreads) MarkShipped(_ context.Context, threadID string, at int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.shipped == nil {
		t.shipped = map[string]int64{}
	}
	t.shipped[threadID] = at
	return nil
}

type testEnv struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	fake    *githubtest.Server
	clock   *testClock
	th      *stubThreads
	s       *Service
	changes int
	chMu    sync.Mutex
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	fake := githubtest.New()
	t.Cleanup(fake.Close)
	e := &testEnv{t: t, ctx: ctx, st: st, fake: fake, clock: &testClock{t: time.Now()}, th: &stubThreads{}}
	e.s = New(Config{
		PublicURL: testPublicURL, APIURL: fake.URL(), WebURL: fake.URL(), Box: box, Now: e.clock.Now,
		Changed: func() { e.chMu.Lock(); e.changes++; e.chMu.Unlock() },
	}, st, e.th, nil, nil)
	return e
}

func TestCallUpstreamError(t *testing.T) {
	e := newEnv(t)
	err := e.s.callAs(e.ctx, "", http.MethodGet, "/no/such/path", nil, nil)
	var ae *apiError
	require.ErrorAs(t, err, &ae)
	assert.Equal(t, 404, ae.Status)
	assert.False(t, retryable(err))
	assert.True(t, IsUpstream(err))
	assert.True(t, retryable(&apiError{Status: 502}))
	assert.True(t, retryable(&apiError{Status: 429}))

	// 公共头
	c := e.fake.CallsTo("GET", "/no/such/path")
	require.Len(t, c, 1)
}

func TestGraphQLAndHeaders(t *testing.T) {
	e := newEnv(t)
	var got http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"boom"}]}`))
	}))
	defer up.Close()
	e.s.cfg.APIURL = up.URL
	err := e.s.graphql(e.ctx, "", "query{viewer{login}}", nil, nil)
	require.ErrorContains(t, err, "boom")
	assert.Equal(t, "application/vnd.github+json", got.Get("Accept"))
	assert.Equal(t, "2022-11-28", got.Get("X-GitHub-Api-Version"))
	assert.Equal(t, "oryn/"+version.Version, got.Get("User-Agent"))
	assert.Empty(t, got.Get("Authorization"), "不带槽位的请求不注入令牌")
}
