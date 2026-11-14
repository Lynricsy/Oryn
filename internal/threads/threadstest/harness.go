// Package threadstest 提供线程服务的测试替身：Orbs 用真实的 orb link（httptest + link.Accept / link.Dial）
// 实现 threads.Orbs，并在 link 另一端扮演 orbd——应答 agent.start、拉起真实的 fakeomp 进程、
// 开 agent 流转发帧、以一个小 tailer 把会话文件行经 entries.append 复制到控制面。仅供测试使用。
package threadstest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// BuildFakeOmp 把 cmd/fakeomp 编译到 dir 下并返回可执行文件路径。
func BuildFakeOmp(dir string) (string, error) {
	bin := filepath.Join(dir, "fakeomp")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/Lynricsy/Oryn/cmd/fakeomp")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build fakeomp: %v\n%s", err, out)
	}
	return bin, nil
}

// Orbs 是 threads.Orbs 的测试实现。
type Orbs struct {
	st      *store.Store
	srv     *httptest.Server
	bin     string
	home    string
	env     []string
	provID  string
	closeCh chan struct{}

	mu       sync.Mutex
	reqs     map[string]func(context.Context, string, json.RawMessage) (any, error)
	notifies map[string]func(context.Context, string, json.RawMessage)
	streams  map[string]func(context.Context, string, link.StreamHeader, *link.Stream)
	subs     []func(threads.OrbEvent)
	links    map[string]*serverLink
	orbds    map[string]*Orbd
	// Calls 记录 fake orbd 收到的 ctl 请求方法（按到达顺序）。
	calls []string
	// Snapshots 记录 Snapshot 调用的源 orb。
	snapshots []string
	blobs     map[string][]byte
	// hold 为真时新建 / 唤醒的 orb 停在 requested，直到 Release（模拟 provider 尚未执行 spawn）。
	hold     bool
	held     map[string]bool
	snapGate chan struct{}
	// features 是 HasFeature 对任意 orb 报告的特性（M9：协调线程要求 "coordinator"）。
	features map[string]bool
	// starts 记录每个线程收到的 agent.start 参数（按到达顺序）。
	starts map[string][]apigen.LinkCtlAgentStartParams
	// deletes 记录 Delete 调用的 orb（按调用顺序）。
	deletes []string
	// gates 让 fake orbd 收到某个 ctl 方法时先阻塞（GateCall）。
	gates map[string]*callGate
}

// callGate 是一个 ctl 方法的闸门：请求到达时关闭 arrived，等 release 关闭后才处理。
type callGate struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

// GateCall 让 fake orbd 下一次（及之后每次）收到 method 时阻塞到 release 被调用；arrived 在第一次到达时关闭。
// release 之后闸门移除。
func (o *Orbs) GateCall(method string) (arrived <-chan struct{}, release func()) {
	g := &callGate{arrived: make(chan struct{}), release: make(chan struct{})}
	o.mu.Lock()
	if o.gates == nil {
		o.gates = map[string]*callGate{}
	}
	o.gates[method] = g
	o.mu.Unlock()
	return g.arrived, func() {
		o.mu.Lock()
		if o.gates[method] == g {
			delete(o.gates, method)
		}
		o.mu.Unlock()
		close(g.release)
	}
}

type serverLink struct {
	sess *link.Session
	ctl  *link.Ctl
}

// NewOrbs 创建测试 Orbs；home 为各 orb 的 $HOME 根（每个 orb 一个子目录），env 追加到 fakeomp 进程环境。
func NewOrbs(st *store.Store, fakeompBin, home string, env ...string) (*Orbs, error) {
	o := &Orbs{
		st: st, bin: fakeompBin, home: home, env: env, closeCh: make(chan struct{}),
		reqs:     map[string]func(context.Context, string, json.RawMessage) (any, error){},
		notifies: map[string]func(context.Context, string, json.RawMessage){},
		streams:  map[string]func(context.Context, string, link.StreamHeader, *link.Stream){},
		links:    map[string]*serverLink{},
		orbds:    map[string]*Orbd{},
		blobs:    map[string][]byte{},
		held:     map[string]bool{},
		features: map[string]bool{},
		starts:   map[string][]apigen.LinkCtlAgentStartParams{},
	}
	o.provID = ids.New("prov")
	if err := st.Q.CreateProvider(context.Background(), db.CreateProviderParams{ID: o.provID, Name: "test", TokenHash: ids.Hash("opv_test"), CreatedAt: time.Now().UnixMilli()}); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(link.Path, o.serveLink)
	o.srv = httptest.NewServer(mux)
	return o, nil
}

