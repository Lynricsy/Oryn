package orbdrt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
)

const (
	// maxBufferedFrames 是断连期间每线程排队的 omp 帧上限（C2）。
	maxBufferedFrames = 32 << 20
	// termHighWater / termLowWater：帧缓冲 ≥8MiB 时丢弃 terminal_output，回落到 ≤1MiB 后发一次 terminal.gap（M3 2.1）。
	termHighWater = 8 << 20
	termLowWater  = 1 << 20
	// maxCommandLine 是 agent 流上单条命令帧的上限（含内联图片）。
	maxCommandLine = 128 << 20
	// stderrTailLines 是 `exited` 上报的 stderr 尾部行数。
	stderrTailLines = 20
	// stableReady 是 agent 保持就绪多久后重置崩溃重启计数。
	stableReady = time.Minute
	// initIDPrefix 标记 orbd 自己发出的初始化命令；其应答不转发给控制面。
	initIDPrefix = "orbd-"
)

// ---- 帧缓冲 ----

type outItem struct {
	seq   uint64
	epoch int
	data  []byte
	// droppable：terminal_output 帧。积压时可丢弃，客户端靠 terminal_snapshot 重同步。
	droppable bool
}

// terminalOutputPrefix 是 omp terminal_output 帧的固定开头（rpc-terminal.ts `#flush` 以 type 起头）。
// 前缀不符只会退化为「不丢弃」。
var terminalOutputPrefix = []byte(`{"type":"terminal_output"`)

func isTerminalOutput(frame []byte) bool { return bytes.HasPrefix(frame, terminalOutputPrefix) }

// outbox 是 omp → 控制面的帧队列；超过 32MiB 时整体丢弃并记录 gap。
// 单帧本身超过整个预算时同样丢弃（连同已排队的帧）并记录 gap：预算对单帧同样生效。
// 可丢弃帧（终端输出）在队列 ≥ termHighWater 时直接丢弃并记 termGap，不影响其它帧。
type outbox struct {
	mu    sync.Mutex
	items []outItem
	size  int
	gap   bool
	// gapGen 每次溢出加一：gap 只在「上报成功时的那一代」仍是最新时才清除，
	// 上报途中再次溢出不会被误清。
	gapGen uint64
	seq    uint64
	// termGap：丢弃过终端输出、尚未发 terminal.gap。
	termGap bool
}

func (o *outbox) push(it outItem) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pushLocked(it)
}

func (o *outbox) pushLocked(it outItem) {
	if it.droppable && o.size >= termHighWater {
		o.termGap = true
		return
	}
	o.seq++
	it.seq = o.seq
	if len(it.data) > maxBufferedFrames {
		o.items, o.size, o.gap = nil, 0, true
		o.gapGen++
		return
	}
	if o.size+len(it.data) > maxBufferedFrames {
		o.items, o.size, o.gap = nil, 0, true
		o.gapGen++
	}
	o.items = append(o.items, it)
	o.size += len(it.data)
}

// takeTermGap 在丢弃过终端输出且队列已回落到 ≤ low 时清除标记并返回 true（调用方随即发 terminal.gap）。
func (o *outbox) takeTermGap(low int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.termGap && o.size <= low {
		o.termGap = false
		return true
	}
	return false
}

// markTermGap 恢复 termGap（terminal.gap 没能发出时，留到下一次再报）。
func (o *outbox) markTermGap() {
	o.mu.Lock()
	o.termGap = true
	o.mu.Unlock()
}

func (o *outbox) head() (outItem, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.items) == 0 {
		return outItem{}, false
	}
	return o.items[0], true
}

// pop 移除队首（仅当它仍是 it；期间发生溢出清空时不误删）。
func (o *outbox) pop(it outItem) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.items) > 0 && o.items[0].seq == it.seq {
		o.size -= len(o.items[0].data)
		o.items[0] = outItem{}
		o.items = o.items[1:]
	}
}

// pendingGap 报告是否有未上报的 gap 及其代数；不清除标记（上报确认后由 clearGap 清除）。
func (o *outbox) pendingGap() (uint64, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gapGen, o.gap
}

// clearGap 在 gen 仍是最新一代时清除 gap 标记。
func (o *outbox) clearGap(gen uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.gapGen == gen {
		o.gap = false
	}
}

// ---- 线程 ----

