package repos

// 托管仓库的 smart-HTTP（M24 阶段 ③）：直接执行 git upload-pack / receive-pack --stateless-rpc，不经 git http-backend
// （它在 libexec 里，部分发行版单独打包；CGI 还会继承 orynd 的环境），沿用阶段 ② 的受控 argv 与环境。

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Endpoint 是 smart-HTTP 的端点（相对 <仓库>.git/ 的路径）。
type Endpoint string

const (
	EndpointInfoRefs    Endpoint = "info/refs"
	EndpointUploadPack  Endpoint = "git-upload-pack"
	EndpointReceivePack Endpoint = "git-receive-pack"
)

const (
	smartGlobalSlots = 8                 // 托管 git 进程的全局名额
	smartOrbSlots    = 2                 // 每台 orb 的名额
	stdinIdle        = 120 * time.Second // 请求体多久没有新数据就关闭 git 的 stdin
	stdinStopWait    = 5 * time.Second   // 收尾时等读请求体的协程退出的上限
	// hiddenRefs 是对客户端不可见、不可改的 ref 前缀（被删或被强推覆盖的旧值留存在 refs/oryn/saved/，步骤 3.5）。
	hiddenRefs = "refs/oryn"
)

var gitProtocolRe = regexp.MustCompile(`^[A-Za-z0-9=:._-]{1,256}$`)

var errStdinIdle = errors.New("请求体超过 120 秒没有新数据")

// IsPush 报告请求是否属于推送：info/refs?service=git-receive-pack 或 git-receive-pack 端点（方法不对的也算，
// 免得归档等只读检查被绕过）。
func IsPush(r *http.Request, ep Endpoint) bool {
	switch ep {
	case EndpointReceivePack:
		return true
	case EndpointInfoRefs:
		return r.URL.Query().Get("service") == string(EndpointReceivePack)
	}
	return false
}

