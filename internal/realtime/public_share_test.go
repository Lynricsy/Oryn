package realtime_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// shareServer 把 `?slug=…&after=&rev=` 交给真实分享服务的 WSHandler（补发走真实的范围函数与分支筛选）。
func (e *env) shareServer(svc *share.Service) *httptest.Server {
	e.t.Helper()
	h := svc.WSHandler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("slug", r.URL.Query().Get("slug"))
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
	}))
	e.t.Cleanup(srv.Close)
	return srv
}

// addEntry 直接在库里写一条主会话消息，返回推送用的条目。
func (e *env) addEntry(tid, id, parent string) threads.Entry {
	e.t.Helper()
	var pp *string
	if parent != "" {
		pp = &parent
	}
	payload := []byte(`{"type":"message","id":"` + id + `","message":{"role":"assistant","content":"text of ` + id + `"}}`)
	seq, err := e.st.Q.UpsertEntry(context.Background(), db.UpsertEntryParams{ThreadID: tid, File: "s.jsonl", EntryID: id, ParentID: pp, Type: "message", Payload: payload})
	require.NoError(e.t, err)
	return threads.Entry{Seq: seq, File: "s.jsonl", EntryID: id, ParentID: pp, Type: "message", Payload: payload}
}

// collectBackfill 读快照与补发，返回补发条目的 id（按到达顺序）。
func collectBackfill(p *pubClient) (msg, []string) {
	snap := p.next(func(m msg) bool { return m.Type == "share.snapshot" })
	var got []string
	if snap.LastSeq == 0 {
		return snap, got
	}
	p.next(func(m msg) bool {
		if m.Type != "entries" {
			return false
		}
		for _, en := range m.Entries {
			got = append(got, en.EntryID)
		}
		return m.LastSeq == snap.LastSeq
	})
	return snap, got
}

// barrier 发 ping 并读到 pong：此前投递给该连接的消息都已收到（同一连接按序写出）。返回途中收到的消息。
func barrier(p *pubClient) []msg {
	start := len(p.all)
	p.send(map[string]string{"type": "ping"})
	p.next(func(m msg) bool { return m.Type == "pong" })
	return p.all[start:]
}

func types(ms []msg) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Type)
	}
	return out
}

// B7：已连接的实时分享页期间所有者切换叶子（不产生新条目）或跳到别的分支写出摘要，匿名客户端
// 收到 transcript.reset，按客户端的做法以 after=0 重连后拿到目标分支的完整历史；沿当前分支续写不触发重建。
func TestPublicLeafSwitchRebuildsBranch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	svc := share.New(share.Config{PublicURL: "http://oryn.test"}, e.st, e.svc, e.hub, nil, nil, nil)
	srv := e.shareServer(svc)
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-000000000007"
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: tid, Title: "branches", TitleSource: "auto", State: "idle", SessionDir: "/x", CreatedAt: 1, UpdatedAt: 1}))
	e.addEntry(tid, "u1", "")
	e.addEntry(tid, "b1", "u1")
	e.addEntry(tid, "b2", "b1")
	e.addEntry(tid, "a1", "u1")
	setLeaf := func(id string) {
		require.NoError(t, e.st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: &id, ID: tid}))
	}
	setLeaf("a1")
	sh, err := svc.Create(ctx, tid, apigen.ShareModeLive, true)
	require.NoError(t, err)

	p, _, err := dialPublic(t, srv, "slug="+sh.Slug+"&after=0")
	require.NoError(t, err)
	snap, got := collectBackfill(p)
	require.Equal(t, []string{"u1", "a1"}, got, "只补发当前分支 A")

	// 沿当前分支续写：照常推送，不重建
	a2 := e.addEntry(tid, "a2", "a1")
	setLeaf("a2")
	e.hub.Entries(tid, []threads.Entry{a2}, &a2.EntryID, a2.Seq)
	th, err := e.svc.Get(ctx, tid)
	require.NoError(t, err)
	e.hub.IndexUpsert(th)
	seen := barrier(p)
	assert.Contains(t, types(seen), "entries")
	assert.NotContains(t, types(seen), "transcript.reset", "续写当前分支不重建")

	// 所有者切到分支 B：threads 只更新叶子并推 index.patch（switch_leaf 应答的处理），没有新条目
	setLeaf("b2")
	th, err = e.svc.Get(ctx, tid)
	require.NoError(t, err)
	e.hub.IndexUpsert(th)
	reset := p.next(func(m msg) bool { return m.Type == "transcript.reset" })
	assert.Equal(t, snap.TranscriptRev, reset.TranscriptRev)

	// 客户端（web/src/share/feed.ts）清空后以 after=0&rev= 重连：拿到分支 B 的完整历史
	p2, _, err := dialPublic(t, srv, "slug="+sh.Slug+"&after=0&rev="+strconv.FormatInt(reset.TranscriptRev, 10))
	require.NoError(t, err)
	_, got = collectBackfill(p2)
	assert.Equal(t, []string{"u1", "b1", "b2"}, got, "重连后按新叶子补发目标分支")
	assert.NotContains(t, types(barrier(p2)), "transcript.reset")

	// 带摘要的分支跳转：新条目挂在客户端没有的分支上并成为叶子 → 再次重建
	s1 := e.addEntry(tid, "s1", "u1")
	setLeaf("s1")
	e.hub.Entries(tid, []threads.Entry{s1}, &s1.EntryID, s1.Seq)
	p2.next(func(m msg) bool { return m.Type == "transcript.reset" })
	p.assertClean()
	p2.assertClean()
}

