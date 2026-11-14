package threads

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 夹具：真库 + 线程服务（nopOrbs），直接经 onAppend / onReset / onResetDone 入库

const factsOrb = "orb-facts"

type factsEnv struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	s       *Service
	offsets map[string]int64 // threadID + "\x00" + file → 下一次追加的 baseOffset
}

func newFactsEnv(t *testing.T) *factsEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := New(Config{DataDir: t.TempDir()}, st, nopOrbs{})
	t.Cleanup(s.Close)
	for _, id := range []string{"prj_1", "prj_2"} {
		require.NoError(t, st.Q.CreateProject(ctx, db.CreateProjectParams{ID: id, Name: id, ShallowClone: true, CreatedAt: 1, UpdatedAt: 1}))
	}
	return &factsEnv{t: t, ctx: ctx, st: st, s: s, offsets: map[string]int64{}}
}

type threadOpt struct {
	kind           string
	project, space *string
	parent         *string
	createdAt      int64
}

// thread 建一个线程行并把它挂到夹具 orb 上（onAppend 只接受本 orb 的线程）。
func (e *factsEnv) thread(id string, o threadOpt) {
	e.t.Helper()
	if o.kind == "" {
		o.kind = KindWork
	}
	if o.createdAt == 0 {
		o.createdAt = 1
	}
	require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		Kind: o.kind, ID: id, ProjectID: o.project, SpaceID: o.space, ParentThreadID: o.parent, Title: "", TitleSource: "auto",
		State: StateIdle, SessionDir: "~/.oryn/threads/" + id, CreatedAt: o.createdAt, UpdatedAt: o.createdAt,
	}))
	th, err := e.s.get(e.ctx, id)
	require.NoError(e.t, err)
	th.mu.Lock()
	th.orbID = factsOrb
	th.mu.Unlock()
}

// appendLines 按游标把若干行追加进线程的某个文件。
func (e *factsEnv) appendLines(tid, file string, lines ...string) {
	e.t.Helper()
	k := tid + "\x00" + file
	base := e.offsets[k]
	end := base
	for _, l := range lines {
		end += int64(len(l) + 1)
	}
	raw, _ := json.Marshal(apigen.LinkCtlEntriesAppendParams{ThreadID: tid, File: file, BaseOffset: base, EndOffset: end, Lines: lines})
	res, err := e.s.onAppend(e.ctx, factsOrb, raw)
	require.NoError(e.t, err)
	require.True(e.t, res.(apigen.LinkCtlEntriesAppendResult).Accepted)
	e.offsets[k] = end
}

// rewrite 模拟 omp 整文件重写：entries.reset → 从 0 重发 lines → entries.reset_done。
func (e *factsEnv) rewrite(tid, file string, lines ...string) {
	e.t.Helper()
	e.reset(tid, file)
	e.appendLines(tid, file, lines...)
	e.resetDone(tid, file)
}

func (e *factsEnv) reset(tid, file string) {
	raw, _ := json.Marshal(apigen.LinkCtlEntriesResetParams{ThreadID: tid, File: file})
	e.s.onReset(e.ctx, factsOrb, raw)
	e.offsets[tid+"\x00"+file] = 0
}

func (e *factsEnv) resetDone(tid, file string) {
	raw, _ := json.Marshal(apigen.LinkCtlEntriesResetDoneParams{ThreadID: tid, File: file})
	e.s.onResetDone(e.ctx, factsOrb, raw)
}

// 三张事实表除自增 id 之外的全部列，按 (thread_id, entry_seq) 排序。
const (
	usageCols = `thread_id, entry_seq, ts, provider, model, purpose, stop_reason, ukey, input_tokens, output_tokens,
		cache_read_tokens, cache_write_tokens, cost_usd, duration_ms, ttft_ms, is_subagent, project_id, space_id`
	toolCols = `thread_id, entry_seq, ts, tool, is_error, ukey, project_id, space_id`
	turnCols = `thread_id, entry_seq, ts, ukey, project_id, space_id`
)

