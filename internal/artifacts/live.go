package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
)

// 实时画布（M14 计划步骤 6.2）。
//
// 每个有人在看（或刚收到用户操作）的画布 artifact 在内存里有一个 live：底稿版本 version、当前文档 doc
// （底稿 + 用户还没落成版本的操作 pending）、实时序号 seq。同一 artifact 的全部实时操作、落版本与重载
// 按 artifact 串行（liveSet.locks）。用户操作静默 CanvasQuiet 后，或最后一个观看者离开时，把累计的结果
// 写成一个用户版本；别处产生了新版本（agent 写入、恢复版本、编辑元素等）时重新加载底稿，把 pending 在
// 新文档上重放（放不上的丢弃），并把整份文档广播给观看者。agent 的画布操作先把用户的 pending 单独落成
// 一个用户版本，再写自己的版本：每个版本只有一个作者。

// CanvasQuiet 是用户画布操作落成版本前的静默时长。
const CanvasQuiet = 30 * time.Second

// BlankPage 是新画框在有内容之前的空白页（一个只有空 <body> 的完整 HTML 文档）。
const BlankPage = "<!doctype html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n</head>\n<body></body>\n</html>\n"

// canvasFlushTimeout 是计时器、观看者离开与关停时落版本的时限（它们没有请求上下文）。
const canvasFlushTimeout = 30 * time.Second

// canvasRetryMax 是落版本失败后重试间隔的上限（从 CanvasQuiet 起逐次翻倍）。
const canvasRetryMax = 10 * time.Minute

// CanvasHub 是实时画布的出口（realtime.Hub 实现）。
type CanvasHub interface {
	// ArtifactCanvas 把画布的实时变化发给正在观看 msg.ID 的连接。
	ArtifactCanvas(msg apigen.WsServerArtifactCanvas)
	// WatchPages 返回正在观看 artifact id 的各连接所在的画布页 id（没报告时为空串）；没人在看时为空。
	WatchPages(id string) []string
	// OnWatchChange 登记观看者变化的回调。
	OnWatchChange(fn func(id string))
}

// CanvasState 是画布的实时状态。
type CanvasState struct {
	Version int
	Seq     int64
	Doc     canvas.Doc
}

// API 返回 REST 表示。
func (c CanvasState) API() apigen.ArtifactCanvasState {
	return apigen.ArtifactCanvasState{Version: c.Version, Seq: c.Seq, Doc: docMap(c.Doc)}
}

// live 是一个画布的实时状态；字段只在持有该 artifact 的 liveSet.locks 时访问。
type live struct {
	version int
	doc     canvas.Doc
	pending []canvas.Op
	// copies：带 copyOf 新增的画框文件 → 复制来源的文件（落版本时复制内容）。
	copies map[string]string
	gen    int // 计时器代数：只有最近一次重置的计时器到点才落版本
	stop   func() bool
	// retries：连续落版本失败的次数（决定下次重试的间隔）；有新操作或落版本成功时清零。
	retries int
}

func (l *live) stopTimer() {
	if l.stop != nil {
		l.stop()
		l.stop = nil
	}
}

// liveSet 是全部实时画布；零值可用。
type liveSet struct {
	mu  sync.Mutex
	m   map[string]*live
	seq map[string]int64 // 实时序号跨 live 的生命周期递增（live 被回收后重建也不回退）
	hub CanvasHub
	// after 安排一次延时回调并返回取消函数；nil 时用 time.AfterFunc（测试注入假时钟）。
	after func(d time.Duration, f func()) (stop func() bool)
	locks keyLocks
}

// AttachCanvasHub 接上实时画布的出口并登记观看者变化的回调；须在开始服务之前调用。
func (s *Service) AttachCanvasHub(h CanvasHub) {
	s.live.mu.Lock()
	s.live.hub = h
	s.live.mu.Unlock()
	h.OnWatchChange(s.canvasWatchChanged)
}

func (s *Service) canvasHub() CanvasHub {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	return s.live.hub
}

func (s *Service) liveOf(id string) *live {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	return s.live.m[id]
}

