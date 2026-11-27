package repos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/Lynricsy/Oryn/internal/lfs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 镜像项目的 Git LFS 客户端（M24 阶段 ② 补充）：读接口遇到合规指针时按需向仓库主机自己的 LFS 接口取对象。
// 凭据与镜像拉取相同（remote()），只发给仓库主机；不读仓库里的 .lfsconfig。传输按主机选：仓库主机是担保主机时
// 直连（沿用进程的代理变量），其余一律经 egress.PublicProxy 守卫，上游给出的内网 href 因此被拒。

const (
	lfsInlineMax      = 100 << 20          // 超过它的对象不内联预览、不进缓存，只能 download=1 流式下载
	lfsBatchTimeout   = 30 * time.Second   // 一次 batch 请求的总时限
	lfsLowSpeedBytes  = 60_000             // lfsLowSpeedWindow 内少于这么多字节即中止（同 GIT_HTTP_LOW_SPEED_*）
	lfsLowSpeedWindow = 60 * time.Second   // 低速检查的窗口
	lfsConcurrency    = 4                  // 同时向上游取对象的个数
	lfsPerProject     = lfsConcurrency / 2 // 同一项目同时占用的名额上限
	lfsRedirectMax    = 5                  // 下载跟随重定向的最大跳数
	lfsOpenRetries    = 3                  // 飞行完成后打开缓存文件的尝试次数（对象可能刚被别的飞行淘汰）
	lfsUserAgent      = "oryn-repos"
)

// lfsEmptyOID 是空内容的 sha256。
const lfsEmptyOID = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var (
	errLFSMissing     = &Error{Status: http.StatusNotFound, Code: "lfs_missing", Message: "找不到这个 LFS 对象"}
	errLFSAuth        = &Error{Status: http.StatusConflict, Code: "lfs_auth_failed", Message: "上游拒绝了读取 LFS 对象的凭据"}
	errLFSUnreachable = &Error{Status: http.StatusBadGateway, Code: "lfs_unreachable", Message: "连不上存放 LFS 对象的主机"}
	errLFSCorrupt     = &Error{Status: http.StatusBadGateway, Code: "lfs_corrupt", Message: "上游返回的内容与 LFS 指针不符"}
	errLFSNoSpace     = &Error{Status: http.StatusInsufficientStorage, Code: "lfs_no_space", Message: "控制面磁盘空间不足，暂时不能读取 LFS 对象"}

	errLFSSlow          = errors.New("lfs: download too slow")
	errLFSFlightTimeout = errors.New("lfs: flight timeout")
)

// lfsErr 复制 base，Message 追加「：<detail>」；detail 先把 secrets 脱敏。
func lfsErr(base *Error, detail string, secrets ...string) *Error {
	e := *base
	if detail = strings.TrimSpace(redactSecrets(detail, secrets...)); detail != "" {
		e.Message += "：" + tailRunes(detail, errMessageMax)
	}
	return &e
}

// lfsClient 是 Service 的 LFS 部分：两个传输与取对象的单飞、名额。
type lfsClient struct {
	vouched *http.Transport // 担保主机：直连，代理按 Config.ProxyEnv
	guarded *http.Transport // 其余：一律经公网守卫（CONNECT 443，只连公网地址）

	sem chan struct{} // 全局名额，容量 lfsConcurrency

	mu      sync.Mutex
	flights map[string]*lfsFlight    // projectID + "/" + oid
	proj    map[string]chan struct{} // 每个项目的名额，容量 lfsPerProject
}

// lfsFlight 是一次进行中的取对象；done 关闭后 err 可读。
type lfsFlight struct {
	done chan struct{}
	err  error
}

