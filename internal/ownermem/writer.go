package ownermem

// writer.go：写入循环（R1、R2、R4、R5、R6）。每 5 分钟扫描到期的来源：构造块，只 retain 内容变了的块，
// 删除多出的块；排除项与已删除线程的 document 走遗忘保护删除。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

const (
	scanInterval = 5 * time.Minute
	// idleAfter 是来源空闲多久后写入（R1、R2）。
	idleAfter = 30 * time.Minute
	// scanBatch 是一次扫描最多处理的来源数（回填时分多轮完成）。
	scanBatch = 20
	// retryBase、retryMax 是写入失败的退避（R4）。
	retryBase  = 30 * time.Second
	retryMax   = 6 * time.Hour
	entryPage  = 500
	maxErrText = 500
)

// errSourceForgotten 表示写入开始前或进行中，同一来源被排除或遗忘：写入放弃，不算失败。
var errSourceForgotten = errors.New("来源已被排除或遗忘")

// sourceGate 串行化同一来源的写入与遗忘。逐块同步写入可能持续数分钟；遗忘先登记 forgets 再等锁，
// 写入在每个请求前看到 forgets 非零就放弃，所以遗忘最多等一个进行中的请求，之后删掉的就是全部已登记的块。
type sourceGate struct {
	mu      sync.Mutex
	forgets atomic.Int32
	refs    int // 持有或等待这把门的调用数，受 Service.gatesMu 保护
}

// lockSource 取得来源的门；forget 为真时先登记遗忘，让进行中的写入尽早放弃。返回的函数释放门。
func (s *Service) lockSource(threadID string, forget bool) (*sourceGate, func()) {
	s.gatesMu.Lock()
	if s.gates == nil {
		s.gates = map[string]*sourceGate{}
	}
	g := s.gates[threadID]
	if g == nil {
		g = &sourceGate{}
		s.gates[threadID] = g
	}
	g.refs++
	s.gatesMu.Unlock()
	if forget {
		g.forgets.Add(1)
	}
	g.mu.Lock()
	return g, func() {
		if forget {
			g.forgets.Add(-1)
		}
		g.mu.Unlock()
		s.gatesMu.Lock()
		if g.refs--; g.refs == 0 {
			delete(s.gates, threadID)
		}
		s.gatesMu.Unlock()
	}
}