func (s *Service) dropLive(id string, l *live) {
	l.stopTimer()
	s.live.mu.Lock()
	if s.live.m[id] == l {
		delete(s.live.m, id)
	}
	s.live.mu.Unlock()
}

func (s *Service) nextSeq(id string) int64 {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	if s.live.seq == nil {
		s.live.seq = map[string]int64{}
	}
	s.live.seq[id]++
	return s.live.seq[id]
}

func (s *Service) curSeq(id string) int64 {
	s.live.mu.Lock()
	defer s.live.mu.Unlock()
	return s.live.seq[id]
}

func (s *Service) broadcastCanvas(msg apigen.WsServerArtifactCanvas) {
	if h := s.canvasHub(); h != nil {
		msg.Type = apigen.WsServerArtifactCanvasTypeArtifactCanvas
		h.ArtifactCanvas(msg)
	}
}

// loadCanvas 读画布 artifact 的最新版本与它的 canvas.json。
func (s *Service) loadCanvas(ctx context.Context, id string) (Artifact, canvas.Doc, error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return Artifact{}, canvas.Doc{}, err
	}
	if a.Deleted() {
		return Artifact{}, canvas.Doc{}, deletedErr(id)
	}
	if a.Kind != KindCanvas {
		return Artifact{}, canvas.Doc{}, opErr(CodeInvalid, "%s 不是画布 artifact，没有画布操作。", id)
	}
	b, _, err := s.ReadFile(ctx, id, a.Version, EntryCanvas)
	if err != nil {
		return Artifact{}, canvas.Doc{}, err
	}
	doc, err := canvas.Parse(b)
	if err != nil {
		return Artifact{}, canvas.Doc{}, fmt.Errorf("artifact %s 版本 %d 的 canvas.json：%w", id, a.Version, err)
	}
	return a, doc, nil
}

// ensureLive 返回 id 的 live，没有就从最新版本建一个。调用方持有该 artifact 的锁。
func (s *Service) ensureLive(ctx context.Context, id string) (*live, error) {
	if l := s.liveOf(id); l != nil {
		return l, nil
	}
	a, doc, err := s.loadCanvas(ctx, id)
	if err != nil {
		return nil, err
	}
	l := &live{version: a.Version, doc: doc}
	s.live.mu.Lock()
	if s.live.m == nil {
		s.live.m = map[string]*live{}
	}
	s.live.m[id] = l
	s.live.mu.Unlock()
	return l, nil
}

// CanvasState 返回画布的实时状态：有 live 时是底稿加未落版本的用户操作，否则是最新版本。
func (s *Service) CanvasState(ctx context.Context, id string) (CanvasState, error) {
	unlock := s.live.locks.lock(id)
	defer unlock()
	if l := s.liveOf(id); l != nil {
		return CanvasState{Version: l.version, Seq: s.curSeq(id), Doc: l.doc}, nil
	}
	a, doc, err := s.loadCanvas(ctx, id)
	if err != nil {
		return CanvasState{}, err
	}
	return CanvasState{Version: a.Version, Seq: s.curSeq(id), Doc: doc}, nil
}

// maxCanvasClientBytes 是 client 标签的长度上限。
const maxCanvasClientBytes = 64

// ApplyCanvas 应用用户的一批画布操作（整批原子）：校验、seq 加一、广播给观看者、重置静默计时器。
// 用户画的标注 by 一律为 null。返回新的 seq。
func (s *Service) ApplyCanvas(ctx context.Context, id string, raw json.RawMessage, client string, clientSeq int64) (int64, error) {
	if client == "" || len(client) > maxCanvasClientBytes {
		return 0, opErr(CodeInvalid, "client 必须是 1–%d 字节的标签页 id。", maxCanvasClientBytes)
	}
	if clientSeq < 0 {
		return 0, opErr(CodeInvalid, "clientSeq 不能是负数。")
	}
	ops, err := parseCanvasOps(raw)
	if err != nil {
		return 0, err
	}
	stampNotes(ops, nil)
	unlock := s.live.locks.lock(id)
	defer unlock()
	l, err := s.ensureLive(ctx, id)
	if err != nil {
		return 0, err
	}
	doc := l.doc
	if err := canvas.Apply(&doc, ops); err != nil {
		return 0, canvasOpsErr(err)
	}
	ops = settleAdds(doc, ops)
	l.copies = addCopies(l.copies, l.doc, doc, ops)
	l.doc = doc
	l.pending = append(l.pending, ops...)
	seq := s.nextSeq(id)
	s.broadcastCanvas(apigen.WsServerArtifactCanvas{ID: id, Seq: seq, Ops: opsMaps(ops), Client: &client, ClientSeq: &clientSeq})
	s.armQuiet(id, l)
	return seq, nil
}