// Close 关闭全部 link 与 agent 进程。
func (o *Orbs) Close() {
	close(o.closeCh)
	o.mu.Lock()
	orbds := make([]*Orbd, 0, len(o.orbds))
	for _, d := range o.orbds {
		orbds = append(orbds, d)
	}
	o.mu.Unlock()
	for _, d := range orbds {
		d.Close()
	}
	o.srv.Close()
}

// Calls 返回 fake orbd 收到的 ctl 请求方法。
func (o *Orbs) Calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.calls...)
}

// Snapshots 返回 Snapshot 调用过的源 orb。
func (o *Orbs) Snapshots() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.snapshots...)
}

// Deletes 返回 Delete 调用过的 orb（按调用顺序）。
func (o *Orbs) Deletes() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.deletes...)
}

// AddBlob 让所有 fake orbd 能经 blob.fetch 提供该内容。
func (o *Orbs) AddBlob(sha string, data []byte) {
	o.mu.Lock()
	o.blobs[sha] = data
	o.mu.Unlock()
}

// OrbdCtl 返回 fake orbd 一侧的 ctl（用于模拟越权请求）。
func (o *Orbs) OrbdCtl(orbID string) *link.Ctl {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d := o.orbds[orbID]; d != nil {
		return d.ctl
	}
	return nil
}

// Home 返回 orb 的 $HOME。
func (o *Orbs) Home(orbID string) string { return filepath.Join(o.home, orbID) }

func (o *Orbs) setState(orbID, state string) {
	ctx := context.Background()
	prev, err := o.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return
	}
	_ = o.st.Q.SetOrbState(ctx, db.SetOrbStateParams{State: state, StateChangedAt: time.Now().UnixMilli(), ID: orbID})
	cur, _ := o.st.Q.GetOrb(ctx, orbID)
	o.emit(threads.OrbEvent{OrbID: orbID, Kind: threads.OrbEventState, From: prev.State, To: state, Orb: cur})
}