// retryBackoff 是第 attempts 次失败后到下次重试的间隔。
func retryBackoff(attempts int64) time.Duration {
	d := retryBase
	for i := int64(1); i < attempts && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// Run 是写入循环：每 5 分钟扫描一次，ctx 取消时返回。
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(scanInterval)
	defer t.Stop()
	for {
		if _, err := s.Scan(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("助手记忆写入扫描失败", "err", err)
		}
		if s.Enabled(ctx) {
			if err := s.daily(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("助手记忆主题整理失败", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Scan 执行一轮：先遗忘已删除或已排除来源的 document，再写入到期的来源。返回本轮写入成功的来源数。
// 助手记忆未运行时什么也不做。
func (s *Service) Scan(ctx context.Context) (int, error) {
	if !s.Enabled(ctx) {
		return 0, nil
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	if st.BackfillSince == nil {
		return 0, nil // PutSettings 开启时一定定下起点；没有说明设置刚被改动，下一轮再来
	}
	b, err := s.bank(ctx)
	if err != nil {
		return 0, err
	}
	if err := s.forgetStale(ctx, b); err != nil {
		return 0, err
	}
	now := s.cfg.Now()
	nowMs, idleBefore := now.UnixMilli(), now.Add(-idleAfter).UnixMilli()
	due, err := s.cfg.Q.ListDueOwnerMemoryThreads(ctx, db.ListDueOwnerMemoryThreadsParams{
		Since: st.BackfillSince, Now: &nowMs, IdleBefore: &idleBefore, Lim: scanBatch,
	})
	if err != nil {
		return 0, err
	}
	done := 0
	for _, row := range due {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		err := s.retainThread(ctx, b, row.ID)
		if err == nil {
			done++
			continue
		}
		if errors.Is(err, errSourceForgotten) {
			continue
		}
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		if ferr := s.markFailed(ctx, row.ID, row.Kind, err); ferr != nil {
			return done, ferr
		}
		slog.Warn("助手记忆写入来源失败", "thread", row.ID, "err", err)
	}
	return done, nil
}

// markFailed 记录失败并按次数退避。
func (s *Service) markFailed(ctx context.Context, threadID, kind string, cause error) error {
	attempts := int64(1)
	if src, err := s.cfg.Q.GetOwnerMemorySource(ctx, threadID); err == nil {
		attempts = src.Attempts + 1
	} else if !errors.Is(err, errNoRows) {
		return err
	}
	now := s.cfg.Now()
	next := now.Add(retryBackoff(attempts)).UnixMilli()
	msg := truncate(cause.Error(), maxErrText)
	return s.cfg.Q.MarkOwnerMemorySourceFailed(context.WithoutCancel(ctx), db.MarkOwnerMemorySourceFailedParams{
		ThreadID: threadID, Kind: SourceKindOf(kind), NextAttemptAt: &next, LastError: &msg, Now: now.UnixMilli(),
	})
}

// retainThread 写入一个来源：构造块、只 retain 哈希变了的块、删除多出的块，最后记下已写入时的线程版本。
// 持有来源的门；等门期间来源可能已被排除或遗忘，拿到门后重新确认仍需写入。
func (s *Service) retainThread(ctx context.Context, b *memory.Bank, threadID string) error {
	g, release := s.lockSource(threadID, false)
	defer release()
	t, err := s.cfg.Q.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	kind := SourceKindOf(t.Kind)
	if kind == "" {
		return fmt.Errorf("线程种类 %q 不写入助手记忆", t.Kind)
	}
	// 先建来源记录（空进度，仍到期）再做最后一次资格检查：排除对话或项目、删除线程的遗忘都从来源记录枚举。
	// 检查之前插入的排除会被检查看到；检查之后插入的，枚举时已能找到这条记录，遗忘随之等门、让写入放弃。
	if err := s.cfg.Q.EnsureOwnerMemorySource(ctx, db.EnsureOwnerMemorySourceParams{
		ThreadID: threadID, Kind: kind, Now: s.cfg.Now().UnixMilli(),
	}); err != nil {
		return err
	}
	if due, err := s.stillDue(ctx, t); err != nil {
		return err
	} else if !due {
		return errSourceForgotten
	}
	if s.afterDueCheck != nil {
		s.afterDueCheck(threadID)
	}
	meta, err := s.threadMeta(ctx, t)
	if err != nil {
		return err
	}
	branch, err := s.activeBranch(ctx, t)
	if err != nil {
		return err
	}
	chunks := Build(meta, branch, s.redact(ctx))
	old, err := s.cfg.Q.ListOwnerMemoryChunks(ctx, threadID)
	if err != nil {
		return err
	}
	byIdx := make(map[int64]db.OwnerMemoryChunk, len(old))
	for _, c := range old {
		byIdx[c.Idx] = c
	}
	var changed []Chunk
	for _, c := range chunks {
		if o, ok := byIdx[int64(c.Idx)]; !ok || o.Hash != c.Hash || o.DocumentID != c.DocumentID {
			changed = append(changed, c)
		}
	}
	// 先登记再写入：把变了的块登记为待写（哈希留空，下一轮必定重写），每个请求成功后再记下它的哈希。
	// 同步写入逐块进行，后面的块失败时前面的已经落库；超时、取消等结果不明的失败也可能已落库。
	// 登记在先，遗忘才删得到它们（删除不存在的 document 按成功处理）。
	for _, c := range changed {
		if err := s.putChunk(ctx, threadID, c, "", 0); err != nil {
			return err
		}
	}
	if err := s.retain(ctx, b, g, changed, func(done []Chunk) error {
		nowMs := s.cfg.Now().UnixMilli()
		for _, c := range done {
			if err := s.putChunk(ctx, threadID, c, c.Hash, nowMs); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	var extra []string
	for _, o := range old {
		if o.Idx >= int64(len(chunks)) { // document id 由线程 id 与块序号决定，序号在范围内时同一块被替换
			extra = append(extra, o.DocumentID)
		}
	}
	if len(extra) > 0 {
		// 失败的删除进入 M16 的重试队列，这里不算写入失败
		if _, _, err := b.DeleteDocuments(ctx, extra); err != nil {
			return err
		}
	}
	nowMs := s.cfg.Now().UnixMilli()
	for _, o := range old {
		if o.Idx >= int64(len(chunks)) {
			if err := s.cfg.Q.DeleteOwnerMemoryChunk(ctx, db.DeleteOwnerMemoryChunkParams{ThreadID: threadID, Idx: o.Idx}); err != nil {
				return err
			}
		}
	}
	// 记下读转录之前的线程版本：读的过程中有新消息时，下一轮仍会到期
	return s.cfg.Q.MarkOwnerMemorySourceRetained(ctx, db.MarkOwnerMemorySourceRetainedParams{
		ThreadID: threadID, Kind: kind, LastMessageAt: t.LastMessageAt, TranscriptRev: t.TranscriptRev,
		Chunks: int64(len(chunks)), Now: &nowMs,
	})
}

// stillDue 在持有来源的门之后重新确认来源仍需写入：没被排除（对话本身或所在项目），且没有被遗忘成当前版本
// （ForgetSource 在门内把来源记成当前线程版本）。
func (s *Service) stillDue(ctx context.Context, t db.Thread) (bool, error) {
	excl, err := s.cfg.Q.ListOwnerMemoryExclusions(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range excl {
		if (e.Kind == string(apigen.OwnerMemoryExclusionThread) && e.TargetID == t.ID) ||
			(e.Kind == string(apigen.OwnerMemoryExclusionProject) && t.ProjectID != nil && e.TargetID == *t.ProjectID) {
			return false, nil
		}
	}
	src, err := s.cfg.Q.GetOwnerMemorySource(ctx, t.ID)
	if errors.Is(err, errNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	same := src.LastMessageAt != nil && t.LastMessageAt != nil && *src.LastMessageAt == *t.LastMessageAt &&
		src.TranscriptRev == t.TranscriptRev
	return !same, nil
}

// putChunk 登记一个块；hash 为空表示已提交写入、尚未确认成功。
func (s *Service) putChunk(ctx context.Context, threadID string, c Chunk, hash string, retainedAt int64) error {
	return s.cfg.Q.UpsertOwnerMemoryChunk(ctx, db.UpsertOwnerMemoryChunkParams{
		ThreadID: threadID, Idx: int64(c.Idx), DocumentID: c.DocumentID, Hash: hash,
		SeqFrom: c.SeqFrom, SeqTo: c.SeqTo, RetainedAt: retainedAt,
	})
}

// retainItem 是发往 Hindsight 的 item：同一 document 再次写入时整体替换。
type retainItem struct {
	RetainItem
	UpdateMode string `json:"update_mode"`
}

// retain 写入块，每个请求成功后用它写入的块调用 done；每个请求之前看到有遗忘在等门就放弃（errSourceForgotten）。
// 每块一个 item 的（工作线程）合成一次异步请求；一块多个 item 的（助手对话：一个单元一个 item，各自带 seq 出处）
// 逐块同步写入——Hindsight 只在同步路径把同一 document_id 的多个 item 合成一个 document，异步批次里 document_id
// 重复直接 400（memory_engine.py retain_batch_async）。
func (s *Service) retain(ctx context.Context, b *memory.Bank, g *sourceGate, chunks []Chunk, done func([]Chunk) error) error {
	var batch []retainItem
	var batched []Chunk
	for _, c := range chunks {
		items := make([]retainItem, 0, len(c.Items))
		for _, it := range c.Items {
			items = append(items, retainItem{RetainItem: it, UpdateMode: "replace"})
		}
		if len(items) > 1 {
			if g.forgets.Load() > 0 {
				return errSourceForgotten
			}
			if err := postRetain(ctx, b, items, false); err != nil {
				return err
			}
			if err := done([]Chunk{c}); err != nil {
				return err
			}
			continue
		}
		batch = append(batch, items...)
		batched = append(batched, c)
	}
	if len(batched) == 0 {
		return nil
	}
	if g.forgets.Load() > 0 {
		return errSourceForgotten
	}
	if err := postRetain(ctx, b, batch, true); err != nil {
		return err
	}
	return done(batched)
}

// postRetain 发一次 retain 请求。同步请求要等 Hindsight 抽取完事实，受 memory 客户端 5 分钟的响应头时限约束。
func postRetain(ctx context.Context, b *memory.Bank, items []retainItem, async bool) error {
	if err := b.Call(ctx, http.MethodPost, "/memories", map[string]any{"items": items, "async": async}, nil); err != nil {
		return upstream("写入记忆", err)
	}
	return nil
}

// threadMeta 取块头与标签用的线程信息（项目名、PR）。
func (s *Service) threadMeta(ctx context.Context, t db.Thread) (ThreadMeta, error) {
	meta := ThreadMeta{
		ID: t.ID, Kind: t.Kind, Title: t.Title, State: t.State,
		Archived: t.ArchivedAt != nil, Shipped: t.ShippedAt != nil,
	}
	if t.ProjectID != nil {
		meta.ProjectID = *t.ProjectID
		p, err := s.cfg.Q.GetProject(ctx, *t.ProjectID)
		if err == nil {
			meta.ProjectName = p.Name
		} else if !errors.Is(err, errNoRows) {
			return meta, err
		}
	}
	pulls, err := s.cfg.Q.ListThreadPulls(ctx, t.ID)
	if err != nil {
		return meta, err
	}
	for _, p := range pulls {
		meta.Pulls = append(meta.Pulls, p.Number)
	}
	return meta, nil
}

// activeBranch 读出线程活动分支上的条目（按 seq 升序）。
func (s *Service) activeBranch(ctx context.Context, t db.Thread) ([]transcript.Entry, error) {
	var all []transcript.Entry
	after := int64(0)
	for {
		rows, err := s.cfg.Q.ListEntriesAfter(ctx, db.ListEntriesAfterParams{ThreadID: t.ID, AfterSeq: after, Limit: entryPage})
		if err != nil {
			return nil, err
		}
		for _, e := range rows {
			all = append(all, transcript.Entry{Seq: e.Seq, File: e.File, EntryID: e.EntryID, ParentID: e.ParentID, Type: e.Type, TS: e.Ts, Payload: e.Payload})
			after = e.Seq
		}
		if len(rows) < entryPage {
			break
		}
	}
	return transcript.ActiveBranch(all, t.LeafEntryID), nil
}

// forgetStale 遗忘线程已删除或已被排除的来源（R6）。
func (s *Service) forgetStale(ctx context.Context, b *memory.Bank) error {
	orphans, err := s.cfg.Q.ListOrphanOwnerMemorySources(ctx, scanBatch)
	if err != nil {
		return err
	}
	excluded, err := s.cfg.Q.ListExcludedOwnerMemorySources(ctx, scanBatch)
	if err != nil {
		return err
	}
	for _, id := range append(orphans, excluded...) {
		if _, err := s.forgetSource(ctx, b, id); err != nil {
			return err
		}
	}
	return nil
}

// forgetSource 在来源的门内遗忘：先登记遗忘让进行中的写入在下一个请求前放弃，等它退出后再删。
func (s *Service) forgetSource(ctx context.Context, b *memory.Bank, threadID string) (apigen.MemoryForgetResult, error) {
	_, release := s.lockSource(threadID, true)
	defer release()
	return s.forgetLocked(ctx, b, threadID)
}

// forgetLocked 删除一个来源在 bank 里的全部 document（遗忘保护：刷新完成前相关主题不再给出），
// 并清掉本地的块与进度。删除失败的 document 进入 M16 的重试队列（计入 Pending）。调用方持有来源的门。
func (s *Service) forgetLocked(ctx context.Context, b *memory.Bank, threadID string) (apigen.MemoryForgetResult, error) {
	var out apigen.MemoryForgetResult
	chunks, err := s.cfg.Q.ListOwnerMemoryChunks(ctx, threadID)
	if err != nil {
		return out, err
	}
	ids := make([]string, 0, len(chunks))
	for _, c := range chunks {
		ids = append(ids, c.DocumentID)
	}
	if len(ids) > 0 {
		if out.Deleted, out.Pending, err = b.DeleteDocuments(ctx, ids); err != nil {
			return out, err
		}
	}
	if err := s.cfg.Q.DeleteOwnerMemoryChunks(ctx, threadID); err != nil {
		return out, err
	}
	return out, s.cfg.Q.DeleteOwnerMemorySource(ctx, threadID)
}

// truncate 按字符截断。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
