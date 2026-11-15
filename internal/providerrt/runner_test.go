package providerrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/jobs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const fakeOrbd = `#!/bin/sh
echo "$ORYN_ORB_ID $ORYN_URL" > "$HOME/started"
printf '%s' "${ORYN_ORB_CA_PEM-unset}" > "$HOME/ca"
trap 'echo hup >> "$HOME/hup"' HUP
trap 'exit 0' TERM
while true; do sleep 0.05; done
`

func procState(t *testing.T, pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "gone"
	}
	s := string(b)
	return strings.Fields(s[strings.LastIndexByte(s, ')')+1:])[0]
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 20*time.Millisecond, msg)
}

func TestProcessDriver(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("需要 /proc")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "fake-orbd")
	require.NoError(t, os.WriteFile(bin, []byte(fakeOrbd), 0o755))
	p := NewProcess(filepath.Join(root, "data"), bin, config.Docker{})
	p.StopTimeout = 3 * time.Second
	t.Cleanup(p.Close)
	ctx := context.Background()
	out := io.Discard
	spawn := Job{Op: "spawn", OrbID: "orb-p", Spawn: &apigen.JobSpawnParams{Type: "small", Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_p"}}}

	res0, err := p.Exec(ctx, spawn, out)
	require.NoError(t, err)
	assert.Nil(t, res0, "没请求快照 → 不给 snapshotUsed")
	home := p.HomeDir("orb-p")
	eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(home, "started"))
		return string(b) == "orb-p http://u\n"
	}, "orbd started with env")
	eventually(t, func() bool { b, _ := os.ReadFile(filepath.Join(home, "ca")); return string(b) == "unset" }, "没有 orbCaPem 时不设 ORYN_ORB_CA_PEM")
	pid := p.procs["orb-p"].cmd.Process.Pid
	tokenFile := filepath.Join(home, ".oryn", "orb-token")
	readToken := func() string {
		b, err := os.ReadFile(tokenFile)
		require.NoError(t, err)
		info, err := os.Stat(tokenFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		return string(b)
	}
	assert.Equal(t, "oot_p", readToken(), "启动前写入令牌文件")
	hupped := func() bool { _, err := os.Stat(filepath.Join(home, "hup")); return err == nil }

	// 已在运行：同一进程，写入轮换后的令牌并 SIGHUP。
	spawn.Spawn.Env.OrynOrbToken = "oot_p2"
	require.NoError(t, execErr(p, ctx, spawn, out))
	assert.Equal(t, pid, p.procs["orb-p"].cmd.Process.Pid)
	assert.Equal(t, "oot_p2", readToken())
	eventually(t, hupped, "SIGHUP after token rotation")
	require.NoError(t, os.Remove(filepath.Join(home, "hup")))

	require.NoError(t, execErr(p, ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "freeze"}, out))
	eventually(t, func() bool { return procState(t, pid) == "T" }, "SIGSTOP")
	spawn.Spawn.Env.OrynOrbToken = "oot_p3"
	require.NoError(t, execErr(p, ctx, spawn, out))
	eventually(t, func() bool { return procState(t, pid) != "T" }, "SIGCONT")
	eventually(t, hupped, "SIGHUP after unfreeze")
	assert.Equal(t, "oot_p3", readToken())

	require.NoError(t, os.WriteFile(filepath.Join(home, "marker"), []byte("m"), 0o600))
	res, err := p.Exec(ctx, Job{Op: "snapshot", OrbID: "orb-p", SnapshotID: "oryn-snap-p"}, out)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotBytes)
	var want int64
	require.NoError(t, filepath.Walk(filepath.Join(root, "data", "snapshots", "oryn-snap-p"), func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			want += fi.Size()
		}
		return err
	}))
	assert.Positive(t, want)
	assert.Equal(t, want, *res.SnapshotBytes, "snapshotBytes = 快照目录的字节数")
	assert.Equal(t, "process", p.Info().BaseImageID)

	require.NoError(t, execErr(p, ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "stop"}, out))
	assert.Equal(t, "gone", procState(t, pid))
	require.NoError(t, execErr(p, ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "stop"}, out), "stop idempotent")

	snap := "oryn-snap-p"
	pem := "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	clone := Job{Op: "spawn", OrbID: "orb-q", Spawn: &apigen.JobSpawnParams{SnapshotID: &snap, OrbCaPem: &pem, Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_q"}}}
	res, err = p.Exec(ctx, clone, out)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotUsed)
	assert.True(t, *res.SnapshotUsed, "HOME 来自快照目录")
	eventually(t, func() bool { b, _ := os.ReadFile(filepath.Join(p.HomeDir("orb-q"), "ca")); return string(b) == pem }, "orbCaPem 作为 ORYN_ORB_CA_PEM 下发")
	// 再次 spawn（运行中 / 停机后）：HOME 仍是当初从快照复制来的。
	res, err = p.Exec(ctx, clone, out)
	require.NoError(t, err)
	assert.True(t, *res.SnapshotUsed)
	require.NoError(t, execErr(p, ctx, Job{Op: "pause", OrbID: "orb-q", PauseMode: "stop"}, out))
	res, err = p.Exec(ctx, clone, out)
	require.NoError(t, err)
	assert.True(t, *res.SnapshotUsed)
	// 快照里带着源 orb 的令牌文件：克隆启动前必须换成自己的，否则会以源 orb 的身份连上控制面。
	tb, err := os.ReadFile(filepath.Join(p.HomeDir("orb-q"), ".oryn", "orb-token"))
	require.NoError(t, err)
	assert.Equal(t, "oot_q", string(tb))
	b, err := os.ReadFile(filepath.Join(p.HomeDir("orb-q"), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "m", string(b))
	info, err := os.Stat(filepath.Join(p.HomeDir("orb-q"), "marker"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, execErr(p, ctx, Job{Op: "delete", OrbID: "orb-q"}, out))
	_, err = os.Stat(p.HomeDir("orb-q"))
	assert.True(t, errors.Is(err, os.ErrNotExist))

	// 快照目录不存在：snapshotOptional 时从空 HOME 起并说明；否则失败。
	missing, yes := "oryn-snap-missing", true
	var buf bytes.Buffer
	res, err = p.Exec(ctx, Job{Op: "spawn", OrbID: "orb-r", Spawn: &apigen.JobSpawnParams{SnapshotID: &missing, SnapshotOptional: &yes,
		Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_r"}}}, &buf)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.SnapshotUsed)
	assert.False(t, *res.SnapshotUsed, "快照缺失 → 退回空 HOME")
	assert.Contains(t, buf.String(), "oryn: 快照 oryn-snap-missing 不存在，改用基础镜像")
	// orb-r 停机后再以同一快照 spawn：HOME 不是从快照来的，仍为 false。
	require.NoError(t, execErr(p, ctx, Job{Op: "pause", OrbID: "orb-r", PauseMode: "stop"}, out))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "data", "snapshots", missing), 0o755))
	res, err = p.Exec(ctx, Job{Op: "spawn", OrbID: "orb-r", Spawn: &apigen.JobSpawnParams{SnapshotID: &missing, SnapshotOptional: &yes,
		Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_r"}}}, out)
	require.NoError(t, err)
	assert.False(t, *res.SnapshotUsed)
	require.NoError(t, os.RemoveAll(filepath.Join(root, "data", "snapshots", missing)))
	_, err = os.Stat(filepath.Join(p.HomeDir("orb-r"), "marker"))
	assert.True(t, errors.Is(err, os.ErrNotExist))
	err = execErr(p, ctx, Job{Op: "spawn", OrbID: "orb-s", Spawn: &apigen.JobSpawnParams{SnapshotID: &missing,
		Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_s"}}}, out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oryn-snap-missing")

	require.NoError(t, execErr(p, ctx, Job{Op: "delete_snapshot", SnapshotID: snap}, out))
	_, err = os.Stat(filepath.Join(root, "data", "snapshots", snap))
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// recDriver 记录收到的任务；op=delete 始终失败；op=snapshot 上报 snapshotBytes=42。
type recDriver struct {
	mu   sync.Mutex
	jobs []Job
}

func (d *recDriver) Info() Info {
	return Info{Driver: apigen.Process, Types: []apigen.OrbType{{Name: "small", Cpus: 1, MemoryMB: 512}}, DefaultType: "small", BaseImageID: "img-1"}
}

func (d *recDriver) Exec(_ context.Context, j Job, out io.Writer) (*apigen.JobResult, error) {
	d.mu.Lock()
	d.jobs = append(d.jobs, j)
	d.mu.Unlock()
	io.WriteString(out, "did "+j.Op)
	switch j.Op {
	case "delete":
		return nil, errors.New("nope")
	case "snapshot":
		n := int64(42)
		return &apigen.JobResult{SnapshotBytes: &n}, nil
	}
	return nil, nil
}

// hello 带 baseImageId；complete 带驱动给出的 result。
func TestRunnerReportsBaseImageAndResult(t *testing.T) {
	var (
		mu      sync.Mutex
		hellos  []apigen.ProviderHelloRequest
		claimed bool
	)
	completed := make(chan apigen.ProviderCompleteRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/provider/v1/hello":
			var h apigen.ProviderHelloRequest
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&h))
			mu.Lock()
			hellos = append(hellos, h)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(apigen.ProviderHelloResponse{ProviderID: "prov_x", LeaseSeconds: 45})
		case req.URL.Path == "/provider/v1/jobs/claim":
			mu.Lock()
			first := !claimed
			claimed = true
			mu.Unlock()
			if !first {
				<-req.Context().Done() // 之后的长轮询一直挂到 runner 退出
				return
			}
			_ = json.NewEncoder(w).Encode(apigen.ProviderClaimResponse{Job: apigen.ProviderJob{
				ID: "job_1", Op: apigen.Snapshot, OrbID: "orb-a", Attempt: 1, ClaimGen: 1, Params: map[string]any{"snapshotId": "oryn-snap-a"},
			}})
		case req.URL.Path == "/provider/v1/jobs/job_1/complete":
			var c apigen.ProviderCompleteRequest
			assert.NoError(t, json.NewDecoder(req.Body).Decode(&c))
			w.WriteHeader(http.StatusNoContent)
			completed <- c
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	r := &Runner{URL: srv.URL, Token: "opv_x", Driver: &recDriver{}, Version: "t", Concurrency: 1, ClaimWait: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var c apigen.ProviderCompleteRequest
	select {
	case c = <-completed:
	case <-time.After(10 * time.Second):
		t.Fatal("no complete")
	}
	cancel()
	require.NoError(t, <-done)

	assert.True(t, c.Ok)
	require.NotNil(t, c.Result)
	require.NotNil(t, c.Result.SnapshotBytes)
	assert.EqualValues(t, 42, *c.Result.SnapshotBytes)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, hellos)
	require.NotNil(t, hellos[0].BaseImageID)
	assert.Equal(t, "img-1", *hellos[0].BaseImageID)
}

func TestRunnerEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	q := jobs.New(st)
	tok := ids.Token("opv")
	provID := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "p", TokenHash: ids.Hash(tok), CreatedAt: 1}))
	srv := httptest.NewServer(q.Handler())
	t.Cleanup(srv.Close)

	drv := &recDriver{}
	r := &Runner{URL: srv.URL, Token: tok, OrbURL: "http://orb-url", Driver: drv, Version: "t", ClaimWait: time.Second, HeartbeatInterval: 100 * time.Millisecond}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- r.Run(runCtx) }()

	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-a", ProviderID: provID, Type: "small", Cpus: 1, MemoryMb: 512, State: "requested", StateChangedAt: 1, TokenHash: "h", CreatedAt: 1}))
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	spawn, err := q.Enqueue(ctx, provID, "orb-a", jobs.OpSpawn, apigen.JobSpawnParams{Type: "small", Env: apigen.JobSpawnEnv{OrynURL: "http://public", OrynOrbID: "orb-a", OrynOrbToken: "oot_a"}})
	require.NoError(t, err)
	j, err := q.Wait(wctx, spawn.ID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateSucceeded, j.State)

	del, err := q.Enqueue(ctx, provID, "orb-a", jobs.OpDelete, nil)
	require.NoError(t, err)
	j, err = q.Wait(wctx, del.ID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateFailed, j.State, "driver error → retried, then failed")
	assert.EqualValues(t, 3, j.Attempt)
	assert.Equal(t, "nope", *j.Error)

	snap, err := q.Enqueue(ctx, provID, "", jobs.OpDeleteSnapshot, apigen.JobDeleteSnapshotParams{SnapshotID: "oryn-snap-a"})
	require.NoError(t, err)
	j, err = q.Wait(wctx, snap.ID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateSucceeded, j.State)
	assert.Equal(t, "did delete_snapshot", j.Output)

	p, err := st.Q.GetProvider(ctx, provID)
	require.NoError(t, err)
	require.NotNil(t, p.InstanceID)
	assert.Equal(t, r.InstanceID(), *p.InstanceID)
	assert.Equal(t, "process", p.Driver)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("runner did not stop")
	}
	p, err = st.Q.GetProvider(ctx, provID)
	require.NoError(t, err)
	assert.Nil(t, p.InstanceID, "bye released the lease")
	drv.mu.Lock()
	defer drv.mu.Unlock()
	var ops []string
	for _, j := range drv.jobs {
		ops = append(ops, j.Op)
	}
	assert.Equal(t, []string{"spawn", "delete", "delete", "delete", "delete_snapshot"}, ops)
	assert.Equal(t, "http://orb-url", drv.jobs[0].Spawn.Env.OrynURL, "orb_url overrides ORYN_URL")
	assert.Equal(t, "oot_a", drv.jobs[0].Spawn.Env.OrynOrbToken)
}