func (e *factsEnv) rows(query string, args ...any) []string {
	e.t.Helper()
	rs, err := e.st.DB.QueryContext(e.ctx, query, args...)
	require.NoError(e.t, err)
	defer rs.Close()
	cols, err := rs.Columns()
	require.NoError(e.t, err)
	var out []string
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(e.t, rs.Scan(ptrs...))
		parts := make([]string, len(vals))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "|"))
	}
	require.NoError(e.t, rs.Err())
	return out
}

// facts 返回三张事实表的全部行（不含自增 id）。
func (e *factsEnv) facts() map[string][]string {
	return map[string][]string{
		"usage": e.rows(`SELECT ` + usageCols + ` FROM usage_records ORDER BY thread_id, entry_seq, ukey`),
		"tools": e.rows(`SELECT ` + toolCols + ` FROM tool_calls ORDER BY thread_id, entry_seq, ukey`),
		"turns": e.rows(`SELECT ` + turnCols + ` FROM turns ORDER BY thread_id, entry_seq, ukey`),
	}
}

type factTotals struct {
	cost                  float64
	requests, tools, errs int
	turns                 int
}

func (e *factsEnv) totals(where string) factTotals {
	e.t.Helper()
	if where == "" {
		where = "1"
	}
	var f factTotals
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT COALESCE(SUM(cost_usd), 0), COUNT(*) FROM usage_records WHERE `+where).Scan(&f.cost, &f.requests))
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*), COALESCE(SUM(is_error), 0) FROM tool_calls WHERE `+where).Scan(&f.tools, &f.errs))
	require.NoError(e.t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM turns WHERE `+where).Scan(&f.turns))
	f.cost = float64(int64(f.cost*1e6+0.5)) / 1e6
	return f
}

func (e *factsEnv) state(key string) (string, bool) {
	v, ok, err := getState(e.ctx, e.st.Q, key)
	require.NoError(e.t, err)
	return v, ok
}

// ---- 会话行构造（与 omp 同形）

const factsT0 = int64(1794000000000)

func isoMs(ts int64) string { return time.UnixMilli(ts).UTC().Format("2006-01-02T15:04:05.000Z") }

func parentJSON(p string) string {
	if p == "" {
		return "null"
	}
	return strconv.Quote(p)
}

func factUserLine(id, parent string, ts int64) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":%q,"message":{"role":"user","content":"q","timestamp":%d}}`,
		id, parentJSON(parent), isoMs(ts), ts)
}

func asstLine(id, parent string, ts int64, input int64, cost float64, stop string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":%q,"message":{"role":"assistant","content":[],"provider":"anthropic","model":"opus","stopReason":%q,"duration":1500.4,"ttft":200,"timestamp":%d,"usage":{"input":%d,"output":30,"cacheRead":5,"cacheWrite":2,"cost":{"total":%g}}}}`,
		id, parentJSON(parent), isoMs(ts), stop, ts, input, cost)
}

func toolLine(id, parent string, ts int64, isErr bool) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":%q,"message":{"role":"toolResult","toolCallId":"c-%s","toolName":"bash","isError":%t,"content":[],"timestamp":%d}}`,
		id, parentJSON(parent), isoMs(ts), id, isErr, ts)
}

func usageLine(id, parent, activeLeaf string, ts int64, purpose string, cost float64) string {
	return fmt.Sprintf(`{"type":"model_usage","id":%q,"parentId":%s,"timestamp":%q,"activeLeafId":%s,"purpose":%q,"api":"fake","provider":"fake","model":"fake-model","stopReason":"stop","usage":{"input":7,"output":3,"cacheRead":0,"cacheWrite":0,"cost":{"total":%g}}}`,
		id, parentJSON(parent), isoMs(ts), parentJSON(activeLeaf), purpose, cost)
}

// turnLines 是 fakeomp 一个回合的会话行：user → assistant(toolUse) → toolResult → assistant(stop) → 分支外 model_usage(title)。
func turnLines(p string, parent string, ts int64) []string {
	return []string{
		factUserLine(p+"u", parent, ts),
		asstLine(p+"a1", p+"u", ts+1, 100, 0.25, "toolUse"),
		toolLine(p+"t", p+"a1", ts+2, false),
		asstLine(p+"a2", p+"t", ts+3, 120, 0.5, "stop"),
		usageLine(p+"m", p+"u", p+"a2", ts+4, "title", 0.125),
	}
}

