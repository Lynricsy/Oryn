package artifacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/artifacts/render"
)

// 缩略图（步骤 4.2）：每个版本渲染一次（1280×800 的窗口、倍率 0.5625，输出 720×450），重编码成 JPEG 存在
// <data_dir>/artifacts/thumbs/<files_key>.jpg——同内容的版本（含不同 artifact 之间）共用一张。
const (
	thumbWidth   = 1280
	thumbHeight  = 800
	thumbScale   = 0.5625
	thumbQuality = 82
	// thumbDelay：新版本之后等这么久再入队（连续写入只渲染最后一版）。
	thumbDelay = 2 * time.Second
	// thumbRetry：渲染失败的内容这么久之内不再重试（坏页面每次都会卡满超时）。
	thumbRetry = time.Hour
	// MaxThumbs 是磁盘上最多保留的缩略图张数，超出按修改时间淘汰最旧的。
	MaxThumbs = 2000
	// CodeNoThumbnail：请求的版本及更早的版本都还没有缩略图（404）。
	CodeNoThumbnail = "no_thumbnail"
)

// Renderer 是控制面渲染器（*render.Renderer 实现）。
type Renderer interface {
	Run(ctx context.Context, j render.Job) ([]byte, error)
	Status() (ok bool, reason string)
}

// thumbs 是缩略图的队列与在途合并；零值可用（map 惰性创建）。
type thumbs struct {
	mu sync.Mutex
	r  Renderer
	// after 安排一次延迟调用并返回取消函数；nil = time.AfterFunc。测试注入假时钟。
	after func(d time.Duration, f func()) (stop func() bool)
	// timers：每个 artifact 至多一个等待中的延迟入队。
	timers map[string]*thumbTimer
	// queue / want：待渲染的 artifact（按入队顺序）与各自要渲染的版本（同一 artifact 只保留最新）。
	queue []string
	want  map[string]int
	wake  chan struct{}
	// calls：按 files_key 合并的在途渲染。
	calls map[string]*thumbCall
	// failed：渲染失败的 files_key → 失败时间。
	failed map[string]time.Time
	// joined 只给测试用：有调用方并入在途渲染时调用。
	joined func()
}

type thumbTimer struct{ stop func() bool }

type thumbCall struct {
	done chan struct{}
	err  error
}

// UseRenderer 接上控制面渲染器：开启缩略图（新版本后延迟入队，后台一个 worker 逐个渲染，直到 ctx 结束）、
// render.png 与 screenshot_artifact。只调用一次，在开始服务请求之前。
func (s *Service) UseRenderer(ctx context.Context, r Renderer) {
	th := &s.thumbs
	th.mu.Lock()
	th.r = r
	th.mu.Unlock()
	s.OnVersion(s.scheduleThumb)
	go s.thumbWorker(ctx)
}

// renderer 返回渲染器；没接上时为 nil。
func (s *Service) renderer() Renderer {
	s.thumbs.mu.Lock()
	defer s.thumbs.mu.Unlock()
	return s.thumbs.r
}

// renderReady 报告渲染当前是否可用。
func (s *Service) renderReady() bool {
	r := s.renderer()
	if r == nil {
		return false
	}
	ok, _ := r.Status()
	return ok
}

func (s *Service) thumbDir() string { return filepath.Join(s.cfg.DataDir, "artifacts", "thumbs") }

func (s *Service) thumbPath(filesKey string) string {
	return filepath.Join(s.thumbDir(), filesKey+".jpg")
}