// serveSmartHTTP 在裸仓库 dir 上服务一个已鉴权、已授权的 smart-HTTP 请求。orbID 非空 = 请求来自该 orb（经出站代理）：
// 推送的对象先过 fsck，并占用该 orb 的名额；为空 = 主人本机。
func (s *Service) serveSmartHTTP(w http.ResponseWriter, r *http.Request, dir string, ep Endpoint, orbID string) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	h.Set("Pragma", "no-cache")
	h.Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")

	var svc string
	switch ep {
	case EndpointInfoRefs:
		if r.Method != http.MethodGet {
			h.Set("Allow", http.MethodGet)
			smartError(w, http.StatusMethodNotAllowed, "oryn: 只接受 GET")
			return
		}
		svc = r.URL.Query().Get("service")
		if svc != string(EndpointUploadPack) && svc != string(EndpointReceivePack) {
			smartError(w, http.StatusForbidden, "oryn: 只支持 smart HTTP")
			return
		}
	case EndpointUploadPack, EndpointReceivePack:
		if r.Method != http.MethodPost {
			h.Set("Allow", http.MethodPost)
			smartError(w, http.StatusMethodNotAllowed, "oryn: 只接受 POST")
			return
		}
		svc = string(ep)
		if r.Header.Get("Content-Type") != "application/x-"+svc+"-request" {
			smartError(w, http.StatusUnsupportedMediaType, "oryn: 请求类型必须是 application/x-"+svc+"-request")
			return
		}
	default:
		smartError(w, http.StatusNotFound, "oryn: 未知的 git 端点")
		return
	}
	push := svc == string(EndpointReceivePack)

	// 协议 v2 只对 upload-pack（与 http-backend 一致）；receive-pack 忽略这个头
	var env []string
	v2 := false
	if !push {
		if gp := r.Header.Get("Git-Protocol"); gp != "" {
			if !gitProtocolRe.MatchString(gp) {
				smartError(w, http.StatusBadRequest, "oryn: Git-Protocol 头无效")
				return
			}
			env = append(env, "GIT_PROTOCOL="+gp)
			v2 = slices.Contains(strings.Split(gp, ":"), "version=2")
		}
	}

	var body io.Reader
	if ep != EndpointInfoRefs {
		var status int
		var msg string
		if body, status, msg = requestBody(r); status != 0 {
			smartError(w, status, msg)
			return
		}
	}

	if push {
		if free, err := freeBytes(s.cfg.Dir); err == nil && free < s.cfg.MinFreeBytes {
			smartError(w, http.StatusServiceUnavailable, "oryn: 控制面磁盘空间不足")
			return
		}
	}
	release, ok := s.smart.acquire(orbID)
	if !ok {
		h.Set("Retry-After", "5")
		smartError(w, http.StatusServiceUnavailable, "oryn: 仓库正忙，请稍后重试")
		return
	}
	defer release()

	args := []string{"-c", "transfer.hideRefs=" + hiddenRefs}
	if push && orbID != "" {
		// agent 推上来的对象先过 fsck：带 .git 路径项或恶意 .gitmodules 的树进不来
		args = append(args, "-c", "receive.fsckObjects=true")
	}
	args = append(args, strings.TrimPrefix(svc, "git-"), "--stateless-rpc")

	rc := http.NewResponseController(w)
	sw := &smartWriter{w: w, rc: rc}
	o := gitRun{dir: dir, env: env}
	ctx := r.Context()
	if ep == EndpointInfoRefs {
		h.Set("Content-Type", "application/x-"+svc+"-advertisement")
		if !v2 || push {
			sw.prefix = append(pktLine("# service="+svc+"\n"), "0000"...)
		}
		args = append(args, "--advertise-refs")
	} else {
		h.Set("Content-Type", "application/x-"+svc+"-result")
		_ = rc.EnableFullDuplex() // 边读请求体边写响应（receive-pack 的 side-band 进度）
		o.stdin = startStdinPump(body, rc, stdinIdle)
		if push {
			// 客户端断开不杀 receive-pack：stdin 读到 EOF 后它自己退出并清理锁文件，被强杀会在正本里留下 *.lock
			ctx = context.WithoutCancel(ctx)
		}
	}
	args = append(args, dir)

	if push {
		var cancel context.CancelCauseFunc
		ctx, cancel = context.WithCancelCause(ctx)
		defer cancel(nil)
		stop := s.watchFree(ctx, cancel)
		defer stop()
	}
	tail, err := s.gitPipe(ctx, o, sw, args...)
	if err == nil {
		_ = sw.start() // 没有任何输出也要回 200（以及 info/refs 的 service 行）
		return
	}
	noSpace := errors.Is(context.Cause(ctx), errSyncNoSpace)
	if !sw.wrote {
		switch {
		case noSpace:
			smartError(w, http.StatusServiceUnavailable, "oryn: 控制面磁盘空间不足")
		case tail == "":
			smartError(w, http.StatusInternalServerError, "oryn: git 启动失败")
		default:
			smartError(w, http.StatusInternalServerError, "oryn: git 执行失败")
		}
	}
	slog.Warn("托管仓库 smart-HTTP", "endpoint", ep, "service", svc, "dir", dir, "orb", orbID,
		"noSpace", noSpace, "err", err, "stderr", strings.TrimSpace(tail))
}

// smartError 写 text/plain 错误：git 会把正文显示给用户（remote-curl.c 的 show_http_message）。
func smartError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}

// requestBody 按 Content-Encoding 解出请求体；status 非 0 时是应当回给客户端的错误。
func requestBody(r *http.Request) (body io.Reader, status int, msg string) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return r.Body, 0, ""
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, http.StatusBadRequest, "oryn: gzip 请求体无效"
		}
		return zr, 0, ""
	}
	return nil, http.StatusUnsupportedMediaType, "oryn: 不支持的 Content-Encoding"
}

// pktLine 把 s 编成一个 pkt-line（4 位十六进制长度含自身）。
func pktLine(s string) []byte {
	n := strconv.FormatInt(int64(len(s)+4), 16)
	return []byte(strings.Repeat("0", 4-len(n)) + n + s)
}