// ---- 测试

func TestParseLineFacts(t *testing.T) {
	p, err := ParseLine(`{"type":"model_usage","id":"m1","parentId":"u1","activeLeafId":"a1","timestamp":"2026-11-12T10:00:03.000Z","purpose":"title","api":"x","provider":"openai","model":"gpt","stopReason":"error","usage":{"input":4,"output":2,"cacheRead":1,"cacheWrite":3,"cost":{"total":0.5}}}`)
	require.NoError(t, err)
	require.NotNil(t, p.Usage)
	assert.Equal(t, UsageRecord{TS: 1794477603000, Provider: "openai", Model: "gpt", Purpose: "title", StopReason: "error",
		Input: 4, Output: 2, CacheRead: 1, CacheWrt: 3, CostUSD: 0.5}, *p.Usage, "model_usage 无 duration / ttft，时间取条目时间")
	assert.Nil(t, p.TurnTS)

	p, err = ParseLine(`{"type":"model_usage","id":"m2","parentId":"u1","usage":{"input":1,"output":1}}`)
	require.NoError(t, err)
	require.NotNil(t, p.Usage)
	assert.Equal(t, "unknown", p.Usage.Purpose, "purpose 缺失记 unknown")
	assert.Zero(t, p.Usage.TS, "没有条目时间时为 0（入库时取 now）")

	p, err = ParseLine(`{"type":"model_usage","id":"m3","purpose":"title"}`)
	require.NoError(t, err)
	assert.Nil(t, p.Usage, "不带 usage 的 model_usage 不产出用量行")

	p, err = ParseLine(asstLine("a1", "u1", 5000, 10, 0.01, "length"))
	require.NoError(t, err)
	require.NotNil(t, p.Usage)
	assert.Equal(t, "length", p.Usage.StopReason)
	assert.Equal(t, "", p.Usage.Purpose)
	assert.Equal(t, int64(1500), *p.Usage.DurationMs)
	assert.Nil(t, p.TurnTS)

	p, err = ParseLine(factUserLine("u1", "", 7000))
	require.NoError(t, err)
	require.NotNil(t, p.TurnTS)
	assert.Equal(t, int64(7000), *p.TurnTS)
	assert.Nil(t, p.Usage)
	p, err = ParseLine(`{"type":"message","id":"u2","parentId":null,"timestamp":"2026-11-12T10:00:00.000Z","message":{"role":"user","content":"x"}}`)
	require.NoError(t, err)
	require.NotNil(t, p.TurnTS)
	assert.Equal(t, int64(1794477600000), *p.TurnTS, "消息时间为 0 时回落条目时间")
}

// 一个回合：用量 3 行（两条 assistant + 一条 title）、工具 1 行、回合 1 行；同一批行重发两次行数不变。
// 子 agent 文件的用量记 is_subagent，其中的 user 消息不算回合。
func TestFactsIngestTurn(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T-1", threadOpt{})
	lines := turnLines("x", "", factsT0)
	e.appendLines("T-1", "main.jsonl", lines...)
	want := factTotals{cost: 0.875, requests: 3, tools: 1, turns: 1}
	assert.Equal(t, want, e.totals(""))
	assert.Equal(t, []string{"title|stop", "|stop", "|toolUse"}, e.rows(`SELECT purpose || '|' || stop_reason FROM usage_records ORDER BY 1`))
	assert.Equal(t, []string{"xa1@" + strconv.FormatInt(factsT0+1, 10)}, e.rows(`SELECT ukey FROM usage_records WHERE purpose = '' AND stop_reason = 'toolUse'`))
	assert.Equal(t, []string{"xu@" + strconv.FormatInt(factsT0, 10)}, e.rows(`SELECT ukey FROM turns`))
	before := e.facts()

	e.appendLines("T-1", "main.jsonl", lines...)
	e.appendLines("T-1", "main.jsonl", lines...)
	assert.Equal(t, before, e.facts(), "重发同一批行不新增事实行")

	e.appendLines("T-1", "main_x/sub.jsonl", factUserLine("su", "", factsT0+10), asstLine("sa", "su", factsT0+11, 9, 0.0625, "stop"))
	assert.Equal(t, factTotals{cost: 0.9375, requests: 4, tools: 1, turns: 1}, e.totals(""), "子 agent 的 user 消息不算回合")
	assert.Equal(t, []string{"1"}, e.rows(`SELECT is_subagent FROM usage_records WHERE ukey LIKE 'sa@%'`))
}

