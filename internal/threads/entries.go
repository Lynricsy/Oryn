package threads

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// entriesBatch 是单条 WS `entries` 消息的条目上限。
const entriesBatch = 200

func (s *Service) onCursors(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlEntriesCursorsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: link.CodeInvalidParams, Message: err.Error()}
	}
	if _, err := s.ownThread(ctx, orbID, p.ThreadID); err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListEntryCursors(ctx, p.ThreadID)
	if err != nil {
		return nil, err
	}
	res := apigen.LinkCtlEntriesCursorsResult{Cursors: make([]apigen.LinkCtlEntryCursor, 0, len(rows))}
	for _, r := range rows {
		res.Cursors = append(res.Cursors, apigen.LinkCtlEntryCursor{File: r.File, Offset: r.ByteOffset})
	}
	return res, nil
}

func (s *Service) cursor(ctx context.Context, q *db.Queries, threadID, file string) (int64, error) {
	c, err := q.GetEntryCursor(ctx, db.GetEntryCursorParams{ThreadID: threadID, File: file})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return c.ByteOffset, err
}

// onAppend 按 D 节入库：baseOffset 必须等于游标，否则要求从游标重发。
func (s *Service) onAppend(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlEntriesAppendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: link.CodeInvalidParams, Message: err.Error()}
	}
	if p.File == "" || p.EndOffset < p.BaseOffset {
		return nil, &link.CtlError{Code: link.CodeInvalidParams, Message: "invalid file or offsets"}
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, err := s.cursor(ctx, s.st.Q, t.id, p.File)
	if err != nil {
		return nil, err
	}
	if h, held := t.held[p.File]; held || (t.forking > 0 && isMainFile(p.File) && cur == 0 && p.BaseOffset == 0 && !t.resetting[p.File]) {
		// fork_from 在途时新出现的主会话文件多半是其产物：先暂存（不推进游标），应答后再决定丢弃或入库
		switch {
		case p.BaseOffset == 0:
			h = []apigen.LinkCtlEntriesAppendParams{p}
		case len(h) > 0 && p.BaseOffset == h[len(h)-1].EndOffset:
			h = append(h, p)
		default:
			delete(t.held, p.File)
			return apigen.LinkCtlEntriesAppendResult{Accepted: false, Offset: 0}, nil
		}
		t.held[p.File] = h
		return apigen.LinkCtlEntriesAppendResult{Accepted: true, Offset: p.EndOffset}, nil
	}
	if p.BaseOffset != cur {
		return apigen.LinkCtlEntriesAppendResult{Accepted: false, Offset: cur}, nil
	}
	if t.ignored[p.File] {
		err := s.st.Q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: t.id, File: p.File, ByteOffset: p.EndOffset})
		return apigen.LinkCtlEntriesAppendResult{Accepted: err == nil, Offset: p.EndOffset}, err
	}
	return s.ingestLocked(ctx, t, orbID, p)
}

// endForkLocked 在 fork_from 结束（成功或失败）后调用（持有 t.mu）：产物文件 product 此后一律丢弃；
// 最后一个在途 fork 结束时，其余暂存的文件按序补入库。
func (s *Service) endForkLocked(ctx context.Context, t *thread, product string) {
	t.forking--
	if product != "" {
		t.ignored[product] = true
		if h := t.held[product]; len(h) > 0 {
			_ = s.st.Q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: t.id, File: product, ByteOffset: h[len(h)-1].EndOffset})
		}
		delete(t.held, product)
	}
	if t.forking > 0 {
		return
	}
	for file, h := range t.held {
		delete(t.held, file)
		for _, p := range h {
			if _, err := s.ingestLocked(ctx, t, t.orbID, p); err != nil {
				slog.Warn("threads: 补入暂存的会话文件失败", "thread", t.id, "file", file, "err", err)
				break
			}
		}
	}
}