func (o *Orbs) emit(ev threads.OrbEvent) {
	o.mu.Lock()
	subs := append([]func(threads.OrbEvent){}, o.subs...)
	o.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

// Create 插入 orb 行并在后台拉起 fake orbd；link 建立后 orb 进入 ready。
func (o *Orbs) Create(ctx context.Context, p threads.OrbCreate) (db.Orb, error) {
	id := ids.Orb()
	row := db.CreateOrbParams{ID: id, ProviderID: o.provID, Type: "medium", Cpus: 4, MemoryMb: 8192, Image: "test",
		State: "requested", StateChangedAt: time.Now().UnixMilli(), TokenHash: ids.Hash("oot_" + id), CreatedAt: time.Now().UnixMilli(),
		Coordinator: p.Coordinator}
	if p.ProjectID != "" {
		row.ProjectID = &p.ProjectID
	}
	if p.SnapshotID != "" {
		row.Image = p.SnapshotID
	}
	if p.SnapshotOf != "" {
		row.SnapshotOf = &p.SnapshotOf
		// 快照副本：复制源 orb 的 $HOME
		if err := copyTree(o.Home(p.SnapshotOf), o.Home(id)); err != nil {
			return db.Orb{}, err
		}
	}
	if err := o.st.Q.CreateOrb(ctx, row); err != nil {
		return db.Orb{}, err
	}
	orb, err := o.st.Q.GetOrb(ctx, id)
	if err != nil {
		return db.Orb{}, err
	}
	o.mu.Lock()
	hold := o.hold
	if hold {
		o.held[id] = true
	}
	o.mu.Unlock()
	if !hold {
		go o.startOrbd(id)
	}
	return orb, nil
}

// Hold 让之后新建的 orb 停在 requested，直到 Release。
func (o *Orbs) Hold(on bool) {
	o.mu.Lock()
	o.hold = on
	o.mu.Unlock()
}

// Release 放行被 Hold 住的 orb（拉起其 fake orbd）。
func (o *Orbs) Release(orbID string) {
	o.mu.Lock()
	held := o.held[orbID]
	delete(o.held, orbID)
	o.mu.Unlock()
	if held {
		go o.startOrbd(orbID)
	}
}

// GateSnapshots 让之后的 Snapshot 调用阻塞到 ctx 结束（模拟快照进行中控制面重启）；off 时恢复立即返回。
func (o *Orbs) GateSnapshots(on bool) {
	o.mu.Lock()
	if on {
		o.snapGate = make(chan struct{})
	} else {
		o.snapGate = nil
	}
	o.mu.Unlock()
}

// Rebind 让新的线程服务接管已有 link（模拟控制面重启：旧服务关闭后，新服务重新注册处理器并订阅事件）。
// 只保留最后注册的订阅者。
func (o *Orbs) Rebind() {
	o.mu.Lock()
	o.subs = nil
	o.mu.Unlock()
}

// OpenBlob 以 orb 的身份主动打开一条 blob 流并写入 data，等服务端关闭该流后返回（用于测试未请求的 blob）。
func (o *Orbs) OpenBlob(ctx context.Context, orbID, sha string, data []byte) error {
	o.mu.Lock()
	d := o.orbds[orbID]
	o.mu.Unlock()
	if d == nil {
		return errors.New("no orbd")
	}
	st, err := d.sess.Open(ctx, link.StreamHeader{T: link.StreamBlob, SHA256: sha, Size: int64(len(data))})
	if err != nil {
		return err
	}
	_, _ = st.Write(data)
	_, _ = io.Copy(io.Discard, st)
	return st.Close()
}

func (o *Orbs) startOrbd(orbID string) {
	time.Sleep(20 * time.Millisecond)
	o.setState(orbID, "connecting")
	d, err := dialOrbd(o, orbID)
	if err != nil {
		o.setState(orbID, "failed")
		return
	}
	o.mu.Lock()
	o.orbds[orbID] = d
	o.mu.Unlock()
	o.setState(orbID, "ready")
}

func (o *Orbs) serveLink(w http.ResponseWriter, r *http.Request) {
	var orbID string
	sess, err := link.Accept(w, r, func(_ *http.Request, token string) error {
		orbID = strings.TrimPrefix(token, "oot_")
		if _, err := o.st.Q.GetOrb(r.Context(), orbID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := sess.AcceptStream(ctx)
	if err != nil || st.Header.T != link.StreamCtl {
		_ = sess.Close()
		return
	}
	ctl := link.NewCtl(st, link.CtlHandlers{
		OnRequest: func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			o.mu.Lock()
			fn := o.reqs[method]
			o.mu.Unlock()
			if fn == nil {
				return nil, link.ErrMethodNotFound
			}
			return fn(ctx, orbID, params)
		},
		OnNotify: func(ctx context.Context, method string, params json.RawMessage) {
			o.mu.Lock()
			fn := o.notifies[method]
			o.mu.Unlock()
			if fn != nil {
				fn(ctx, orbID, params)
			}
		},
	})
	o.mu.Lock()
	o.links[orbID] = &serverLink{sess: sess, ctl: ctl}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		if l := o.links[orbID]; l != nil && l.sess == sess {
			delete(o.links, orbID)
		}
		o.mu.Unlock()
	}()
	for {
		st, err := sess.AcceptStream(ctx)
		if err != nil {
			return
		}
		o.mu.Lock()
		fn := o.streams[st.Header.T]
		o.mu.Unlock()
		go func() {
			defer st.Close()
			if fn != nil {
				fn(ctx, orbID, st.Header, st)
			}
		}()
	}
}

// Wake 让非 ready 的 orb 回到 ready（重建 link）。
func (o *Orbs) Wake(_ context.Context, orbID string) error {
	orb, err := o.st.Q.GetOrb(context.Background(), orbID)
	if err != nil {
		return err
	}
	if orb.State == "ready" {
		return nil
	}
	o.mu.Lock()
	held := o.held[orbID]
	o.mu.Unlock()
	if !held {
		go o.startOrbd(orbID)
	}
	return nil
}

// Snapshot 记录调用并返回一个快照 id（GateSnapshots 打开时阻塞到 ctx 结束）。
func (o *Orbs) Snapshot(ctx context.Context, orbID string) (string, error) {
	o.mu.Lock()
	o.snapshots = append(o.snapshots, orbID)
	gate := o.snapGate
	o.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return ids.Snapshot(), nil
}

// Ctl 实现 threads.Orbs。
func (o *Orbs) Ctl(orbID string) (*link.Ctl, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	l := o.links[orbID]
	if l == nil {
		return nil, false
	}
	return l.ctl, true
}

// HandleRequest 实现 threads.Orbs。
func (o *Orbs) HandleRequest(method string, fn func(context.Context, string, json.RawMessage) (any, error)) {
	o.mu.Lock()
	o.reqs[method] = fn
	o.mu.Unlock()
}

// HandleNotify 实现 threads.Orbs。
func (o *Orbs) HandleNotify(method string, fn func(context.Context, string, json.RawMessage)) {
	o.mu.Lock()
	o.notifies[method] = fn
	o.mu.Unlock()
}

// HandleStream 实现 threads.Orbs。
func (o *Orbs) HandleStream(t string, fn func(context.Context, string, link.StreamHeader, *link.Stream)) {
	o.mu.Lock()
	o.streams[t] = fn
	o.mu.Unlock()
}

// Subscribe 实现 threads.Orbs。
func (o *Orbs) Subscribe(fn func(threads.OrbEvent)) {
	o.mu.Lock()
	o.subs = append(o.subs, fn)
	o.mu.Unlock()
}

// TouchUser 实现 threads.Orbs。
func (o *Orbs) TouchUser(context.Context, string) {}

// TouchAgent 实现 threads.Orbs。
func (o *Orbs) TouchAgent(context.Context, string) {}

// SetQuiescence 实现 threads.Orbs。
func (o *Orbs) SetQuiescence(func(string, func() bool) bool) {}

// SetFeature 设置 HasFeature 对全部 orb 报告的特性。
func (o *Orbs) SetFeature(feature string, on bool) {
	o.mu.Lock()
	o.features[feature] = on
	o.mu.Unlock()
}

// HasFeature 实现 threads.Orbs：有 link 且该特性已用 SetFeature 打开。
func (o *Orbs) HasFeature(orbID, feature string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.links[orbID] != nil && o.features[feature]
}

// Starts 返回线程收到过的 agent.start 参数。
func (o *Orbs) Starts(threadID string) []apigen.LinkCtlAgentStartParams {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]apigen.LinkCtlAgentStartParams(nil), o.starts[threadID]...)
}