// thread 汇集一个线程在 orb 内的全部状态：会话目录 tailer、帧缓冲与 agent 流转发、agent 监督器。
type thread struct {
	r      *Runtime
	ctx    context.Context // 线程寿命；dropThread 时取消
	cancel context.CancelFunc
	id     string
	dir    string
	out    outbox
	poke   chan struct{}
	tail   *tailer

	mu     sync.Mutex
	sup    *supervisor
	proc   *proc // 当前存活进程
	epoch  int   // 每次拉起进程加一；agent 流按 epoch 切换
	status *apigen.LinkCtlAgentStatusParams
}

func newThread(r *Runtime, id, dir string) *thread {
	t := &thread{r: r, id: id, dir: dir, poke: make(chan struct{}, 1)}
	t.tail = newTailer(t)
	return t
}

func (t *thread) wake() {
	select {
	case t.poke <- struct{}{}:
	default:
	}
}

func (t *thread) agentRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.proc != nil
}

func (t *thread) lastStatus() *apigen.LinkCtlAgentStatusParams {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *thread) setStatus(s apigen.LinkCtlAgentStatusParams) {
	s.ThreadID = t.id
	t.mu.Lock()
	t.status = &s
	t.mu.Unlock()
	t.r.notify("agent.status", s)
}

func (t *thread) procForEpoch(e int) *proc {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.proc != nil && t.proc.epoch == e {
		return t.proc
	}
	return nil
}

type agentStream struct {
	s     *link.Stream
	c     *conn
	epoch int
	// announced 表示已在这条流上报过 ready：ready 只在「当前进程代的流已打开、旧代帧已写完」之后上报，
	// 控制面收到 ready 时下发的排队命令因此一定落到当前进程上。
	announced bool
}

// 仅供测试的钩子：testHookBeforeGapReport 在上报 agent.gap 之前调用（可借此在上报途中断开 link）；
// testHookAgentInitialized 在 agent 进程完成初始化之后调用。
var (
	testHookBeforeGapReport  func(c *conn)
	testHookAgentInitialized func(t *thread)
)

// forward 维持 agent 流：有存活进程或待发帧且 link 可用时保持一条流，
// 按序写出缓冲帧；进程换代（重启）或 link 重连时重开新流。
func (t *thread) forward(ctx context.Context) {
	var st *agentStream
	closeSt := func() {
		if st != nil {
			_ = st.s.Close()
			st = nil
		}
	}
	defer closeSt()
	for {
		changed := t.r.changedCh()
		c := t.r.current()
		if st != nil && st.c != c {
			closeSt()
		}
		head, hasHead := t.out.head()
		t.mu.Lock()
		p := t.proc
		t.mu.Unlock()
		target := -1
		switch {
		case hasHead:
			target = head.epoch
		case p != nil:
			target = p.epoch
		}
		if st != nil && st.epoch != target {
			closeSt()
		}
		if c != nil && target >= 0 && st == nil {
			s, err := c.sess.Open(ctx, link.StreamHeader{T: link.StreamAgent, ThreadID: t.id})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				t.r.log.Warn("open agent stream failed", "thread", t.id, "err", err)
				if !sleepOr(ctx, c.done, 200*time.Millisecond) {
					return
				}
				continue
			}
			st = &agentStream{s: s, c: c, epoch: target}
			sp := t.procForEpoch(target)
			go t.pumpCommands(s, sp)
			// 已就绪的 agent 换了新流（重连）：重放仍待答的交互（控制面可能已重启、丢了它们）。
			// 重复的请求按 id 去重，无副作用。ready 由下面的统一检查在这条流上重报。
			if sp != nil && sp.isReady() {
				for _, f := range sp.ui.replay() {
					t.out.push(outItem{epoch: sp.epoch, data: f})
				}
			}
		}
		// gap 必须先于其后的帧被控制面处理：上报成功（含屏障确认）之前不写任何帧；失败则保留标记，重连后重报。
		if st != nil {
			if gen, gap := t.out.pendingGap(); gap {
				if err := t.reportGap(ctx, c); err != nil {
					t.r.log.Debug("agent.gap report failed; will retry", "thread", t.id, "err", err)
					closeSt()
					if !sleepOr(ctx, c.done, 200*time.Millisecond) {
						return
					}
					continue
				}
				t.out.clearGap(gen)
				continue
			}
		}
		// 丢弃过终端输出且积压已回落：通知控制面（转为 term.lagged，客户端以快照重同步）。发不出去则留待下次。
		if st != nil && t.out.takeTermGap(termLowWater) {
			if err := c.ctl.Notify(string(apigen.MethodTerminalGap), apigen.LinkCtlTerminalGapParams{ThreadID: t.id}); err != nil {
				t.out.markTermGap()
			}
		}
		// 流属于当前进程代且该进程已完成初始化：在这条流上报 ready（每条流一次）。
		if st != nil && !st.announced {
			if sp := t.procForEpoch(st.epoch); sp != nil && sp.isReady() {
				st.announced = true
				t.setStatus(apigen.LinkCtlAgentStatusParams{Status: apigen.LinkCtlAgentStatusParamsStatusReady})
			}
		}
		if st != nil && hasHead && head.epoch == st.epoch {
			if err := writeLine(st.s, head.data); err != nil {
				t.r.log.Debug("agent stream write failed", "thread", t.id, "err", err)
				closeSt()
				if !sleepOr(ctx, c.done, 200*time.Millisecond) {
					return
				}
				continue
			}
			t.out.pop(head)
			continue
		}
		var cdone <-chan struct{}
		if c != nil {
			cdone = c.done
		}
		select {
		case <-t.poke:
		case <-changed:
		case <-cdone:
		case <-ctx.Done():
			return
		}
	}
}