// reset：内容相同 → 行与 id 不变；某条 assistant 的 usage 改了 → 该行随之更新；
// 去掉一条 assistant 并新增 discarded-turn 补记 → reset_done 后旧行消失、新行出现，总成本不变。
func TestFactsReset(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T-1", threadOpt{})
	lines := turnLines("x", "", factsT0)
	e.appendLines("T-1", "main.jsonl", lines...)
	idsBefore := e.rows(`SELECT id FROM usage_records ORDER BY id`)
	before := e.facts()

	e.rewrite("T-1", "main.jsonl", lines...)
	assert.Equal(t, before, e.facts())
	assert.Equal(t, idsBefore, e.rows(`SELECT id FROM usage_records ORDER BY id`), "内容相同不重写行")

	changed := append([]string(nil), lines...)
	changed[1] = asstLine("xa1", "xu", factsT0+1, 999, 2.0, "toolUse")
	e.rewrite("T-1", "main.jsonl", changed...)
	assert.Equal(t, []string{"999|2"}, e.rows(`SELECT input_tokens, cost_usd FROM usage_records WHERE ukey LIKE 'xa1@%'`))
	assert.Equal(t, factTotals{cost: 2.625, requests: 3, tools: 1, turns: 1}, e.totals(""))

	// omp discardEntryDurably：删掉 xa2，以 model_usage{purpose: discarded-turn, timestamp: 原条目时间} 补记其用量
	discarded := []string{changed[0], changed[1], changed[2], changed[4],
		usageLine("xd", "xt", "xt", factsT0+3, "discarded-turn", 0.5)}
	e.reset("T-1", "main.jsonl")
	e.appendLines("T-1", "main.jsonl", discarded...)
	e.resetDone("T-1", "main.jsonl")
	assert.Equal(t, factTotals{cost: 2.625, requests: 3, tools: 1, turns: 1}, e.totals(""), "总成本不变")
	assert.Empty(t, e.rows(`SELECT 1 FROM usage_records WHERE ukey LIKE 'xa2@%'`))
	assert.Equal(t, []string{"discarded-turn"}, e.rows(`SELECT purpose FROM usage_records WHERE ukey LIKE 'xd@%'`))
}

// dropFile 整体移除一个文件：它的三类事实行清零，其它文件不受影响。
func TestFactsDropFile(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T-1", threadOpt{})
	e.appendLines("T-1", "main.jsonl", turnLines("x", "", factsT0)...)
	keep := e.facts()
	e.appendLines("T-1", "other.jsonl", turnLines("y", "", factsT0+100)...)
	assert.Equal(t, 2, e.totals("").turns)
	th, err := e.s.get(e.ctx, "T-1")
	require.NoError(t, err)
	e.s.dropFile(e.ctx, th, "other.jsonl")
	assert.Equal(t, keep, e.facts())
}