func newLFSClient(proxyEnv []string, guardURL string) (*lfsClient, error) {
	gu, err := url.Parse(guardURL)
	if err != nil {
		return nil, fmt.Errorf("守卫代理地址: %w", err)
	}
	pf := proxyConfig(proxyEnv).ProxyFunc()
	vouched := lfsTransport(func(r *http.Request) (*url.URL, error) { return pf(r.URL) })
	guarded := lfsTransport(http.ProxyURL(gu))
	return &lfsClient{
		vouched: vouched, guarded: guarded,
		sem:     make(chan struct{}, lfsConcurrency),
		flights: map[string]*lfsFlight{}, proj: map[string]chan struct{}{},
	}, nil
}

func lfsTransport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// proxyConfig 按 KEY=VALUE 形式的代理变量构造 httpproxy.Config（大写优先，同 http.ProxyFromEnvironment）。
func proxyConfig(env []string) *httpproxy.Config {
	get := func(keys ...string) string {
		for _, k := range keys {
			for _, kv := range env {
				if v, ok := strings.CutPrefix(kv, k+"="); ok && v != "" {
					return v
				}
			}
		}
		return ""
	}
	return &httpproxy.Config{
		HTTPSProxy: get("HTTPS_PROXY", "https_proxy"),
		HTTPProxy:  get("HTTP_PROXY", "http_proxy"),
		NoProxy:    get("NO_PROXY", "no_proxy"),
	}
}

func (c *lfsClient) close() {
	c.vouched.CloseIdleConnections()
	c.guarded.CloseIdleConnections()
}

// projSem 返回项目的名额信号量（按需创建）。
func (c *lfsClient) projSem(projectID string) chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.proj[projectID]
	if ch == nil {
		ch = make(chan struct{}, lfsPerProject)
		c.proj[projectID] = ch
	}
	return ch
}

// forget 删掉项目的名额信号量（项目删除时）；进行中的飞行仍释放到旧通道。
func (c *lfsClient) forget(projectID string) {
	c.mu.Lock()
	delete(c.proj, projectID)
	c.mu.Unlock()
}

// lfsRouter 逐跳选择传输：与 endpoint 同主机时担保主机直连、未担保主机经守卫；其它主机一律经守卫。
type lfsRouter struct {
	c        *lfsClient
	endpoint *url.URL
	vouched  bool
}

func (r lfsRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.vouched && sameHost(req.URL, r.endpoint) {
		return r.c.vouched.RoundTrip(req)
	}
	return r.c.guarded.RoundTrip(req)
}

// sameHost 比较主机（不区分大小写）与端口（缺省端口按 scheme 补齐）。
func sameHost(a, b *url.URL) bool {
	return strings.EqualFold(a.Hostname(), b.Hostname()) && urlPort(a) == urlPort(b)
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && sameHost(a, b)
}

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// lfsEndpoint 是拉取地址对应的 LFS 接口：去掉末尾 /，不以 .git 结尾时补上，再加 /info/lfs（server-discovery.md）。
func lfsEndpoint(fetchURL string) (*url.URL, error) {
	ep := strings.TrimRight(fetchURL, "/")
	if !strings.HasSuffix(ep, ".git") {
		ep += ".git"
	}
	u, err := url.Parse(ep + "/info/lfs")
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, lfsErr(errLFSUnreachable, "仓库地址无效")
	}
	return u, nil
}

