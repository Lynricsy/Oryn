package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"time"
)

// 运行时调度器（M14 步骤 7.1）：`POST <帧根>_oryn/v1` 上 use / call / stream / cancel 四种消息的校验与执行。
//
// 校验顺序（runtime.go 先查消息形状）：帧的运行时位 → 限速（每个 artifact 一个令牌桶）→ artifact 存在且不在回收站
// → 能力已注册 → 最新版本声明了它（不需要声明的能力除外）→ 能力当前可用 → 操作在能力的操作表里且种类相符
// → 并发上限。use 的任何拒绝都回 `{ok: true, value: null}`，页面分不出是没声明、不可用还是别的原因。
//
// stream 的应答是 application/x-ndjson：逐行 `{v, id, event}`，正常结束是 `{v, id, done: true}`；校验失败或中途出错时
// 最后一行是 `{v, id, ok: false, error}`。客户端断开连接或发来同 id 的 cancel 即取消。

// 调度器的限额。
const (
	// runtimeBurst / runtimeRate 是每个 artifact 令牌桶的容量与每秒补充量（use、call、stream 各取一个令牌）。
	runtimeBurst = 120
	runtimeRate  = 30
	// maxRuntimeCalls / maxRuntimeStreams 是每个 artifact 同时进行的调用与流的上限。
	maxRuntimeCalls   = 32
	maxRuntimeStreams = 16
	// maxRuntimeBuckets 超出时清掉已经补满的令牌桶（补满的桶和没有桶等价）。
	maxRuntimeBuckets = 1024
)

// codeNotDeclared：artifact 的最新版本没有声明这个能力。
const codeNotDeclared = "not_declared"

// streamOp 执行一个流式操作：每个事件经 emit 送出；返回 nil 表示正常结束。ctx 在客户端断开或取消时结束。
type streamOp func(ctx context.Context, rc *runtimeCall, emit func(event any) error) error

// runtimeGates 是调度器的进程内状态：令牌桶、并发计数与可取消的进行中调用（零值可用）。
type runtimeGates struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	calls   map[string]int
	streams map[string]int
	// running：帧令牌 + 消息 id → 取消函数。
	running map[string]context.CancelFunc
}

type tokenBucket struct {
	tokens float64
	at     time.Time
}

// refill 把桶补到 now。
func (b *tokenBucket) refill(now time.Time) {
	if d := now.Sub(b.at); d > 0 {
		b.tokens = min(runtimeBurst, b.tokens+d.Seconds()*runtimeRate)
	}
	b.at = now
}