// reportGap 发出 agent.gap，并以一次 ctl 请求作屏障：控制面在读循环里同步处理通知，
// 因此收到其后请求的应答（成功或 ctl 错误）即说明 gap 已被处理，之后写出的帧不会先于它生效。
func (t *thread) reportGap(ctx context.Context, c *conn) error {
	if h := testHookBeforeGapReport; h != nil {
		h(c)
	}
	if err := c.ctl.Notify("agent.gap", apigen.LinkCtlAgentGapParams{ThreadID: t.id}); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := c.ctl.Call(cctx, "entries.cursors", apigen.LinkCtlEntriesCursorsParams{ThreadID: t.id}, nil)
	var ce *link.CtlError
	if err != nil && !errors.As(err, &ce) {
		return err
	}
	return nil
}

// sleepOr 等待 d、done 关闭或 ctx 结束；ctx 结束时返回 false。
func sleepOr(ctx context.Context, done <-chan struct{}, d time.Duration) bool {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
	case <-done:
	case <-ctx.Done():
		return false
	}
	return true
}

func writeLine(w io.Writer, data []byte) error {
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, '\n')
	_, err := w.Write(buf)
	return err
}

// sideChannelTypes 是 omp 在命令队列之外即时处理的侧通道应答（oryn-core rpc-mode.ts
// `dispatchRpcControlFrame`：extension_ui_response、host_tool_result、host_tool_update、host_uri_result）。
// omp 在启动期间（session_start 可能在等一个对话框）就已在读这些帧，因此它们不等初始化完成。
var sideChannelTypes = map[string]bool{
	"extension_ui_response": true,
	"host_tool_result":      true,
	"host_tool_update":      true,
	"host_uri_result":       true,
}

type cmdHead struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func parseCmdHead(line []byte) cmdHead {
	var h cmdHead
	_ = json.Unmarshal(line, &h)
	return h
}

// pumpCommands 把控制面写来的命令帧原样写入 omp stdin。侧通道应答立即写入（启动期间也是）；
// 普通命令按序排队，进程完成初始化后依次写入。读循环从不阻塞，因此排队的命令不会挡住其后的侧通道应答。
// 流所属的进程代已退出（或 p 为 nil）时，普通命令得到一条合成的失败应答，而不是被静默丢弃。
func (t *thread) pumpCommands(s *link.Stream, p *proc) {
	rd := omprpc.NewReader(s, maxCommandLine)
	var q *cmdQueue
	if p != nil {
		q = newCmdQueue()
		defer q.close()
		go t.drainCommands(p, q)
	}
	for {
		line, err := rd.ReadLine()
		if err != nil {
			if errors.Is(err, omprpc.ErrLineTooLong) {
				t.r.log.Warn("command frame too long; dropped", "thread", t.id)
				continue
			}
			return
		}
		h := parseCmdHead(line)
		if p == nil {
			t.rejectUndelivered(h)
			continue
		}
		if sideChannelTypes[h.Type] {
			select {
			case <-p.exited:
				continue
			default:
			}
			p.ui.observeIn(line)
			if err := p.w.WriteRaw(line); err != nil {
				t.r.log.Debug("write omp stdin failed", "thread", t.id, "err", err)
			}
			continue
		}
		q.push(line)
	}
}