// lfsDownloadAction 向 <endpoint>/objects/batch 请求一个对象的下载动作。
func (s *Service) lfsDownloadAction(ctx context.Context, rm remote, p lfs.Pointer) (lfs.Action, error) {
	ep, err := lfsEndpoint(rm.url)
	if err != nil {
		return lfs.Action{}, err
	}
	body, _ := json.Marshal(lfs.BatchRequest{
		Operation: "download", Transfers: []string{"basic"},
		Objects: []lfs.ObjectSpec{{OID: p.OID, Size: p.Size}}, HashAlgo: "sha256",
	})
	ctx, cancel := context.WithTimeout(ctx, lfsBatchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.String()+"/objects/batch", bytes.NewReader(body))
	if err != nil {
		return lfs.Action{}, lfsErr(errLFSUnreachable, err.Error(), rm.token)
	}
	req.Header.Set("Accept", lfs.MediaType)
	req.Header.Set("Content-Type", lfs.MediaType)
	req.Header.Set("User-Agent", lfsUserAgent)
	if rm.token != "" {
		req.Header.Set("Authorization", "Basic "+basicAuth(rm.token))
	}
	client := &http.Client{
		Transport:     lfsRouter{c: s.lfsc, endpoint: ep, vouched: rm.vouched},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return lfs.Action{}, lfsErr(errLFSUnreachable, transportDetail(err), rm.token)
	}
	defer resp.Body.Close()
	switch st := resp.StatusCode; {
	case st == http.StatusOK:
	case st == http.StatusUnauthorized || st == http.StatusForbidden:
		return lfs.Action{}, lfsErr(errLFSAuth, "HTTP "+strconv.Itoa(st))
	case st == http.StatusNotFound:
		return lfs.Action{}, lfsErr(errLFSMissing, "上游没有 LFS 接口或看不到这个仓库")
	case st >= 300 && st < 400 || st == http.StatusTooManyRequests || st >= 500:
		return lfs.Action{}, lfsErr(errLFSUnreachable, "HTTP "+strconv.Itoa(st))
	default:
		return lfs.Action{}, lfsErr(errLFSCorrupt, "批量接口回 HTTP "+strconv.Itoa(st))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, lfs.BatchBodyMax+1))
	if err != nil {
		return lfs.Action{}, lfsErr(errLFSUnreachable, err.Error(), rm.token)
	}
	if len(raw) > lfs.BatchBodyMax {
		return lfs.Action{}, lfsErr(errLFSCorrupt, "批量响应过大")
	}
	var br lfs.BatchResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return lfs.Action{}, lfsErr(errLFSCorrupt, "批量响应不是合法 JSON")
	}
	if br.Transfer != "" && br.Transfer != "basic" {
		return lfs.Action{}, lfsErr(errLFSCorrupt, "不支持的传输方式 "+strconv.Quote(br.Transfer))
	}
	for _, o := range br.Objects {
		if o.OID != p.OID {
			continue
		}
		if o.Error != nil {
			switch o.Error.Code {
			case http.StatusNotFound, http.StatusGone:
				return lfs.Action{}, errLFSMissing
			case http.StatusUnauthorized, http.StatusForbidden:
				return lfs.Action{}, errLFSAuth
			default:
				return lfs.Action{}, lfsErr(errLFSCorrupt, fmt.Sprintf("对象错误 %d %s", o.Error.Code, o.Error.Message), rm.token)
			}
		}
		if a := o.Actions["download"]; a != nil && a.Href != "" {
			return *a, nil
		}
		return lfs.Action{}, lfsErr(errLFSCorrupt, "响应里没有下载动作")
	}
	return lfs.Action{}, lfsErr(errLFSCorrupt, "响应里没有这个对象")
}

// actionHeader 是下载请求的头：只放 action.header（Oryn 不另加凭据）。href 与 endpoint 不同主机时，值里带令牌
// （原文、base64 或 Basic 串，或 Basic 解码后含令牌）的头一律丢掉：回显型上游不能把令牌指向别的主机。
func actionHeader(href, ep *url.URL, h map[string]string, tok string) http.Header {
	out := http.Header{}
	same := sameHost(href, ep)
	for k, v := range h {
		if !same && leaksToken(v, tok) {
			continue
		}
		out.Set(k, v)
	}
	return out
}

func leaksToken(v, tok string) bool {
	if tok == "" {
		return false
	}
	if redactSecrets(v, tok) != v {
		return true
	}
	if scheme, cred, ok := strings.Cut(strings.TrimSpace(v), " "); ok && strings.EqualFold(scheme, "basic") {
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cred)); err == nil && strings.Contains(string(dec), tok) {
			return true
		}
	}
	return false
}

