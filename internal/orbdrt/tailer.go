package orbdrt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// entries.append 批次上限：编码后字节与行数。单行 JSON 字符串编码后至多约为原文 2 倍
// （SetEscapeHTML(false)），因此 ctl 行始终远低于 link.MaxCtlLineBytes。
const (
	maxBatchBytes = 4 << 20
	maxBatchLines = 500
	// maxEntryLine 是可复制的单行上限（编码后）；更长的行只推进偏移、不发送内容。
	maxEntryLine = 40 << 20
	// artifactDepth 是会话产物子目录内查找 *.jsonl 的最大深度。
	artifactDepth = 4
)

// tailer 把线程会话目录下的 JSONL 行复制到控制面（计划 C2 entries.*、H4.2）。
// 一个线程一个 goroutine，所有文件按序串行同步：同一 file 的 append 请求天然串行。
type tailer struct {
	t       *thread
	trigger chan struct{}
	flushCh chan chan struct{}

	files   map[string]*tfile
	cursors map[string]int64
	watched map[string]bool
	watcher *fsnotify.Watcher
}

type tfile struct {
	offset       int64 // 服务端已确认的偏移
	ino          uint64
	resetPending bool
}

func newTailer(t *thread) *tailer {
	return &tailer{
		t:       t,
		trigger: make(chan struct{}, 1),
		flushCh: make(chan chan struct{}),
		files:   map[string]*tfile{},
		watched: map[string]bool{},
	}
}

func (tl *tailer) kick() {
	select {
	case tl.trigger <- struct{}{}:
	default:
	}
}

// flush 请求一次完整同步并等待其完成（或 ctx 结束）。
func (tl *tailer) flush(ctx context.Context) {
	done := make(chan struct{})
	select {
	case tl.flushCh <- done:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (tl *tailer) run(ctx context.Context) {
	r := tl.t.r
	if w, err := fsnotify.NewWatcher(); err == nil {
		tl.watcher = w
		defer w.Close()
		go tl.watchLoop(ctx, w)
	} else {
		r.log.Warn("fsnotify unavailable; polling only", "thread", tl.t.id, "err", err)
	}
	tick := time.NewTicker(r.cfg.PollInterval)
	defer tick.Stop()
	var waiters []chan struct{}
	for {
		c, err := r.waitConn(ctx)
		if err != nil {
			return
		}
		if err := tl.loadCursors(ctx, c); err != nil {
			if ctx.Err() != nil {
				return
			}
			if isForeignThread(err) {
				r.dropThread(tl.t)
				return
			}
			r.log.Warn("entries.cursors failed", "thread", tl.t.id, "err", err)
			if !sleepOr(ctx, c.done, time.Second) {
				return
			}
			continue
		}
	inner:
		for {
			if err := tl.scan(ctx, c); err != nil {
				if ctx.Err() != nil {
					return
				}
				if isForeignThread(err) {
					r.dropThread(tl.t)
					return
				}
				r.log.Warn("entries sync failed", "thread", tl.t.id, "err", err)
			} else {
				for _, w := range waiters {
					close(w)
				}
				waiters = nil
			}
			select {
			case <-tl.trigger:
			case <-tick.C:
			case w := <-tl.flushCh:
				waiters = append(waiters, w)
			case <-c.done:
				break inner
			case <-ctx.Done():
				return
			}
		}
	}
}

func (tl *tailer) watchLoop(ctx context.Context, w *fsnotify.Watcher) {
	for {
		select {
		case _, ok := <-w.Events:
			if !ok {
				return
			}
			tl.kick()
		case _, ok := <-w.Errors:
			if !ok {
				return
			}
			tl.kick()
		case <-ctx.Done():
			return
		}
	}
}

func (tl *tailer) watch(dir string) {
	if tl.watcher == nil || tl.watched[dir] {
		return
	}
	if err := tl.watcher.Add(dir); err == nil {
		tl.watched[dir] = true
	}
}

// loadCursors 以服务端游标为准重建本地偏移（重连续传）；重置未完成的文件保持重置。
func (tl *tailer) loadCursors(ctx context.Context, c *conn) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res apigen.LinkCtlEntriesCursorsResult
	if err := c.ctl.Call(cctx, "entries.cursors", apigen.LinkCtlEntriesCursorsParams{ThreadID: tl.t.id}, &res); err != nil {
		return err
	}
	tl.cursors = make(map[string]int64, len(res.Cursors))
	for _, cur := range res.Cursors {
		tl.cursors[cur.File] = cur.Offset
	}
	for rel, f := range tl.files {
		if !f.resetPending {
			f.offset = tl.cursors[rel]
		}
	}
	return nil
}

// list 返回需要复制的文件（相对路径，`/` 分隔）：顶层 *.jsonl 按名称（即创建时间）排序，
// 每个主会话文件之后紧跟其同名产物子目录内的 *.jsonl。
func (tl *tailer) list() []string {
	dir := tl.t.dir
	tl.watch(dir)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var mains []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			mains = append(mains, e.Name())
		}
	}
	sort.Strings(mains)
	var out []string
	for _, m := range mains {
		out = append(out, m)
		art := filepath.Join(dir, strings.TrimSuffix(m, ".jsonl"))
		st, err := os.Stat(art)
		if err != nil || !st.IsDir() {
			continue
		}
		var subs []string
		_ = filepath.WalkDir(art, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			if d.IsDir() {
				if strings.Count(rel, string(filepath.Separator)) >= artifactDepth {
					return filepath.SkipDir
				}
				tl.watch(p)
				return nil
			}
			if strings.HasSuffix(d.Name(), ".jsonl") {
				subs = append(subs, filepath.ToSlash(rel))
			}
			return nil
		})
		sort.Strings(subs)
		out = append(out, subs...)
	}
	return out
}

