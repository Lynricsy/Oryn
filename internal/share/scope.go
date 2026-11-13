package share

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// rangeBatch 是范围内按 seq 分批读库的批量。
const rangeBatch = 500

// scope 是一个分享的公开范围。
//   - 实时分享：线程当前 last_seq 之前、当前活动分支上的主会话条目；每次请求现算、不缓存
//     （只读 seq / file / id / parent / type 小列），撤销与转录重写因此即时生效。
//   - 快照分享：创建时固化的条目集合（thread_share_entries，按分支顺序）与当时的标题；
//     每行只有在当前原始 payload 的 SHA-256 仍等于创建时的摘要时才公开——被改写或删除的行从此消失，
//     快照里永远不会出现创建之后（未经扫描）的内容（F5）。
type scope struct {
	share  db.ThreadShare
	thread db.Thread
	title  string // snapshot：share.title；live：thread.title
	until  int64  // snapshot：share.until_seq；live：thread.last_seq
	leaf   string // snapshot：share.leaf_entry_id；live：thread.leaf_entry_id
	branch []int64
	onPath map[int64]bool
	sorted []int64 // branch 按 seq 升序
	// digest 仅快照：seq → 创建时原始 payload 的 SHA-256。
	digest map[int64][]byte
	// record 非 nil 时 accept 记下放行的每一行原始 payload 的 SHA-256（创建快照时固化扫描过的内容）。
	record map[int64][]byte
}

// scopeOf 读取线程行后计算分享的范围；线程已不存在 → ErrNotFound。
func (s *Service) scopeOf(ctx context.Context, sh db.ThreadShare) (*scope, error) {
	th, err := s.getThread(ctx, sh.ThreadID)
	if err != nil {
		return nil, err
	}
	if sh.Mode == "snapshot" {
		return s.frozenScope(ctx, sh, th)
	}
	leaf := ""
	if th.LeafEntryID != nil {
		leaf = *th.LeafEntryID
	}
	return s.branchScope(ctx, sh, th, th.LastSeq, leaf)
}

// branchScope 计算 until 之前、以 leaf 为叶子的活动分支（实时分享，以及创建快照时的扫描范围）。
func (s *Service) branchScope(ctx context.Context, sh db.ThreadShare, th db.Thread, until int64, leaf string) (*scope, error) {
	sc := &scope{share: sh, thread: th, title: th.Title, until: until, leaf: leaf}
	rows, err := s.st.Q.ListMainEntryLinks(ctx, db.ListMainEntryLinksParams{ThreadID: th.ID, UntilSeq: until})
	if err != nil {
		return nil, err
	}
	links := make([]transcript.Entry, len(rows))
	for i, r := range rows {
		links[i] = transcript.Entry{Seq: r.Seq, File: r.File, EntryID: r.EntryID, ParentID: r.ParentID, Type: r.Type}
	}
	br := transcript.ActiveBranch(links, leaf)
	seqs := make([]int64, len(br))
	for i, e := range br {
		seqs[i] = e.Seq
	}
	sc.setBranch(seqs)
	return sc, nil
}

// frozenScope 读出快照创建时固化的条目集合。
func (s *Service) frozenScope(ctx context.Context, sh db.ThreadShare, th db.Thread) (*scope, error) {
	sc := &scope{share: sh, thread: th}
	if sh.UntilSeq != nil {
		sc.until = *sh.UntilSeq
	}
	if sh.LeafEntryID != nil {
		sc.leaf = *sh.LeafEntryID
	}
	if sh.Title != nil {
		sc.title = *sh.Title
	}
	rows, err := s.st.Q.ListThreadShareEntries(ctx, sh.ID)
	if err != nil {
		return nil, err
	}
	seqs := make([]int64, len(rows))
	sc.digest = make(map[int64][]byte, len(rows))
	for i, r := range rows {
		seqs[i] = r.Seq
		sc.digest[r.Seq] = r.PayloadSha256
	}
	sc.setBranch(seqs)
	return sc, nil
}