// lfsGet 按下载动作取对象：返回的读取器边读边校验大小与 sha256，到 EOF 时不符即报 lfs_corrupt；
// 低速（每 lfsLowSpeedWindow 少于 lfsLowSpeedBytes）时中止。调用方关闭。
func (s *Service) lfsGet(ctx context.Context, rm remote, p lfs.Pointer, a lfs.Action) (io.ReadCloser, error) {
	ep, err := lfsEndpoint(rm.url)
	if err != nil {
		return nil, err
	}
	href, err := url.Parse(a.Href)
	if err != nil || !href.IsAbs() || href.Host == "" || href.User != nil {
		return nil, lfsErr(errLFSUnreachable, "传输地址无效")
	}
	if href.Scheme != "https" && !sameOrigin(href, ep) {
		return nil, lfsErr(errLFSUnreachable, "传输地址不是 https")
	}
	ctx, cancel := context.WithCancelCause(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href.String(), nil)
	if err != nil {
		cancel(nil)
		return nil, lfsErr(errLFSUnreachable, "传输地址无效")
	}
	req.Header = actionHeader(href, ep, a.Header, rm.token)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", lfsUserAgent)
	}
	client := &http.Client{
		Transport: lfsRouter{c: s.lfsc, endpoint: ep, vouched: rm.vouched},
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > lfsRedirectMax {
				return errors.New("重定向过多")
			}
			prev := via[len(via)-1].URL
			if next.URL.Scheme != "https" && next.URL.Scheme != "http" {
				return errors.New("重定向到不支持的地址")
			}
			if prev.Scheme == "https" && next.URL.Scheme != "https" {
				return errors.New("重定向从 https 降级为 http")
			}
			if !sameHost(next.URL, prev) {
				next.Header = http.Header{} // 换主机：不带任何原请求头（含 Referer）
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel(nil)
		return nil, lfsErr(errLFSUnreachable, transportDetail(err), rm.token)
	}
	switch st := resp.StatusCode; st {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		resp.Body.Close()
		cancel(nil)
		return nil, errLFSMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		cancel(nil)
		return nil, lfsErr(errLFSAuth, "HTTP "+strconv.Itoa(st))
	default:
		resp.Body.Close()
		cancel(nil)
		return nil, lfsErr(errLFSUnreachable, "HTTP "+strconv.Itoa(st))
	}
	r := &lfsReader{body: resp.Body, h: sha256.New(), want: p.Size, oid: p.OID, ctx: ctx, cancel: cancel,
		tok: rm.token, stop: make(chan struct{})}
	go r.watchSpeed()
	return r, nil
}

// lfsReader 校验对象内容（大小与 sha256）并做低速保护。
type lfsReader struct {
	body   io.ReadCloser
	h      hash.Hash
	n      int64
	want   int64
	oid    string
	read   atomic.Int64
	ctx    context.Context
	cancel context.CancelCauseFunc
	tok    string
	stop   chan struct{}
	once   sync.Once
}

func (r *lfsReader) watchSpeed() {
	t := time.NewTicker(lfsLowSpeedWindow)
	defer t.Stop()
	var last int64
	for {
		select {
		case <-r.stop:
			return
		case <-r.ctx.Done():
			return
		case <-t.C:
			cur := r.read.Load()
			if cur-last < lfsLowSpeedBytes {
				r.cancel(errLFSSlow)
				return
			}
			last = cur
		}
	}
}

func (r *lfsReader) Read(b []byte) (int, error) {
	n, err := r.body.Read(b)
	if n > 0 {
		if r.n+int64(n) > r.want {
			return 0, lfsErr(errLFSCorrupt, "内容比指针记录的长")
		}
		r.h.Write(b[:n])
		r.n += int64(n)
		r.read.Add(int64(n))
	}
	switch {
	case err == io.EOF:
		if r.n != r.want || hex.EncodeToString(r.h.Sum(nil)) != r.oid {
			return n, lfsErr(errLFSCorrupt, "大小或 sha256 与指针不符")
		}
		return n, io.EOF
	case err != nil:
		if errors.Is(context.Cause(r.ctx), errLFSSlow) {
			return n, lfsErr(errLFSUnreachable, "下载过慢")
		}
		if r.ctx.Err() != nil {
			return n, err // 调用方取消或飞行超时：由调用方按自己的 ctx 归类
		}
		return n, lfsErr(errLFSUnreachable, err.Error(), r.tok)
	}
	return n, nil
}