// parseCanvasOps 解析一批操作；空批次是错误。
func parseCanvasOps(raw json.RawMessage) ([]canvas.Op, error) {
	ops, err := canvas.ParseOps(raw)
	if err != nil {
		return nil, canvasOpsErr(err)
	}
	if len(ops) == 0 {
		return nil, opErr(CodeInvalid, "画布操作是空的：至少给一条 add、set、remove 或 order。")
	}
	return ops, nil
}

func canvasOpsErr(err error) *OpError {
	return opErr(CodeInvalid, "画布操作不合法：%v。整批操作都没有生效；操作的格式见 read_artifact_guide 的 canvas 主题。", err)
}

// stampNotes 把新增标注的 by 记为 thread（nil = 用户）：作者由服务端决定，不信任提交方。
func stampNotes(ops []canvas.Op, thread *string) {
	for _, op := range ops {
		if op.Op == canvas.OpAdd && op.ObjectType() == canvas.TypeNote {
			if thread != nil {
				op.Object["by"] = *thread
			} else {
				op.Object["by"] = nil
			}
		}
	}
}

// settleAdds 把 add 的对象换成它在应用后文档里的完整形态（补上缺省的位置、文件、所属画布页等），
// 让各端与重放都得到同一个结果。同一批里随后被删掉的对象保持原样。
func settleAdds(doc canvas.Doc, ops []canvas.Op) []canvas.Op {
	out := slices.Clone(ops)
	for i, op := range out {
		if op.Op != canvas.OpAdd {
			continue
		}
		var v any
		switch id := op.ObjectID(); op.ObjectType() {
		case canvas.TypeCanvas:
			if j := slices.IndexFunc(doc.Canvases, func(c canvas.Canvas) bool { return c.ID == id }); j >= 0 {
				v = doc.Canvases[j]
			}
		case canvas.TypePage:
			if p, ok := doc.PageByID(id); ok {
				v = p
			}
		case canvas.TypeNote:
			if j := slices.IndexFunc(doc.Notes, func(n canvas.Note) bool { return n.ID == id }); j >= 0 {
				v = doc.Notes[j]
			}
		}
		if v == nil {
			continue
		}
		m := map[string]any{}
		b, err := json.Marshal(v)
		if err != nil || json.Unmarshal(b, &m) != nil {
			continue
		}
		m["type"] = op.ObjectType()
		out[i].Object = m
	}
	return out
}

// addCopies 记下带 copyOf 新增的画框：新画框的文件 → 来源画框的文件（来源先在 pre 里找，再在 post 里找）。
func addCopies(m map[string]string, pre, post canvas.Doc, ops []canvas.Op) map[string]string {
	for _, op := range ops {
		if op.Op != canvas.OpAdd || op.CopyOf == "" {
			continue
		}
		np, ok := post.PageByID(op.ObjectID())
		if !ok {
			continue
		}
		src, ok := pre.PageByID(op.CopyOf)
		if !ok {
			src, ok = post.PageByID(op.CopyOf)
		}
		if !ok || src.File == np.File {
			continue
		}
		if m == nil {
			m = map[string]string{}
		}
		m[np.File] = src.File
	}
	return m
}

// armQuiet 重置静默计时器（有新操作时调用，清零失败重试计数）。调用方持有该 artifact 的锁。
func (s *Service) armQuiet(id string, l *live) {
	l.retries = 0
	s.arm(id, l, CanvasQuiet)
}

