package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type fixture struct {
	st    *store.Store
	q     *Queue
	srv   *httptest.Server
	prov  db.Provider
	token string

	mu       sync.Mutex
	claimed  []db.Job
	finished []db.Job
	oks      []bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, q: New(st), token: ids.Token("opv")}
	id := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: id, Name: "local", TokenHash: ids.Hash(f.token), CreatedAt: time.Now().UnixMilli()}))
	f.prov, err = st.Q.GetProvider(ctx, id)
	require.NoError(t, err)
	f.q.SetHooks(Hooks{
		OnClaimed: func(_ context.Context, j db.Job) {
			f.mu.Lock()
			f.claimed = append(f.claimed, j)
			f.mu.Unlock()
		},
		OnFinished: func(_ context.Context, j db.Job, ok bool) {
			f.mu.Lock()
			f.finished = append(f.finished, j)
			f.oks = append(f.oks, ok)
			f.mu.Unlock()
		},
	})
	f.srv = httptest.NewServer(f.q.Handler())
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) post(t *testing.T, path string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+f.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
	return resp.StatusCode
}

func hello(instance string) apigen.ProviderHelloRequest {
	return apigen.ProviderHelloRequest{
		InstanceID: instance, Driver: apigen.Docker, Version: "test",
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true},
		Types:        []apigen.OrbType{{Name: "small", Cpus: 2, MemoryMB: 4096}}, DefaultType: "small",
	}
}

func TestHelloLeaseAndBye(t *testing.T) {
	f := newFixture(t)
	var res apigen.ProviderHelloResponse
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), &res))
	assert.Equal(t, f.prov.ID, res.ProviderID)
	assert.Equal(t, 45, res.LeaseSeconds)

	p, err := f.st.Q.GetProvider(context.Background(), f.prov.ID)
	require.NoError(t, err)
	assert.Equal(t, "docker", p.Driver)
	assert.JSONEq(t, `[{"name":"small","cpus":2,"memoryMB":4096}]`, p.OrbTypes)

	// 另一实例：租约未过期 → 409。
	assert.Equal(t, http.StatusConflict, f.post(t, "/provider/v1/hello", hello("b"), nil))
	assert.Equal(t, http.StatusConflict, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "b"}, nil))
	// 同一实例重复 hello 视为续租。
	assert.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	// bye 后另一实例可接管。
	assert.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/bye", map[string]string{"instanceId": "a"}, nil))
	assert.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("b"), nil))

	// 租约过期后也可接管。
	f.q.Now = func() time.Time { return time.Now().Add(time.Minute) }
	assert.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("c"), nil))

	// 声明 allowMultiple 的两个实例可并存。
	f.q.Now = time.Now
	m := hello("c")
	m.AllowMultiple = true
	assert.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", m, nil))
	m.InstanceID = "d"
	assert.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", m, nil))
}

func TestUnauthorized(t *testing.T) {
	f := newFixture(t)
	f.token = "opv_nope"
	assert.Equal(t, http.StatusUnauthorized, f.post(t, "/provider/v1/hello", hello("a"), nil))
	f.token = "odt_x"
	assert.Equal(t, http.StatusUnauthorized, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a"}, nil))
}

func TestClaimCompleteAndWait(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))

	// 空队列：超时 204。
	start := time.Now()
	assert.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a", WaitSeconds: 1}, nil))
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)

	// 长轮询中途入队 → 立即返回。
	type claimRes struct {
		code int
		res  apigen.ProviderClaimResponse
	}
	got := make(chan claimRes, 1)
	go func() {
		var r apigen.ProviderClaimResponse
		code := f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a", WaitSeconds: 10}, &r)
		got <- claimRes{code, r}
	}()
	time.Sleep(150 * time.Millisecond)
	params := apigen.JobSpawnParams{Image: "img", Type: "small", Env: apigen.JobSpawnEnv{OrynURL: "http://x", OrynOrbID: "orb-1", OrynOrbToken: "oot_secret"}}
	job, err := f.q.Enqueue(ctx, f.prov.ID, "", OpSpawn, params)
	require.NoError(t, err)
	var c claimRes
	select {
	case c = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("long poll did not return")
	}
	require.Equal(t, http.StatusOK, c.code)
	assert.Equal(t, job.ID, c.res.Job.ID)
	assert.Equal(t, apigen.Spawn, c.res.Job.Op)
	assert.Equal(t, 1, c.res.Job.Attempt)
	assert.Equal(t, "oot_secret", c.res.Job.Params["env"].(map[string]any)["ORYN_ORB_TOKEN"])

	waited := make(chan db.Job, 1)
	go func() {
		j, err := f.q.Wait(ctx, job.ID)
		assert.NoError(t, err)
		waited <- j
	}()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/"+job.ID+"/complete", apigen.ProviderCompleteRequest{Ok: true, Output: "done", DurationMs: 5}, nil))
	select {
	case j := <-waited:
		assert.Equal(t, StateSucceeded, j.State)
		assert.Equal(t, "done", j.Output)
		assert.NotContains(t, j.Params, "oot_secret", "token scrubbed after finish")
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return")
	}
	// 已完成的任务再上报 → 409；Wait 已终态任务立即返回。
	assert.Equal(t, http.StatusConflict, f.post(t, "/provider/v1/jobs/"+job.ID+"/complete", apigen.ProviderCompleteRequest{Ok: true}, nil))
	assert.Equal(t, http.StatusNotFound, f.post(t, "/provider/v1/jobs/job_nope/complete", apigen.ProviderCompleteRequest{Ok: true}, nil))
	j, err := f.q.Wait(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateSucceeded, j.State)

	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.claimed, 1)
	require.Len(t, f.finished, 1)
	assert.True(t, f.oks[0])
}

