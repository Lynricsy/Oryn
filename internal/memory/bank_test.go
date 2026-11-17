package memory_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerFake 是只实现 bank `oryn-owner` 里遗忘保护与删除重试用到的端点的假 Hindsight。
type ownerFake struct {
	srv *httptest.Server

	mu          sync.Mutex
	key         string
	models      []string
	failDeletes bool
	ops         map[string]string // operation id → status
	calls       []string
	refreshes   []string
	bodies      map[string]map[string]any
}

const ownerRoot = "/v1/default/banks/oryn-owner"

func newOwnerFake(t *testing.T, models ...string) *ownerFake {
	t.Helper()
	f := &ownerFake{models: models, ops: map[string]string{}, bodies: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *ownerFake) with(fn func(f *ownerFake)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *ownerFake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key == "" || r.Header.Get("Authorization") != "Bearer "+f.key {
		writeJSONTo(w, http.StatusUnauthorized, map[string]string{"detail": "bad key"})
		return
	}
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, call)
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.bodies[call] = body
	rel, ok := strings.CutPrefix(r.URL.Path, ownerRoot)
	if !ok {
		http.NotFound(w, r)
		return
	}
	switch {
	case rel == "" && r.Method == http.MethodPut, rel == "/config" && r.Method == http.MethodPatch:
		writeJSONTo(w, 200, map[string]any{})
	case rel == "/mental-models" && r.Method == http.MethodGet:
		items := []map[string]any{}
		for _, id := range f.models {
			items = append(items, map[string]any{"id": id, "name": id, "tags": []string{}})
		}
		writeJSONTo(w, 200, map[string]any{"items": items})
	case strings.HasPrefix(rel, "/documents/") && r.Method == http.MethodDelete:
		if f.failDeletes {
			writeJSONTo(w, http.StatusServiceUnavailable, map[string]string{"detail": "down"})
			return
		}
		writeJSONTo(w, 200, map[string]any{})
	case strings.HasPrefix(rel, "/mental-models/") && strings.HasSuffix(rel, "/refresh") && r.Method == http.MethodPost:
		id := strings.TrimSuffix(strings.TrimPrefix(rel, "/mental-models/"), "/refresh")
		op := "op-" + id
		f.ops[op] = "pending"
		f.refreshes = append(f.refreshes, id+":"+body["mode"].(string))
		writeJSONTo(w, 200, map[string]any{"operation_id": op})
	case strings.HasPrefix(rel, "/mental-models/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(rel, "/mental-models/")
		writeJSONTo(w, 200, map[string]any{"id": id, "name": id, "tags": []string{}})
	case strings.HasPrefix(rel, "/operations/") && r.Method == http.MethodGet:
		st, ok := f.ops[strings.TrimPrefix(rel, "/operations/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSONTo(w, 200, map[string]any{"status": st})
	default:
		http.NotFound(w, r)
	}
}

func ownerEnv(t *testing.T, models ...string) (*admEnv, *ownerFake) {
	t.Helper()
	e := newAdmEnv(t, nil)
	f := newOwnerFake(t, models...)
	key := e.configure(t, f.srv.URL)
	f.with(func(f *ownerFake) { f.key = key })
	return e, f
}

func TestMemoryBankRejectsEngineeringBank(t *testing.T) {
	e, _ := ownerEnv(t)
	_, err := e.svc.Bank(context.Background(), "oryn")
	require.Error(t, err)
}

func TestMemoryBankEnsurePatchesConfig(t *testing.T) {
	ctx := context.Background()
	e, f := ownerEnv(t)
	b, err := e.svc.Bank(ctx, "oryn-owner")
	require.NoError(t, err)
	require.NoError(t, b.Ensure(ctx, map[string]any{"retain_mission": "只记主人", "enable_reranking": false}))
	f.with(func(f *ownerFake) {
		assert.Equal(t, []string{"PUT " + ownerRoot, "PATCH " + ownerRoot + "/config"}, f.calls)
		assert.Equal(t, map[string]any{"retain_mission": "只记主人", "enable_reranking": false}, f.bodies["PATCH "+ownerRoot+"/config"]["updates"])
	})
}

// 主人记忆删除 document：先把 bank 里全部模型标为待刷新，删除后提交全量刷新；工程记忆的隐藏列表不受影响。
func TestMemoryBankDeleteDocumentsHidesUntilRefreshed(t *testing.T) {
	ctx := context.Background()
	e, f := ownerEnv(t, "owner-profile", "owner-topic-a")
	b, err := e.svc.Bank(ctx, "oryn-owner")
	require.NoError(t, err)

	deleted, pending, err := b.DeleteDocuments(ctx, []string{"conv:T-1:0"})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	assert.Zero(t, pending)
	f.with(func(f *ownerFake) {
		assert.Contains(t, f.calls, "DELETE "+ownerRoot+"/documents/conv:T-1:0")
		assert.ElementsMatch(t, []string{"owner-profile:full", "owner-topic-a:full"}, f.refreshes)
	})

	ids, err := b.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"owner-profile": true, "owner-topic-a": true}, ids)
	engineering, err := e.svc.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, engineering, "工程记忆网关的隐藏列表只含 bank oryn 的模型")
	pd, err := e.svc.ListPendingDeletes(ctx)
	require.NoError(t, err)
	assert.Empty(t, pd.Items, "工程记忆的待删除列表不显示主人记忆")

	f.with(func(f *ownerFake) {
		for op := range f.ops {
			f.ops[op] = "completed"
		}
	})
	e.now = e.now.Add(time.Hour)
	assert.Equal(t, 2, retryOnce(t, e))
	ids, err = b.StaleModelIDs(ctx)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// 删除失败进入重试队列（记在 oryn-owner 名下），后台重试按 oryn-owner 的路径删除并刷新。
func TestMemoryBankDeleteRetriesInOwnBank(t *testing.T) {
	ctx := context.Background()
	e, f := ownerEnv(t, "owner-profile")
	b, err := e.svc.Bank(ctx, "oryn-owner")
	require.NoError(t, err)
	f.with(func(f *ownerFake) { f.failDeletes = true })

	deleted, pending, err := b.DeleteDocuments(ctx, []string{"thread:T-2:0"})
	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.Equal(t, 1, pending)
	n, err := b.PendingDeletes(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "一条待删 document，加一个等待刷新的模型")
	f.with(func(f *ownerFake) { assert.Empty(t, f.refreshes, "删除失败不提交刷新") })

	f.with(func(f *ownerFake) { f.failDeletes = false; f.calls = nil })
	e.now = e.now.Add(time.Hour)
	done, err := e.svc.RetryTombstones(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, done)
	f.with(func(f *ownerFake) {
		assert.Contains(t, f.calls, "DELETE "+ownerRoot+"/documents/thread:T-2:0")
		assert.Equal(t, []string{"owner-profile:full"}, f.refreshes)
	})
}
