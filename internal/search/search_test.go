package search

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	svc *Service
}

func newFixture(t *testing.T) *fixture {
	st, err := store.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return &fixture{t: t, ctx: t.Context(), st: st, svc: New(st)}
}

func (f *fixture) thread(id string, createdAt int64) {
	f.t.Helper()
	require.NoError(f.t, f.st.Q.CreateThread(f.ctx, db.CreateThreadParams{
		ID: id, Title: "title " + id, TitleSource: "user", State: "idle", StateChangedAt: 1, SessionDir: "d",
		CreatedAt: createdAt, UpdatedAt: createdAt, Kind: "work",
	}))
}

// msg 写一条 message 条目；content 原样作为 message.content 的 JSON。
func (f *fixture) msg(thread, file, id, role string, ts int64, content any) int64 {
	f.t.Helper()
	c, err := json.Marshal(content)
	require.NoError(f.t, err)
	payload := fmt.Sprintf(`{"type":"message","id":%q,"timestamp":%d,"message":{"role":%q,"content":%s}}`, id, ts, role, c)
	seq, err := f.st.Q.UpsertEntry(f.ctx, db.UpsertEntryParams{ThreadID: thread, File: file, EntryID: id, Type: "message", Ts: &ts, Payload: []byte(payload)})
	require.NoError(f.t, err)
	return seq
}

func (f *fixture) search(q string) []apigen.SearchHit {
	f.t.Helper()
	res, err := f.svc.Search(f.ctx, Params{Q: q})
	require.NoError(f.t, err)
	return res.Hits
}

func (f *fixture) count(table string) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.st.DB.QueryRowContext(f.ctx, "SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func entryIDs(hits []apigen.SearchHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.EntryID)
	}
	return out
}

// highlighted 取出片段里每个高亮区间（按 UTF-16 下标）对应的文字。
func highlighted(h apigen.SearchHit) []string {
	u := utf16.Encode([]rune(h.Snippet))
	out := make([]string, 0, len(h.Highlights))
	for _, r := range h.Highlights {
		out = append(out, string(utf16.Decode(u[r.Start:r.End])))
	}
	return out
}

// 只索引主会话 user / assistant 消息的 text 块；改写、作废、清除作废、删除线程都同步到索引。
func TestIndexSync(t *testing.T) {
	f := newFixture(t)
	f.thread("T-a", 1)
	f.msg("T-a", "s.jsonl", "u1", "user", 1000, []map[string]any{{"type": "text", "text": "please deploy the gateway"}, {"type": "image", "data": "x"}})
	f.msg("T-a", "s.jsonl", "a1", "assistant", 2000, []map[string]any{
		{"type": "thinking", "thinking": "secret gateway musing"}, {"type": "text", "text": "Deployed the gateway."},
		{"type": "toolCall", "id": "c1", "name": "bash", "arguments": map[string]any{"cmd": "gateway"}},
	})
	f.msg("T-a", "s.jsonl", "t1", "toolResult", 3000, []map[string]any{{"type": "text", "text": "gateway output"}})
	f.msg("T-a", "sub/agent.jsonl", "s1", "user", 4000, "subagent gateway prompt")
	// 非法 JSON 与非 message 条目不影响入库
	_, err := f.st.Q.UpsertEntry(f.ctx, db.UpsertEntryParams{ThreadID: "T-a", File: "s.jsonl", EntryID: "bad", Type: "message", Payload: []byte("{not json")})
	require.NoError(t, err)

	require.Equal(t, []string{"a1", "u1"}, entryIDs(f.search("gateway")))
	require.Empty(t, f.search("musing"), "thinking 不进索引")
	hits := f.search("deploy")
	require.Equal(t, []string{"a1", "u1"}, entryIDs(hits))
	require.Equal(t, apigen.SearchHitRoleAssistant, hits[0].Role)
	require.Equal(t, int64(2000), hits[0].TS)
	require.Equal(t, "title T-a", hits[0].ThreadTitle)

	// 重发改了内容：旧词搜不到，新词搜得到
	f.msg("T-a", "s.jsonl", "u1", "user", 1000, "please rollback the gateway")
	require.Equal(t, []string{"a1"}, entryIDs(f.search("deploy")))
	require.Equal(t, []string{"u1"}, entryIDs(f.search("rollback")))

	// entries.reset：标 stale 后不在结果里；同内容重发清除 stale 后回来；reset_done 删除后也没有
	_, err = f.st.Q.MarkEntriesStale(f.ctx, db.MarkEntriesStaleParams{ThreadID: "T-a", File: "s.jsonl"})
	require.NoError(t, err)
	require.Empty(t, f.search("gateway"))
	f.msg("T-a", "s.jsonl", "a1", "assistant", 2000, "Deployed the gateway.")
	require.Equal(t, []string{"a1"}, entryIDs(f.search("gateway")))
	_, err = f.st.Q.DeleteStaleEntries(f.ctx, db.DeleteStaleEntriesParams{ThreadID: "T-a", File: "s.jsonl"})
	require.NoError(t, err)
	require.Equal(t, []string{"a1"}, entryIDs(f.search("gateway")))
	require.Equal(t, 1, f.count("search_docs"))
	require.Equal(t, 1, f.count("search_fts"))

	// 删线程：条目级联删除，索引行与全文行随之删除
	_, err = f.st.Q.DeleteThread(f.ctx, "T-a")
	require.NoError(t, err)
	require.Empty(t, f.search("gateway"))
	require.Equal(t, 0, f.count("search_docs"))
	require.Equal(t, 0, f.count("search_fts"))
}

