package providerrt

import (
	"context"
	"errors"
	"io"
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

	require.NoError(t, p.Exec(ctx, spawn, out))
	home := p.HomeDir("orb-p")
	eventually(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(home, "started"))
		return string(b) == "orb-p http://u\n"
	}, "orbd started with env")
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
	require.NoError(t, p.Exec(ctx, spawn, out))
	assert.Equal(t, pid, p.procs["orb-p"].cmd.Process.Pid)
	assert.Equal(t, "oot_p2", readToken())
	eventually(t, hupped, "SIGHUP after token rotation")
	require.NoError(t, os.Remove(filepath.Join(home, "hup")))

	require.NoError(t, p.Exec(ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "freeze"}, out))
	eventually(t, func() bool { return procState(t, pid) == "T" }, "SIGSTOP")
	spawn.Spawn.Env.OrynOrbToken = "oot_p3"
	require.NoError(t, p.Exec(ctx, spawn, out))
	eventually(t, func() bool { return procState(t, pid) != "T" }, "SIGCONT")
	eventually(t, hupped, "SIGHUP after unfreeze")
	assert.Equal(t, "oot_p3", readToken())

	require.NoError(t, os.WriteFile(filepath.Join(home, "marker"), []byte("m"), 0o600))
	require.NoError(t, p.Exec(ctx, Job{Op: "snapshot", OrbID: "orb-p", SnapshotID: "oryn-snap-p"}, out))

	require.NoError(t, p.Exec(ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "stop"}, out))
	assert.Equal(t, "gone", procState(t, pid))
	require.NoError(t, p.Exec(ctx, Job{Op: "pause", OrbID: "orb-p", PauseMode: "stop"}, out), "stop idempotent")

	snap := "oryn-snap-p"
	clone := Job{Op: "spawn", OrbID: "orb-q", Spawn: &apigen.JobSpawnParams{SnapshotID: &snap, Env: apigen.JobSpawnEnv{OrynURL: "http://u", OrynOrbToken: "oot_q"}}}
	require.NoError(t, p.Exec(ctx, clone, out))
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

	require.NoError(t, p.Exec(ctx, Job{Op: "delete", OrbID: "orb-q"}, out))
	_, err = os.Stat(p.HomeDir("orb-q"))
	assert.True(t, errors.Is(err, os.ErrNotExist))
	require.NoError(t, p.Exec(ctx, Job{Op: "delete_snapshot", SnapshotID: snap}, out))
	_, err = os.Stat(filepath.Join(root, "data", "snapshots", snap))
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

// recDriver 记录收到的任务；op=delete 始终失败。
type recDriver struct {
	mu   sync.Mutex
	jobs []Job
}

func (d *recDriver) Info() Info {
	return Info{Driver: apigen.Process, Types: []apigen.OrbType{{Name: "small", Cpus: 1, MemoryMB: 512}}, DefaultType: "small"}
}

func (d *recDriver) Exec(_ context.Context, j Job, out io.Writer) error {
	d.mu.Lock()
	d.jobs = append(d.jobs, j)
	d.mu.Unlock()
	io.WriteString(out, "did "+j.Op)
	if j.Op == "delete" {
		return errors.New("nope")
	}
	return nil
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
