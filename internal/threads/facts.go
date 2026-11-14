package threads

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// FactsVersion 是派生事实行的版本；提高它会让下次启动整体重算。
const FactsVersion = 2

// stats_state 的键。
const (
	stateFactsVersion  = "facts_version"
	stateBackfillCur   = "backfill_cursor" // "<created_at>:<thread_id>"；空串 = 尚未完成任何线程
	stateBackfillDone  = "backfill_done"
	stateBackfillTotal = "backfill_total"
)

// rebuildPage 是 RebuildThreadFacts 每个事务处理的条目数。
const rebuildPage = 500

// beforeBackfillThread 是测试钩子：BackfillFacts 重算每个线程之前调用（测试据此在指定线程处取消 ctx）。
var beforeBackfillThread func(threadID string)

// factKey 是跨线程去重键：fork 拷贝的条目 id 与时间戳都与源条目相同。
func factKey(entryID string, ts int64) string { return entryID + "@" + strconv.FormatInt(ts, 10) }

// writeFacts 写出一条条目派生的事实行（用量 / 工具调用 / 回合）。ts 为 0 时取 now。
// 三类写入都是 INSERT OR IGNORE：同一 (thread_id, entry_seq) 已有行、或 ukey 已被别的线程（含已删线程）占用时不再写——
// fork 拷贝来的历史因此只记在先入库的线程名下。回合只记主会话文件里的 user 消息。
func writeFacts(ctx context.Context, q *db.Queries, threadID string, seq int64, file string, pe ParsedEntry, now int64) error {
	tid := threadID
	at := func(ts int64) (int64, *string) {
		if ts == 0 {
			ts = now
		}
		k := factKey(pe.EntryID, ts)
		return ts, &k
	}
	if u := pe.Usage; u != nil {
		ts, key := at(u.TS)
		if _, err := q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{
			ThreadID: &tid, EntrySeq: seq, Ts: ts, Provider: u.Provider, Model: u.Model,
			Purpose: u.Purpose, StopReason: u.StopReason, Ukey: key,
			InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrt,
			CostUsd: u.CostUSD, DurationMs: u.DurationMs, TtftMs: u.TTFTMs, IsSubagent: IsSubagentFile(file),
		}); err != nil {
			return err
		}
	}
	if c := pe.Tool; c != nil {
		ts, key := at(c.TS)
		if _, err := q.InsertToolCall(ctx, db.InsertToolCallParams{
			ThreadID: &tid, EntrySeq: seq, Ts: ts, Tool: c.Tool, IsError: c.IsError, Ukey: key,
		}); err != nil {
			return err
		}
	}
	if pe.TurnTS != nil && isMainFile(file) {
		ts, key := at(*pe.TurnTS)
		if _, err := q.InsertTurn(ctx, db.InsertTurnParams{ThreadID: &tid, EntrySeq: seq, Ts: ts, Ukey: key}); err != nil {
			return err
		}
	}
	return nil
}

// deleteFactsAt 删除一条条目（thread, seq）派生的全部事实行（reset 重发且内容变了时先清再写）。
func deleteFactsAt(ctx context.Context, q *db.Queries, threadID string, seq int64) error {
	if err := q.DeleteUsageAt(ctx, db.DeleteUsageAtParams{ThreadID: threadID, EntrySeq: seq}); err != nil {
		return err
	}
	if err := q.DeleteToolCallAt(ctx, db.DeleteToolCallAtParams{ThreadID: threadID, EntrySeq: seq}); err != nil {
		return err
	}
	return q.DeleteTurnAt(ctx, db.DeleteTurnAtParams{ThreadID: threadID, EntrySeq: seq})
}

