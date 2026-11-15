package threads

import (
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
// 3：入库钳制数值 / 字符串 / 时间，超长条目 id 在去重键里改为哈希（回填开头先由 sanitizeFacts 改写已有行）。
// 4：经 write xd://<tool> 派发的工具结果按真实工具名（details.xdev.tool）计入 tool_calls。
// 5：只认 details.xdev 带 args 的派发（设备解析成功），不存在的设备名仍记为 write。
const FactsVersion = 5

// stats_state 的键。
const (
	stateFactsVersion  = "facts_version"
	stateBackfillCur   = "backfill_cursor" // "<created_at>:<thread_id>"；空串 = 尚未完成任何线程
	stateBackfillDone  = "backfill_done"
	stateBackfillTotal = "backfill_total"
	// stateUkeyScheme 记录库里去重键的编码；不是 ukeyScheme 时 sanitizeFacts 把旧编码的键改写一次。
	// 独立于 facts_version：admin rebuild-stats 会把版本置 0，但已改写过的键不能再哈希第二次。
	stateUkeyScheme = "ukey_scheme"
	ukeyScheme      = "2"
)

// rebuildPage 是 RebuildThreadFacts 每个事务处理的条目数。
const rebuildPage = 500

// beforeBackfillThread 是测试钩子：BackfillFacts 重算每个线程之前调用（测试据此在指定线程处取消 ctx）。
var beforeBackfillThread func(threadID string)

// 去重键里的条目 id：常规 id（omp 是 8 位十六进制）原样保留；超过 maxFactKeyIDLen 字节的 id 换成
// 「前 ≤factKeyPrefixLen 字节 + "~" + sha256 十六进制」——长度 > 64，不会与任何原样保留的 id 相撞；前缀供 restoreFacts
// 走 entries_entry_id 索引按范围找回原条目。
const (
	maxFactKeyIDLen  = 64
	factKeyPrefixLen = 32
)

// 事实时间的合理范围：[minFactTS, now + maxFactSkewMs]，越界取 now（入库时间）。下界是固定日期（omp 与 Oryn 都晚于它，
// 任何真实会话、包括 fork 拷贝来的旧历史都在其后），不随线程创建时间变化——fork 线程的历史本就早于它的创建时间；
// 上界留一天给 orb 与控制面之间的时钟偏差。
const (
	minFactTS     = 1577836800000 // 2020-01-01T00:00:00Z
	maxFactSkewMs = 24 * 60 * 60 * 1000
)

func clampFactTS(ts, now int64) int64 {
	if ts < minFactTS || ts > now+maxFactSkewMs {
		return now
	}
	return ts
}

// factKey 是跨线程去重键：fork 拷贝的条目 id 与时间戳都与源条目相同。ts 用条目里的原值（不钳制），
// 同一条目无论何时、在哪个线程入库都得到同一个键。
func factKey(entryID string, ts int64) string { return factKeyID(entryID) + "@" + strconv.FormatInt(ts, 10) }

func factKeyID(entryID string) string {
	if len(entryID) <= maxFactKeyIDLen {
		return entryID
	}
	sum := sha256.Sum256([]byte(entryID))
	return truncBytes(entryID, factKeyPrefixLen) + "~" + hex.EncodeToString(sum[:])
}

// factKeyIDPart 取去重键里 "@<ts>" 之前的部分（ts 是十进制整数，不含 '@'）。
func factKeyIDPart(ukey string) (string, bool) {
	i := strings.LastIndexByte(ukey, '@')
	if i < 0 {
		return "", false
	}
	return ukey[:i], true
}

// factTable 是事实表编号（与 queries/stats.sql 的 ListFactKeysAt / FactKeyTaken 的 tbl 一致）。
type factTable int64

const (
	factUsage factTable = 1
	factTool  factTable = 2
	factTurn  factTable = 3
)

// factRef 指一张事实表里的一个去重键。
type factRef struct {
	tbl  factTable
	ukey string
}

// writeFacts 写出一条条目派生的事实行（用量 / 工具调用 / 回合）。ts 为 0 时取 now。
// 三类写入都是 INSERT OR IGNORE：同一 (thread_id, entry_seq) 已有行、或 ukey 已被别的线程（含已删线程）占用时不再写——
// fork 拷贝来的历史因此只记在先入库的线程名下。回合只记主会话文件里的 user 消息。
func writeFacts(ctx context.Context, q *db.Queries, threadID string, seq int64, file string, pe ParsedEntry, now int64) error {
	_, err := writeFactsFor(ctx, q, threadID, seq, file, pe, now, nil)
	return err
}

// writeFactsFor 同 writeFacts；want 非 nil 时只写 (表, 键) 在 want 里的事实行。返回实际写入的行。
// 去重键用条目里的原始时间，事实时间按 clampFactTS 钳制。
func writeFactsFor(ctx context.Context, q *db.Queries, threadID string, seq int64, file string, pe ParsedEntry, now int64, want map[factRef]bool) ([]factRef, error) {
	tid := threadID
	var wrote []factRef
	at := func(tbl factTable, ts int64) (int64, factRef, bool) {
		if ts == 0 {
			ts = now
		}
		r := factRef{tbl, factKey(pe.EntryID, ts)}
		return clampFactTS(ts, now), r, want == nil || want[r]
	}
	record := func(r factRef, n int64, err error) error {
		if err == nil && n > 0 {
			wrote = append(wrote, r)
		}
		return err
	}
	if u := pe.Usage; u != nil {
		if ts, r, ok := at(factUsage, u.TS); ok {
			n, err := q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{
				ThreadID: &tid, EntrySeq: seq, Ts: ts, Provider: u.Provider, Model: u.Model,
				Purpose: u.Purpose, StopReason: u.StopReason, Ukey: &r.ukey,
				InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrt,
				CostUsd: u.CostUSD, DurationMs: u.DurationMs, TtftMs: u.TTFTMs, IsSubagent: IsSubagentFile(file),
			})
			if err := record(r, n, err); err != nil {
				return nil, err
			}
		}
	}
	if c := pe.Tool; c != nil {
		if ts, r, ok := at(factTool, c.TS); ok {
			n, err := q.InsertToolCall(ctx, db.InsertToolCallParams{
				ThreadID: &tid, EntrySeq: seq, Ts: ts, Tool: c.Tool, IsError: c.IsError, Ukey: &r.ukey,
			})
			if err := record(r, n, err); err != nil {
				return nil, err
			}
		}
	}
	if pe.TurnTS != nil && isMainFile(file) {
		if ts, r, ok := at(factTurn, *pe.TurnTS); ok {
			n, err := q.InsertTurn(ctx, db.InsertTurnParams{ThreadID: &tid, EntrySeq: seq, Ts: ts, Ukey: &r.ukey})
			if err := record(r, n, err); err != nil {
				return nil, err
			}
		}
	}
	return wrote, nil
}