// fork 拷贝的同一条消息（条目 id 与时间相同）只返回一次，归最早的线程；源线程删掉后改由 fork 返回。
func TestForkDedupe(t *testing.T) {
	f := newFixture(t)
	f.thread("T-src", 10)
	f.thread("T-fork", 20)
	// fork 先入库也不改变归属：按线程创建时间判定
	f.msg("T-fork", "f.jsonl", "m1", "user", 5000, "shared kubernetes question")
	f.msg("T-src", "s.jsonl", "m1", "user", 5000, "shared kubernetes question")
	f.msg("T-fork", "f.jsonl", "m2", "assistant", 6000, "fork-only kubernetes answer")

	hits := f.search("kubernetes")
	require.Len(t, hits, 2)
	require.Equal(t, "m2", hits[0].EntryID)
	require.Equal(t, "T-fork", hits[0].ThreadID)
	require.Equal(t, 1, hits[0].Copies)
	require.Equal(t, "m1", hits[1].EntryID)
	require.Equal(t, "T-src", hits[1].ThreadID)
	require.Equal(t, 2, hits[1].Copies)

	// 短词走子串过滤，去重口径相同
	require.Len(t, f.search("qu"), 1)

	_, err := f.st.Q.DeleteThread(f.ctx, "T-src")
	require.NoError(t, err)
	hits = f.search("shared")
	require.Len(t, hits, 1)
	require.Equal(t, "T-fork", hits[0].ThreadID)
	require.Equal(t, 1, hits[0].Copies)
}

// 中文按 trigram 子串匹配；不足 3 个字的词走子串过滤；高亮按 UTF-16 下标落在原文上。
func TestCJKAndHighlights(t *testing.T) {
	f := newFixture(t)
	f.thread("T-a", 1)
	f.msg("T-a", "s.jsonl", "c1", "user", 1000, "😀 我们需要把服务部署到生产环境，然后检查日志")
	f.msg("T-a", "s.jsonl", "c2", "assistant", 2000, "测试环境已经就绪")
	f.msg("T-a", "s.jsonl", "c3", "assistant", 3000, "Le Café   est\n\nouvert")

	hits := f.search("生产环境")
	require.Equal(t, []string{"c1"}, entryIDs(hits))
	require.Equal(t, []string{"生产环境"}, highlighted(hits[0]))

	require.Equal(t, []string{"c2", "c1"}, entryIDs(f.search("环境")), "两个字的词走子串过滤")
	hits = f.search("部署 日志")
	require.Equal(t, []string{"c1"}, entryIDs(hits))
	require.Equal(t, []string{"部署", "日志"}, highlighted(hits[0]))
	require.Empty(t, f.search("部署 测试"), "多词是 AND")

	// 大小写与变音符折叠；片段里的空白折叠成单个空格
	hits = f.search("CAFE")
	require.Equal(t, []string{"c3"}, entryIDs(hits))
	require.Equal(t, "Le Café est ouvert", hits[0].Snippet)
	require.Equal(t, []string{"Café"}, highlighted(hits[0]))

	// FTS5 语法字符只当普通文字
	require.Empty(t, f.search(`"Café OR NEAR(`))
}

// 长正文截取命中附近的一段，两端带省略号。
func TestSnippetWindow(t *testing.T) {
	long := ""
	for range 40 {
		long += "lorem ipsum "
	}
	snippet, ranges := buildSnippet(long+"needle in the haystack "+long, [][]rune{[]rune("needle")})
	require.Equal(t, "…", string([]rune(snippet)[0]))
	require.Equal(t, "…", string([]rune(snippet)[len([]rune(snippet))-1]))
	require.Len(t, ranges, 1)
	u := utf16.Encode([]rune(snippet))
	require.Equal(t, "needle", string(utf16.Decode(u[ranges[0].Start:ranges[0].End])))
}

