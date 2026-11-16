package artifacts

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rtEnv 是一个带 db 声明的页面与它的跟随帧。
type rtEnv struct {
	e    *env
	h    *Host
	mw   http.Handler
	art  Artifact
	host string
}

func newRtEnv(t *testing.T, caps ...string) *rtEnv {
	t.Helper()
	e := newEnv(t)
	h := newHost(t, e, "localhost", fakeViewerAssets)
	a, err := e.s.Create(context.Background(), CreateParams{Title: "待办", Files: []FileChange{put("index.html", page)}, Capabilities: caps})
	require.NoError(t, err)
	r := &rtEnv{e: e, h: h, mw: h.Middleware(http.NotFoundHandler()), art: a}
	r.host = r.frame(t, 0)
	return r
}

// frame 申请一个帧，返回它的主机名。
func (r *rtEnv) frame(t *testing.T, version int) string {
	t.Helper()
	token, _, err := r.h.OpenFrame(context.Background(), r.art.ID, version, "", "")
	require.NoError(t, err)
	return "a-" + token + ".localhost:7600"
}

// send 发一条消息（v 与 id 由调用方给在 msg 里，缺省补上），回应答。
func (r *rtEnv) send(t *testing.T, host string, msg map[string]any) map[string]any {
	t.Helper()
	if _, ok := msg["v"]; !ok {
		msg["v"] = 1
	}
	if _, ok := msg["id"]; !ok {
		msg["id"] = "m"
	}
	b, _ := json.Marshal(msg)
	rec := postRuntime(r.mw, host, "/_oryn/v1", "null", string(b))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return replyOf(t, rec)
}

func (r *rtEnv) call(t *testing.T, op string, args ...any) map[string]any {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	return r.send(t, r.host, map[string]any{"kind": "call", "cap": "db", "op": op, "args": args})
}

func (r *rtEnv) ok(t *testing.T, op string, args ...any) any {
	t.Helper()
	rep := r.call(t, op, args...)
	require.Equal(t, true, rep["ok"], rep)
	return rep["value"]
}

func replyCode(t *testing.T, rep map[string]any) string {
	t.Helper()
	require.Equal(t, false, rep["ok"], rep)
	return rep["error"].(map[string]any)["code"].(string)
}

// 能力声明：没声明 db 时 use 回 null、call 回 not_declared；判定看最新版本；钉住旧版本的帧上一切调用被拒。
func TestDispatchDeclaration(t *testing.T) {
	ctx := context.Background()
	plain := newRtEnv(t)
	assert.Nil(t, plain.send(t, plain.host, map[string]any{"kind": "use", "cap": "db"})["value"])
	assert.Equal(t, codeNotDeclared, replyCode(t, plain.call(t, "get", "c", "d")))

	r := newRtEnv(t, "db")
	use := r.send(t, r.host, map[string]any{"kind": "use", "cap": "db"})["value"]
	assert.Equal(t, map[string]any{
		"ops":     []any{"collections", "delete", "get", "list", "query", "set", "update", "watch"},
		"streams": []any{"watch"},
	}, use)
	r.ok(t, "set", "todos", "t1", map[string]any{"text": "a"})

	// 新版本去掉声明：同一个跟随帧立刻失去 db
	none := []string{}
	_, err := r.e.s.Write(ctx, WriteParams{ID: r.art.ID, BaseVersion: 1, Capabilities: &none})
	require.NoError(t, err)
	assert.Nil(t, r.send(t, r.host, map[string]any{"kind": "use", "cap": "db"})["value"])
	assert.Equal(t, codeNotDeclared, replyCode(t, r.call(t, "get", "todos", "t1")))
	// 恢复 v1：声明跟着回来
	_, err = r.e.s.RestoreVersion(ctx, r.art.ID, 1, 2, Author{})
	require.NoError(t, err)
	assert.Equal(t, "t1", r.ok(t, "get", "todos", "t1").(map[string]any)["id"])

	// 钉住旧版本的帧：use 回 null，call 与 stream 都被拒
	pinned := r.frame(t, 1)
	assert.Nil(t, r.send(t, pinned, map[string]any{"kind": "use", "cap": "db"})["value"])
	for _, op := range []string{"get", "set", "collections"} {
		assert.Equal(t, codeNotAllowed, replyCode(t, r.send(t, pinned, map[string]any{"kind": "call", "cap": "db", "op": op, "args": []any{"todos", "t1", 1}})), op)
	}
	rec := postRuntime(r.mw, pinned, "/_oryn/v1", "null", `{"v":1,"id":"w","kind":"stream","cap":"db","op":"watch"}`)
	assert.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))
	assert.Equal(t, codeNotAllowed, replyCode(t, replyOf(t, rec)))

	// 回收站里：use 回 null，call 回 not_found
	_, err = r.e.s.Delete(ctx, r.art.ID)
	require.NoError(t, err)
	assert.Nil(t, r.send(t, r.host, map[string]any{"kind": "use", "cap": "db"})["value"])
	assert.Equal(t, CodeNotFound, replyCode(t, r.call(t, "get", "todos", "t1")))
}

