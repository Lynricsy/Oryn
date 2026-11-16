package threads

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// startProbeOrbs 在 agent 启动的第一步（startAgent 取 Ctl）回调 onStart；新 orb 直接是 ready（走 scheduleStart）。
type startProbeOrbs struct {
	*stubOrbs
	onStart func(orbID string)
}

func (o *startProbeOrbs) Create(ctx context.Context, p OrbCreate) (db.Orb, error) {
	o.mu.Lock()
	o.creates = append(o.creates, p)
	o.mu.Unlock()
	return o.st.Q.GetOrb(ctx, mkOrbRow(ctx, o.st, "ready"))
}

func (o *startProbeOrbs) Ctl(orbID string) (*link.Ctl, bool) {
	o.onStart(orbID)
	return nil, false
}

func newStartProbe(t *testing.T) (*Service, *startProbeOrbs, *store.Store, chan bool) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	started := make(chan bool, 4)
	o := &startProbeOrbs{stubOrbs: &stubOrbs{st: st, wakes: make(chan string, 16)}}
	s := New(Config{DataDir: dir}, st, o)
	t.Cleanup(s.Close)
	s.SetSink(&recSink{})
	o.onStart = func(string) {
		// 启动的那一刻读 InitTx 写入的行：必须已提交、可读
		threadIDs, err := st.Q.ListThreadIssues(context.Background())
		started <- err == nil && len(threadIDs) == 1
	}
	return s, o, st, started
}

// initIssue 是写一行 thread_issues 的 InitTx。
func initIssue(number int64) func(context.Context, *db.Queries, string) error {
	return func(ctx context.Context, q *db.Queries, threadID string) error {
		return q.UpsertThreadIssue(ctx, db.UpsertThreadIssueParams{RepoID: 1, Number: number, ThreadID: threadID, CreatedAt: 1})
	}
}

func waitStart(t *testing.T, started chan bool) {
	t.Helper()
	select {
	case ok := <-started:
		assert.True(t, ok, "agent 启动时 InitTx 的写入已可读")
	case <-time.After(5 * time.Second):
		t.Fatal("agent 没有启动")
	}
}

// M15：新 orb 已 ready（scheduleStart）时，agent 启动的那一刻 InitTx 的写入已提交。
func TestCreateInitTxBeforeScheduleStart(t *testing.T) {
	s, _, _, started := newStartProbe(t)
	_, err := s.Create(context.Background(), CreateParams{Prompt: "go", InitTx: initIssue(7)})
	require.NoError(t, err)
	waitStart(t, started)
}

// M15：挂到已有 orb（wake）时同样。
func TestCreateInitTxBeforeWake(t *testing.T) {
	s, _, st, started := newStartProbe(t)
	orbID := mkOrbRow(context.Background(), st, "ready")
	_, err := s.Create(context.Background(), CreateParams{Prompt: "go", OrbID: orbID, InitTx: initIssue(8)})
	require.NoError(t, err)
	waitStart(t, started)
}

// M15：InitTx 失败时 Create 失败、线程行与首批命令都不落库、agent 不启动，新 orb 被删。
func TestCreateInitTxFails(t *testing.T) {
	s, o, st, started := newStartProbe(t)
	boom := errors.New("boom")
	_, err := s.Create(context.Background(), CreateParams{Prompt: "go", InitTx: func(context.Context, *db.Queries, string) error { return boom }})
	require.Error(t, err)
	select {
	case <-started:
		t.Fatal("InitTx 失败后不应启动 agent")
	case <-time.After(200 * time.Millisecond):
	}
	o.mu.Lock()
	assert.Len(t, o.creates, 1)
	assert.Len(t, o.deletes, 1, "新 orb 被删")
	o.mu.Unlock()
	ths, err := st.Q.ListThreads(context.Background(), db.ListThreadsParams{})
	require.NoError(t, err)
	assert.Empty(t, ths)
}