// armRetry 在落版本失败后安排下一次重试：间隔从 CanvasQuiet 起逐次翻倍，最多 canvasRetryMax。
// 调用方持有该 artifact 的锁。
func (s *Service) armRetry(id string, l *live) {
	l.retries++
	d := CanvasQuiet << min(l.retries, 10)
	s.arm(id, l, min(d, canvasRetryMax))
}

func (s *Service) arm(id string, l *live, d time.Duration) {
	l.stopTimer()
	l.gen++
	gen := l.gen
	after := s.live.after
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	l.stop = after(d, func() { s.canvasQuiet(id, gen) })
}

// canvasQuiet 是静默（或重试）计时器到点：落版本；落完且没人在看就回收 live。
func (s *Service) canvasQuiet(id string, gen int) {
	unlock := s.live.locks.lock(id)
	defer unlock()
	l := s.liveOf(id)
	if l == nil || l.gen != gen {
		return
	}
	l.stop = nil
	ctx, cancel := context.WithTimeout(context.Background(), canvasFlushTimeout)
	defer cancel()
	if !s.flushOrRetry(ctx, id, l) {
		return
	}
	if l = s.liveOf(id); l != nil && !s.watched(id) {
		s.dropLive(id, l)
	}
}

func (s *Service) watched(id string) bool {
	h := s.canvasHub()
	return h != nil && len(h.WatchPages(id)) > 0
}

// canvasWatchChanged 是观看者变化：有人在看就确保有 live（以便别处的新版本能实时推给他们）；
// 最后一个观看者离开时落版本并回收 live。
func (s *Service) canvasWatchChanged(id string) {
	if !ValidID(id) {
		return
	}
	unlock := s.live.locks.lock(id)
	defer unlock()
	l := s.liveOf(id)
	// 在锁内看观看者：并发的进出事件按拿到锁的顺序各自以当时的观看者为准
	if s.watched(id) {
		if l == nil {
			// 不是画布、不存在或在回收站里的 artifact 没有 live
			_, _ = s.ensureLive(context.Background(), id)
		}
		return
	}
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), canvasFlushTimeout)
	defer cancel()
	// 没落成（暂时性错误）时保留 live 与 pending，由重试计时器继续；落成后才回收
	if s.flushOrRetry(ctx, id, l) {
		if l = s.liveOf(id); l != nil {
			s.dropLive(id, l)
		}
	}
}

// FlushCanvases 把全部实时画布里未落版本的用户操作写成版本并停掉计时器（控制面关停时调用）。
func (s *Service) FlushCanvases() {
	s.live.mu.Lock()
	ids := make([]string, 0, len(s.live.m))
	for id := range s.live.m {
		ids = append(ids, id)
	}
	s.live.mu.Unlock()
	for _, id := range ids {
		func() {
			unlock := s.live.locks.lock(id)
			defer unlock()
			l := s.liveOf(id)
			if l == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), canvasFlushTimeout)
			defer cancel()
			if err := s.flushLive(ctx, id, l); err != nil {
				slog.Warn("artifacts: 关停时画布操作没能落成版本", "artifact", id, "err", err)
			}
			s.dropLive(id, l)
		}()
	}
}

// flushOrRetry 落版本，返回是否已经没有待落的操作。artifact 已被永久删除（not_found）时丢弃 pending 并回收
// live；其它失败（数据库、磁盘、超时、在回收站里等）一律保留已经接受并广播过的 pending，安排一次重试。
// 调用方持有锁。
func (s *Service) flushOrRetry(ctx context.Context, id string, l *live) bool {
	err := s.flushLive(ctx, id, l)
	if err == nil {
		l.retries = 0
		return true
	}
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		slog.Warn("artifacts: artifact 已不存在，丢弃未落版本的画布操作", "artifact", id, "ops", len(l.pending))
		l.pending, l.copies = nil, nil
		s.dropLive(id, l)
		return false
	}
	s.armRetry(id, l)
	slog.Warn("artifacts: 画布操作没能落成版本，稍后重试", "artifact", id, "ops", len(l.pending), "retries", l.retries, "err", err)
	return false
}

