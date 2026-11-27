package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/attachments"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// 文件库与 orb 之间的字节搬运（M13 计划 4.3 / 4.4 / 4.6）：
//   - 同步：把助手可读的文件（共享文件与属于某个对话的文件）按 SandboxPath 写进协调宿主沙箱的 ~/library/，
//     先发 library.sync 清单（宿主删掉清单外的一切并回报缺失），再为每个缺失文件开一条 library 流推送字节；
//   - 取回：对 orb 发 library.fetch{id, path}，orbd 另开一条流头带同一 id 的 library 流回传字节——助手的 save_file
//     从协调宿主的 ~/library/out/ 取，orb 内 `oryn library put` 从发起请求的 orb 取。

const (
	// pushTimeout 是推送一个文件（含等应答行）的上限。
	pushTimeout = 10 * time.Minute
	// fetchStreamWait 是 library.fetch 应答之后等字节流到达的上限（orbd 先开流再应答，正常已到）。
	fetchStreamWait = 30 * time.Second
	// syncDebounce 合并突发的变化（例如一次上传多个文件）。
	syncDebounce = 200 * time.Millisecond
	// replyMax 是同步流应答行的上限。
	replyMax = 4 << 10
)

// ErrHostUnavailable 表示目标 orb 当前没有可用的 link。
var ErrHostUnavailable = errors.New("library: orb is not connected")

// HostLinks 是同步与取回需要的 orb link 能力（*orbs.Service 实现）。
type HostLinks interface {
	Ctl(orbID string) (*link.Ctl, bool)
	HasFeature(orbID, feature string) bool
	OpenStream(ctx context.Context, orbID, feature string, h link.StreamHeader) (*link.Stream, error)
	HandleStream(t string, fn orbs.StreamFunc)
}

// Syncer 把文件库同步进协调宿主，并从 orb 取回文件。
type Syncer struct {
	lib   *Service
	links HostLinks
	// mu 串行化同步（清单与推送之间不能插进另一次同步）。
	mu   sync.Mutex
	kick chan struct{}

	fmu     sync.Mutex
	fetches map[string]*fetchWait
}

// fetchWait 是一次在途的 library.fetch：只接受目标 orb 为它打开的流。
type fetchWait struct {
	orbID string
	ch    chan fetched
}

// fetched 是到达的字节流；读完后关闭 done，流处理器随即返回（流随之关闭）。
type fetched struct {
	st   *link.Stream
	done chan struct{}
}

// NewSyncer 创建同步器：向 links 注册 library 流的处理器，并让 lib 在文件变化时请求同步。须在 orb 接受 link 之前调用。
func NewSyncer(lib *Service, links HostLinks) *Syncer {
	y := &Syncer{lib: lib, links: links, kick: make(chan struct{}, 1), fetches: map[string]*fetchWait{}}
	links.HandleStream(link.StreamLibrary, y.onStream)
	lib.onChange = y.Trigger
	return y
}

// Trigger 请求一次后台同步（Run 执行）。
func (y *Syncer) Trigger() {
	select {
	case y.kick <- struct{}{}:
	default:
	}
}

// Run 在文件库变化后把它同步进协调宿主，直到 ctx 结束。宿主未连上时跳过：每次助手 agent 启动之前还会再同步。
func (y *Syncer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-y.kick:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(syncDebounce):
		}
		orb, err := y.lib.st.Q.GetCoordinatorOrb(ctx)
		if err != nil {
			continue
		}
		if err := y.Sync(ctx, orb.ID); err != nil && ctx.Err() == nil {
			slog.Warn("library: 同步文件库到协调宿主失败", "orb", orb.ID, "err", err)
		}
	}
}