// fork 拷贝来的历史只记在先入库的源线程：共用 orb（fork_from 产物 = 到叶子的分支）与连环境（整目录副本，含分支外
// 条目与子 agent 转录）两种都不改变全库合计；新线程自己的新回合照常计入它名下。
func TestFactsForkDedup(t *testing.T) {
	for _, name := range []string{"share", "copy"} {
		t.Run(name, func(t *testing.T) {
			e := newFactsEnv(t)
			e.thread("T-src", threadOpt{createdAt: 1})
			turn1 := turnLines("x", "", factsT0)
			sub := []string{factUserLine("su", "", factsT0+5), asstLine("sa", "su", factsT0+6, 9, 0.0625, "stop")}
			e.appendLines("T-src", "s1.jsonl", turn1...)
			e.appendLines("T-src", "s1/sub.jsonl", sub...)
			total := e.totals("")
			require.Equal(t, factTotals{cost: 0.9375, requests: 4, tools: 1, turns: 1}, total)

			e.thread("T-fork", threadOpt{createdAt: 2, parent: new("T-src")})
			var last string
			if name == "share" {
				// fork_from 产物：路径 u → a1 → t → a2（分支外的 model_usage 不在路径上）
				e.appendLines("T-fork", "f1.jsonl", turn1[:4]...)
				last = "f1.jsonl"
			} else {
				e.appendLines("T-fork", "s1.jsonl", turn1...)
				e.appendLines("T-fork", "s1/sub.jsonl", sub...)
				last = "s1.jsonl"
			}
			assert.Equal(t, total, e.totals(""), "拷贝来的历史不重复计入")
			assert.Equal(t, factTotals{}, e.totals(`thread_id = 'T-fork'`))

			e.appendLines("T-fork", last, turnLines("y", "xa2", factsT0+1000)...)
			assert.Equal(t, factTotals{cost: 0.875, requests: 3, tools: 1, turns: 1}, e.totals(`thread_id = 'T-fork'`))
			assert.Equal(t, total, e.totals(`thread_id = 'T-src'`))
		})
	}
}

// 删线程：三张表该线程的行 thread_id 置空、写入删除前的项目 / Space；之后别的线程再入库同 ukey 的条目仍被忽略。
// Space 聊天线程走同一条删除路径。
func TestFactsDeleteTombstone(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T-del", threadOpt{project: new("prj_1"), space: new("spc_1")})
	lines := turnLines("x", "", factsT0)
	e.appendLines("T-del", "main.jsonl", lines...)
	e.thread("T-chat", threadOpt{kind: KindSpace, project: new("prj_2"), space: new("spc_2")})
	e.appendLines("T-chat", "main.jsonl", turnLines("c", "", factsT0+500)...)
	total := e.totals("")

	require.NoError(t, e.s.Delete(e.ctx, "T-del"))
	for _, tbl := range []string{"usage_records", "tool_calls", "turns"} {
		assert.Equal(t, []string{"<nil>|prj_1|spc_1"}, e.rows(`SELECT DISTINCT thread_id, project_id, space_id FROM `+tbl+` WHERE ukey LIKE 'x%'`), tbl)
	}
	assert.Equal(t, total, e.totals(""), "删线程不删事实行")

	inTxRan := false
	require.NoError(t, e.s.DeleteSpaceThread(e.ctx, "T-chat", func(*db.Queries) error {
		inTxRan = true
		return nil
	}))
	assert.True(t, inTxRan)
	for _, tbl := range []string{"usage_records", "tool_calls", "turns"} {
		assert.Equal(t, []string{"<nil>|prj_2|spc_2"}, e.rows(`SELECT DISTINCT thread_id, project_id, space_id FROM `+tbl+` WHERE ukey LIKE 'c%'`), tbl)
	}

	e.thread("T-other", threadOpt{createdAt: 5})
	e.appendLines("T-other", "main.jsonl", lines...)
	assert.Equal(t, total, e.totals(""), "已删线程的 ukey 仍然占用")
	assert.Equal(t, factTotals{}, e.totals(`thread_id = 'T-other'`))
}