// drainCommands 等进程完成初始化后按序写入排队的普通命令；进程在此之前（或之后）退出时，
// 剩余命令一律得到合成的失败应答。
func (t *thread) drainCommands(p *proc, q *cmdQueue) {
	select {
	case <-p.readyCh:
	case <-p.exited:
	}
	for {
		line, ok := q.pop()
		if !ok {
			return
		}
		select {
		case <-p.exited:
			t.rejectUndelivered(parseCmdHead(line))
			continue
		default:
		}
		p.ui.observeIn(line)
		if err := p.w.WriteRaw(line); err != nil {
			t.r.log.Debug("write omp stdin failed", "thread", t.id, "err", err)
		}
	}
}

// rejectUndelivered 为没能送达进程的命令合成一条失败应答（经当前进程代的流回到控制面），
// 让发起方得到明确结果而不是永远等待。侧通道应答与无 id 的帧没有应答可言，直接丢弃。
func (t *thread) rejectUndelivered(h cmdHead) {
	if h.ID == "" || sideChannelTypes[h.Type] {
		return
	}
	resp, err := json.Marshal(map[string]any{
		"type": "response", "id": h.ID, "command": h.Type, "success": false,
		"error": undeliveredError,
	})
	if err != nil {
		return
	}
	t.mu.Lock()
	epoch := t.epoch
	t.mu.Unlock()
	t.out.push(outItem{epoch: epoch, data: resp})
	t.wake()
}

// undeliveredError 是合成失败应答的错误文本。控制面（internal/threads durable.go 的 undeliveredError）按原文匹配它
// 来重排启动 prompt：改动须两边同步。
const undeliveredError = "agent process exited before the command was delivered"

// cmdQueue 是不设上限的 FIFO（控制面侧已限制排队命令数）；close 之后 pop 在取完剩余项后返回 false。
type cmdQueue struct {
	mu     sync.Mutex
	items  [][]byte
	closed bool
	signal chan struct{}
}

func newCmdQueue() *cmdQueue { return &cmdQueue{signal: make(chan struct{}, 1)} }

func (q *cmdQueue) push(line []byte) {
	q.mu.Lock()
	q.items = append(q.items, line)
	q.mu.Unlock()
	q.notify()
}

func (q *cmdQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.notify()
}

func (q *cmdQueue) notify() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *cmdQueue) pop() ([]byte, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			line := q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
			q.mu.Unlock()
			return line, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return nil, false
		}
		<-q.signal
	}
}

// ---- agent.start / agent.stop ----

func (r *Runtime) agentStart(p apigen.LinkCtlAgentStartParams) (apigen.LinkCtlAgentStartResult, error) {
	if !validThreadID(p.ThreadID) {
		return apigen.LinkCtlAgentStartResult{}, ctlErr(link.CodeInvalidParams, "invalid threadId %q", p.ThreadID)
	}
	dir := filepath.Join(r.home, ".oryn", "threads", p.ThreadID)
	if p.SessionDir != "" {
		dir = r.resolvePath(p.SessionDir)
	}
	cwd := r.currentWorkdir()
	if p.Cwd != "" {
		cwd = r.resolvePath(p.Cwd)
	}
	for _, d := range []string{dir, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return apigen.LinkCtlAgentStartResult{}, ctlErr("failed", "%v", err)
		}
	}
	if err := r.reg.add(p.ThreadID, dir); err != nil {
		r.log.Warn("persist thread registry failed", "err", err)
	}
	t := r.thread(p.ThreadID, dir)

	frames := make([][]byte, 0, len(p.InitFrames))
	for i, f := range p.InitFrames {
		b, err := json.Marshal(f)
		if err != nil {
			return apigen.LinkCtlAgentStartResult{}, ctlErr(link.CodeInvalidParams, "initFrames[%d]: %v", i, err)
		}
		frames = append(frames, r.fixOpenSession(b, dir))
	}
	env := r.baseEnv()
	for k, v := range r.setupEnvSnapshot() {
		env[k] = v
	}
	// agent 以登录 shell 的环境启动（setup 写进 ~/.profile 的开发环境由此生效）；Oryn 自己的变量与
	// agent.start 下发的环境（env 模式密钥）在其后覆盖。
	env = loginEnv(r.ctx, cwd, env)
	env["PI_RPC_EMIT_TITLE"] = "1"
	env["PI_NOTIFICATIONS"] = "off"
	env["ORYN_THREAD_ID"] = p.ThreadID
	for k, v := range p.Env {
		env[k] = v
	}
	pid, err := t.start(startSpec{
		args:       []string{"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", dir, "-e", r.extensionPath()},
		cwd:        cwd,
		env:        envList(env),
		initFrames: frames,
	})
	if err != nil {
		return apigen.LinkCtlAgentStartResult{}, ctlErr("failed", "start agent: %v", err)
	}
	// 线程登记之前已启动、因此没拿到 PUBLIC_URL 的 portal 服务，此时重启一次补上。
	go r.svc.refreshPublicURLs()
	return apigen.LinkCtlAgentStartResult{Pid: pid}, nil
}

