package ownermem

// writer.go：写入循环（R1、R2、R4、R5、R6）。每 5 分钟扫描到期的来源：构造块，只 retain 内容变了的块，
// 删除多出的块；排除项与已删除线程的 document 走遗忘保护删除。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
func (s *Service) retainThread(ctx context.Context, b *memory.Bank, threadID string) error {
	t, err := s.cfg.Q.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	kind := SourceKindOf(t.Kind)
	if kind == "" {
		return fmt.Errorf("线程种类 %q 不写入助手记忆", t.Kind)
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
	if err := s.retain(ctx, b, changed); err != nil {
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
	for _, c := range changed {
		if err := s.cfg.Q.UpsertOwnerMemoryChunk(ctx, db.UpsertOwnerMemoryChunkParams{
			ThreadID: threadID, Idx: int64(c.Idx), DocumentID: c.DocumentID, Hash: c.Hash,
			SeqFrom: c.SeqFrom, SeqTo: c.SeqTo, RetainedAt: nowMs,
		}); err != nil {
			return err
		}
	}
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

// retainItem 是发往 Hindsight 的 item：同一 document 再次写入时整体替换。
type retainItem struct {
	RetainItem
	UpdateMode string `json:"update_mode"`
}

// retain 异步写入块（一次请求）。
func (s *Service) retain(ctx context.Context, b *memory.Bank, chunks []Chunk) error {
	var items []retainItem
	for _, c := range chunks {
		for _, it := range c.Items {
			items = append(items, retainItem{RetainItem: it, UpdateMode: "replace"})
		}
	}
	if len(items) == 0 {
		return nil
	}
	if err := b.Call(ctx, http.MethodPost, "/memories", map[string]any{"items": items, "async": true}, nil); err != nil {
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
	leaf := ""
	if t.LeafEntryID != nil {
		leaf = *t.LeafEntryID
	}
	return transcript.ActiveBranch(all, leaf), nil
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

// forgetSource 删除一个来源在 bank 里的全部 document（遗忘保护：刷新完成前相关主题不再给出），
// 并清掉本地的块与进度。删除失败的 document 进入 M16 的重试队列（计入 Pending）。
func (s *Service) forgetSource(ctx context.Context, b *memory.Bank, threadID string) (apigen.MemoryForgetResult, error) {
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
