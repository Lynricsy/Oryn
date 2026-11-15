package threads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// runnerStubOrbs 在 stubOrbs 之上按库实现 RunnerOrb（与 orbs.Service.RunnerOrb 同义）。
type runnerStubOrbs struct{ *stubOrbs }

func (o runnerStubOrbs) RunnerOrb(ctx context.Context, providerID string) (orbs.Runner, error) {
	p, err := o.st.Q.GetProvider(ctx, providerID)
	if err != nil || p.Driver != orbs.DriverRunner || p.DisabledAt != nil {
		return orbs.Runner{}, orbs.ErrNotFound
	}
	row, err := o.st.Q.GetRunnerByProvider(ctx, providerID)
	if err != nil {
		return orbs.Runner{}, orbs.ErrNotFound
	}
	orb, err := o.st.Q.GetOrb(ctx, row.OrbID)
	if err != nil || orb.State == orbs.StateDeleted {
		return orbs.Runner{}, orbs.ErrNotFound
	}
	r := orbs.Runner{Name: p.Name, CreatedAt: p.CreatedAt, Orb: orb, Row: row}
	_ = json.Unmarshal([]byte(row.Dirs), &r.Dirs)
	return r, nil
}

func newRunnerFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	o := &stubOrbs{st: st, wakes: make(chan string, 16)}
	s := New(Config{DataDir: dir}, st, runnerStubOrbs{o})
	t.Cleanup(s.Close)
	sink := &recSink{}
	s.SetSink(sink)
	return &fixture{t: t, ctx: ctx, st: st, orbs: o, sink: sink, s: s}
}

// runner 建一个声明了 /w/app 的 runner（provider + kind=runner 的 orb + runners 行），orb 处于 state；返回 provider id 与 orb id。
func (f *fixture) runner(name, state string) (string, string) {
	f.t.Helper()
	provID, orbID := ids.New("prov"), ids.Orb()
	require.NoError(f.t, f.st.Tx(f.ctx, func(q *db.Queries) error {
		if err := q.CreateRunnerProvider(f.ctx, db.CreateRunnerProviderParams{ID: provID, Name: name, TokenHash: ids.Hash(ids.RunnerToken()), CreatedAt: 1}); err != nil {
			return err
		}
		if err := q.CreateOrb(f.ctx, db.CreateOrbParams{ID: orbID, ProviderID: provID, Type: orbs.KindRunner, State: state, StateChangedAt: 1,
			TokenHash: ids.Hash(ids.Token("rnd")), CreatedAt: 1, Kind: new(orbs.KindRunner)}); err != nil {
			return err
		}
		if err := q.CreateRunner(f.ctx, db.CreateRunnerParams{ProviderID: provID, OrbID: orbID}); err != nil {
			return err
		}
		return q.UpdateRunnerHello(f.ctx, db.UpdateRunnerHelloParams{Dirs: `[{"path":"/w/app","name":"app"}]`, Features: "[]", LastSeenAt: 1, OrbID: orbID})
	}))
	return provID, orbID
}