func TestPagination(t *testing.T) {
	f := newFixture(t)
	f.thread("T-a", 1)
	for i := range 5 {
		f.msg("T-a", "s.jsonl", fmt.Sprintf("p%d", i), "user", int64(1000+i), "paging probe")
	}
	var got []string
	cur := ""
	for {
		res, err := f.svc.Search(f.ctx, Params{Q: "probe", Limit: 2, Cursor: cur})
		require.NoError(t, err)
		got = append(got, entryIDs(res.Hits)...)
		if res.NextCursor == nil {
			break
		}
		cur = *res.NextCursor
	}
	require.Equal(t, []string{"p4", "p3", "p2", "p1", "p0"}, got)

	_, err := f.svc.Search(f.ctx, Params{Q: "probe", Cursor: "zz"})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = f.svc.Search(f.ctx, Params{Q: "   "})
	require.ErrorIs(t, err, ErrInvalid)
	_, err = f.svc.Search(f.ctx, Params{Q: "a b c d e f g h i"})
	require.ErrorIs(t, err, ErrInvalid)
}

// 升级前已有的条目由回填补进索引；回填可中断并从游标续作，完成后写版本号。
func TestBackfill(t *testing.T) {
	f := newFixture(t)
	for i := range 3 {
		id := fmt.Sprintf("T-%d", i)
		f.thread(id, int64(i+1))
		for j := range 3 {
			f.msg(id, "s.jsonl", fmt.Sprintf("e%d-%d", i, j), "user", int64(1000*i+j), fmt.Sprintf("backfill marker %d %d", i, j))
		}
	}
	// 模拟迁移之前写入的条目：清空索引
	_, err := f.st.DB.ExecContext(f.ctx, "DELETE FROM search_fts; DELETE FROM search_docs; DELETE FROM search_state")
	require.NoError(t, err)
	require.Empty(t, f.search("marker"))
	indexing, err := f.svc.Indexing(f.ctx)
	require.NoError(t, err)
	require.True(t, indexing)

	// 第二个线程处中断
	ctx, cancel := context.WithCancel(f.ctx)
	beforeBackfillPage = func(threadID string, _ int64) {
		if threadID == "T-1" {
			cancel()
		}
	}
	t.Cleanup(func() { beforeBackfillPage = nil })
	require.ErrorIs(t, Backfill(ctx, f.st), context.Canceled)
	require.Len(t, f.search("marker"), 3)

	// 续作：回填期间新写入的条目由触发器入索引，回填不会重复写
	f.msg("T-2", "s.jsonl", "late", "user", 9999, "late marker")
	var visited []string
	beforeBackfillPage = func(threadID string, _ int64) { visited = append(visited, threadID) }
	require.NoError(t, Backfill(f.ctx, f.st))
	require.Equal(t, []string{"T-1", "T-2"}, visited)
	require.Len(t, f.search("marker"), 10)
	require.Equal(t, 10, f.count("search_fts"))
	indexing, err = f.svc.Indexing(f.ctx)
	require.NoError(t, err)
	require.False(t, indexing)

	// 版本已是当前：不再回填
	visited = nil
	require.NoError(t, Backfill(f.ctx, f.st))
	require.Empty(t, visited)
}

// TestSearchHistoryFilters：按线程种类、项目与时间过滤（M17 search_history），长词与短词两条查询路径都生效。
func TestSearchHistoryFilters(t *testing.T) {
	f := newFixture(t)
	require.NoError(t, f.st.Q.CreateProject(f.ctx, db.CreateProjectParams{ID: "p", Name: "P", CreatedAt: 1, UpdatedAt: 1}))
	f.thread("w", 1)
	f.thread("a", 2)
	f.thread("s", 3)
	_, err := f.st.DB.Exec(`UPDATE threads SET kind = 'assistant' WHERE id = 'a'`)
	require.NoError(t, err)
	_, err = f.st.DB.Exec(`UPDATE threads SET kind = 'space', project_id = 'p' WHERE id = 's'`)
	require.NoError(t, err)
	_, err = f.st.DB.Exec(`UPDATE threads SET project_id = 'p' WHERE id = 'w'`)
	require.NoError(t, err)
	f.msg("w", "s.jsonl", "w1", "user", 100, "部署 oryn 到 UltraServer go")
	f.msg("w", "s.jsonl", "w2", "user", 300, "部署 oryn 第二次 go")
	f.msg("a", "s.jsonl", "a1", "user", 200, "部署 oryn 的计划 go")
	f.msg("s", "s.jsonl", "s1", "user", 250, "部署 oryn 空间 go")

	run := func(q string, p Params) []string {
		t.Helper()
		p.Q = q
		res, err := f.svc.Search(f.ctx, p)
		require.NoError(t, err)
		return entryIDs(res.Hits)
	}
	for _, q := range []string{"部署 oryn", "go"} {
		assert.Equal(t, []string{"w2", "s1", "a1", "w1"}, run(q, Params{}), q)
		assert.Equal(t, []string{"w2", "a1", "w1"}, run(q, Params{Kinds: []string{"assistant", "work"}}), q)
		assert.Equal(t, []string{"a1"}, run(q, Params{Kinds: []string{"assistant"}}), q)
		assert.Equal(t, []string{"w2", "s1", "w1"}, run(q, Params{ProjectID: "p"}), q)
		assert.Equal(t, []string{"a1", "w1"}, run(q, Params{Kinds: []string{"assistant", "work"}, Before: 250}), q)
	}
}