func TestRetryUntilMaxAttempts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/hello", hello("a"), nil))
	job, err := f.q.Enqueue(ctx, f.prov.ID, "", OpDelete, nil)
	require.NoError(t, err)
	for attempt := 1; attempt <= 3; attempt++ {
		var r apigen.ProviderClaimResponse
		require.Equal(t, http.StatusOK, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a", WaitSeconds: 1}, &r))
		assert.Equal(t, attempt, r.Job.Attempt)
		e := "boom"
		require.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/"+job.ID+"/complete", apigen.ProviderCompleteRequest{Ok: false, Error: &e}, nil))
	}
	j, err := f.q.Wait(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, j.State)
	assert.EqualValues(t, 3, j.Attempt)
	assert.Equal(t, "boom", *j.Error)
	assert.Equal(t, http.StatusNoContent, f.post(t, "/provider/v1/jobs/claim", apigen.ProviderClaimRequest{InstanceID: "a", WaitSeconds: 0}, nil))
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Len(t, f.claimed, 3)
	require.Len(t, f.finished, 1)
	assert.False(t, f.oks[0])
}

func TestLeaseExpiryRequeue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.q.ClaimLease = 20 * time.Millisecond
	job, err := f.q.Enqueue(ctx, f.prov.ID, "", OpSnapshot, apigen.JobSnapshotParams{SnapshotID: "oryn-snap-x"})
	require.NoError(t, err)
	c, ok, err := f.q.Claim(ctx, f.prov.ID, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, job.ID, c.ID)
	time.Sleep(40 * time.Millisecond)
	require.NoError(t, f.q.Sweep(ctx))
	j, err := f.st.Q.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateQueued, j.State)
	// 过期后迟到的完成上报被拒绝。
	assert.ErrorIs(t, f.q.Complete(ctx, f.prov.ID, job.ID, Result{OK: true}), ErrNotClaimed)

	for range 2 {
		_, ok, err = f.q.Claim(ctx, f.prov.ID, 0)
		require.NoError(t, err)
		require.True(t, ok)
		time.Sleep(40 * time.Millisecond)
		require.NoError(t, f.q.Sweep(ctx))
	}
	j, err = f.st.Q.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, j.State)
	assert.EqualValues(t, 3, j.Attempt)
}

func TestTailOutput(t *testing.T) {
	s := string(bytes.Repeat([]byte("界"), MaxOutputBytes))
	out := TailOutput(s)
	assert.LessOrEqual(t, len(out), MaxOutputBytes)
	assert.True(t, len(out) > MaxOutputBytes-4)
	assert.Equal(t, "界", out[:3])
	assert.Equal(t, "abc", TailOutput("abc"))
}

func TestPerOrbSerialization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, id := range []string{"orb-a", "orb-b"} {
		require.NoError(t, f.st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: id, ProviderID: f.prov.ID, Type: "s", State: "requested", StateChangedAt: 1, TokenHash: id, CreatedAt: 1}))
	}
	a1, err := f.q.Enqueue(ctx, f.prov.ID, "orb-a", OpSpawn, nil)
	require.NoError(t, err)
	a2, err := f.q.Enqueue(ctx, f.prov.ID, "orb-a", OpDelete, nil)
	require.NoError(t, err)
	b1, err := f.q.Enqueue(ctx, f.prov.ID, "orb-b", OpSpawn, nil)
	require.NoError(t, err)

	j, ok, err := f.q.Claim(ctx, f.prov.ID, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, a1.ID, j.ID)
	// orb-a 有在途任务：下一条应为 orb-b 的任务，而不是 a2。
	j, ok, err = f.q.Claim(ctx, f.prov.ID, 0)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, b1.ID, j.ID)
	_, ok, err = f.q.Claim(ctx, f.prov.ID, 0)
	require.NoError(t, err)
	assert.False(t, ok, "a2 blocked while a1 is claimed")

	// 长轮询中的认领在 a1 完成后立即拿到 a2。
	got := make(chan string, 1)
	go func() {
		j, ok, err := f.q.Claim(ctx, f.prov.ID, 5*time.Second)
		if err == nil && ok {
			got <- j.ID
		} else {
			got <- ""
		}
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, f.q.Complete(ctx, f.prov.ID, a1.ID, Result{OK: true}))
	select {
	case id := <-got:
		assert.Equal(t, a2.ID, id)
	case <-time.After(5 * time.Second):
		t.Fatal("a2 not claimed after a1 finished")
	}
}