// Kick 关闭 orb 的 link。
func (o *Orbs) Kick(orbID, _ string) {
	o.mu.Lock()
	l := o.links[orbID]
	o.mu.Unlock()
	if l != nil {
		_ = l.sess.Close()
	}
}

// Delete 实现 threads.Orbs：记录调用，关闭 fake orbd，orb 行进入 deleted（已删除时无操作）。
func (o *Orbs) Delete(ctx context.Context, orbID string) error {
	orb, err := o.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.deletes = append(o.deletes, orbID)
	d := o.orbds[orbID]
	delete(o.orbds, orbID)
	delete(o.held, orbID)
	o.mu.Unlock()
	if orb.State == "deleted" {
		return nil
	}
	if d != nil {
		d.Close()
	}
	o.setState(orbID, "deleted")
	return nil
}

// ---- fake orbd ----

// Orbd 是 link 另一端的 orbd 替身。
type Orbd struct {
	o     *Orbs
	orbID string
	home  string
	sess  *link.Session
	ctl   *link.Ctl
	ctx   context.Context
	stop  context.CancelFunc

	mu     sync.Mutex
	agents map[string]*agentProc
}

type agentProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
}

func dialOrbd(o *Orbs, orbID string) (*Orbd, error) {
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := link.Dial(ctx, link.URL(o.srv.URL), "oot_"+orbID)
	if err != nil {
		cancel()
		return nil, err
	}
	d := &Orbd{o: o, orbID: orbID, home: o.Home(orbID), sess: sess, ctx: ctx, stop: cancel, agents: map[string]*agentProc{}}
	if err := os.MkdirAll(filepath.Join(d.home, "workspace"), 0o755); err != nil {
		cancel()
		return nil, err
	}
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamCtl})
	if err != nil {
		cancel()
		return nil, err
	}
	d.ctl = link.NewCtl(st, link.CtlHandlers{OnRequest: d.onRequest})
	_ = d.ctl.Notify("hello", apigen.LinkCtlHelloParams{OrbdVersion: "test", AgentVersion: "fakeomp", Os: "linux", Arch: "amd64", Threads: []apigen.LinkCtlHelloThread{}})
	// 等服务端登记 link
	for range 200 {
		if _, ok := o.Ctl(orbID); ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return d, nil
}