func (r *lfsReader) Close() error {
	r.once.Do(func() { close(r.stop) })
	err := r.body.Close()
	r.cancel(nil)
	return err
}

// lfsEmpty 处理大小为 0 的对象：oid 必须是空内容的 sha256，不访问上游。
func lfsEmpty(p lfs.Pointer) error {
	if p.OID != lfsEmptyOID {
		return lfsErr(errLFSCorrupt, "空对象的 oid 不是空内容的 sha256")
	}
	return nil
}

// lfsRemote 是取对象用的上游（与镜像拉取相同的地址、凭据与守卫）。
func (s *Service) lfsRemote(ctx context.Context, prj db.Project) (remote, error) {
	if s.lfsc == nil {
		return remote{}, errUnavailable
	}
	if !hasRepo(prj) {
		return remote{}, errNoRepo
	}
	rm, err := s.remote(ctx, prj)
	if err != nil {
		var se *syncError
		if errors.As(err, &se) {
			if se.code == "auth_failed" {
				return remote{}, lfsErr(errLFSAuth, se.msg)
			}
			return remote{}, lfsErr(errLFSUnreachable, se.msg)
		}
		return remote{}, err
	}
	return rm, nil
}

// lfsObject 打开项目的 LFS 对象（调用方保证 p.Size ≤ lfsInlineMax，或项目是托管项目）：托管项目直接打开库里的对象；
// 镜像项目先查缓存，未命中时向上游取并写入缓存；同一 projectID+oid 单飞。调用方关闭文件。
func (s *Service) lfsObject(ctx context.Context, prj db.Project, p lfs.Pointer) (*os.File, error) {
	if p.Size == 0 {
		if err := lfsEmpty(p); err != nil {
			return nil, err
		}
		return os.Open(os.DevNull)
	}
	if prj.Hosted {
		return s.hostedLFSObject(prj.ID, p)
	}
	if s.lfsc == nil {
		return nil, errUnavailable
	}
	if !hasRepo(prj) {
		return nil, errNoRepo
	}
	for i := 0; ; i++ {
		if path, ok := s.lfsCache.get(prj.ID, p.OID); ok {
			f, err := os.Open(path)
			if err == nil {
				return f, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			s.lfsCache.drop(prj.ID, p.OID)
		}
		if i == lfsOpenRetries {
			return nil, lfsErr(errLFSNoSpace, "对象刚取下就被缓存淘汰")
		}
		fl := s.lfsJoin(prj, p)
		select {
		case <-fl.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if fl.err != nil {
			return nil, fl.err
		}
	}
}

// lfsJoin 加入或发起 projectID+oid 的飞行。飞行在 Service 的 ctx 下运行，不随某个浏览器请求取消。
func (s *Service) lfsJoin(prj db.Project, p lfs.Pointer) *lfsFlight {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		fl := &lfsFlight{done: make(chan struct{}), err: errUnavailable}
		close(fl.done)
		return fl
	}
	s.wg.Add(1)
	s.mu.Unlock()
	key := prj.ID + "/" + p.OID
	c := s.lfsc
	c.mu.Lock()
	if fl := c.flights[key]; fl != nil {
		c.mu.Unlock()
		s.wg.Done()
		return fl
	}
	fl := &lfsFlight{done: make(chan struct{})}
	c.flights[key] = fl
	c.mu.Unlock()
	go func() {
		defer s.wg.Done()
		fl.err = s.lfsFly(prj, p)
		c.mu.Lock()
		delete(c.flights, key)
		c.mu.Unlock()
		close(fl.done)
	}()
	return fl
}

// lfsFly 占名额（先项目后全局），在 LFSFlightTimeout 内取对象并写入缓存。
func (s *Service) lfsFly(prj db.Project, p lfs.Pointer) error {
	ctx := s.ctx
	closing := lfsErr(errLFSUnreachable, "控制面正在关闭")
	ps := s.lfsc.projSem(prj.ID)
	select {
	case ps <- struct{}{}:
	case <-ctx.Done():
		return closing
	}
	defer func() { <-ps }()
	select {
	case s.lfsc.sem <- struct{}{}:
	case <-ctx.Done():
		return closing
	}
	defer func() { <-s.lfsc.sem }()
	ctx, cancel := context.WithTimeoutCause(ctx, s.cfg.LFSFlightTimeout, errLFSFlightTimeout)
	defer cancel()
	err := s.lfsFetch(ctx, prj, p)
	if err != nil && ctx.Err() != nil {
		if errors.Is(context.Cause(ctx), errLFSFlightTimeout) {
			return lfsErr(errLFSUnreachable, "下载超时")
		}
		return closing
	}
	return err
}

// lfsFetch 检查磁盘、取对象，写到 <cache>/<pid>/tmp-<oid>-<rand>，校验通过后改名进缓存。失败不留临时文件。
func (s *Service) lfsFetch(ctx context.Context, prj db.Project, p lfs.Pointer) error {
	// 写入量以 p.Size（≤ lfsInlineMax）为上界，开始前查一次即可
	if free, err := freeBytes(s.cfg.Dir); err == nil && free-p.Size < s.minFreeBytes() {
		return errLFSNoSpace
	}
	rm, err := s.lfsRemote(ctx, prj)
	if err != nil {
		return err
	}
	a, err := s.lfsDownloadAction(ctx, rm, p)
	if err != nil {
		return err
	}
	body, err := s.lfsGet(ctx, rm, p, a)
	if err != nil {
		return err
	}
	defer body.Close()
	dir := filepath.Join(s.lfsCache.dir, prj.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return diskErr(err)
	}
	tmp, err := os.CreateTemp(dir, "tmp-"+p.OID+"-*")
	if err != nil {
		return diskErr(err)
	}
	defer os.Remove(tmp.Name()) // 改名成功后是空操作
	if _, err := io.Copy(tmp, body); err != nil {
		_ = tmp.Close()
		return diskErr(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return diskErr(err)
	}
	if err := tmp.Close(); err != nil {
		return diskErr(err)
	}
	s.lfsCache.reserve(p.Size)
	target := s.lfsCache.path(prj.ID, p.OID)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return diskErr(err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return diskErr(err)
	}
	s.lfsCache.add(prj.ID, p.OID, p.Size)
	return nil
}

// diskErr 把磁盘写满归为 lfs_no_space，其余原样返回。
func diskErr(err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return errLFSNoSpace
	}
	return err
}

// lfsOpenStream 不经缓存从上游流式读取（> lfsInlineMax 的 download=1）；校验规则同 lfsGet，
// 内容不符时 Read 在 EOF 处报错（调用方中止连接）。不占取对象的名额：随浏览器请求取消。
func (s *Service) lfsOpenStream(ctx context.Context, prj db.Project, p lfs.Pointer) (io.ReadCloser, error) {
	if p.Size == 0 {
		if err := lfsEmpty(p); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	rm, err := s.lfsRemote(ctx, prj)
	if err != nil {
		return nil, err
	}
	a, err := s.lfsDownloadAction(ctx, rm, p)
	if err != nil {
		return nil, err
	}
	return s.lfsGet(ctx, rm, p, a)
}

// transportDetail 是传输错误的说明，不带请求地址（下载 href 的查询串常是预签名凭据）。
func transportDetail(err error) string {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err.Error()
	}
	return err.Error()
}
