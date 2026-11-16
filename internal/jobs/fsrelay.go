package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
)

// 停机 orb 的只读文件中继（offline fs）：控制面不能直接访问 provider 宿主，用户的只读文件请求先进入按 provider
// 划分的内存队列，由 provider 长轮询 POST /provider/v1/fs/claim 认领，执行后以 POST /provider/v1/fs/{id}/reply
// 回应——reply 的请求体就是用户响应体，经本中继流式交给等待中的调用方。请求不落库：控制面重启后在途请求随用户连接一起失效。

const (
	// DefaultFSClaimTimeout 是请求入队后等待 provider 认领的上限（超出即 ErrProviderUnavailable）。
	DefaultFSClaimTimeout = 10 * time.Second
	// DefaultFSReplyTimeout 是认领后等待 provider 回应的上限（超出即 ErrFSReplyTimeout）。
	DefaultFSReplyTimeout = 60 * time.Second
)

// ErrProviderUnavailable 表示 provider 未在 ClaimTimeout 内认领文件请求（不在线或没有认领循环）。
var ErrProviderUnavailable = errors.New("jobs: provider did not claim the fs request in time")

// ErrFSReplyTimeout 表示 provider 认领后未在 ReplyTimeout 内回应。
var ErrFSReplyTimeout = errors.New("jobs: provider did not reply to the fs request in time")

// errFSReplyClosed 表示 provider 的 reply 请求已结束，响应体不可再读。
var errFSReplyClosed = errors.New("jobs: fs reply body closed")

// fsReplyHeaders 是 reply 请求中转交给调用方的头（Content-Length 另按请求的实际长度给出）。
var fsReplyHeaders = []string{"Content-Type", "Content-Range", "Etag", "Accept-Ranges", "Retry-After"}

// FSReply 是 provider 对一次文件请求的回应。调用方读完（或放弃）Body 后必须恰好调用一次 Done，
// provider 的 reply 请求在此之前一直挂起。
type FSReply struct {
	// Status 是 provider 声明的用户响应状态码（X-Oryn-Status，未经校验）。
	Status int
	// Header 是 provider 声明的响应头（仅 fsReplyHeaders 与 Content-Length）。
	Header http.Header
	// Body 是用户响应体（流式；provider 的 reply 请求结束后读取返回错误）。
	Body io.Reader

	done chan struct{}
	once sync.Once
}

// Done 通知 provider 侧本次回应已转发完毕（或调用方已放弃）。可重复调用，只有第一次生效。
func (r *FSReply) Done() { r.once.Do(func() { close(r.done) }) }

// fsCall 是一个在途的文件请求。
type fsCall struct {
	req        apigen.ProviderFsRequest
	providerID string
	// claimed 在被认领时关闭。
	claimed chan struct{}
	// reply 容量为 1：回应方在锁内投递，从不阻塞。
	reply chan *FSReply
}

// FSRelay 是按 provider 划分的文件请求中继。零值不可用，用 NewFSRelay 创建。
type FSRelay struct {
	// ClaimTimeout 是等待认领的上限（测试可缩短）。
	ClaimTimeout time.Duration
	// ReplyTimeout 是认领后等待回应的上限（测试可缩短）。
	ReplyTimeout time.Duration

	mu       sync.Mutex
	pending  map[string][]*fsCall     // providerID → 待认领（按入队顺序）
	signals  map[string]chan struct{} // providerID → 有新请求时关闭并替换
	inflight map[string]*fsCall       // 请求 ID → 已认领、待回应
}

// NewFSRelay 创建中继。
func NewFSRelay() *FSRelay {
	return &FSRelay{
		ClaimTimeout: DefaultFSClaimTimeout,
		ReplyTimeout: DefaultFSReplyTimeout,
		pending:      map[string][]*fsCall{},
		signals:      map[string]chan struct{}{},
		inflight:     map[string]*fsCall{},
	}
}

// Do 把 req（ID 由中继分配）交给 providerID 执行并等待回应。未在 ClaimTimeout 内被认领 → ErrProviderUnavailable；
// 认领后未在 ReplyTimeout 内回应 → ErrFSReplyTimeout；ctx 结束 → ctx.Err()。成功时调用方必须调用 reply.Done()。
func (f *FSRelay) Do(ctx context.Context, providerID string, req apigen.ProviderFsRequest) (*FSReply, error) {
	req.ID = ids.New("fsr")
	c := &fsCall{req: req, providerID: providerID, claimed: make(chan struct{}), reply: make(chan *FSReply, 1)}
	f.mu.Lock()
	f.pending[providerID] = append(f.pending[providerID], c)
	f.signalLocked(providerID)
	f.mu.Unlock()

	claimTimer := time.NewTimer(f.ClaimTimeout)
	defer claimTimer.Stop()
	select {
	case <-c.claimed:
	case <-claimTimer.C:
		if f.dequeue(c) {
			return nil, ErrProviderUnavailable
		}
	case <-ctx.Done():
		if f.dequeue(c) {
			return nil, ctx.Err()
		}
	}
	// 已认领（可能与超时 / 取消同时发生：此时下面的 select 立即走放弃分支）
	replyTimer := time.NewTimer(f.ReplyTimeout)
	defer replyTimer.Stop()
	select {
	case rep := <-c.reply:
		return rep, nil
	case <-replyTimer.C:
		return nil, f.abandon(c, ErrFSReplyTimeout)
	case <-ctx.Done():
		return nil, f.abandon(c, ctx.Err())
	}
}