// take 从 artifact 的令牌桶取一个令牌；桶空时回假。
func (g *runtimeGates) take(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.buckets == nil {
		g.buckets = map[string]*tokenBucket{}
	}
	b := g.buckets[id]
	if b == nil {
		if len(g.buckets) >= maxRuntimeBuckets {
			for k, o := range g.buckets {
				if o.refill(now); o.tokens >= runtimeBurst {
					delete(g.buckets, k)
				}
			}
		}
		b = &tokenBucket{tokens: runtimeBurst, at: now}
		g.buckets[id] = b
	}
	b.refill(now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// acquire 占一个并发名额（stream 为真时占流的名额）；满了回假。成功时返回释放函数。
func (g *runtimeGates) acquire(id string, stream bool) (func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	counts, limit := &g.calls, maxRuntimeCalls
	if stream {
		counts, limit = &g.streams, maxRuntimeStreams
	}
	if *counts == nil {
		*counts = map[string]int{}
	}
	if (*counts)[id] >= limit {
		return nil, false
	}
	(*counts)[id]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if (*counts)[id]--; (*counts)[id] <= 0 {
			delete(*counts, id)
		}
	}, true
}

func runningKey(token, id string) string { return token + "\x00" + id }

// track 登记一个可取消的进行中调用；同一帧上同一 id 已在进行时报 invalid。返回的 done 撤销登记并结束 ctx。
func (g *runtimeGates) track(parent context.Context, token, id string) (context.Context, func(), error) {
	key := runningKey(token, id)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, busy := g.running[key]; busy {
		return nil, nil, opErr(CodeInvalid, "id %s 的调用还在进行；每条消息用新的 id。", id)
	}
	if g.running == nil {
		g.running = map[string]context.CancelFunc{}
	}
	ctx, cancel := context.WithCancel(parent)
	g.running[key] = cancel
	return ctx, func() {
		g.mu.Lock()
		delete(g.running, key)
		g.mu.Unlock()
		cancel()
	}, nil
}

// cancel 取消帧上 id 对应的进行中调用，回是否找到。
func (g *runtimeGates) cancel(token, id string) bool {
	g.mu.Lock()
	cancel, ok := g.running[runningKey(token, id)]
	g.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// describe 是 use 通过时的应答：全部操作名（排序），流式操作另列在 streams 里。
func (c runtimeCap) describe() map[string]any {
	ops := make([]string, 0, len(c.ops)+len(c.streams))
	for n := range c.ops {
		ops = append(ops, n)
	}
	streams := make([]string, 0, len(c.streams))
	for n := range c.streams {
		streams = append(streams, n)
	}
	ops = append(ops, streams...)
	slices.Sort(ops)
	out := map[string]any{"ops": ops}
	if len(streams) > 0 {
		slices.Sort(streams)
		out["streams"] = streams
	}
	return out
}

// admit 按调度器的顺序校验一条 use / call / stream 消息。call 与 stream 通过时占一个并发名额，release 释放它。
// 拒绝一律是 *OpError。
func (h *Host) admit(ctx context.Context, token string, f frame, m runtimeMsg) (rc *runtimeCall, c runtimeCap, release func(), err error) {
	if !f.runtime {
		return nil, c, nil, opErr(codeNotAllowed, "这个页面不能使用运行时能力（旧版本或在浏览器中单独打开）。")
	}
	if !h.s.rt.take(f.artifactID, h.s.now()) {
		return nil, c, nil, opErr(CodeRateLimited, "调用太频繁了，稍后再试。")
	}
	a, err := h.s.Get(ctx, f.artifactID)
	if err != nil {
		if _, ok := AsOpError(err); ok {
			return nil, c, nil, opErr(CodeNotFound, "artifact 已不在。")
		}
		return nil, c, nil, err
	}
	if a.Deleted() {
		return nil, c, nil, opErr(CodeNotFound, "artifact 已被删除。")
	}
	c, ok := h.caps[m.Cap]
	if !ok {
		return nil, c, nil, opErr(codeUnsupported, "没有能力 %s。", m.Cap)
	}
	if c.declared {
		head, err := h.s.Version(ctx, a.ID, 0)
		if err != nil {
			return nil, c, nil, err
		}
		if !slices.Contains(head.Capabilities, m.Cap) {
			return nil, c, nil, opErr(codeNotDeclared, "这个 artifact 的最新版本没有声明能力 %s。", m.Cap)
		}
		if strictCapability(m.Cap) {
			h.escalate(token, true)
		}
	}
	rc = &runtimeCall{token: token, frame: f, art: a, args: m.Args}
	if c.available != nil && !c.available(ctx, rc) {
		return nil, c, nil, opErr(codeUnsupported, "能力 %s 在这个页面上不可用。", m.Cap)
	}
	if m.Kind == "use" {
		return rc, c, nil, nil
	}
	_, isCall := c.ops[m.Op]
	_, isStream := c.streams[m.Op]
	switch {
	case !isCall && !isStream:
		return nil, c, nil, opErr(codeUnsupported, "能力 %s 没有操作 %s。", m.Cap, m.Op)
	case m.Kind == "call" && !isCall:
		return nil, c, nil, opErr(CodeInvalid, "%s.%s 是流式操作，要用 stream 调用。", m.Cap, m.Op)
	case m.Kind == "stream" && !isStream:
		return nil, c, nil, opErr(CodeInvalid, "%s.%s 不是流式操作，要用 call 调用。", m.Cap, m.Op)
	}
	release, ok = h.s.rt.acquire(a.ID, isStream)
	if !ok {
		return nil, c, nil, opErr(CodeRateLimited, "同时进行的调用太多了，等前面的完成再试。")
	}
	return rc, c, release, nil
}

// dispatch 执行 use 与 call。use 的任何拒绝都回 (nil, nil)。
func (h *Host) dispatch(ctx context.Context, token string, f frame, m runtimeMsg) (any, error) {
	rc, c, release, err := h.admit(ctx, token, f, m)
	if err != nil {
		if _, ok := AsOpError(err); ok && m.Kind == "use" {
			return nil, nil
		}
		return nil, err
	}
	if m.Kind == "use" {
		return c.describe(), nil
	}
	defer release()
	ctx, done, err := h.s.rt.track(ctx, token, m.ID)
	if err != nil {
		return nil, err
	}
	defer done()
	return c.ops[m.Op](ctx, rc)
}

// serveStream 执行 stream：应答一律是 NDJSON（校验失败时只有一行错误）。
func (h *Host) serveStream(w http.ResponseWriter, r *http.Request, token string, f frame, m runtimeMsg) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	rctl := http.NewResponseController(w)
	line := func(v map[string]any) error {
		b, _ := json.Marshal(v)
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		return rctl.Flush()
	}
	rc, c, release, err := h.admit(r.Context(), token, f, m)
	if err == nil {
		defer release()
		var done func()
		var ctx context.Context
		if ctx, done, err = h.s.rt.track(r.Context(), token, m.ID); err == nil {
			defer done()
			w.WriteHeader(http.StatusOK)
			_ = rctl.Flush()
			err = c.streams[m.Op](ctx, rc, func(ev any) error {
				return line(map[string]any{"v": 1, "id": m.ID, "event": ev})
			})
			if ctx.Err() != nil {
				err = nil
			}
		}
	}
	if err != nil {
		_ = line(runtimeReply(m.ID, nil, err))
		return
	}
	_ = line(map[string]any{"v": 1, "id": m.ID, "done": true})
}

// posArgs 把位置参数解成至多 maxN 个、至少 minN 个；末尾的 null 当作没给。
func posArgs(args json.RawMessage, minN, maxN int) ([]json.RawMessage, error) {
	var list []json.RawMessage
	if raw := bytes.TrimSpace(args); len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, opErr(CodeInvalid, "args 必须是参数数组。")
		}
	}
	for len(list) > 0 && isNull(list[len(list)-1]) {
		list = list[:len(list)-1]
	}
	if len(list) > maxN {
		return nil, opErr(CodeInvalid, "这个操作最多 %d 个参数，收到 %d 个。", maxN, len(list))
	}
	if len(list) < minN {
		return nil, opErr(CodeInvalid, "这个操作至少要 %d 个参数，收到 %d 个。", minN, len(list))
	}
	return list, nil
}

func isNull(raw json.RawMessage) bool {
	r := bytes.TrimSpace(raw)
	return len(r) == 0 || string(r) == "null"
}

// argAt 取第 i 个位置参数（缺省或 null 时回 nil）。
func argAt(list []json.RawMessage, i int) json.RawMessage {
	if i >= len(list) || isNull(list[i]) {
		return nil
	}
	return list[i]
}

// stringArg 把第 i 个位置参数解成字符串。
func stringArg(list []json.RawMessage, i int, name string) (string, error) {
	var s string
	if raw := argAt(list, i); raw == nil || json.Unmarshal(raw, &s) != nil {
		return "", opErr(CodeInvalid, "第 %d 个参数 %s 必须是字符串。", i+1, name)
	}
	return s, nil
}

// optsArg 把第 i 个位置参数（可缺省）严格解成选项对象 v。
func optsArg(list []json.RawMessage, i int, v any) error {
	raw := argAt(list, i)
	if raw == nil {
		return nil
	}
	return decodeParams(raw, v)
}
