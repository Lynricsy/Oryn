package threads

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 控制面重启不丢已受理的工作（CP5）：
//   - 首批命令（Create 的首条 prompt 及其图片、set_session_name）与线程行在同一事务写入 threads.startup_frames，
//     agent 对 prompt 作出应答后才清除；重启后重新排队。
//   - fork 的后台阶段写入 threads.fork_op，每完成一步推进一次；重启后由 recoverForks / 线程加载续作。

// fork 续作阶段。
const (
	// forkStageSnapshot：fork copy 待对源 orb 做快照（线程尚无 orb）。
	forkStageSnapshot = "snapshot"
	// forkStageCreateOrb：快照已完成（SnapshotID），待以它新建 orb。
	forkStageCreateOrb = "create_orb"
	// forkStageCopy：orb 已绑定，agent 首次启动前须 fs.copyTree 源线程会话目录（之后若 ForkAt 非空进入 fork）。
	forkStageCopy = "copy"
	// forkStageFork：会话目录已就位，agent 就绪后须发 fork{entryId: ForkAt}。
	forkStageFork = "fork"
)

// forkOp 是持久化的 fork 续作（threads.fork_op）。
type forkOp struct {
	Stage string `json:"stage"`
	// Src 是源线程；SrcOrb 是源 orb（快照来源）。
	Src    string `json:"src"`
	SrcOrb string `json:"srcOrb,omitempty"`
	// ForkAt 非空时 agent 就绪后发 fork{entryId}。
	ForkAt     string `json:"forkAt,omitempty"`
	SnapshotID string `json:"snapshotId,omitempty"`
	Type       string `json:"type,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
}

func parseForkOp(b []byte) *forkOp {
	if len(b) == 0 {
		return nil
	}
	var op forkOp
	if json.Unmarshal(b, &op) != nil || op.Stage == "" {
		return nil
	}
	return &op
}

func (op *forkOp) bytes() []byte {
	if op == nil {
		return nil
	}
	b, _ := json.Marshal(op)
	return b
}

// setForkOpLocked 持久化 fork 续作阶段（nil = 已完成）。调用方持有 t.mu。
func (s *Service) setForkOpLocked(ctx context.Context, t *thread, op *forkOp) error {
	if err := s.st.Q.SetThreadForkOp(ctx, db.SetThreadForkOpParams{ForkOp: op.bytes(), ID: t.id}); err != nil {
		slog.Warn("threads: 写入 fork 续作失败", "thread", t.id, "err", err)
		return err
	}
	t.forkOp = op
	return nil
}

// startupFrames 把首批命令编码成 threads.startup_frames。
func startupFrames(frames ...[]byte) []byte {
	raw := make([]json.RawMessage, 0, len(frames))
	for _, f := range frames {
		raw = append(raw, f)
	}
	b, _ := json.Marshal(raw)
	return b
}

// pendingStartup 返回线程行里仍未被 agent 确认的首批命令。主会话里已有消息条目说明 prompt 在重启前已被 agent
// 接收（应答丢在了重启里），此时清除记录而不重发，免得同一提示词执行两遍。
func (s *Service) pendingStartup(ctx context.Context, row db.Thread) []json.RawMessage {
	if len(row.StartupFrames) == 0 {
		return nil
	}
	var frames []json.RawMessage
	if err := json.Unmarshal(row.StartupFrames, &frames); err != nil {
		slog.Warn("threads: 无法解析首批命令，丢弃", "thread", row.ID, "err", err)
		frames = nil
	}
	if n, err := s.st.Q.CountThreadMessageEntries(ctx, row.ID); err == nil && n > 0 {
		frames = nil
	}
	if len(frames) == 0 {
		_ = s.st.Q.SetThreadStartupFrames(ctx, db.SetThreadStartupFramesParams{StartupFrames: nil, ID: row.ID})
	}
	return frames
}

// undeliveredError 是 orbd 为写进已退出进程的 agent 流的命令合成的失败应答文本（internal/orbdrt）：
// 命令没有到达 agent，不算 agent 的确认。
const undeliveredError = "agent process exited before the command was delivered"

// undelivered 报告应答是否是 orbd 合成的「未送达」失败。
func undelivered(env omprpc.Envelope) bool {
	return !env.OK() && env.ErrorText() == undeliveredError
}

// queueStartupLocked 排入首批命令；prompt 收到 agent 的应答后清除持久化记录。
// 应答是「未送达」时（进程在命令到达前退出）重新排队，等重启后的进程就绪再发。调用方持有 t.mu。
func (s *Service) queueStartupLocked(t *thread, frames []json.RawMessage) {
	for _, f := range frames {
		s.queueStartupFrameLocked(t, f, false)
	}
}

// queueStartupFrameLocked 排入一条首批命令；hold 为真时只进队列、不立即写流（「未送达」后的重排：
// 此时 agent 状态可能还没变成 exited，直接写会再次落到已退出的进程上）。
func (s *Service) queueStartupFrameLocked(t *thread, f json.RawMessage, hold bool) {
	typ, _, ok := frameHeader(f)
	if !ok {
		return
	}
	p := &pendingCmd{cmdType: typ}
	p.after = func(t *thread, env omprpc.Envelope, _ json.RawMessage) {
		if undelivered(env) {
			s.queueStartupFrameLocked(t, f, true)
			return
		}
		if typ != "prompt" {
			return
		}
		if err := s.st.Q.SetThreadStartupFrames(context.Background(), db.SetThreadStartupFramesParams{StartupFrames: nil, ID: t.id}); err != nil {
			slog.Warn("threads: 清除首批命令失败", "thread", t.id, "err", err)
		}
	}
	// 每次排队用新 id：frame 本身不带 id（SetID 只作用于副本）
	if hold {
		line, err := omprpc.SetID(append([]byte(nil), f...), s.newCmdID())
		if err != nil {
			return
		}
		_, id, _ := frameHeader(line)
		t.pending[id] = p
		t.queue = append([][]byte{line}, t.queue...)
		t.settled = false
		return
	}
	if err := s.enqueueLocked(t, append([]byte(nil), f...), p); err != nil {
		slog.Warn("threads: 首批命令排队失败", "thread", t.id, "err", err)
	}
}

// recoverForks 在启动时续作停在快照 / 建 orb 阶段的 fork copy（其余阶段随线程加载与 orb 事件续作）。
func (s *Service) recoverForks(ctx context.Context) {
	rows, err := s.st.Q.ListThreadsWithForkOp(ctx)
	if err != nil {
		slog.Warn("threads: 读取未完成的 fork 失败", "err", err)
		return
	}
	for _, r := range rows {
		t, err := s.get(ctx, r.ID)
		if err != nil {
			continue
		}
		t.mu.Lock()
		op := t.forkOp
		orbID := t.orbID
		t.mu.Unlock()
		if op == nil {
			continue
		}
		switch op.Stage {
		case forkStageSnapshot, forkStageCreateOrb:
			s.runForkCopy(ctx, t, *op)
		default:
			if orbID != "" && s.orbState(orbID) == orbStateReady {
				s.startAgent(ctx, t.id)
			}
		}
	}
}

// runForkCopy 推进 fork copy 的后台阶段：快照 → 以快照新建（或复用已建的）orb 并绑定 → orb 就绪后由 startAgent 复制目录。
func (s *Service) runForkCopy(ctx context.Context, t *thread, op forkOp) {
	t.mu.Lock()
	if t.forkRunning {
		t.mu.Unlock()
		return
	}
	t.forkRunning = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.forkRunning = false
		t.mu.Unlock()
	}()
	if op.Stage == forkStageSnapshot {
		snap, err := s.orbs.Snapshot(ctx, op.SrcOrb)
		if err != nil {
			if ctx.Err() != nil {
				return // 控制面关闭：保留阶段，重启后续作
			}
			slog.Warn("threads: fork copy 快照失败", "orb", op.SrcOrb, "err", err)
			s.failFork(ctx, t)
			return
		}
		op.Stage, op.SnapshotID = forkStageCreateOrb, snap
		t.mu.Lock()
		err = s.setForkOpLocked(ctx, t, &op)
		t.mu.Unlock()
		if err != nil {
			return
		}
	}
	// 重启前可能已建好 orb 但没来得及绑定：复用它，不再建第二台
	o, err := s.st.Q.FindUnboundSnapshotOrb(ctx, db.FindUnboundSnapshotOrbParams{SnapshotOf: &op.SrcOrb, Image: op.SnapshotID})
	if err != nil {
		o, err = s.orbs.Create(ctx, OrbCreate{Type: op.Type, ProjectID: op.ProjectID, SnapshotOf: op.SrcOrb, SnapshotID: op.SnapshotID})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("threads: fork copy 新建 orb 失败", "err", err)
			s.failFork(ctx, t)
			return
		}
	}
	next := forkOp{Stage: forkStageCopy, Src: op.Src, SrcOrb: op.SrcOrb, ForkAt: op.ForkAt}
	t.mu.Lock()
	if err := s.st.Q.SetThreadOrbAndForkOp(ctx, db.SetThreadOrbAndForkOpParams{OrbID: &o.ID, ForkOp: next.bytes(), UpdatedAt: s.now(), ID: t.id}); err != nil {
		t.mu.Unlock()
		slog.Warn("threads: 绑定 fork copy 的 orb 失败", "thread", t.id, "err", err)
		return
	}
	t.orbID, t.forkOp = o.ID, &next
	t.copyFrom, t.forkEntry = next.Src, next.ForkAt
	// 同 Create：绑定前已发生的 orb 状态变化（如 failed）在此补算
	if !s.recompute(ctx, t) {
		s.publish(ctx, t.id)
	}
	t.mu.Unlock()
	if s.orbState(o.ID) == orbStateReady {
		s.startAgent(ctx, t.id)
	}
}

// failFork 结束无法继续的 fork：线程进入 error，清除续作（不在每次重启时重试注定失败的操作）。
func (s *Service) failFork(ctx context.Context, t *thread) {
	t.mu.Lock()
	_ = s.setForkOpLocked(ctx, t, nil)
	t.copyFrom, t.forkEntry = "", ""
	t.mu.Unlock()
	s.markFailed(ctx, t)
}