// ingestLocked 把一批已通过游标校验的行入库并推送（持有 t.mu）。
func (s *Service) ingestLocked(ctx context.Context, t *thread, orbID string, p apigen.LinkCtlEntriesAppendParams) (apigen.LinkCtlEntriesAppendResult, error) {
	type ingested struct {
		parsed ParsedEntry
		entry  Entry
		isNew  bool
	}
	var out []ingested
	main := isMainFile(p.File)
	sub := IsSubagentFile(p.File)
	resetting := t.resetting[p.File]
	prevLast := t.lastSeq
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		for _, line := range p.Lines {
			pe, err := ParseLine(line)
			if err != nil {
				slog.Warn("threads: 跳过无法解析的会话行", "thread", t.id, "file", p.File, "err", err)
				continue
			}
			var old []byte
			if resetting {
				if e, err := q.GetEntry(ctx, db.GetEntryParams{ThreadID: t.id, File: p.File, EntryID: pe.EntryID}); err == nil {
					old = e.Payload
				}
			}
			seq, err := q.UpsertEntry(ctx, db.UpsertEntryParams{
				ThreadID: t.id, File: p.File, EntryID: pe.EntryID, ParentID: pe.ParentID, Type: pe.Type, Ts: pe.TS, Payload: pe.Payload,
			})
			if err != nil {
				return err
			}
			isNew := seq > prevLast
			if !isNew && old != nil && string(old) == string(pe.Payload) {
				continue
			}
			tid := t.id
			if pe.Usage != nil {
				u := pe.Usage
				if err := q.InsertUsageRecord(ctx, db.InsertUsageRecordParams{
					ThreadID: &tid, EntrySeq: seq, Ts: u.TS, Provider: u.Provider, Model: u.Model,
					InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrt,
					CostUsd: u.CostUSD, DurationMs: u.DurationMs, TtftMs: u.TTFTMs, IsSubagent: sub,
				}); err != nil {
					return err
				}
			}
			if pe.Tool != nil {
				if err := q.InsertToolCall(ctx, db.InsertToolCallParams{ThreadID: &tid, EntrySeq: seq, Ts: pe.Tool.TS, Tool: pe.Tool.Tool, IsError: pe.Tool.IsError}); err != nil {
					return err
				}
			}
			out = append(out, ingested{parsed: pe, isNew: isNew, entry: Entry{
				Seq: seq, File: p.File, EntryID: pe.EntryID, ParentID: pe.ParentID, Type: pe.Type, TS: pe.TS, Payload: pe.Payload,
			}})
		}
		return q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: t.id, File: p.File, ByteOffset: p.EndOffset})
	})
	if err != nil {
		return apigen.LinkCtlEntriesAppendResult{}, err
	}

	now := s.now()
	changed := false
	var lastMsg *int64
	var blobs []string
	entries := make([]Entry, 0, len(out))
	for _, in := range out {
		entries = append(entries, in.entry)
		if in.entry.Seq > t.lastSeq {
			t.lastSeq = in.entry.Seq
		}
		blobs = append(blobs, in.parsed.BlobRefs...)
		if !main {
			continue
		}
		t.live.ApplyEntry(in.parsed.Type, in.parsed.Payload)
		if in.parsed.Title != "" {
			if n, err := s.st.Q.SetThreadAutoTitle(ctx, db.SetThreadAutoTitleParams{Title: in.parsed.Title, UpdatedAt: now, ID: t.id}); err == nil && n > 0 {
				changed = true
			}
		}
		if in.isNew && !strings.HasPrefix(in.parsed.EntryID, "__") {
			if in.parsed.OffBranch {
				// 写在活动分支之外的条目（迟到的 model_usage 等）不移动叶子
				t.leaf = in.parsed.ActiveLeaf
			} else {
				id := in.parsed.EntryID
				t.leaf = &id
			}
		}
		if in.isNew && in.parsed.Type == "message" {
			ts := now
			if in.parsed.TS != nil {
				ts = *in.parsed.TS
			}
			lastMsg = &ts
		}
	}
	if len(entries) > 0 && main {
		_ = s.st.Q.SetThreadLeaf(ctx, db.SetThreadLeafParams{LeafEntryID: t.leaf, ID: t.id})
	}
	if lastMsg != nil {
		_ = s.st.Q.TouchThreadLastMessage(ctx, db.TouchThreadLastMessageParams{LastMessageAt: lastMsg, ID: t.id})
		changed = true
	}
	for i := 0; i < len(entries); i += entriesBatch {
		s.out().Entries(t.id, entries[i:min(i+entriesBatch, len(entries))], t.leaf, t.lastSeq)
	}
	if changed {
		s.publish(ctx, t.id)
	}
	s.recompute(ctx, t)
	if len(blobs) > 0 {
		s.fetchBlobs(orbID, blobs)
	}
	return apigen.LinkCtlEntriesAppendResult{Accepted: true, Offset: p.EndOffset}, nil
}

func (s *Service) onReset(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlEntriesResetParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resetting[p.File] = true
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.MarkEntriesStale(ctx, db.MarkEntriesStaleParams{ThreadID: t.id, File: p.File}); err != nil {
			return err
		}
		return q.UpsertEntryCursor(ctx, db.UpsertEntryCursorParams{ThreadID: t.id, File: p.File, ByteOffset: 0})
	})
	if err != nil {
		slog.Warn("threads: entries.reset 失败", "thread", t.id, "file", p.File, "err", err)
	}
}

func (s *Service) onResetDone(ctx context.Context, orbID string, params json.RawMessage) {
	var p apigen.LinkCtlEntriesResetDoneParams
	if json.Unmarshal(params, &p) != nil {
		return
	}
	t, err := s.ownThread(ctx, orbID, p.ThreadID)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.resetting, p.File)
	if _, err := s.st.Q.DeleteStaleEntries(ctx, db.DeleteStaleEntriesParams{ThreadID: t.id, File: p.File}); err != nil {
		slog.Warn("threads: entries.reset_done 失败", "thread", t.id, "file", p.File, "err", err)
	}
}