// deleteOrphanFacts 删除线程中条目已不存在的事实行（reset_done 删 stale 条目、dropFile 整体移除文件之后）。
func deleteOrphanFacts(ctx context.Context, q *db.Queries, threadID string) error {
	if _, err := q.DeleteOrphanUsage(ctx, threadID); err != nil {
		return err
	}
	if _, err := q.DeleteOrphanToolCalls(ctx, threadID); err != nil {
		return err
	}
	_, err := q.DeleteOrphanTurns(ctx, threadID)
	return err
}

// tombstoneFacts 把线程当前的项目 / Space 写进它的事实行（随后删线程行，thread_id 经外键置空，事实行冻结）。
func tombstoneFacts(ctx context.Context, q *db.Queries, threadID string, projectID, spaceID *string) error {
	if err := q.TombstoneUsage(ctx, db.TombstoneUsageParams{ProjectID: projectID, SpaceID: spaceID, ThreadID: threadID}); err != nil {
		return err
	}
	if err := q.TombstoneToolCalls(ctx, db.TombstoneToolCallsParams{ProjectID: projectID, SpaceID: spaceID, ThreadID: threadID}); err != nil {
		return err
	}
	return q.TombstoneTurns(ctx, db.TombstoneTurnsParams{ProjectID: projectID, SpaceID: spaceID, ThreadID: threadID})
}

func nowFunc(now func() int64) func() int64 {
	if now != nil {
		return now
	}
	return func() int64 { return time.Now().UnixMilli() }
}

// RebuildThreadFacts 按当前条目重算一个线程的事实行：首个事务清空该线程的三张事实表并处理前 500 条条目，
// 之后每个事务再处理 500 条（ListEntriesAfter 翻页），直到取尽。now 为 nil 时用 time.Now。
func RebuildThreadFacts(ctx context.Context, st *store.Store, threadID string, now func() int64) error {
	now = nowFunc(now)
	var after int64
	for first := true; ; first = false {
		done := false
		next := after
		err := st.Tx(ctx, func(q *db.Queries) error {
			if first {
				if err := q.DeleteThreadUsage(ctx, threadID); err != nil {
					return err
				}
				if err := q.DeleteThreadToolCalls(ctx, threadID); err != nil {
					return err
				}
				if err := q.DeleteThreadTurns(ctx, threadID); err != nil {
					return err
				}
			}
			rows, err := q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: threadID, AfterSeq: after, Limit: rebuildPage})
			if err != nil {
				return err
			}
			ts := now()
			for _, e := range rows {
				pe, err := ParseLine(string(e.Payload))
				if err != nil {
					continue
				}
				if err := writeFacts(ctx, q, threadID, e.Seq, e.File, pe, ts); err != nil {
					return err
				}
				next = e.Seq
			}
			done = len(rows) < rebuildPage
			return nil
		})
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		after = next
	}
}

// backfillPos 是回填游标 (created_at, thread_id)。
type backfillPos struct {
	createdAt int64
	id        string
}

func (p backfillPos) String() string { return strconv.FormatInt(p.createdAt, 10) + ":" + p.id }

func parseBackfillPos(s string) (backfillPos, bool) {
	ts, id, ok := strings.Cut(s, ":")
	if !ok {
		return backfillPos{}, false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return backfillPos{}, false
	}
	return backfillPos{n, id}, true
}

func (p backfillPos) cmp(o backfillPos) int {
	return cmp.Or(cmp.Compare(p.createdAt, o.createdAt), cmp.Compare(p.id, o.id))
}

// getState 读一个 stats_state 键；不存在时 ok = false。
func getState(ctx context.Context, q *db.Queries, key string) (string, bool, error) {
	v, err := q.GetStatsState(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// finishBackfill 写入当前版本号并删除三个进度键。
func finishBackfill(ctx context.Context, st *store.Store) error {
	return st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetStatsState(ctx, db.SetStatsStateParams{Key: stateFactsVersion, Value: strconv.Itoa(FactsVersion)}); err != nil {
			return err
		}
		for _, k := range []string{stateBackfillCur, stateBackfillDone, stateBackfillTotal} {
			if err := q.DeleteStatsState(ctx, k); err != nil {
				return err
			}
		}
		return nil
	})
}