// flushLive 把 pending 写成一个用户版本。底稿之后 canvas.json 被别处改过时先重载、重放再写（最多三次）。
// 调用方持有该 artifact 的锁。
func (s *Service) flushLive(ctx context.Context, id string, l *live) error {
	if len(l.pending) == 0 {
		return nil
	}
	for attempt := 1; ; attempt++ {
		a, err := s.writeCanvas(ctx, id, l.version, l.doc, l.copies, Author{}, Turn{}, "", nil)
		if err == nil {
			l.version, l.pending, l.copies = a.Version, nil, nil
			l.stopTimer()
			// 用户的画布改动可能带来新的 sticky：设计画布标脏，由便签投递去抖后判断
			s.markNotesDirty(ctx, id)
			return nil
		}
		if oe, ok := AsOpError(err); attempt == 3 || !ok || oe.Code != CodeConflict || oe.State == nil || !slices.Contains(oe.State.Changed, EntryCanvas) {
			return err
		}
		if err := s.reloadLive(ctx, id, l); err != nil {
			return err
		}
		if len(l.pending) == 0 {
			return nil
		}
	}
}

// writeCanvas 把 doc 作为 canvas.json、连同 extra 写成 base 之上的新版本（记在 turn 回合下）。画框引用了、但最新版本与 extra
// 里都没有的文件由服务端补上：带 copyOf 的复制来源画框的文件，其余是 BlankPage。canvas.json 以 base 为底稿
// 参与冲突检查（base 之后被别处改过就 conflict），补上的文件不参与。
func (s *Service) writeCanvas(ctx context.Context, id string, base int, doc canvas.Doc, copies map[string]string, author Author, turn Turn, note string, extra []FileChange) (Artifact, error) {
	head, err := s.Files(ctx, id, 0)
	if err != nil {
		return Artifact{}, err
	}
	inHead := make(map[string]bool, len(head))
	for _, f := range head {
		inHead[f.Path] = true
	}
	inExtra := make(map[string]bool, len(extra))
	for _, f := range extra {
		inExtra[f.Path] = true
	}
	files := append(slices.Clone(extra), FileChange{Path: EntryCanvas, Data: bytes.NewReader(doc.JSON())})
	var made []string
	for _, p := range doc.Pages {
		// 本次写了的不补；本次删掉却仍被画框引用的交给 Write 的校验报错
		if inExtra[p.File] || inHead[p.File] || slices.Contains(made, p.File) {
			continue
		}
		data := []byte(BlankPage)
		if src, ok := copies[p.File]; ok && inHead[src] {
			if data, _, err = s.ReadFile(ctx, id, 0, src); err != nil {
				return Artifact{}, err
			}
		}
		files = append(files, FileChange{Path: p.File, Data: bytes.NewReader(data)})
		made = append(made, p.File)
	}
	return s.Write(ctx, WriteParams{ID: id, BaseVersion: base, Author: author, Turn: turn, Note: note, Files: files, Unchecked: made})
}

// reloadLive 以最新版本为底稿重建文档，把 pending 逐条重放（放不上的丢弃），广播整份文档。调用方持有锁。
func (s *Service) reloadLive(ctx context.Context, id string, l *live) error {
	a, doc, err := s.loadCanvas(ctx, id)
	if err != nil {
		return err
	}
	kept := l.pending[:0]
	for _, op := range l.pending {
		if canvas.Apply(&doc, []canvas.Op{op}) == nil {
			kept = append(kept, op)
		}
	}
	l.version, l.doc, l.pending = a.Version, doc, kept
	if len(kept) == 0 {
		l.copies = nil
		l.stopTimer()
	}
	s.broadcastDoc(id, l)
	return nil
}

func (s *Service) broadcastDoc(id string, l *live) {
	v := l.version
	s.broadcastCanvas(apigen.WsServerArtifactCanvas{ID: id, Seq: s.nextSeq(id), Doc: docMap(l.doc), Version: &v})
}