// db 的操作：读写、乐观并发、上限与参数校验。
func TestDispatchDB(t *testing.T) {
	r := newRtEnv(t, "db")
	assert.Nil(t, r.ok(t, "get", "todos", "t1"))
	v1 := r.ok(t, "set", "todos", "t1", map[string]any{"text": "买牛奶", "done": false}).(map[string]any)
	assert.Equal(t, "t1", v1["id"])
	ver := v1["version"].(float64)
	doc := r.ok(t, "get", "todos", "t1").(map[string]any)
	assert.Equal(t, map[string]any{"text": "买牛奶", "done": false}, doc["value"])
	assert.Equal(t, ver, doc["version"])

	// update 带正确的 baseVersion：浅合并
	v2 := r.ok(t, "update", "todos", "t1", map[string]any{"done": true}, map[string]any{"baseVersion": ver}).(map[string]any)
	assert.Greater(t, v2["version"].(float64), ver)
	assert.Equal(t, map[string]any{"text": "买牛奶", "done": true}, r.ok(t, "get", "todos", "t1").(map[string]any)["value"])
	// 过期的 baseVersion → conflict；baseVersion 0 对已存在的文档 → conflict
	assert.Equal(t, CodeConflict, replyCode(t, r.call(t, "set", "todos", "t1", 1, map[string]any{"baseVersion": ver})))
	assert.Equal(t, CodeConflict, replyCode(t, r.call(t, "set", "todos", "t1", 1, map[string]any{"baseVersion": 0})))
	assert.Equal(t, CodeConflict, replyCode(t, r.call(t, "delete", "todos", "t1", map[string]any{"baseVersion": ver})))
	r.ok(t, "set", "todos", "t2", "纯字符串也行", map[string]any{"baseVersion": 0})
	// 超过 1 MiB → too_large；太深 → invalid
	assert.Equal(t, CodeTooLarge, replyCode(t, r.call(t, "set", "todos", "big", strings.Repeat("x", MaxDocBytes))))
	deep := strings.Repeat("[", MaxDocDepth+1) + strings.Repeat("]", MaxDocDepth+1)
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "set", "todos", "deep", json.RawMessage(deep))))
	// update 不存在的文档 → not_found；patch 不是对象 → invalid
	assert.Equal(t, CodeNotFound, replyCode(t, r.call(t, "update", "todos", "nope", map[string]any{"a": 1})))
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "update", "todos", "t1", []any{1})))
	// 名字规则与参数个数
	for _, bad := range [][]any{{"-x", "t1"}, {"todos", "a/b"}, {"todos"}, {"todos", "t1", 1}, {1, "t1"}} {
		assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "get", bad...)), bad)
	}
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "set", "todos", "t3", 1, map[string]any{"baseVersion": 1, "x": 1})))
	// 种类不符
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "watch")))
	assert.Equal(t, codeUnsupported, replyCode(t, r.call(t, "drop")))
	rec := postRuntime(r.mw, r.host, "/_oryn/v1", "null", `{"v":1,"id":"s","kind":"stream","cap":"db","op":"get","args":["a","b"]}`)
	assert.Equal(t, CodeInvalid, replyCode(t, replyOf(t, rec)))

	// list 分页、query、collections、delete
	for _, id := range []string{"a1", "a2", "a3"} {
		r.ok(t, "set", "notes", id, map[string]any{"n": len(id) + int(id[1]-'0'), "tag": map[bool]string{true: "x", false: "y"}[id != "a2"]})
	}
	page1 := r.ok(t, "list", "notes", map[string]any{"limit": 2}).(map[string]any)
	assert.Len(t, page1["docs"], 2)
	assert.Equal(t, "a2", page1["next"])
	page2 := r.ok(t, "list", "notes", map[string]any{"after": "a2"}).(map[string]any)
	assert.Len(t, page2["docs"], 1)
	assert.Nil(t, page2["next"])
	q := r.ok(t, "query", "notes", map[string]any{"where": []any{map[string]any{"field": "tag", "op": "==", "value": "x"}},
		"orderBy": map[string]any{"field": "n", "direction": "desc"}}).([]any)
	require.Len(t, q, 2)
	assert.Equal(t, "a3", q[0].(map[string]any)["id"])
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "query", "notes", map[string]any{"limit": DataListMax + 1})))
	assert.Equal(t, CodeInvalid, replyCode(t, r.call(t, "query", "notes", map[string]any{"where": []any{map[string]any{"field": "n", "op": "<", "value": nil}}})))
	cs := r.ok(t, "collections").([]any)
	require.Len(t, cs, 2)
	assert.Equal(t, "notes", cs[0].(map[string]any)["name"])
	assert.EqualValues(t, 3, cs[0].(map[string]any)["count"])
	assert.Equal(t, map[string]any{"deleted": true}, r.ok(t, "delete", "notes", "a1"))
	assert.Equal(t, map[string]any{"deleted": false}, r.ok(t, "delete", "notes", "a1"))
}