// fixOpenSession 把 open_session 帧里 `~/…` 或相对形式的 sessionDir 换成本机绝对路径，
// 缺省时填入线程会话目录（omp 不展开 `~`）。
func (r *Runtime) fixOpenSession(frame []byte, dir string) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(frame, &m) != nil {
		return frame
	}
	var typ, sd string
	_ = json.Unmarshal(m["type"], &typ)
	if typ != "open_session" {
		return frame
	}
	_ = json.Unmarshal(m["sessionDir"], &sd)
	want := dir
	if sd != "" {
		want = r.resolvePath(sd)
	}
	if want == sd {
		return frame
	}
	m["sessionDir"], _ = json.Marshal(want)
	out, err := json.Marshal(m)
	if err != nil {
		return frame
	}
	return out
}

func (r *Runtime) agentStop(p apigen.LinkCtlAgentStopParams) (apigen.LinkCtlAgentStopResult, error) {
	t := r.lookupThread(p.ThreadID)
	if t == nil {
		return apigen.LinkCtlAgentStopResult{}, nil
	}
	grace := time.Duration(p.GraceSeconds) * time.Second
	if grace <= 0 {
		grace = DefaultStopGrace
	}
	return apigen.LinkCtlAgentStopResult{ExitCode: t.stopAgent(grace)}, nil
}

type startSpec struct {
	args       []string
	cwd        string
	env        []string
	initFrames [][]byte
}

// start 拉起监督器；已有活动监督器时幂等返回当前 pid。
func (t *thread) start(spec startSpec) (int, error) {
	t.mu.Lock()
	if s := t.sup; s != nil && !s.finished() {
		pid := 0
		if t.proc != nil {
			pid = t.proc.pid
		}
		t.mu.Unlock()
		t.wake() // 没有 agent 流时由转发器重开
		return pid, nil
	}
	s := &supervisor{t: t, spec: spec, stopCh: make(chan struct{}), done: make(chan struct{})}
	t.sup = s
	t.mu.Unlock()
	p, err := s.spawn()
	if err != nil {
		t.mu.Lock()
		if t.sup == s {
			t.sup = nil
		}
		t.mu.Unlock()
		close(s.done)
		return 0, err
	}
	go s.run(p)
	return p.pid, nil
}

// stopAgent 请求优雅停止并返回退出码；没有监督器时返回 0。
func (t *thread) stopAgent(grace time.Duration) int {
	t.mu.Lock()
	s := t.sup
	t.mu.Unlock()
	if s == nil {
		return 0
	}
	return s.stop(grace)
}

// ---- 监督器 ----

type supervisor struct {
	t        *thread
	spec     startSpec
	stopOnce sync.Once
	stopCh   chan struct{}
	done     chan struct{}
	lastExit int
}

func (s *supervisor) finished() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *supervisor) stopped() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *supervisor) stop(grace time.Duration) int {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.t.mu.Lock()
	p := s.t.proc
	s.t.mu.Unlock()
	if p != nil {
		_ = p.stdin.Close()
		tm := time.NewTimer(grace)
		select {
		case <-p.exited:
		case <-tm.C:
			s.t.r.log.Warn("agent did not exit in time; killing", "thread", s.t.id, "grace", grace)
			p.kill()
		}
		tm.Stop()
	}
	<-s.done
	return s.lastExit
}