// dequeue 把尚未认领的 c 移出队列；c 已被认领时返回 false。
func (f *FSRelay) dequeue(c *fsCall) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.pending[c.providerID]
	for i, x := range q {
		if x != c {
			continue
		}
		q = append(q[:i:i], q[i+1:]...)
		if len(q) == 0 {
			delete(f.pending, c.providerID)
		} else {
			f.pending[c.providerID] = q
		}
		return true
	}
	return false
}

// abandon 放弃已认领的 c：之后的 reply 得 404。回应已在投递中时收下它并立即 Done，放行 provider。
func (f *FSRelay) abandon(c *fsCall, err error) error {
	f.mu.Lock()
	if f.inflight[c.req.ID] == c {
		delete(f.inflight, c.req.ID)
		f.mu.Unlock()
		return err
	}
	f.mu.Unlock()
	// 不在 inflight 中 = 回应方已在锁内投递（容量 1 的通道里必有一条）
	(<-c.reply).Done()
	return err
}

// Claim 长轮询认领 providerID 最早的文件请求：最多等待 wait（上限 MaxClaimWait）；超时或 ctx 结束返回 (_, false)。
func (f *FSRelay) Claim(ctx context.Context, providerID string, wait time.Duration) (apigen.ProviderFsRequest, bool) {
	wait = min(max(wait, 0), MaxClaimWait)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		f.mu.Lock()
		if c := f.popLocked(providerID); c != nil {
			f.mu.Unlock()
			return c.req, true
		}
		ch, ok := f.signals[providerID]
		if !ok {
			ch = make(chan struct{})
			f.signals[providerID] = ch
		}
		f.mu.Unlock()
		select {
		case <-ch:
		case <-timer.C:
			return apigen.ProviderFsRequest{}, false
		case <-ctx.Done():
			return apigen.ProviderFsRequest{}, false
		}
	}
}

// popLocked 取出 providerID 队首的请求并登记为在途；队列为空时返回 nil。调用方持有 f.mu。
func (f *FSRelay) popLocked(providerID string) *fsCall {
	q := f.pending[providerID]
	if len(q) == 0 {
		return nil
	}
	c := q[0]
	if len(q) == 1 {
		delete(f.pending, providerID)
	} else {
		q[0] = nil
		f.pending[providerID] = q[1:]
	}
	f.inflight[c.req.ID] = c
	close(c.claimed)
	return c
}

// signalLocked 唤醒 providerID 的长轮询。调用方持有 f.mu。
func (f *FSRelay) signalLocked(providerID string) {
	if ch, ok := f.signals[providerID]; ok {
		close(ch)
		delete(f.signals, providerID)
	}
}

// deliver 把 rep 交给 providerID 名下在途请求 id 的调用方；请求不存在、已结束或不属于该 provider 时返回 false。
func (f *FSRelay) deliver(providerID, id string, rep *FSReply) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.inflight[id]
	if c == nil || c.providerID != providerID {
		return false
	}
	delete(f.inflight, id)
	c.reply <- rep
	return true
}

// fsReplyBody 包住 reply 的请求体：reply 处理器返回前先 close，之后调用方的读取一律失败，
// 不会读到已被 net/http 回收的请求体。
type fsReplyBody struct {
	mu     sync.Mutex
	r      io.Reader
	closed bool
}

func (b *fsReplyBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errFSReplyClosed
	}
	return b.r.Read(p)
}

func (b *fsReplyBody) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

// FSRelay 返回停机 orb 的文件请求中继。
func (q *Queue) FSRelay() *FSRelay { return q.fs }

// ClaimFsHTTP 处理 POST /provider/v1/fs/claim：续租（同 jobs/claim）后长轮询认领一个文件请求。
func (q *Queue) ClaimFsHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	var req apigen.ProviderFsClaimRequest
	if !decode(w, r, &req) {
		return
	}
	if req.InstanceID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "instanceId 必填")
		return
	}
	held, err := q.renewLease(r.Context(), p, req.InstanceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !held {
		writeError(w, http.StatusConflict, "provider_already_connected", "另一个 provider 实例持有未过期的租约")
		return
	}
	fr, found := q.fs.Claim(r.Context(), p.ID, time.Duration(req.WaitSeconds)*time.Second)
	if !found {
		if r.Context().Err() == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	writeJSON(w, http.StatusOK, apigen.ProviderFsClaimResponse{Request: fr})
}

// ReplyFsHTTP 处理 POST /provider/v1/fs/{id}/reply：把状态码（X-Oryn-Status）、选定的响应头与请求体交给等待中的
// 调用方，等它转发完毕（或放弃）后回 204。请求不存在、已超时或不属于该 provider → 404 not_found。
func (q *Queue) ReplyFsHTTP(w http.ResponseWriter, r *http.Request, id string, status int) {
	p, ok := q.authOrFail(w, r)
	if !ok {
		return
	}
	hdr := make(http.Header, len(fsReplyHeaders)+1)
	for _, k := range fsReplyHeaders {
		if v := r.Header.Values(k); len(v) > 0 {
			hdr[k] = v
		}
	}
	if r.ContentLength >= 0 {
		hdr.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
	}
	body := &fsReplyBody{r: r.Body}
	rep := &FSReply{Status: status, Header: hdr, Body: body, done: make(chan struct{})}
	if !q.fs.deliver(p.ID, id, rep) {
		writeError(w, http.StatusNotFound, "not_found", "文件请求不存在或已结束")
		return
	}
	select {
	case <-rep.done:
	case <-r.Context().Done():
	}
	body.close()
	w.WriteHeader(http.StatusNoContent)
}