// F2 / F3：实时帧与条目共用隐藏规则——display:false 的注入消息、delta 形态里无法判定的非 assistant 更新、
// agent_end 带的整轮 messages、子 agent 的进度与会话路径都不发给匿名连接。
func TestPublicFramesHideInjectedAndSubagent(t *testing.T) {
	e := newEnv(t)
	pub := e.pubServer()
	const tid = "T-01932f6e-8d1a-7c3b-9f2e-000000000008"
	e.seedThread(tid, 1)
	p, _, err := dialPublic(t, pub, "share=shr_f&thread="+tid+"&after=1")
	require.NoError(t, err)
	snap := p.next(func(m msg) bool { return m.Type == "share.snapshot" })
	assert.Contains(t, string(snap.raw), `"subagents":[]`)

	frames := []string{
		`{"type":"message_start","messageId":"m1","message":{"role":"custom","customType":"x","display":false,"content":"HIDDEN-1"}}`,
		`{"type":"message_end","messageId":"m1","message":{"role":"custom","customType":"x","display":false,"content":"HIDDEN-1"}}`,
		`{"type":"message_start","messageId":"m2","message":{"role":"hookMessage","display":false,"content":"HIDDEN-2"}}`,
		`{"type":"message_update","messageId":"m2","message":{"role":"hookMessage"},"assistantMessageEvent":{"type":"text_delta","delta":"HIDDEN-3"}}`,
		`{"type":"message_start","messageId":"m3","message":{"role":"custom","customType":"x","display":true,"content":"VISIBLE-1"}}`,
		`{"type":"message_update","messageId":"m4","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","delta":"VISIBLE-2"}}`,
		`{"type":"agent_end","isTerminal":true,"messages":[{"role":"custom","display":false,"content":"HIDDEN-4"}]}`,
		`{"type":"subagent_progress","index":0,"progress":{"recentOutput":["HIDDEN-5"]}}`,
		`{"type":"subagent_lifecycle","index":0,"sessionFile":"/home/user/.oryn/threads/T-x/HIDDEN-6.jsonl"}`,
		`{"type":"tool_execution_update","toolCallId":"t1","toolName":"task","partialResult":{"details":{"progress":[{"recentOutput":["HIDDEN-7"]}]}}}`,
		`{"type":"tool_stream_update","toolCallId":"t1","toolName":"task","update":"HIDDEN-8"}`,
		`{"type":"tool_execution_update","toolCallId":"t2","toolName":"bash","partialResult":{"content":[{"type":"text","text":"VISIBLE-3"}]}}`,
	}
	for _, f := range frames {
		e.hub.Frame(tid, json.RawMessage(f))
	}
	var got []string
	for _, m := range barrier(p) {
		if m.Type == "frame" {
			got = append(got, string(m.Frame))
		}
	}
	all := strings.Join(got, "\n")
	assert.NotContains(t, all, "HIDDEN")
	for _, v := range []string{"VISIBLE-1", "VISIBLE-2", "VISIBLE-3"} {
		assert.Contains(t, all, v)
	}
	assert.Contains(t, got, `{"type":"agent_end","isTerminal":true}`, "agent_end 只留 type 与 isTerminal")
	p.assertClean()
}

// B7 重连缺口：观众断线期间所有者经 switch_leaf / navigate_tree 切换分支（真实应答路径，fakeomp 的应答形状与 omp
// 一致：navigate_tree 不带叶子，控制面另发内部 get_tree），转录版本随之递增；观众带旧 rev 与旧游标重连时收到
// fullReplay 与新 rev，按 after=0 补发到完整的新分支。
func TestPublicReconnectAfterLeafSwitch(t *testing.T) {
	for _, typ := range []string{"switch_leaf", "navigate_tree"} {
		t.Run(typ, func(t *testing.T) { testPublicReconnectAfterLeafSwitch(t, typ) })
	}
}