// manifest 返回助手可读的全部文件：共享文件，以及属于某个对话的文件（含已上传、尚未随消息发送的）。
func (y *Syncer) manifest(ctx context.Context) ([]File, error) {
	all, err := y.lib.List(ctx, Filter{IncludeUnbound: true})
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, f := range all {
		if f.Scope == ScopeShared || f.ThreadID != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// Sync 把文件库同步进 orbID（协调宿主）：发 library.sync 清单，再逐个推送缺失的文件。
// 宿主没有已 hello 的 link 或不支持文件库（旧版 orbd）时什么也不做。
func (y *Syncer) Sync(ctx context.Context, orbID string) error {
	if orbID == "" || !y.links.HasFeature(orbID, orbs.FeatureLibrary) {
		return nil
	}
	ctl, ok := y.links.Ctl(orbID)
	if !ok {
		return nil
	}
	y.mu.Lock()
	defer y.mu.Unlock()
	files, err := y.manifest(ctx)
	if err != nil {
		return err
	}
	byPath := make(map[string]File, len(files))
	params := apigen.LinkCtlLibrarySyncParams{Files: make([]apigen.LinkCtlLibrarySyncFile, 0, len(files))}
	for _, f := range files {
		p := SandboxPath(f)
		byPath[p] = f
		params.Files = append(params.Files, apigen.LinkCtlLibrarySyncFile{Path: p, Size: f.Size, Sha256: f.Sha256})
	}
	var res apigen.LinkCtlLibrarySyncResult
	if err := ctl.Call(ctx, string(apigen.MethodLibrarySync), params, &res); err != nil {
		return fmt.Errorf("library.sync: %w", err)
	}
	var errs []error
	for _, p := range res.Missing {
		f, ok := byPath[p]
		if !ok {
			continue
		}
		if err := y.push(ctx, orbID, f); err != nil {
			if errors.Is(err, ErrNotFound) {
				// 清单之后被删除：下次同步会清理
				continue
			}
			errs = append(errs, fmt.Errorf("%s: %w", f.ID, err))
		}
	}
	return errors.Join(errs...)
}

// push 经一条 library 流把 f 的字节推给宿主，并等它的应答行。
func (y *Syncer) push(ctx context.Context, orbID string, f File) error {
	rc, _, err := y.lib.Open(ctx, f.ID)
	if err != nil {
		return err
	}
	defer rc.Close()
	pctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	st, err := y.links.OpenStream(pctx, orbID, orbs.FeatureLibrary, link.StreamHeader{
		T: link.StreamLibrary, Path: SandboxPath(f), Size: f.Size, SHA256: f.Sha256,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	stop := context.AfterFunc(pctx, func() { _ = st.Close() })
	defer stop()
	if _, err := io.CopyN(st, rc, f.Size); err != nil {
		return err
	}
	line, err := st.ReadLine(replyMax)
	if err != nil {
		return fmt.Errorf("read reply: %w", err)
	}
	var rep apigen.LinkLibraryReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return fmt.Errorf("invalid reply: %w", err)
	}
	if !rep.Ok {
		code, msg := "failed", ""
		if rep.Code != nil {
			code = *rep.Code
		}
		if rep.Message != nil {
			msg = *rep.Message
		}
		return &link.CtlError{Code: code, Message: msg}
	}
	return nil
}

// Fetch 让 orbID 经 library.fetch 回传 path 的字节，上限是文件库的单文件上限（attachments.MaxBytes），见 FetchLimit。
func (y *Syncer) Fetch(ctx context.Context, orbID, path string) (io.ReadCloser, int64, error) {
	return y.FetchLimit(ctx, orbID, path, attachments.MaxBytes)
}

// FetchLimit 让 orbID 经 library.fetch 回传 path 的字节；大于 max 字节时报 ErrTooLarge。返回的读取器恰好给出 size 字节
// （流提前结束时报 io.ErrUnexpectedEOF），调用方必须关闭。orbd 拒绝时返回其 *link.CtlError（invalid_params、not_found…）。
func (y *Syncer) FetchLimit(ctx context.Context, orbID, path string, max int64) (io.ReadCloser, int64, error) {
	ctl, ok := y.links.Ctl(orbID)
	if !ok {
		return nil, 0, ErrHostUnavailable
	}
	id := ids.New("lfx")
	w := &fetchWait{orbID: orbID, ch: make(chan fetched, 1)}
	y.fmu.Lock()
	y.fetches[id] = w
	y.fmu.Unlock()
	defer func() {
		// 流在这次调用放弃之后才到（或已到、但没交给调用方）：放行它的处理器。onStream 在 fmu 下交付，
		// 这里删掉登记之后不会再有新的交付。
		y.fmu.Lock()
		delete(y.fetches, id)
		y.fmu.Unlock()
		select {
		case f := <-w.ch:
			close(f.done)
		default:
		}
	}()
	var res apigen.LinkCtlLibraryFetchResult
	if err := ctl.Call(ctx, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: id, Path: path}, &res); err != nil {
		return nil, 0, err
	}
	timer := time.NewTimer(fetchStreamWait)
	defer timer.Stop()
	select {
	case f := <-w.ch:
		r := &fetchReader{f: f, r: io.LimitReader(f.st, res.Size), want: res.Size}
		if res.Size > max || f.st.Header.Size != res.Size {
			_ = r.Close()
			if res.Size > max {
				return nil, 0, ErrTooLarge
			}
			return nil, 0, fmt.Errorf("library: stream size %d does not match %d", f.st.Header.Size, res.Size)
		}
		return r, res.Size, nil
	case <-timer.C:
		return nil, 0, errors.New("library: the orb did not send the file")
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
}

// PutFrom 从 orbID 取回 path（Fetch）并以 p 存入文件库（p.R 被忽略）；字节不足时不留记录。
func (y *Syncer) PutFrom(ctx context.Context, orbID, path string, p PutParams) (File, error) {
	rc, size, err := y.Fetch(ctx, orbID, path)
	if err != nil {
		return File{}, err
	}
	defer rc.Close()
	p.R = rc
	f, err := y.lib.Put(ctx, p)
	if err != nil {
		return File{}, err
	}
	if f.Size != size {
		_ = y.lib.Delete(ctx, f.ID)
		return File{}, fmt.Errorf("library: received %d of %d bytes", f.Size, size)
	}
	return f, nil
}

// onStream 接收 orbd 打开的 library 流：只交给本服务向该 orb 发出、仍在等待的 library.fetch，其余直接关闭。
// 交出后等读取方读完（或放弃）才返回——返回时流被关闭。
func (y *Syncer) onStream(ctx context.Context, orbID string, h link.StreamHeader, st *link.Stream) {
	done := make(chan struct{})
	y.fmu.Lock()
	w := y.fetches[h.ID]
	if w != nil && w.orbID == orbID {
		delete(y.fetches, h.ID)
		w.ch <- fetched{st: st, done: done} // 容量 1、每个登记只交付一次：不阻塞
	} else {
		w = nil
	}
	y.fmu.Unlock()
	if w == nil {
		slog.Warn("library: 未请求的 library 流，关闭", "orb", orbID, "id", h.ID)
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// fetchReader 是取回的字节：读满 want 字节后 EOF，不足则 io.ErrUnexpectedEOF。
type fetchReader struct {
	f    fetched
	r    io.Reader
	want int64
	got  int64
	once sync.Once
}

func (r *fetchReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.got += int64(n)
	if errors.Is(err, io.EOF) && r.got < r.want {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *fetchReader) Close() error {
	r.once.Do(func() { close(r.f.done) })
	return nil
}