func (tl *tailer) scan(ctx context.Context, c *conn) error {
	present := map[string]bool{}
	for _, rel := range tl.list() {
		present[rel] = true
		if err := tl.syncFile(ctx, c, rel); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
	}
	for rel := range tl.files {
		if !present[rel] {
			delete(tl.files, rel)
		}
	}
	return nil
}

func inodeOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}

func (tl *tailer) syncFile(ctx context.Context, c *conn, rel string) error {
	abs := filepath.Join(tl.t.dir, filepath.FromSlash(rel))
	fi, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			delete(tl.files, rel)
			return nil
		}
		return err
	}
	ino := inodeOf(fi)
	f := tl.files[rel]
	if f == nil {
		f = &tfile{offset: tl.cursors[rel], ino: ino}
		tl.files[rel] = f
	}
	if f.ino != ino {
		f.ino = ino
		f.resetPending = true
	}
	if fi.Size() < f.offset {
		f.resetPending = true
	}
	if f.resetPending {
		return tl.reset(ctx, c, rel, abs, f)
	}
	if fi.Size() > f.offset {
		return tl.appendFrom(ctx, c, rel, abs, f)
	}
	return nil
}

// reset 通知服务端文件被重写，从 0 全量重发，完成后发 reset_done。
// 中途断连时 resetPending 保持为真，重连后重新开始。
func (tl *tailer) reset(ctx context.Context, c *conn, rel, abs string, f *tfile) error {
	tl.t.r.log.Info("session file rewritten; resending", "thread", tl.t.id, "file", rel)
	if err := c.ctl.Notify("entries.reset", apigen.LinkCtlEntriesResetParams{ThreadID: tl.t.id, File: rel}); err != nil {
		return err
	}
	f.offset = 0
	if err := tl.appendFrom(ctx, c, rel, abs, f); err != nil {
		return err
	}
	if err := c.ctl.Notify("entries.reset_done", apigen.LinkCtlEntriesResetDoneParams{ThreadID: tl.t.id, File: rel}); err != nil {
		return err
	}
	f.resetPending = false
	return nil
}