func testPublicReconnectAfterLeafSwitch(t *testing.T, switchCmd string) {
	e := newEnv(t)
	ctx := context.Background()
	svc := share.New(share.Config{PublicURL: "http://oryn.test"}, e.st, e.svc, e.hub, nil, nil, nil)
	srv := e.shareServer(svc)
	a := e.dial("dev_a")
	th, err := e.svc.Create(ctx, threads.CreateParams{Prompt: "one"})
	require.NoError(t, err)
	a.send(map[string]any{"type": "sub", "threadId": th.ID, "afterSeq": 0})

	var order []string
	seen := map[string]bool{}
	take := func(n int) {
		t.Helper()
		a.next(func(m msg) bool {
			if m.Type == "entries" && m.ThreadID == th.ID {
				for _, en := range m.Entries {
					if !strings.Contains(en.File, "/") && !strings.HasPrefix(en.EntryID, "__") && !seen[en.EntryID] {
						seen[en.EntryID] = true
						order = append(order, en.EntryID)
					}
				}
			}
			return len(order) >= n
		})
	}
	n := 0
	// cmd 发命令并收集到其应答为止的全部消息（transcript.reset 在应答之前广播）
	cmd := func(frame map[string]any) []msg {
		t.Helper()
		n++
		id := "c" + strconv.Itoa(n)
		a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": id, "frame": frame})
		return a.collect(func(m msg) bool { return m.Type == "res" && m.ID == id })
	}
	ok := func(ms []msg) []msg {
		t.Helper()
		var resp struct {
			Success bool `json:"success"`
		}
		require.NoError(t, json.Unmarshal(ms[len(ms)-1].Response, &resp))
		require.True(t, resp.Success, string(ms[len(ms)-1].Response))
		return ms
	}
	// switchTo 切换叶子，返回所有者收到的 transcript.reset 的版本：switch_leaf 在应答之前广播，navigate_tree
	// 要等应答之后的内部 get_tree 应答，因此收集到二者都到为止
	switchTo := func(entry string) int64 {
		t.Helper()
		n++
		id := "c" + strconv.Itoa(n)
		a.send(map[string]any{"type": "cmd", "threadId": th.ID, "id": id, "frame": map[string]any{"type": switchCmd, "entryId": entry}})
		var res, reset *msg
		a.next(func(m msg) bool {
			switch {
			case m.Type == "res" && m.ID == id:
				res = &m
			case m.Type == "transcript.reset" && m.ThreadID == th.ID:
				reset = &m
			}
			return res != nil && reset != nil
		})
		ok([]msg{*res})
		return reset.TranscriptRev
	}
	take(4)
	e.waitState(th.ID, threads.StateIdle)
	ok(cmd(map[string]any{"type": "prompt", "message": "two"}))
	take(8)
	e.waitState(th.ID, threads.StateIdle)
	// 分支 B：从第一轮末尾另起一轮
	assert.EqualValues(t, 1, switchTo(order[3]))
	ok(cmd(map[string]any{"type": "prompt", "message": "three"}))
	take(12)
	e.waitState(th.ID, threads.StateIdle)
	branchA := append(append([]string{}, order[:4]...), order[4:8]...)
	branchB := append(append([]string{}, order[:4]...), order[8:12]...)

	sh, err := svc.Create(ctx, th.ID, apigen.ShareModeLive, true)
	require.NoError(t, err)
	p, _, err := dialPublic(t, srv, "slug="+sh.Slug+"&after=0")
	require.NoError(t, err)
	snap, got := collectBackfill(p)
	require.Equal(t, branchB, got)
	assert.EqualValues(t, 1, snap.TranscriptRev)
	require.NoError(t, p.ws.Close(4000, "offline"))

	// 断线期间所有者切回分支 A（不产生新条目）
	assert.EqualValues(t, 2, switchTo(order[7]))

	// 观众按 feed.ts 带旧游标与旧 rev 重连：版本不符 → 从头补发新分支
	p2, _, err := dialPublic(t, srv, "slug="+sh.Slug+"&after="+strconv.FormatInt(snap.LastSeq, 10)+"&rev=1")
	require.NoError(t, err)
	snap2, got := collectBackfill(p2)
	assert.True(t, snap2.FullReplay)
	assert.EqualValues(t, 2, snap2.TranscriptRev)
	assert.Equal(t, branchA, got)
	p2.assertClean()
}