// dropFile 删除线程中某文件的全部条目与游标，并丢弃之后对它的追加（fork share 回退时移走新线程里的产物）。
func (s *Service) dropFile(ctx context.Context, t *thread, file string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ignored[file] = true
	_ = s.st.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.MarkEntriesStale(ctx, db.MarkEntriesStaleParams{ThreadID: t.id, File: file}); err != nil {
			return err
		}
		if _, err := q.DeleteStaleEntries(ctx, db.DeleteStaleEntriesParams{ThreadID: t.id, File: file}); err != nil {
			return err
		}
		return nil
	})
}

// ---- blob ----

// BlobPath 返回 blob 在数据目录中的路径。
func (s *Service) BlobPath(sha string) string {
	return filepath.Join(s.cfg.DataDir, "blobs", sha[:2], sha)
}

// fetchBlobs 对库中缺失的 blob 发 blob.fetch；内容经 orbd 另开的 blob 流到达。
func (s *Service) fetchBlobs(orbID string, shas []string) {
	for _, sha := range shas {
		if _, err := s.st.Q.GetBlob(context.Background(), sha); err == nil {
			continue
		}
		s.mu.Lock()
		if _, busy := s.blobs[sha]; busy {
			s.mu.Unlock()
			continue
		}
		done := make(chan struct{})
		s.blobs[sha] = done
		s.mu.Unlock()
		s.go_(func(ctx context.Context) {
			defer func() {
				s.mu.Lock()
				if s.blobs[sha] == done {
					delete(s.blobs, sha)
				}
				s.mu.Unlock()
			}()
			ctl, ok := s.orbs.Ctl(orbID)
			if !ok {
				return
			}
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			var res apigen.LinkCtlBlobFetchResult
			if err := ctl.Call(cctx, "blob.fetch", apigen.LinkCtlBlobFetchParams{Sha256: sha}, &res); err != nil || !res.Found {
				if err != nil {
					slog.Warn("threads: blob.fetch 失败", "sha256", sha, "err", err)
				}
				return
			}
			select {
			case <-done:
			case <-cctx.Done():
				slog.Warn("threads: 等待 blob 流超时", "sha256", sha)
			}
		})
	}
}

// maxBlobBytes 是单个 blob 的上限。
const maxBlobBytes = 1 << 30

// onBlobStream 接收 blob 流：校验 sha256 与大小后写入 <data_dir>/blobs/<2>/<sha> 并登记 blobs 行。
func (s *Service) onBlobStream(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream) {
	defer func() {
		s.mu.Lock()
		if ch := s.blobs[h.SHA256]; ch != nil {
			close(ch)
			delete(s.blobs, h.SHA256)
		}
		s.mu.Unlock()
	}()
	if err := s.storeBlob(ctx, h.SHA256, h.Size, st); err != nil {
		slog.Warn("threads: 接收 blob 失败", "orb", orbID, "sha256", h.SHA256, "err", err)
	}
}

func (s *Service) storeBlob(ctx context.Context, sha string, size int64, r io.Reader) error {
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return errors.New("invalid sha256")
	}
	if size < 0 || size > maxBlobBytes {
		return fmt.Errorf("invalid size %d", size)
	}
	if _, err := s.st.Q.GetBlob(ctx, sha); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r, size))
		return nil
	}
	path := s.BlobPath(sha)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".blob-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	hsh := sha256.New()
	var head [512]byte
	n, err := io.ReadFull(io.LimitReader(r, size), head[:min(int64(len(head)), size)])
	if err != nil {
		f.Close()
		return err
	}
	_, _ = hsh.Write(head[:n])
	if _, err := f.Write(head[:n]); err != nil {
		f.Close()
		return err
	}
	m, err := io.Copy(io.MultiWriter(f, hsh), io.LimitReader(r, size-int64(n)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if int64(n)+m != size {
		return fmt.Errorf("short blob: %d of %d bytes", int64(n)+m, size)
	}
	if got := hex.EncodeToString(hsh.Sum(nil)); got != sha {
		return fmt.Errorf("sha256 mismatch: got %s", got)
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return s.st.Q.InsertBlob(ctx, db.InsertBlobParams{Sha256: sha, Size: size, Mime: http.DetectContentType(head[:n]), CreatedAt: s.now()})
}

// Blob 返回 blob 元数据与文件路径。
func (s *Service) Blob(ctx context.Context, sha string) (db.Blob, string, error) {
	b, err := s.st.Q.GetBlob(ctx, sha)
	if errors.Is(err, sql.ErrNoRows) {
		return b, "", errf("not_found", "blob not found")
	}
	if err != nil {
		return b, "", err
	}
	return b, s.BlobPath(sha), nil
}