// run 是监督循环：初始化 → 等待退出 → 按 1s/2s/4s 重启至多 3 次。
func (s *supervisor) run(p *proc) {
	t := s.t
	defer func() {
		close(s.done)
		t.wake()
	}()
	attempt := 0
	var spawnErr error
	for {
		code, tail := -1, ""
		var readyAt time.Time
		if p != nil {
			if err := s.initialize(p); err == nil {
				readyAt = time.Now()
				// ready 由转发器在当前进程代的流上报（见 forward）：此处只唤醒它。
				t.wake()
				if h := testHookAgentInitialized; h != nil {
					h(t)
				}
			} else if !s.stopped() {
				t.r.log.Warn("agent init failed", "thread", t.id, "err", err)
				p.stderr.add("orbd: agent init failed: " + err.Error())
				p.kill()
			} else {
				p.kill()
			}
			<-p.exited
			code, tail = p.exitCode, p.stderr.String()
		} else {
			tail = "orbd: spawn agent: " + spawnErr.Error()
		}
		s.lastExit = code
		if s.stopped() {
			t.setStatus(apigen.LinkCtlAgentStatusParams{Status: apigen.LinkCtlAgentStatusParamsStatusExited, ExitCode: &code})
			return
		}
		if !readyAt.IsZero() && time.Since(readyAt) >= stableReady {
			attempt = 0
		}
		left := len(t.r.cfg.RestartDelays) - attempt
		t.r.log.Warn("agent exited", "thread", t.id, "code", code, "restartsLeft", left)
		t.setStatus(apigen.LinkCtlAgentStatusParams{
			Status: apigen.LinkCtlAgentStatusParamsStatusExited, ExitCode: &code, StderrTail: &tail, RestartsLeft: &left,
		})
		if left <= 0 {
			return
		}
		delay := t.r.cfg.RestartDelays[attempt]
		attempt++
		tm := time.NewTimer(delay)
		select {
		case <-tm.C:
		case <-s.stopCh:
			tm.Stop()
			return
		}
		p, spawnErr = s.spawn()
		if p == nil && s.stopped() {
			return
		}
	}
}

// spawn 启动一个 omp 进程（C4 第 1 步）。
func (s *supervisor) spawn() (*proc, error) {
	t := s.t
	if s.stopped() {
		return nil, errors.New("stopped")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd := exec.Command(t.r.cfg.AgentBin, s.spec.args...)
	cmd.Dir = s.spec.cwd
	cmd.Env = s.spec.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	inR.Close()
	outW.Close()
	errW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, err
	}
	t.mu.Lock()
	t.epoch++
	p := &proc{
		epoch:      t.epoch,
		pid:        cmd.Process.Pid,
		cmd:        cmd,
		stdin:      inW,
		w:          omprpc.NewWriter(inW, 0),
		readyCh:    make(chan struct{}),
		sawReady:   make(chan struct{}),
		exited:     make(chan struct{}),
		stdoutDone: make(chan struct{}),
		waiters:    map[string]chan omprpc.Envelope{},
		stderr:     &lineRing{max: stderrTailLines},
	}
	t.proc = p
	t.mu.Unlock()
	t.setStatus(apigen.LinkCtlAgentStatusParams{Status: apigen.LinkCtlAgentStatusParamsStatusStarting})
	t.wake()

	stderrDone := make(chan struct{})
	go p.readStdout(t, outR)
	go func() {
		defer close(stderrDone)
		p.stderr.consume(errR)
	}()
	go func() {
		_ = cmd.Wait()
		// 子进程（omp 的工具进程）可能继承了管道：主进程退出后给 2s 读完，再强行关闭。
		waitOrClose(p.stdoutDone, outR)
		waitOrClose(stderrDone, errR)
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		p.exitCode = exitCodeOf(cmd.ProcessState)
		_ = inW.Close()
		t.mu.Lock()
		if t.proc == p {
			t.proc = nil
		}
		t.mu.Unlock()
		close(p.exited)
		t.wake()
	}()
	return p, nil
}

func waitOrClose(done <-chan struct{}, f *os.File) {
	tm := time.NewTimer(2 * time.Second)
	defer tm.Stop()
	select {
	case <-done:
	case <-tm.C:
		_ = f.Close()
		<-done
	}
	_ = f.Close()
}

