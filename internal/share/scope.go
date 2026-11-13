package share

import (
	"context"
	"slices"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// rangeBatch 是范围内按 seq 分批读库的批量。
const rangeBatch = 500

// scope 是一个分享的公开范围：until 之前、活动分支上的主会话条目。
// 每次请求现算、不缓存（只读 seq / file / id / parent / type 小列），撤销与转录重写因此即时生效。
type scope struct {
	share  db.ThreadShare
	thread db.Thread
	until  int64  // snapshot：share.until_seq；live：thread.last_seq
	leaf   string // snapshot：share.leaf_entry_id；live：thread.leaf_entry_id
	branch []int64
	onPath map[int64]bool
	sorted []int64 // branch 按 seq 升序
}

// scopeOf 读取线程行后计算分享的范围；线程已不存在 → ErrNotFound。
func (s *Service) scopeOf(ctx context.Context, sh db.ThreadShare) (*scope, error) {
	th, err := s.getThread(ctx, sh.ThreadID)
	if err != nil {
		return nil, err
	}
	return s.scopeFor(ctx, sh, th)
}

func (s *Service) scopeFor(ctx context.Context, sh db.ThreadShare, th db.Thread) (*scope, error) {
	sc := &scope{share: sh, thread: th, until: th.LastSeq}
	if th.LeafEntryID != nil {
		sc.leaf = *th.LeafEntryID
	}
	if sh.Mode == "snapshot" {
		sc.until, sc.leaf = 0, ""
		if sh.UntilSeq != nil {
			sc.until = *sh.UntilSeq
		}
		if sh.LeafEntryID != nil {
			sc.leaf = *sh.LeafEntryID
		}
	}
	rows, err := s.st.Q.ListMainEntryLinks(ctx, db.ListMainEntryLinksParams{ThreadID: th.ID, UntilSeq: sc.until})
	if err != nil {
		return nil, err
	}
	links := make([]transcript.Entry, len(rows))
	for i, r := range rows {
		links[i] = transcript.Entry{Seq: r.Seq, File: r.File, EntryID: r.EntryID, ParentID: r.ParentID, Type: r.Type}
	}
	// 快照的叶子若已被重写删掉，ActiveBranch 自然回落到各文件最后一条
	br := transcript.ActiveBranch(links, sc.leaf)
	sc.branch = make([]int64, len(br))
	sc.onPath = make(map[int64]bool, len(br))
	for i, e := range br {
		sc.branch[i] = e.Seq
		sc.onPath[e.Seq] = true
	}
	sc.sorted = slices.Clone(sc.branch)
	slices.Sort(sc.sorted)
	return sc, nil
}

// page 返回 (after, min(upTo, until)] 内活动分支上的条目（经 transcript.Public），至多 limit 条，按 seq 升序。
// hasMore 为真表示范围内仍有更多条目。
func (sc *scope) page(ctx context.Context, q *db.Queries, after, upTo int64, limit int) ([]transcript.Entry, bool, error) {
	end := min(upTo, sc.until)
	// 选出本页的 seq（活动分支已知，不必逐行判断）
	i, _ := slices.BinarySearch(sc.sorted, after+1)
	var want []int64
	for ; i < len(sc.sorted) && sc.sorted[i] <= end && len(want) < limit; i++ {
		want = append(want, sc.sorted[i])
	}
	hasMore := i < len(sc.sorted) && sc.sorted[i] <= end
	if len(want) == 0 {
		return []transcript.Entry{}, hasMore, nil
	}
	out := make([]transcript.Entry, 0, len(want))
	cur, last := want[0]-1, want[len(want)-1]
	for cur < last {
		rows, err := q.ListEntriesBySeqRange(ctx, db.ListEntriesBySeqRangeParams{ThreadID: sc.thread.ID, AfterSeq: cur, UntilSeq: last, Limit: rangeBatch})
		if err != nil {
			return nil, false, err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			cur = r.Seq
			if !sc.onPath[r.Seq] {
				continue
			}
			if pe, ok := transcript.Public(fromDB(r)); ok {
				out = append(out, pe)
			}
		}
		if len(rows) < rangeBatch {
			break
		}
	}
	return out, hasMore, nil
}

// all 返回 (after, upTo] 内的全部公开条目（按 seq 升序）。
func (sc *scope) all(ctx context.Context, q *db.Queries, after, upTo int64) ([]transcript.Entry, error) {
	var out []transcript.Entry
	for {
		es, more, err := sc.page(ctx, q, after, upTo, rangeBatch)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
		if !more || len(es) == 0 {
			return out, nil
		}
		after = es[len(es)-1].Seq
	}
}

// branchEntries 按活动分支顺序返回全部公开条目（`.md` 导出用）。
func (sc *scope) branchEntries(ctx context.Context, q *db.Queries) ([]transcript.Entry, error) {
	es, err := sc.all(ctx, q, 0, sc.until)
	if err != nil {
		return nil, err
	}
	bySeq := make(map[int64]transcript.Entry, len(es))
	for _, e := range es {
		bySeq[e.Seq] = e
	}
	out := make([]transcript.Entry, 0, len(es))
	for _, seq := range sc.branch {
		if e, ok := bySeq[seq]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func fromDB(e db.Entry) transcript.Entry {
	return transcript.Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload}
}

// toThreads / fromThreads 在两个同形的条目类型之间做字段拷贝。
func toThreads(e transcript.Entry) threads.Entry { return threads.Entry(e) }

func fromThreads(e threads.Entry) transcript.Entry { return transcript.Entry(e) }