// watchFree 每 monitorEvery 复查数据盘可用空间，低于 MinFreeBytes 时以 errSyncNoSpace 取消 ctx（终止 git）。
func (s *Service) watchFree(ctx context.Context, cancel context.CancelCauseFunc) (stop func()) {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(s.monitorEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if free, err := freeBytes(s.cfg.Dir); err == nil && free < s.cfg.MinFreeBytes {
				cancel(errSyncNoSpace)
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// smartWriter 把 git 的 stdout 写进响应：第一次写时才发 200 头（与 info/refs 的 service 行），
// 这样 git 没有任何输出就失败时还能回错误状态码。
type smartWriter struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	prefix []byte
	wrote  bool
}

func (s *smartWriter) start() error {
	if s.wrote {
		return nil
	}
	s.wrote = true
	s.w.WriteHeader(http.StatusOK)
	if len(s.prefix) == 0 {
		return nil
	}
	_, err := s.w.Write(s.prefix)
	return err
}

func (s *smartWriter) Write(b []byte) (int, error) {
	if err := s.start(); err != nil {
		return 0, err
	}
	return s.w.Write(b)
}

func (s *smartWriter) Flush() error {
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	return nil
}

// smartSlots 是托管 git 进程的名额：全局 smartGlobalSlots 个，每台 orb smartOrbSlots 个；零值可用。
type smartSlots struct {
	mu    sync.Mutex
	total int
	byOrb map[string]int
}

// acquire 占一个名额；没有名额时 ok=false（不排队）。orbID 为空（主人本机）只占全局名额。
func (s *smartSlots) acquire(orbID string) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total >= smartGlobalSlots || orbID != "" && s.byOrb[orbID] >= smartOrbSlots {
		return nil, false
	}
	s.total++
	if orbID != "" {
		if s.byOrb == nil {
			s.byOrb = map[string]int{}
		}
		s.byOrb[orbID]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.total--
			if orbID == "" {
				return
			}
			if s.byOrb[orbID]--; s.byOrb[orbID] <= 0 {
				delete(s.byOrb, orbID)
			}
		})
	}, true
}

// stdinPump 经管道把请求体喂给 git：请求体超过 idle 没有新数据就关闭管道（git 读到 EOF 自行退出）；
// 连接支持读期限时同时让阻塞的读返回。git 看到的任何读错误都是 EOF（截断的输入由 git 自己报错）。
type stdinPump struct {
	pr         *io.PipeReader
	rc         *http.ResponseController
	stopped    atomic.Bool
	deadlineOK atomic.Bool
	done       chan struct{}
}

func startStdinPump(src io.Reader, rc *http.ResponseController, idle time.Duration) *stdinPump {
	pr, pw := io.Pipe()
	p := &stdinPump{pr: pr, rc: rc, done: make(chan struct{})}
	go p.run(src, pw, idle)
	return p
}

func (p *stdinPump) run(src io.Reader, pw *io.PipeWriter, idle time.Duration) {
	defer close(p.done)
	buf := make([]byte, 32<<10)
	for {
		// 计时只覆盖读请求体：写管道阻塞是 git 慢，不是客户端空闲
		t := time.AfterFunc(idle, func() { _ = pw.CloseWithError(errStdinIdle) })
		p.setDeadline(time.Now().Add(idle))
		if p.stopped.Load() {
			t.Stop()
			return
		}
		n, err := src.Read(buf)
		t.Stop()
		if n > 0 {
			if _, werr := pw.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
	}
}

func (p *stdinPump) setDeadline(t time.Time) {
	if p.rc != nil && p.rc.SetReadDeadline(t) == nil {
		p.deadlineOK.Store(true)
	}
}

func (p *stdinPump) Read(b []byte) (int, error) {
	n, err := p.pr.Read(b)
	if err != nil {
		err = io.EOF
	}
	return n, err
}

// Close 在 git 结束后调用：关闭管道；请求体还没读完时让阻塞的读返回，并等读协程退出（至多 stdinStopWait）。
func (p *stdinPump) Close() error {
	p.stopped.Store(true)
	_ = p.pr.Close()
	select {
	case <-p.done:
		return nil
	default:
	}
	if p.deadlineOK.Load() {
		p.setDeadline(time.Now())
		select {
		case <-p.done:
		case <-time.After(stdinStopWait):
		}
	}
	return nil
}