// deleteFactsAt 删除一条条目（thread, seq）派生的全部事实行（reset 重发且内容变了时先清再写），返回放出的去重键：
// 调用方写完新内容后交给 restoreFacts（新内容不再产出该键时，键回到仍持有它的副本上）。
func deleteFactsAt(ctx context.Context, q *db.Queries, threadID string, seq int64) ([]factRef, error) {
	keys, err := q.ListFactKeysAt(ctx, db.ListFactKeysAtParams{ThreadID: threadID, EntrySeq: seq})
	if err != nil {
		return nil, err
	}
	if err := q.DeleteUsageAt(ctx, db.DeleteUsageAtParams{ThreadID: threadID, EntrySeq: seq}); err != nil {
		return nil, err
	}
	if err := q.DeleteToolCallAt(ctx, db.DeleteToolCallAtParams{ThreadID: threadID, EntrySeq: seq}); err != nil {
		return nil, err
	}
	if err := q.DeleteTurnAt(ctx, db.DeleteTurnAtParams{ThreadID: threadID, EntrySeq: seq}); err != nil {
		return nil, err
	}
	refs := make([]factRef, len(keys))
	for i, k := range keys {
		refs[i] = factRef{factTable(k.Tbl), k.Ukey}
	}
	return refs, nil
}

// deleteOrphanFacts 删除线程中条目已不存在的事实行（reset_done 删 stale 条目、dropFile 整体移除文件之后），
// 再由 restoreFacts 把其中仍有副本的事实重建到副本上。
func deleteOrphanFacts(ctx context.Context, q *db.Queries, threadID string, now int64) error {
	keys, err := q.ListOrphanFactKeys(ctx, threadID)
	if err != nil {
		return err
	}
	if _, err := q.DeleteOrphanUsage(ctx, threadID); err != nil {
		return err
	}
	if _, err := q.DeleteOrphanToolCalls(ctx, threadID); err != nil {
		return err
	}
	if _, err := q.DeleteOrphanTurns(ctx, threadID); err != nil {
		return err
	}
	refs := make([]factRef, len(keys))
	for i, k := range keys {
		refs[i] = factRef{factTable(k.Tbl), k.Ukey}
	}
	return restoreFacts(ctx, q, refs, now)
}