func exitCodeOf(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// initialize 执行 C4 第 2–3 步：等 ready → negotiate_protocol v2 → 依次发送 initFrames 并等待应答。
func (s *supervisor) initialize(p *proc) error {
	timeout := s.t.r.cfg.InitTimeout
	tm := time.NewTimer(timeout)
	defer tm.Stop()
	select {
	case <-p.sawReady:
	case <-p.exited:
		return errors.New("agent exited before ready")
	case <-s.stopCh:
		return errors.New("stopped")
	case <-tm.C:
		return errors.New("timed out waiting for ready")
	}
	if err := s.call(p, []byte(`{"type":"negotiate_protocol","protocolVersion":2}`), initIDPrefix+"negotiate"); err != nil {
		return err
	}
	for i, f := range s.spec.initFrames {
		if err := s.call(p, f, fmt.Sprintf("%sinit-%d", initIDPrefix, i)); err != nil {
			return err
		}
	}
	close(p.readyCh)
	return nil
}

func (s *supervisor) call(p *proc, frame []byte, id string) error {
	frame, err := omprpc.SetID(frame, id)
	if err != nil {
		return err
	}
	var typ struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(frame, &typ)
	ch := make(chan omprpc.Envelope, 1)
	p.mu.Lock()
	p.waiters[id] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, id)
		p.mu.Unlock()
	}()
	if err := p.w.WriteRaw(frame); err != nil {
		return fmt.Errorf("%s: write: %w", typ.Type, err)
	}
	tm := time.NewTimer(s.t.r.cfg.InitTimeout)
	defer tm.Stop()
	select {
	case env := <-ch:
		if !env.OK() {
			return fmt.Errorf("%s failed: %s", typ.Type, env.ErrorText())
		}
		return nil
	case <-p.exited:
		return fmt.Errorf("%s: agent exited", typ.Type)
	case <-s.stopCh:
		return errors.New("stopped")
	case <-tm.C:
		return fmt.Errorf("%s: timed out", typ.Type)
	}
}

// ---- 进程 ----

type proc struct {
	epoch int
	pid   int
	cmd   *exec.Cmd
	stdin io.WriteCloser
	w     *omprpc.Writer

	readyCh    chan struct{} // 初始化成功
	sawReady   chan struct{} // 读到 ready 帧
	exited     chan struct{}
	stdoutDone chan struct{}
	exitCode   int
	stderr     *lineRing

	mu      sync.Mutex
	waiters map[string]chan omprpc.Envelope

	ui pendingUI // 待答交互，新 agent 流上重放
}

func (p *proc) isReady() bool {
	select {
	case <-p.readyCh:
		return true
	default:
		return false
	}
}

func (p *proc) kill() { _ = syscall.Kill(-p.pid, syscall.SIGKILL) }

// readStdout 解码 omp stdout（含 rpc_chunk 重组）：ready 帧与 orbd 初始化命令的应答由本地消费，
// 其余帧进入线程帧缓冲。
func (p *proc) readStdout(t *thread, r io.Reader) {
	defer close(p.stdoutDone)
	dec := omprpc.NewDecoder(r)
	readySeen := false
	for {
		frame, err := dec.Next()
		if err != nil {
			if omprpc.IsRecoverable(err) {
				t.r.log.Warn("bad omp frame", "thread", t.id, "err", err)
				continue
			}
			return
		}
		env, err := omprpc.ParseEnvelope(frame)
		if err != nil {
			continue
		}
		if env.Type == omprpc.TypeReady && !readySeen {
			readySeen = true
			close(p.sawReady)
			continue
		}
		if env.IsResponse() && strings.HasPrefix(env.ID, initIDPrefix) {
			p.mu.Lock()
			ch := p.waiters[env.ID]
			p.mu.Unlock()
			if ch != nil {
				ch <- env
			}
			continue
		}
		p.ui.observeOut(frame)
		t.out.push(outItem{epoch: p.epoch, data: frame, droppable: isTerminalOutput(frame)})
		t.wake()
	}
}

// lineRing 保存最近 max 行文本。
type lineRing struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (l *lineRing) add(line string) {
	if len(line) > 2048 {
		line = line[:2048] + "…"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.max {
		l.lines = l.lines[len(l.lines)-l.max:]
	}
}

func (l *lineRing) consume(r io.Reader) {
	rd := omprpc.NewReader(r, 64<<10)
	for {
		line, err := rd.ReadLine()
		if err != nil {
			if errors.Is(err, omprpc.ErrLineTooLong) {
				continue
			}
			return
		}
		l.add(string(bytes.ToValidUTF8(line, []byte("?"))))
	}
}

func (l *lineRing) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