// RebuildThreadFacts：手工写坏（删行、改行、插入多余行）→ 重算后与全新入库逐行相等；条目超过 500 条时走到第二页。
func TestRebuildThreadFacts(t *testing.T) {
	e := newFactsEnv(t)
	e.thread("T-1", threadOpt{project: new("prj_1")})
	var lines []string
	parent := ""
	for i := range 130 { // 130 回合 × 5 行 = 650 条条目
		ts := factsT0 + int64(i)*100
		p := fmt.Sprintf("r%03d", i)
		lines = append(lines, turnLines(p, parent, ts)...)
		parent = p + "a2"
	}
	e.appendLines("T-1", "main.jsonl", lines...)
	e.appendLines("T-1", "main/sub.jsonl", factUserLine("su", "", factsT0+7), asstLine("sa", "su", factsT0+8, 3, 0.03125, "stop"))
	fresh := e.facts()
	require.Len(t, fresh["usage"], 130*3+1)
	require.Len(t, fresh["turns"], 130)

	_, err := e.st.DB.ExecContext(e.ctx, `DELETE FROM usage_records WHERE ukey LIKE 'r12%'`)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE usage_records SET cost_usd = 99, purpose = 'bogus' WHERE ukey LIKE 'r0%'`)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `DELETE FROM turns WHERE ukey LIKE 'r129%'`)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE tool_calls SET is_error = 1`)
	require.NoError(t, err)
	_, err = e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: new("T-1"), EntrySeq: 99999, Ts: 1, Provider: "x", Model: "y", CostUsd: 5})
	require.NoError(t, err)
	_, err = e.st.Q.InsertTurn(e.ctx, db.InsertTurnParams{ThreadID: new("T-1"), EntrySeq: 99998, Ts: 1})
	require.NoError(t, err)
	require.NotEqual(t, fresh, e.facts())

	require.NoError(t, RebuildThreadFacts(e.ctx, e.st, "T-1", nil))
	assert.Equal(t, fresh, e.facts())
}

// backfillWorld 建三个线程：源 T-z-src（最早）、它的 fork T-a-fork（id 字典序更小，但创建更晚）、无关的 T-m-other；
// 各自按新管线入库，返回全新入库的事实行作为期望值。
func backfillWorld(t *testing.T) (*factsEnv, map[string][]string) {
	e := newFactsEnv(t)
	e.thread("T-z-src", threadOpt{createdAt: 10, project: new("prj_1")})
	e.thread("T-a-fork", threadOpt{createdAt: 20, parent: new("T-z-src")})
	e.thread("T-m-other", threadOpt{createdAt: 30, project: new("prj_2")})
	turn1 := turnLines("x", "", factsT0)
	e.appendLines("T-z-src", "s.jsonl", turn1...)
	e.appendLines("T-a-fork", "f.jsonl", turn1[:4]...)
	e.appendLines("T-a-fork", "f.jsonl", turnLines("y", "xa2", factsT0+1000)...)
	e.appendLines("T-m-other", "o.jsonl", turnLines("z", "", factsT0+2000)...)
	return e, e.facts()
}

// legacyFacts 模拟升级前的数据：清空事实行后按旧管线逐线程逐条目写一行（ukey 为 NULL，fork 拷贝的历史父子各一份）。
func (e *factsEnv) legacyFacts() {
	e.t.Helper()
	for _, tbl := range []string{"usage_records", "tool_calls", "turns"} {
		_, err := e.st.DB.ExecContext(e.ctx, `DELETE FROM `+tbl)
		require.NoError(e.t, err)
	}
	for _, tid := range []string{"T-z-src", "T-a-fork", "T-m-other"} {
		rows, err := e.st.Q.ListEntriesAfter(e.ctx, db.ListEntriesAfterParams{ThreadID: tid, AfterSeq: 0, Limit: 1000})
		require.NoError(e.t, err)
		for _, r := range rows {
			pe, err := ParseLine(string(r.Payload))
			require.NoError(e.t, err)
			if u := pe.Usage; u != nil {
				_, err := e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: &tid, EntrySeq: r.Seq, Ts: u.TS,
					Provider: u.Provider, Model: u.Model, InputTokens: u.Input, OutputTokens: u.Output, CostUsd: u.CostUSD})
				require.NoError(e.t, err)
			}
			if c := pe.Tool; c != nil {
				_, err := e.st.Q.InsertToolCall(e.ctx, db.InsertToolCallParams{ThreadID: &tid, EntrySeq: r.Seq, Ts: c.TS, Tool: c.Tool})
				require.NoError(e.t, err)
			}
		}
	}
}