// restoreFacts 在事实行随其所属条目删除之后调用：同一去重键若还有当前条目（本线程另一个文件里的同一条目，或拷贝了它的
// 其它活线程——它们入库时因键已被占用而没有写事实），按 (非 stale 优先, 线程创建时间, 线程, seq) 取第一个能写出该键的
// 条目重建事实行，与 BackfillFacts 按创建时间升序分配键的结果一致。这样仍在某个活转录里的用量恰好计一次。
// 键已被别的行占着（新内容又写出了它，或已删线程的冻结行持有它）时不动：冻结行已计过一次，副本不再补记。
func restoreFacts(ctx context.Context, q *db.Queries, released []factRef, now int64) error {
	want := map[factRef]bool{}
	byID := map[string][]factRef{}
	for _, r := range released {
		if want[r] {
			continue
		}
		id, ok := factKeyIDPart(r.ukey)
		if !ok {
			continue
		}
		taken, err := q.FactKeyTaken(ctx, db.FactKeyTakenParams{Tbl: int64(r.tbl), Ukey: r.ukey})
		if err != nil {
			return err
		}
		if taken {
			continue
		}
		want[r] = true
		byID[id] = append(byID[id], r)
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		cands, err := factCandidates(ctx, q, id)
		if err != nil {
			return err
		}
		for _, c := range cands {
			pe, err := ParseLine(string(c.Payload))
			if err != nil {
				continue
			}
			wrote, err := writeFactsFor(ctx, q, c.ThreadID, c.Seq, c.File, pe, now, want)
			if err != nil {
				return err
			}
			for _, w := range wrote {
				delete(want, w)
			}
			if !slices.ContainsFunc(byID[id], func(r factRef) bool { return want[r] }) {
				break
			}
		}
	}
	return nil
}