func (f *fixture) threadCount() int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.st.DB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM threads`).Scan(&n))
	return n
}

// F2：dir_in_use 的检查与写线程行同一事务——两条同目录、未给 shareDir 的请求都越过准备步骤后再放行，恰好一条成功，
// 另一条 dir_in_use 且带成功线程的 id；shareDir 时两条都成功。
func TestRunnerCreateDirInUseRace(t *testing.T) {
	for _, share := range []bool{false, true} {
		f := newRunnerFixture(t)
		prov, orbID := f.runner("laptop", orbs.StateReady)
		arrived := make(chan struct{}, 2)
		release := make(chan struct{})
		f.s.afterSpacePrecheck = func() {
			arrived <- struct{}{}
			<-release
		}
		type result struct {
			th  apigen.Thread
			err error
		}
		out := make(chan result, 2)
		for range 2 {
			go func() {
				th, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", RunnerID: prov, Dir: "/w/app", ShareDir: share})
				out <- result{th, err}
			}()
		}
		<-arrived
		<-arrived
		close(release)
		r1, r2 := <-out, <-out
		got, err := f.st.Q.ListThreadsByOrbCwd(f.ctx, db.ListThreadsByOrbCwdParams{OrbID: orbID, Cwd: "/w/app"})
		require.NoError(t, err)
		if share {
			require.NoError(t, r1.err)
			require.NoError(t, r2.err)
			assert.Len(t, got, 2, "shareDir 时两条都成功")
			continue
		}
		ok, fail := r1, r2
		if r1.err != nil {
			ok, fail = r2, r1
		}
		require.NoError(t, ok.err)
		var te *Error
		require.ErrorAs(t, fail.err, &te, "恰好一条成功")
		assert.Equal(t, CodeDirInUse, te.Code)
		body, isConflict := te.Body.(apigen.DirInUseConflict)
		require.True(t, isConflict, "错误体是 DirInUseConflict")
		assert.Equal(t, []string{ok.th.ID}, body.ThreadIds)
		assert.Equal(t, []string{ok.th.ID}, got, "库里只有成功的那条")
	}
}

// F5：executor.kind=orb 指向 runner 的常驻 orb（在线或离线）→ 创建前 runner_unsupported，不留线程、不唤醒 / 启动 agent。
func TestCreateOnRunnerOrbRejected(t *testing.T) {
	for _, state := range []string{orbs.StateReady, orbs.StateStopped} {
		f := newRunnerFixture(t)
		_, orbID := f.runner("laptop", state)
		_, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", OrbID: orbID})
		var te *Error
		require.ErrorAs(t, err, &te, state)
		assert.Equal(t, CodeRunnerUnsupported, te.Code, state)
		assert.Zero(t, f.threadCount(), "没有线程行（%s）", state)
		assert.Empty(t, f.orbs.wakes, "没有唤醒 / agent.start（%s）", state)
	}
}

// F6：Create 越过准备步骤后，runner 被删除（吊销 + orb deleted，同 orbs.DeleteRunner 的事务）且线程服务已消费 deleted 事件，
// 再放行 → not_found，不留指向已删除 runner 的线程。
func TestRunnerCreateRecheckedInTx(t *testing.T) {
	f := newRunnerFixture(t)
	prov, orbID := f.runner("laptop", orbs.StateReady)
	f.s.afterSpacePrecheck = func() {
		now := time.Now().UnixMilli()
		var seq int64
		require.NoError(t, f.st.Tx(f.ctx, func(q *db.Queries) error {
			n, err := q.RevokeRunnerProvider(f.ctx, db.RevokeRunnerProviderParams{DisabledAt: &now, TokenHash: ids.Hash(ids.Token("rnd")), ID: prov})
			if err != nil {
				return err
			}
			if n != 1 {
				return errors.New("runner not revoked")
			}
			if err := q.MarkOrbDeleted(f.ctx, db.MarkOrbDeletedParams{DeletedAt: now, ID: orbID}); err != nil {
				return err
			}
			seq, err = q.InsertOrbEventSeq(f.ctx, db.InsertOrbEventSeqParams{OrbID: orbID, Ts: now, FromState: orbs.StateReady, ToState: orbs.StateDeleted, Detail: "{}"})
			return err
		}))
		// 同步处理 deleted 事件：此时还没有本次的线程行，解绑扫不到它
		f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: orbID, Kind: OrbEventState, From: orbs.StateReady, To: orbs.StateDeleted, Seq: seq})
	}
	_, err := f.s.Create(f.ctx, CreateParams{Prompt: "x", RunnerID: prov, Dir: "/w/app"})
	var te *Error
	require.ErrorAs(t, err, &te)
	assert.Equal(t, "not_found", te.Code)
	assert.Zero(t, f.threadCount(), "没有孤立线程行")
	assert.Empty(t, f.orbs.wakes)
}

// syncBuffer 是可并发写的日志缓冲。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// agent 退出日志记退出码的值（不是指针地址）；没有退出码时记 unknown。
func TestAgentExitLogsExitCodeValue(t *testing.T) {
	f := newFixture(t, Config{})
	orbID := f.orb("ready")
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	th := f.thread(orbID, StateIdle)
	f.s.onAgentStatus(f.ctx, orbID, json.RawMessage(`{"threadId":"`+th.id+`","status":"exited","exitCode":3,"restartsLeft":0,"stderrTail":"boom"}`))
	assert.Contains(t, buf.String(), "exitCode=3 ")
	th2 := f.thread(orbID, StateIdle)
	f.s.onAgentStatus(f.ctx, orbID, json.RawMessage(`{"threadId":"`+th2.id+`","status":"exited","restartsLeft":0,"stderrTail":"boom"}`))
	assert.Contains(t, buf.String(), "exitCode=unknown ")
	assert.NotContains(t, buf.String(), "exitCode=0x")
}