func (sc *scope) setBranch(seqs []int64) {
	sc.branch = seqs
	sc.onPath = make(map[int64]bool, len(seqs))
	for _, q := range seqs {
		sc.onPath[q] = true
	}
	sc.sorted = slices.Clone(seqs)
	slices.Sort(sc.sorted)
}

// accept 返回一行在公开面上的视图：须在范围的分支上、（快照）内容未被改写，再经 transcript.Public 清洗。
// 条目分页、`.md`、扫描、图片与附件授权都只经由它判定，因此「公开的」与「扫过的」恒等。
func (sc *scope) accept(r db.Entry) (transcript.Entry, bool) {
	if !sc.onPath[r.Seq] || r.Seq > sc.until {
		return transcript.Entry{}, false
	}
	if sc.digest != nil || sc.record != nil {
		sum := sha256.Sum256(r.Payload)
		if sc.digest != nil && !bytes.Equal(sum[:], sc.digest[r.Seq]) {
			return transcript.Entry{}, false
		}
		if sc.record != nil {
			sc.record[r.Seq] = sum[:]
		}
	}
	return transcript.Public(fromDB(r))
}

// row 读出单条主会话条目并经 accept 判定（图片与附件授权用）。
func (sc *scope) row(ctx context.Context, q *db.Queries, seq int64) (transcript.Entry, bool, error) {
	rows, err := q.ListEntriesBySeqRange(ctx, db.ListEntriesBySeqRangeParams{ThreadID: sc.thread.ID, AfterSeq: seq - 1, UntilSeq: seq, Limit: 1})
	if err != nil || len(rows) == 0 {
		return transcript.Entry{}, false, err
	}
	pe, ok := sc.accept(rows[0])
	return pe, ok, nil
}

// references 报告范围内是否有公开（清洗后）的条目内容包含 needle。原始 payload 上的子串查询只用来挑候选，
// 授权以清洗后的内容为准：隐藏条目、display:false 注入消息里的引用不算（B8）。
func (sc *scope) references(ctx context.Context, q *db.Queries, needle []byte) (bool, error) {
	seqs, err := q.ListEntrySeqsContaining(ctx, db.ListEntrySeqsContainingParams{ThreadID: sc.thread.ID, UntilSeq: sc.until, Needle: needle})
	if err != nil {
		return false, err
	}
	for _, seq := range seqs {
		if !sc.onPath[seq] {
			continue
		}
		pe, ok, err := sc.row(ctx, q, seq)
		if err != nil {
			return false, err
		}
		if ok && bytes.Contains(pe.Payload, needle) {
			return true, nil
		}
	}
	return false, nil
}

// page 返回 (after, min(upTo, until)] 内活动分支上的公开条目（经 accept），至多 limit 条，按 seq 升序。
// 被 accept 拒掉的行（快照里被改写的行）不占名额：本页不满时继续向后取，因此空页只出现在范围末尾。
// hasMore 为真表示范围内仍有更多条目。
func (sc *scope) page(ctx context.Context, q *db.Queries, after, upTo int64, limit int) ([]transcript.Entry, bool, error) {
	end := min(upTo, sc.until)
	i, _ := slices.BinarySearch(sc.sorted, after+1)
	out := make([]transcript.Entry, 0, min(limit, len(sc.sorted)-i))
	for len(out) < limit && i < len(sc.sorted) && sc.sorted[i] <= end {
		// 选出这一轮要读的 seq（活动分支已知，不必逐行判断）
		first := sc.sorted[i]
		var last int64
		for n := limit - len(out); n > 0 && i < len(sc.sorted) && sc.sorted[i] <= end; n-- {
			last = sc.sorted[i]
			i++
		}
		cur := first - 1
		for cur < last {
			rows, err := q.ListEntriesBySeqRange(ctx, db.ListEntriesBySeqRangeParams{ThreadID: sc.thread.ID, AfterSeq: cur, UntilSeq: last, Limit: rangeBatch})
			if err != nil {
				return nil, false, err
			}
			for _, r := range rows {
				cur = r.Seq
				if pe, ok := sc.accept(r); ok {
					out = append(out, pe)
				}
			}
			if len(rows) < rangeBatch {
				break
			}
		}
	}
	return out, i < len(sc.sorted) && sc.sorted[i] <= end, nil
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