// factCandidates 列出可能持有去重键 id 部分 idPart 的当前条目（见 factKeyID）。哈希形式的 idPart 按其前缀
// 在索引上取范围，再由调用方逐条重算键筛选。
func factCandidates(ctx context.Context, q *db.Queries, idPart string) ([]db.ListFactCandidatesRow, error) {
	if len(idPart) <= maxFactKeyIDLen {
		return q.ListFactCandidates(ctx, idPart)
	}
	prefix := idPart[:len(idPart)-1-2*sha256.Size]
	rows, err := q.ListFactCandidatesByPrefix(ctx, db.ListFactCandidatesByPrefixParams{Lo: prefix, Hi: prefix + "\xff"})
	if err != nil {
		return nil, err
	}
	out := make([]db.ListFactCandidatesRow, len(rows))
	for i, r := range rows {
		out[i] = db.ListFactCandidatesRow(r)
	}
	return out, nil
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

// sanitizeFacts 在一次全量回填开始前，于一个事务里改写库中已有的事实行（含已删线程的冻结行——回填只重算活线程）：
//   - 越界的数值、超长字符串、越界时间按 ingest.go / clampFactTS 的同一规则钳制（入库校验之前写下的行）；
//   - ukey_scheme 尚不是 ukeyScheme 时，把含超长条目 id 的去重键改成 factKeyID 的哈希形式：冻结行与随后整体重算的
//     活线程得到同一个键，fork 副本与冻结行之间的去重不变（常规 id 的键不受影响）。
//
// 改动过任何行时整表重建三张 15 分钟汇总：旧的越界行可能已在汇总里留下 +Inf（Inf − x 仍是 Inf，触发器减不回来）。
func sanitizeFacts(ctx context.Context, st *store.Store, now int64) error {
	return st.Tx(ctx, func(q *db.Queries) error {
		scheme, _, err := getState(ctx, q, stateUkeyScheme)
		if err != nil {
			return err
		}
		rekey := scheme != ukeyScheme
		key := func(k *string) *string {
			if !rekey || k == nil {
				return k
			}
			id, ok := factKeyIDPart(*k)
			if !ok || len(id) <= maxFactKeyIDLen {
				return k
			}
			nk := factKeyID(id) + (*k)[len(id):]
			return &nk
		}
		tok := func(n int64) int64 {
			v, _ := clampTokens(float64(n))
			return v
		}
		ms := func(v *int64) *int64 {
			if v == nil {
				return nil
			}
			return clampMs(*v)
		}
		lo, hi := int64(minFactTS), now+maxFactSkewMs
		if rekey {
			// 活线程的行随后整体重算、以新键写回；先删掉可能含超长 id 的活行，免得新代码在回填前已写下的新键行
			// 挡住冻结行的改写（那样冻结行留旧键、活行重算后又得新键，会计两次）
			for _, f := range []func(context.Context) error{q.DeleteLiveLongKeyUsage, q.DeleteLiveLongKeyToolCalls, q.DeleteLiveLongKeyTurns} {
				if err := f(ctx); err != nil {
					return err
				}
			}
		}
		var changed int
		us, err := q.ListUnsafeUsageRecords(ctx, db.ListUnsafeUsageRecordsParams{Rekey: rekey, MinTs: lo, MaxTs: hi})
		if err != nil {
			return err
		}
		for _, r := range us {
			p := db.UpdateUsageRecordFactsParams{
				ID: r.ID, Ts: clampFactTS(r.Ts, now), Provider: truncBytes(r.Provider, maxNameLen), Model: truncBytes(r.Model, maxNameLen),
				Purpose: r.Purpose, StopReason: truncBytes(r.StopReason, maxStopReasonLen), Ukey: key(r.Ukey),
				InputTokens: tok(r.InputTokens), OutputTokens: tok(r.OutputTokens), CacheReadTokens: tok(r.CacheReadTokens),
				CacheWriteTokens: tok(r.CacheWriteTokens), DurationMs: ms(r.DurationMs), TtftMs: ms(r.TtftMs),
			}
			if r.Purpose != "" {
				p.Purpose = cleanPurpose(r.Purpose)
			}
			p.CostUsd, _ = clampCost(r.CostUsd)
			same := p.Ts == r.Ts && p.Provider == r.Provider && p.Model == r.Model && p.Purpose == r.Purpose &&
				p.StopReason == r.StopReason && eqPtr(p.Ukey, r.Ukey) && p.InputTokens == r.InputTokens &&
				p.OutputTokens == r.OutputTokens && p.CacheReadTokens == r.CacheReadTokens && p.CacheWriteTokens == r.CacheWriteTokens &&
				p.CostUsd == r.CostUsd && eqInt(p.DurationMs, r.DurationMs) && eqInt(p.TtftMs, r.TtftMs)
			if same {
				continue
			}
			if _, err := q.UpdateUsageRecordFacts(ctx, p); err != nil {
				return err
			}
			changed++
		}
		tcs, err := q.ListUnsafeToolCalls(ctx, db.ListUnsafeToolCallsParams{Rekey: rekey, MinTs: lo, MaxTs: hi})
		if err != nil {
			return err
		}
		for _, r := range tcs {
			p := db.UpdateToolCallFactsParams{ID: r.ID, Ts: clampFactTS(r.Ts, now), Tool: truncBytes(r.Tool, maxNameLen), Ukey: key(r.Ukey)}
			if p.Ts == r.Ts && p.Tool == r.Tool && eqPtr(p.Ukey, r.Ukey) {
				continue
			}
			if _, err := q.UpdateToolCallFacts(ctx, p); err != nil {
				return err
			}
			changed++
		}
		turns, err := q.ListUnsafeTurns(ctx, db.ListUnsafeTurnsParams{Rekey: rekey, MinTs: lo, MaxTs: hi})
		if err != nil {
			return err
		}
		for _, r := range turns {
			p := db.UpdateTurnFactsParams{ID: r.ID, Ts: clampFactTS(r.Ts, now), Ukey: key(r.Ukey)}
			if p.Ts == r.Ts && eqPtr(p.Ukey, r.Ukey) {
				continue
			}
			if _, err := q.UpdateTurnFacts(ctx, p); err != nil {
				return err
			}
			changed++
		}
		if changed > 0 {
			for _, f := range []func(context.Context) error{q.ClearUsageQ, q.FillUsageQ, q.ClearToolQ, q.FillToolQ, q.ClearTurnQ, q.FillTurnQ} {
				if err := f(ctx); err != nil {
					return err
				}
			}
			slog.Warn("threads: 已钳制越界的统计事实行并重建汇总", "rows", changed)
		}
		if rekey {
			return q.SetStatsState(ctx, db.SetStatsStateParams{Key: stateUkeyScheme, Value: ukeyScheme})
		}
		return nil
	})
}

func eqInt(a, b *int64) bool { return a == b || a != nil && b != nil && *a == *b }

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
		if err := sanitizeFacts(ctx, st, nowFunc(now)()); err != nil {
			return fmt.Errorf("钳制已有事实行: %w", err)
		}
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