func TestBackfillFacts(t *testing.T) {
	t.Run("upgrade", func(t *testing.T) {
		e, want := backfillWorld(t)
		e.legacyFacts()
		require.Greater(t, e.totals(`thread_id = 'T-a-fork'`).requests, 3, "旧数据里 fork 线程也有一份拷贝来的用量")
		require.NoError(t, BackfillFacts(e.ctx, e.st, nil, false))
		assert.Equal(t, want, e.facts(), "重复旧行只归创建更早的源线程")
		assert.Equal(t, factTotals{cost: 0.875, requests: 3, tools: 1, turns: 1}, e.totals(`thread_id = 'T-a-fork'`))
		v, ok := e.state(stateFactsVersion)
		assert.True(t, ok)
		assert.Equal(t, strconv.Itoa(FactsVersion), v)
		for _, k := range []string{stateBackfillCur, stateBackfillDone, stateBackfillTotal} {
			_, ok := e.state(k)
			assert.False(t, ok, k)
		}

		// 版本相符：不动任何行
		_, err := e.st.Q.InsertUsageRecord(e.ctx, db.InsertUsageRecordParams{ThreadID: new("T-m-other"), EntrySeq: 99999, Ts: 1, CostUsd: 7})
		require.NoError(t, err)
		junk := e.facts()
		require.NoError(t, BackfillFacts(e.ctx, e.st, nil, false))
		assert.Equal(t, junk, e.facts())
	})

	t.Run("resume", func(t *testing.T) {
		e, want := backfillWorld(t)
		e.legacyFacts()
		ctx, cancel := context.WithCancel(e.ctx)
		var seen []string
		beforeBackfillThread = func(id string) {
			seen = append(seen, id)
			if id == "T-a-fork" {
				cancel()
			}
		}
		t.Cleanup(func() { beforeBackfillThread = nil })
		err := BackfillFacts(ctx, e.st, nil, false)
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, []string{"T-z-src", "T-a-fork"}, seen)
		cur, _ := e.state(stateBackfillCur)
		assert.Equal(t, "10:T-z-src", cur)
		done, _ := e.state(stateBackfillDone)
		assert.Equal(t, "1", done)
		total, _ := e.state(stateBackfillTotal)
		assert.Equal(t, "3", total)
		_, ok := e.state(stateFactsVersion)
		assert.False(t, ok)

		seen = nil
		require.NoError(t, BackfillFacts(e.ctx, e.st, nil, false))
		assert.Equal(t, []string{"T-a-fork", "T-m-other"}, seen, "从游标之后续作")
		assert.Equal(t, want, e.facts(), "与一次跑完相同")
		v, _ := e.state(stateFactsVersion)
		assert.Equal(t, strconv.Itoa(FactsVersion), v)
	})

	t.Run("purge", func(t *testing.T) {
		e, want := backfillWorld(t)
		// 已删线程的冻结行不受 purge 影响
		e.thread("T-gone", threadOpt{createdAt: 5, project: new("prj_2")})
		e.appendLines("T-gone", "g.jsonl", turnLines("g", "", factsT0+5000)...)
		require.NoError(t, e.s.Delete(e.ctx, "T-gone"))
		frozen := e.rows(`SELECT ` + usageCols + ` FROM usage_records WHERE thread_id IS NULL ORDER BY ukey`)
		require.Len(t, frozen, 3)
		want["usage"] = append(append([]string(nil), frozen...), want["usage"]...)
		want["tools"] = append(e.rows(`SELECT `+toolCols+` FROM tool_calls WHERE thread_id IS NULL`), want["tools"]...)
		want["turns"] = append(e.rows(`SELECT `+turnCols+` FROM turns WHERE thread_id IS NULL`), want["turns"]...)

		_, err := e.st.DB.ExecContext(e.ctx, `UPDATE usage_records SET cost_usd = 42 WHERE thread_id IS NOT NULL`)
		require.NoError(t, err)
		require.NoError(t, e.st.Q.SetStatsState(e.ctx, db.SetStatsStateParams{Key: stateFactsVersion, Value: "0"}))
		require.NoError(t, BackfillFacts(e.ctx, e.st, nil, true))
		assert.Equal(t, want, e.facts())
	})

	t.Run("empty", func(t *testing.T) {
		e := newFactsEnv(t)
		require.NoError(t, BackfillFacts(e.ctx, e.st, nil, false))
		v, ok := e.state(stateFactsVersion)
		assert.True(t, ok)
		assert.Equal(t, strconv.Itoa(FactsVersion), v)
		_, ok = e.state(stateBackfillTotal)
		assert.False(t, ok)
	})
}