// appendFrom 从 f.offset 起把完整行分批发出，直到文件末尾（不含未以 \n 结尾的残行）。
func (tl *tailer) appendFrom(ctx context.Context, c *conn, rel, abs string, f *tfile) error {
	fh, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer fh.Close()
	for {
		lines, end, err := readBatch(fh, f.offset)
		if err != nil {
			return err
		}
		if end == f.offset {
			return nil
		}
		params := encodeAppend(tl.t.id, rel, f.offset, end, lines)
		var res apigen.LinkCtlEntriesAppendResult
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		err = c.ctl.Call(cctx, "entries.append", params, &res)
		cancel()
		if err != nil {
			return err
		}
		if res.Accepted {
			f.offset = end
			continue
		}
		if res.Offset == f.offset || res.Offset < 0 {
			return fmt.Errorf("entries.append rejected at offset %d", f.offset)
		}
		tl.t.r.log.Info("entries cursor mismatch; resending", "thread", tl.t.id, "file", rel, "from", res.Offset, "had", f.offset)
		f.offset = res.Offset
		if fi, err := fh.Stat(); err == nil && res.Offset > fi.Size() {
			f.resetPending = true
			return nil
		}
	}
}

// readBatch 从 off 起读取一批完整行，返回各行的 JSON 字符串编码与批末偏移。
func readBatch(fh *os.File, off int64) ([][]byte, int64, error) {
	if _, err := fh.Seek(off, io.SeekStart); err != nil {
		return nil, off, err
	}
	br := bufio.NewReaderSize(fh, 256<<10)
	var lines [][]byte
	end, size := off, 0
	for len(lines) < maxBatchLines && size < maxBatchBytes {
		line, n, complete, err := readLine(br, maxEntryLine/2)
		if err != nil {
			return nil, off, err
		}
		if !complete {
			break
		}
		if line == nil { // 超长行：跳过内容，只推进偏移
			end += n
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			end += n
			continue
		}
		enc := encodeString(line)
		if len(lines) > 0 && size+len(enc) > maxBatchBytes {
			break // 留给下一批
		}
		if len(enc) > maxEntryLine {
			end += n
			continue
		}
		lines = append(lines, enc)
		size += len(enc)
		end += n
	}
	return lines, end, nil
}

// readLine 读取一行（含 \n 的字节数为 n）。行超过 limit 时返回 line=nil（内容被丢弃）；
// 读到 EOF 仍无 \n 时 complete=false。
func readLine(br *bufio.Reader, limit int) (line []byte, n int64, complete bool, err error) {
	var buf []byte
	over := false
	for {
		chunk, rerr := br.ReadSlice('\n')
		n += int64(len(chunk))
		if !over {
			if len(buf)+len(chunk) > limit+1 {
				over, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case rerr == nil:
			if over {
				return nil, n, true, nil
			}
			return buf[:len(buf)-1], n, true, nil
		case errors.Is(rerr, bufio.ErrBufferFull):
			continue
		case errors.Is(rerr, io.EOF):
			return nil, n, false, nil
		default:
			return nil, n, false, rerr
		}
	}
}

func encodeString(b []byte) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(string(b))
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// encodeAppend 手工拼出 entries.append 的 params，避免对大批次再做一次带 HTML 转义的编码。
func encodeAppend(threadID, file string, base, end int64, lines [][]byte) json.RawMessage {
	size := 128 + len(threadID) + len(file)
	for _, l := range lines {
		size += len(l) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, `{"threadId":`...)
	out = append(out, encodeString([]byte(threadID))...)
	out = append(out, `,"file":`...)
	out = append(out, encodeString([]byte(file))...)
	out = append(out, `,"baseOffset":`...)
	out = strconv.AppendInt(out, base, 10)
	out = append(out, `,"endOffset":`...)
	out = strconv.AppendInt(out, end, 10)
	out = append(out, `,"lines":[`...)
	for i, l := range lines {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, l...)
	}
	out = append(out, "]}"...)
	return out
}

// 编译期保证：最大单行（编码后）加批次开销仍低于 ctl 行上限。
const _ = uint(link.MaxCtlLineBytes - maxEntryLine - maxBatchBytes - (1 << 20))
