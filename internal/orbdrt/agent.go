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
}

// outbox 是 omp → 控制面的帧队列；超过 32MiB 时整体丢弃并记录 gap。
type outbox struct {
	mu    sync.Mutex
	items []outItem
	size  int
	gap   bool
	seq   uint64
}

func (o *outbox) push(it outItem) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seq++
	it.seq = o.seq
	if o.size > 0 && o.size+len(it.data) > maxBufferedFrames {
		o.items, o.size, o.gap = nil, 0, true
	}
	o.items = append(o.items, it)
	o.size += len(it.data)
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

func (o *outbox) takeGap() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	g := o.gap
	o.gap = false
	return g
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
}

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
			// 已就绪的 agent 换了新流（重连）：再报一次 ready，让控制面知道新流可用；
			// 并重放仍待答的交互（控制面可能已重启、丢了它们）。重复的请求按 id 去重，无副作用。
			if sp != nil && sp.isReady() {
				t.setStatus(apigen.LinkCtlAgentStatusParams{Status: apigen.LinkCtlAgentStatusParamsStatusReady})
				for _, f := range sp.ui.replay() {
					t.out.push(outItem{epoch: sp.epoch, data: f})
				}
			}
		}
		if st != nil && t.out.takeGap() {
			_ = c.ctl.Notify("agent.gap", apigen.LinkCtlAgentGapParams{ThreadID: t.id})
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

// pumpCommands 把控制面写来的命令帧原样写入 omp stdin；进程就绪之前的命令等待就绪。
// p 为 nil（流属于已退出的进程代）时丢弃。
func (t *thread) pumpCommands(s *link.Stream, p *proc) {
	rd := omprpc.NewReader(s, maxCommandLine)
	for {
		line, err := rd.ReadLine()
		if err != nil {
			if errors.Is(err, omprpc.ErrLineTooLong) {
				t.r.log.Warn("command frame too long; dropped", "thread", t.id)
				continue
			}
			return
		}
		if p == nil {
			continue
		}
		select {
		case <-p.readyCh:
		case <-p.exited:
			continue
		}
		p.ui.observeIn(line)
		if err := p.w.WriteRaw(line); err != nil {
			t.r.log.Debug("write omp stdin failed", "thread", t.id, "err", err)
		}
	}
}

// ---- agent.start / agent.stop ----

func (r *Runtime) agentStart(p apigen.LinkCtlAgentStartParams) (apigen.LinkCtlAgentStartResult, error) {
	if p.ThreadID == "" || strings.ContainsAny(p.ThreadID, "/\\") || p.ThreadID == "." || p.ThreadID == ".." {
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
	env["PI_RPC_EMIT_TITLE"] = "1"
	env["PI_NOTIFICATIONS"] = "off"
	env["ORYN_THREAD_ID"] = p.ThreadID
	for k, v := range p.Env {
		env[k] = v
	}
	pid, err := t.start(startSpec{
		args:       []string{"--mode", "rpc-ui", "--cwd", cwd, "--session-dir", dir},
		cwd:        cwd,
		env:        envList(env),
		initFrames: frames,
	})
	if err != nil {
		return apigen.LinkCtlAgentStartResult{}, ctlErr("failed", "start agent: %v", err)
	}
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
				t.setStatus(apigen.LinkCtlAgentStatusParams{Status: apigen.LinkCtlAgentStatusParamsStatusReady})
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
		t.out.push(outItem{epoch: p.epoch, data: frame})
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