// Close 结束全部 agent 并断开 link。
func (d *Orbd) Close() {
	d.mu.Lock()
	agents := d.agents
	d.agents = map[string]*agentProc{}
	d.mu.Unlock()
	for _, a := range agents {
		a.kill()
	}
	d.stop()
	_ = d.sess.Close()
}

func (a *agentProc) kill() {
	_ = a.stdin.Close()
	select {
	case <-a.done:
	case <-time.After(3 * time.Second):
		_ = a.cmd.Process.Kill()
		<-a.done
	}
}

func (d *Orbd) resolve(p string) string {
	switch {
	case p == "~":
		return d.home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(d.home, p[2:])
	case filepath.IsAbs(p):
		return p
	}
	return filepath.Join(d.home, p)
}

func (d *Orbd) onRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	d.o.mu.Lock()
	d.o.calls = append(d.o.calls, method)
	g := d.o.gates[method]
	d.o.mu.Unlock()
	if g != nil {
		g.once.Do(func() { close(g.arrived) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	switch method {
	case "agent.start":
		var p apigen.LinkCtlAgentStartParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		d.o.mu.Lock()
		d.o.starts[p.ThreadID] = append(d.o.starts[p.ThreadID], p)
		d.o.mu.Unlock()
		return d.agentStart(p)
	case "agent.stop":
		var p apigen.LinkCtlAgentStopParams
		_ = json.Unmarshal(params, &p)
		d.mu.Lock()
		a := d.agents[p.ThreadID]
		delete(d.agents, p.ThreadID)
		d.mu.Unlock()
		if a != nil {
			a.kill()
		}
		return apigen.LinkCtlAgentStopResult{ExitCode: 0}, nil
	case "fs.copyTree", "fs.move":
		var p apigen.LinkCtlFsMoveParams
		_ = json.Unmarshal(params, &p)
		src, dst := d.resolve(p.From), d.resolve(p.To)
		var err error
		if method == "fs.move" {
			if err = os.MkdirAll(filepath.Dir(dst), 0o755); err == nil {
				err = os.Rename(src, dst)
			}
		} else {
			err = copyTree(src, dst)
		}
		if err != nil {
			return nil, &link.CtlError{Code: "failed", Message: err.Error()}
		}
		return apigen.LinkCtlOkResult{Ok: true}, nil
	case "blob.fetch":
		var p apigen.LinkCtlBlobFetchParams
		_ = json.Unmarshal(params, &p)
		d.o.mu.Lock()
		data, ok := d.o.blobs[p.Sha256]
		d.o.mu.Unlock()
		if !ok {
			return apigen.LinkCtlBlobFetchResult{Found: false}, nil
		}
		st, err := d.sess.Open(ctx, link.StreamHeader{T: link.StreamBlob, SHA256: p.Sha256, Size: int64(len(data))})
		if err != nil {
			return nil, err
		}
		go func() {
			defer st.Close()
			_, _ = st.Write(data)
		}()
		return apigen.LinkCtlBlobFetchResult{Found: true}, nil
	case "shutdown":
		return apigen.LinkCtlOkResult{Ok: true}, nil
	}
	return nil, link.ErrMethodNotFound
}

func (d *Orbd) agentStart(p apigen.LinkCtlAgentStartParams) (any, error) {
	d.mu.Lock()
	if a := d.agents[p.ThreadID]; a != nil {
		d.mu.Unlock()
		// 幂等：已在运行
		_ = d.ctl.Notify("agent.status", apigen.LinkCtlAgentStatusParams{ThreadID: p.ThreadID, Status: "ready"})
		return apigen.LinkCtlAgentStartResult{Pid: a.cmd.Process.Pid}, nil
	}
	dir := d.resolve(p.SessionDir)
	if p.SessionDir == "" {
		dir = filepath.Join(d.home, ".oryn", "threads", p.ThreadID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	cmd := exec.Command(d.o.bin, "--mode", "rpc-ui", "--cwd", filepath.Join(d.home, "workspace"), "--session-dir", dir)
	cmd.Env = append(os.Environ(), d.o.env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	a := &agentProc{cmd: cmd, stdin: stdin, done: make(chan struct{})}
	d.agents[p.ThreadID] = a
	d.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		close(a.done)
	}()
	go d.runAgent(p, dir, a, stdout)
	go d.tail(p.ThreadID, dir)
	return apigen.LinkCtlAgentStartResult{Pid: cmd.Process.Pid}, nil
}

// runAgent 执行 C4 启动序列（ready → negotiate_protocol → initFrames），然后开 agent 流双向转发。
func (d *Orbd) runAgent(p apigen.LinkCtlAgentStartParams, dir string, a *agentProc, stdout io.Reader) {
	_ = d.ctl.Notify("agent.status", apigen.LinkCtlAgentStatusParams{ThreadID: p.ThreadID, Status: "starting"})
	dec := omprpc.NewDecoder(stdout)
	w := omprpc.NewWriter(a.stdin, omprpc.MaxReassembledBytes)
	if _, err := dec.Next(); err != nil { // ready
		return
	}
	call := func(frame map[string]any, id string) error {
		frame["id"] = id
		if err := w.WriteFrame(frame); err != nil {
			return err
		}
		for {
			f, err := dec.Next()
			if err != nil {
				return err
			}
			env, _ := omprpc.ParseEnvelope(f)
			if env.IsResponse() && env.ID == id {
				if !env.OK() {
					return errors.New(env.ErrorText())
				}
				return nil
			}
		}
	}
	if err := call(map[string]any{"type": "negotiate_protocol", "protocolVersion": 2}, "init-neg"); err != nil {
		return
	}
	for i, f := range p.InitFrames {
		frame := map[string]any(f)
		if frame["type"] == "open_session" {
			frame["sessionDir"] = dir
		}
		if err := call(frame, fmt.Sprintf("init-%d", i)); err != nil {
			code := 1
			_ = d.ctl.Notify("agent.status", apigen.LinkCtlAgentStatusParams{ThreadID: p.ThreadID, Status: "exited", ExitCode: &code})
			return
		}
	}
	st, err := d.sess.Open(d.ctx, link.StreamHeader{T: link.StreamAgent, ThreadID: p.ThreadID})
	if err != nil {
		return
	}
	_ = d.ctl.Notify("agent.status", apigen.LinkCtlAgentStatusParams{ThreadID: p.ThreadID, Status: "ready"})
	go func() {
		r := bufio.NewReaderSize(st, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 {
				if _, werr := a.stdin.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		f, err := dec.Next()
		if err != nil {
			if omprpc.IsRecoverable(err) {
				continue
			}
			break
		}
		if _, err := st.Write(append(f, '\n')); err != nil {
			break
		}
	}
	_ = st.Close()
}

// tail 是极简 tailer：先取游标，之后每 20ms 扫描会话目录（含一级子目录）并发送新的完整行。
func (d *Orbd) tail(threadID, dir string) {
	offsets := map[string]int64{}
	var cur apigen.LinkCtlEntriesCursorsResult
	if err := d.ctl.Call(d.ctx, "entries.cursors", apigen.LinkCtlEntriesCursorsParams{ThreadID: threadID}, &cur); err != nil {
		return
	}
	for _, c := range cur.Cursors {
		offsets[c.File] = c.Offset
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.o.closeCh:
			return
		case <-tick.C:
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		sub, _ := filepath.Glob(filepath.Join(dir, "*", "*.jsonl"))
		files = append(files, sub...)
		sort.Strings(files)
		for _, abs := range files {
			rel, _ := filepath.Rel(dir, abs)
			rel = filepath.ToSlash(rel)
			b, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			off := offsets[rel]
			if off >= int64(len(b)) {
				continue
			}
			chunk := b[off:]
			last := strings.LastIndexByte(string(chunk), '\n')
			if last < 0 {
				continue
			}
			chunk = chunk[:last+1]
			lines := strings.Split(strings.TrimSuffix(string(chunk), "\n"), "\n")
			var res apigen.LinkCtlEntriesAppendResult
			err = d.ctl.Call(d.ctx, "entries.append", apigen.LinkCtlEntriesAppendParams{
				ThreadID: threadID, File: rel, BaseOffset: off, EndOffset: off + int64(len(chunk)), Lines: lines,
			}, &res)
			if err != nil {
				continue
			}
			offsets[rel] = res.Offset
		}
	}
}

func copyTree(src, dst string) error {
	fi, err := os.Stat(src)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dst, 0o755)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