// BackfillFacts 在 stats_state.facts_version != FactsVersion 时按 (created_at, id) 升序逐线程 RebuildThreadFacts：
// 开始时写 backfill_total 与空的 backfill_cursor，每完成一个线程更新 backfill_cursor / backfill_done，
// 全部完成后写 facts_version 并删除三个进度键。可被 ctx 中断，下次从游标之后继续。库里没有线程时直接写版本号。
// purge 为真且尚无游标时，先在一个事务里清空全部活线程的事实行（手动全量重算用）。
//
// 升序重算即可去掉历史上的 fork 重复：源线程先于 fork 线程创建，先拿到 ukey；轮到 fork 线程时它的旧行被清空，
// 拷贝来的条目因 ukey 已被占用而不再写入。升级前的旧行 ukey 为 NULL，不会挡住源线程。
func BackfillFacts(ctx context.Context, st *store.Store, now func() int64, purge bool) error {
	ver, _, err := getState(ctx, st.Q, stateFactsVersion)
	if err != nil {
		return err
	}
	if ver == strconv.Itoa(FactsVersion) {
		return nil
	}
	rows, err := st.Q.ListThreads(ctx, db.ListThreadsParams{})
	if err != nil {
		return err
	}
	list := make([]backfillPos, 0, len(rows))
	for _, r := range rows {
		list = append(list, backfillPos{r.CreatedAt, r.ID})
	}
	slices.SortFunc(list, backfillPos.cmp)

	curRaw, resuming, err := getState(ctx, st.Q, stateBackfillCur)
	if err != nil {
		return err
	}
	var cur *backfillPos
	var done int64
	if resuming {
		if p, ok := parseBackfillPos(curRaw); ok {
			cur = &p
		}
		if v, ok, err := getState(ctx, st.Q, stateBackfillDone); err != nil {
			return err
		} else if ok {
			done, _ = strconv.ParseInt(v, 10, 64)
		}
	} else {
		if len(list) == 0 {
			return finishBackfill(ctx, st)
		}
		err := st.Tx(ctx, func(q *db.Queries) error {
			if purge {
				if err := q.DeleteLiveUsage(ctx); err != nil {
					return err
				}
				if err := q.DeleteLiveToolCalls(ctx); err != nil {
					return err
				}
				if err := q.DeleteLiveTurns(ctx); err != nil {
					return err
				}
			}
			for k, v := range map[string]string{stateBackfillTotal: strconv.Itoa(len(list)), stateBackfillCur: "", stateBackfillDone: "0"} {
				if err := q.SetStatsState(ctx, db.SetStatsStateParams{Key: k, Value: v}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	slog.Info("threads: 开始回填统计事实行", "threads", len(list), "resume", cur != nil, "version", FactsVersion)
	for _, p := range list {
		if cur != nil && p.cmp(*cur) <= 0 {
			continue
		}
		if beforeBackfillThread != nil {
			beforeBackfillThread(p.id)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := RebuildThreadFacts(ctx, st, p.id, now); err != nil {
			return fmt.Errorf("重算线程 %s 的事实行: %w", p.id, err)
		}
		done++
		err := st.Tx(ctx, func(q *db.Queries) error {
			if err := q.SetStatsState(ctx, db.SetStatsStateParams{Key: stateBackfillCur, Value: p.String()}); err != nil {
				return err
			}
			return q.SetStatsState(ctx, db.SetStatsStateParams{Key: stateBackfillDone, Value: strconv.FormatInt(done, 10)})
		})
		if err != nil {
			return err
		}
	}
	if err := finishBackfill(ctx, st); err != nil {
		return err
	}
	slog.Info("threads: 统计事实行回填完成", "threads", done, "version", FactsVersion)
	return nil
}