func TestRunnerUnauthorized(t *testing.T) {
	drv := &recDriver{}
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(jobs.New(st).Handler())
	t.Cleanup(srv.Close)
	bad := &Runner{URL: srv.URL, Token: "opv_bad", Driver: drv}
	assert.ErrorIs(t, bad.Run(ctx), ErrUnauthorized)
}

// CP17：运行中的 provider 令牌被吊销后，Run 停止全部循环并返回 ErrUnauthorized（不会因心跳而永远挂着）。
func TestRunnerStopsWhenTokenRevoked(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	tok := ids.Token("opv")
	provID := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "p", TokenHash: ids.Hash(tok), CreatedAt: 1}))
	srv := httptest.NewServer(jobs.New(st).Handler())
	t.Cleanup(srv.Close)
	r := &Runner{URL: srv.URL, Token: tok, Driver: &recDriver{}, ClaimWait: time.Second, HeartbeatInterval: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	eventually(t, func() bool {
		p, err := st.Q.GetProvider(ctx, provID)
		return err == nil && p.InstanceID != nil
	}, "provider hello")
	_, err = st.Q.DisableProvider(ctx, db.DisableProviderParams{DisabledAt: ptr(int64(1)), ID: provID})
	require.NoError(t, err)
	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrUnauthorized)
	case <-time.After(10 * time.Second):
		t.Fatal("令牌吊销后 Run 没有返回")
	}
}

func ptr[T any](v T) *T { return &v }