// readLine 读 NDJSON 的下一行。
func readLine(t *testing.T, sc *bufio.Scanner) map[string]any {
	t.Helper()
	require.True(t, sc.Scan(), "流提前结束")
	var m map[string]any
	require.NoError(t, json.Unmarshal(sc.Bytes(), &m), sc.Text())
	return m
}

// watch 流：另一处 set / delete 之后收到事件，只收所监听集合的；cancel 消息结束流；客户端断开即取消。
func TestDispatchWatch(t *testing.T) {
	r := newRtEnv(t, "db")
	srv := httptest.NewServer(r.mw)
	t.Cleanup(srv.Close)
	open := func(id string, args string) (*http.Response, *bufio.Scanner) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/_oryn/v1",
			strings.NewReader(`{"v":1,"id":"`+id+`","kind":"stream","cap":"db","op":"watch","args":`+args+`}`))
		require.NoError(t, err)
		req.Host = r.host
		req.Header.Set("Origin", "null")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "application/x-ndjson", resp.Header.Get("Content-Type"))
		return resp, bufio.NewScanner(resp.Body)
	}
	resp, sc := open("w1", `["todos"]`)
	defer resp.Body.Close()

	r.ok(t, "set", "other", "x", 1) // 不在监听的集合里
	r.ok(t, "set", "todos", "t1", map[string]any{"text": "a"})
	ev := readLine(t, sc)
	assert.Equal(t, "w1", ev["id"])
	e1 := ev["event"].(map[string]any)
	assert.Equal(t, "set", e1["type"])
	assert.Equal(t, "todos", e1["collection"])
	assert.Equal(t, "t1", e1["id"])
	assert.Equal(t, map[string]any{"text": "a"}, e1["value"])
	assert.NotZero(t, e1["version"])
	r.ok(t, "delete", "todos", "t1")
	assert.Equal(t, map[string]any{"type": "delete", "collection": "todos", "id": "t1"}, readLine(t, sc)["event"])

	// 同 id 的第二个调用被拒；cancel 结束流（最后一行 done）
	dup := postRuntime(r.mw, r.host, "/_oryn/v1", "null", `{"v":1,"id":"w1","kind":"stream","cap":"db","op":"watch"}`)
	assert.Equal(t, CodeInvalid, replyCode(t, replyOf(t, dup)))
	assert.Equal(t, map[string]any{"cancelled": true}, r.send(t, r.host, map[string]any{"id": "w1", "kind": "cancel"})["value"])
	assert.Equal(t, map[string]any{"v": float64(1), "id": "w1", "done": true}, readLine(t, sc))
	assert.False(t, sc.Scan())
	assert.Equal(t, map[string]any{"cancelled": false}, r.send(t, r.host, map[string]any{"id": "w1", "kind": "cancel"})["value"])

	// 客户端断开：监听者被摘掉，流的名额还回来
	resp2, _ := open("w2", `null`)
	require.Eventually(t, func() bool { return watcherCount(r.e.s, r.art.ID) == 1 }, 5*time.Second, 5*time.Millisecond)
	_ = resp2.Body.Close()
	require.Eventually(t, func() bool { return watcherCount(r.e.s, r.art.ID) == 0 }, 5*time.Second, 5*time.Millisecond)
}

func watcherCount(s *Service, id string) int {
	s.data.mu.Lock()
	defer s.data.mu.Unlock()
	return len(s.data.watchers[id])
}

// 令牌桶：突发 120，之后每秒 30；use 被限速时回 null。
func TestDispatchRateLimit(t *testing.T) {
	r := newRtEnv(t, "db")
	for i := range runtimeBurst {
		require.Equal(t, true, r.call(t, "collections")["ok"], i)
	}
	assert.Equal(t, CodeRateLimited, replyCode(t, r.call(t, "collections")))
	assert.Nil(t, r.send(t, r.host, map[string]any{"kind": "use", "cap": "db"})["value"])
	r.e.clk.add(time.Second)
	for i := range runtimeRate {
		require.Equal(t, true, r.call(t, "collections")["ok"], i)
	}
	assert.Equal(t, CodeRateLimited, replyCode(t, r.call(t, "collections")), "每秒第 31 个")
	// 别的 artifact 有自己的桶
	other := newRtEnv(t, "db")
	assert.Equal(t, true, other.call(t, "collections")["ok"])
}

// 并发上限：每个 artifact 同时 32 个调用、16 条流。
func TestDispatchConcurrency(t *testing.T) {
	var g runtimeGates
	var releases []func()
	for range maxRuntimeStreams {
		rel, ok := g.acquire("art", true)
		require.True(t, ok)
		releases = append(releases, rel)
	}
	_, ok := g.acquire("art", true)
	assert.False(t, ok)
	_, ok = g.acquire("other", true)
	assert.True(t, ok)
	for range maxRuntimeCalls {
		_, ok := g.acquire("art", false)
		require.True(t, ok)
	}
	_, ok = g.acquire("art", false)
	assert.False(t, ok)
	releases[0]()
	_, ok = g.acquire("art", true)
	assert.True(t, ok)
}