// liveVersioned 由每个新版本触发：有 live 时在后台重新加载。写入方可能正持有该 artifact 的锁
// （自己落版本），所以不能在这里同步等锁；自己写出的版本在拿到锁时已经不比底稿新，直接跳过。
func (s *Service) liveVersioned(id string, n int) {
	if s.liveOf(id) == nil {
		return
	}
	go s.liveFollow(id, n)
}

func (s *Service) liveFollow(id string, n int) {
	unlock := s.live.locks.lock(id)
	defer unlock()
	l := s.liveOf(id)
	if l == nil || n <= l.version {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), canvasFlushTimeout)
	defer cancel()
	if err := s.reloadLive(ctx, id, l); err != nil {
		slog.Debug("artifacts: 画布重新加载失败，回收实时状态", "artifact", id, "err", err)
		s.dropLive(id, l)
	}
}

// agentCanvas 执行带 canvas 的 update_artifact：先把用户未落版本的操作单独写成一个用户版本，再在最新文档上
// 应用 agent 的操作，把合并后的 canvas.json 与本次的文件一起写成 agent 的版本。base 是 agent 的 baseVersion：
// files 里的文件按它做冲突检查；canvas.json 由服务端合并，不按它检查。
func (s *Service) agentCanvas(ctx context.Context, c Caller, id string, base int, raw json.RawMessage, files []FileChange, note string) (Artifact, error) {
	ops, err := parseCanvasOps(raw)
	if err != nil {
		return Artifact{}, err
	}
	var by *string
	if c.ThreadID != "" {
		by = &c.ThreadID
	}
	stampNotes(ops, by)
	unlock := s.live.locks.lock(id)
	defer unlock()
	l := s.liveOf(id)
	if l != nil {
		if err := s.flushLive(ctx, id, l); err != nil {
			return Artifact{}, err
		}
	}
	a, doc, err := s.loadCanvas(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	pre := doc
	if err := canvas.Apply(&doc, ops); err != nil {
		return Artifact{}, canvasOpsErr(err)
	}
	if base < a.Version {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		if err := checkBase(ctx, s.st.Q, id, base, int64(a.Version), paths); err != nil {
			return Artifact{}, err
		}
	}
	r, err := s.writeCanvas(ctx, id, a.Version, doc, addCopies(nil, pre, doc, ops), c.author(), c.Turn, note, files)
	if err != nil {
		return Artifact{}, err
	}
	if l != nil {
		l.version, l.doc, l.pending, l.copies = r.Version, doc, nil, nil
		s.broadcastDoc(id, l)
	}
	return r, nil
}

// canvasViewing 返回观看者所在画布页的名字（去重，按观看者顺序；没报告画布页的算第一个画布页）。
func (s *Service) canvasViewing(ctx context.Context, id string) []string {
	h := s.canvasHub()
	if h == nil {
		return nil
	}
	pages := h.WatchPages(id)
	if len(pages) == 0 {
		return nil
	}
	var doc canvas.Doc
	unlock := s.live.locks.lock(id)
	l := s.liveOf(id)
	if l != nil {
		doc = l.doc
	}
	unlock()
	if l == nil {
		var err error
		if _, doc, err = s.loadCanvas(ctx, id); err != nil {
			return nil
		}
	}
	var out []string
	for _, p := range pages {
		if p == "" && len(doc.Canvases) > 0 {
			p = doc.Canvases[0].ID
		}
		if i := slices.IndexFunc(doc.Canvases, func(c canvas.Canvas) bool { return c.ID == p }); i >= 0 && !slices.Contains(out, doc.Canvases[i].Name) {
			out = append(out, doc.Canvases[i].Name)
		}
	}
	return out
}

// opsMaps 把操作换成 JSON 对象（实时消息用）。
func opsMaps(ops []canvas.Op) []map[string]any {
	b, err := json.Marshal(ops)
	if err != nil {
		return nil
	}
	var out []map[string]any
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// docMap 把文档换成 JSON 对象（REST 与实时消息用）。
func docMap(d canvas.Doc) map[string]any {
	var m map[string]any
	if json.Unmarshal(d.JSON(), &m) != nil {
		return nil
	}
	return m
}