// scheduleThumb 是新版本监听者：thumbDelay 之后把这个版本入队；同一 artifact 等待中的上一次被取消。
func (s *Service) scheduleThumb(id string, n int) {
	th := &s.thumbs
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.timers == nil {
		th.timers = map[string]*thumbTimer{}
	}
	if old := th.timers[id]; old != nil {
		old.stop()
	}
	t := &thumbTimer{}
	after := th.after
	if after == nil {
		after = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	t.stop = after(thumbDelay, func() {
		th.mu.Lock()
		if th.timers[id] == t {
			delete(th.timers, id)
		}
		th.mu.Unlock()
		s.enqueueThumb(id, n)
	})
	th.timers[id] = t
}

// enqueueThumb 把 artifact 的版本 n 放进渲染队列（已在队列里时只把版本推到较新的那个）。
func (s *Service) enqueueThumb(id string, n int) {
	th := &s.thumbs
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.want == nil {
		th.want = map[string]int{}
		th.wake = make(chan struct{}, 1)
	}
	if cur, ok := th.want[id]; ok {
		th.want[id] = max(cur, n)
		return
	}
	th.want[id] = n
	th.queue = append(th.queue, id)
	select {
	case th.wake <- struct{}{}:
	default:
	}
}

// nextThumb 取出队首；队列空时 ok 为假，wake 是有新条目时会收到信号的通道。
func (s *Service) nextThumb() (id string, n int, wake <-chan struct{}, ok bool) {
	th := &s.thumbs
	th.mu.Lock()
	defer th.mu.Unlock()
	if th.wake == nil {
		th.want = map[string]int{}
		th.wake = make(chan struct{}, 1)
	}
	if len(th.queue) == 0 {
		return "", 0, th.wake, false
	}
	id = th.queue[0]
	th.queue = th.queue[1:]
	n = th.want[id]
	delete(th.want, id)
	return id, n, th.wake, true
}

// thumbWorker 逐个渲染队列里的缩略图（只占渲染器的一个并发位，用户的 PNG 下载与 agent 截图不必排在它后面）。
func (s *Service) thumbWorker(ctx context.Context) {
	for {
		id, n, wake, ok := s.nextThumb()
		if !ok {
			select {
			case <-wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		if !s.renderReady() {
			continue
		}
		if err := s.ensureThumb(ctx, id, n); err != nil && ctx.Err() == nil {
			slog.Debug("artifacts: 缩略图渲染失败", "artifact", id, "version", n, "err", err)
		}
	}
}

// thumbJob 是某个版本的缩略图渲染作业：web 渲染入口页；document / slides 渲染查看器页、canvas 渲染概览页，
// 都带 view=thumbnail（这三类的根由 site.go 提供，渲染出什么由它们自己的查看器决定）。
func thumbJob(a Artifact, version int) render.Job {
	j := render.Job{ArtifactID: a.ID, Version: version, Width: thumbWidth, Height: thumbHeight, Scale: thumbScale}
	if a.Kind != KindWeb {
		j.Query = "view=thumbnail"
	}
	return j
}

// ensureThumb 确保某个版本有缩略图：已有直接返回；同一 files_key 的并发请求合并成一次渲染。
// 回收站里的 artifact 不渲染。完成后通知列表重取。
func (s *Service) ensureThumb(ctx context.Context, id string, version int) error {
	a, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if a.Deleted() {
		return nil
	}
	v, err := s.Version(ctx, id, version)
	if err != nil {
		return err
	}
	key := v.FilesKey
	if _, err := os.Stat(s.thumbPath(key)); err == nil {
		return nil
	}
	th := &s.thumbs
	th.mu.Lock()
	if c := th.calls[key]; c != nil {
		joined := th.joined
		th.mu.Unlock()
		if joined != nil {
			joined()
		}
		select {
		case <-c.done:
			return c.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if at, ok := th.failed[key]; ok && s.now().Sub(at) < thumbRetry {
		th.mu.Unlock()
		return nil
	}
	if th.calls == nil {
		th.calls = map[string]*thumbCall{}
	}
	c := &thumbCall{done: make(chan struct{})}
	th.calls[key] = c
	r := th.r
	th.mu.Unlock()

	c.err = s.renderThumb(ctx, r, thumbJob(a, v.Version), key)
	th.mu.Lock()
	delete(th.calls, key)
	switch {
	case c.err == nil:
		delete(th.failed, key)
	case !errors.Is(c.err, render.ErrUnavailable) && ctx.Err() == nil:
		if th.failed == nil {
			th.failed = map[string]time.Time{}
		}
		th.failed[key] = s.now()
	}
	th.mu.Unlock()
	close(c.done)
	if c.err == nil {
		s.changed()
	}
	return c.err
}

// renderThumb 渲染、把 PNG 重编码成 JPEG 并原子地落盘，然后淘汰超出上限的旧图。
func (s *Service) renderThumb(ctx context.Context, r Renderer, j render.Job, key string) error {
	if r == nil {
		return fmt.Errorf("%w：控制面没有配置渲染器", render.ErrUnavailable)
	}
	out, err := r.Run(ctx, j)
	if err != nil {
		return err
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("artifacts: 解码缩略图 PNG：%w", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return fmt.Errorf("artifacts: 编码缩略图 JPEG：%w", err)
	}
	dir := s.thumbDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.thumbPath(key)); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	evictThumbs(dir, MaxThumbs)
	return nil
}

// evictThumbs 在缩略图超过 limit 张时按修改时间删掉最旧的。
func evictThumbs(dir string, limit int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type thumb struct {
		name string
		mod  time.Time
	}
	list := make([]thumb, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jpg") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, thumb{e.Name(), info.ModTime()})
	}
	if len(list) <= limit {
		return
	}
	slices.SortFunc(list, func(a, b thumb) int { return a.mod.Compare(b.mod) })
	for _, t := range list[:len(list)-limit] {
		_ = os.Remove(filepath.Join(dir, t.name))
	}
}

// Thumbnail 返回某个版本（0 = 最新）的缩略图文件路径与它实际属于的版本：请求的版本还没有时回最近一个有缩略图的
// 更早版本（卡片宁可稍旧也不要空），都没有回 no_thumbnail。请求的版本没有缩略图且渲染可用时把它补入队。
func (s *Service) Thumbnail(ctx context.Context, id string, version int) (path string, served int, err error) {
	a, err := s.Get(ctx, id)
	if err != nil {
		return "", 0, err
	}
	want := version
	if want == 0 {
		want = a.Version
	}
	if want > a.Version {
		return "", 0, opErr(CodeNotFound, "artifact %s 没有版本 %d（最新是 %d）。", id, want, a.Version)
	}
	vs, err := s.Versions(ctx, id)
	if err != nil {
		return "", 0, err
	}
	for _, v := range vs { // 新的在前
		if v.Version > want {
			continue
		}
		p := s.thumbPath(v.FilesKey)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if v.Version != want {
			s.missedThumb(a, want)
		}
		return p, v.Version, nil
	}
	s.missedThumb(a, want)
	return "", 0, opErr(CodeNoThumbnail, "artifact %s 的版本 %d 还没有缩略图。", id, want)
}

// missedThumb 在接口未命中时补入队（渲染不可用或在回收站里时不入队）。
func (s *Service) missedThumb(a Artifact, version int) {
	if !a.Deleted() && s.renderReady() {
		s.enqueueThumb(a.ID, version)
	}
}

// RenderPNG 以倍率 1 把某个版本（0 = 最新）的一个页面渲染成 PNG。path 为空时渲染根（入口；类型化的是查看器页），
// 否则必须是该版本里的文件。渲染不可用回 render_unavailable。返回渲染所用的版本。
func (s *Service) RenderPNG(ctx context.Context, id string, version int, path string, width, height int) ([]byte, Version, error) {
	v, err := s.Version(ctx, id, version)
	if err != nil {
		return nil, Version{}, err
	}
	if path != "" {
		files, err := s.Files(ctx, id, v.Version)
		if err != nil {
			return nil, Version{}, err
		}
		if !slices.ContainsFunc(files, func(f File) bool { return f.Path == path }) {
			return nil, Version{}, opErr(CodeInvalid, "版本 %d 里没有文件 %s。path 取该版本文件清单（read_artifact 返回的 files）里的路径；不给表示入口。", v.Version, path)
		}
	}
	r := s.renderer()
	if r == nil {
		return nil, Version{}, opErr(CodeRenderUnavailable, "控制面没有配置渲染器，不能渲染页面。")
	}
	out, err := r.Run(ctx, render.Job{ArtifactID: id, Version: v.Version, Path: path, Width: width, Height: height, Scale: 1})
	if errors.Is(err, render.ErrUnavailable) {
		return nil, Version{}, opErr(CodeRenderUnavailable, "控制面现在不能渲染页面（%s）。这和页面内容无关，需要运维按原因修好渲染后才能用。", err)
	}
	if err != nil {
		return nil, Version{}, fmt.Errorf("渲染 artifact %s 版本 %d 失败：%w", id, v.Version, err)
	}
	return out, v, nil
}

type screenshotArgs struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// agentScreenshot 是 screenshot_artifact（步骤 4.4）：把最新版本的一个页面渲染成 PNG 给 agent 自检。
func (s *Service) agentScreenshot(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p screenshotArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Width == 0 {
		p.Width = 1280
	}
	if p.Height == 0 {
		p.Height = 800
	}
	if p.Width < 64 || p.Width > 1600 || p.Height < 64 || p.Height > 4000 {
		return Result{}, opErr(CodeInvalid, "窗口尺寸 %d×%d 超出范围：width 取 64–1600，height 取 64–4000（缺省 1280×800）。", p.Width, p.Height)
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	out, v, err := s.RenderPNG(ctx, a.ID, a.Version, p.Path, p.Width, p.Height)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeRenderUnavailable {
		return Result{}, opErr(CodeRenderUnavailable, "%s 不要重试 screenshot_artifact；改用 read_artifact 检查页面的源码。", oe.Message)
	}
	if err != nil {
		return Result{}, err
	}
	shown := p.Path
	if shown == "" {
		shown = a.Entry()
	}
	return Result{
		Text:    fmt.Sprintf("%s v%d 的 %s，%d×%d", a.ID, v.Version, shown, p.Width, p.Height),
		Details: obj{"op": OpScreenshot, "id": a.ID, "title": a.Title, "kind": a.Kind, "version": v.Version, "path": shown, "width": p.Width, "height": p.Height},
		Image:   &Image{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(out)},
	}, nil
}
