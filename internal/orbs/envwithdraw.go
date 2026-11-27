package orbs

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

// AI 数据同意（App Store 5.1.2(i)）：以环境变量降级的模型密钥让 omp 直连上游，出站代理管不到，只能不放进 orb 的环境。
// setup 只给已同意的 provider 注入（DefaultSetup.envConsent），但 orbd 的环境只在 setup.run / resume.run 时整体替换，
// 在跑的 agent 进程（及 orbd 崩溃自重启沿用的启动参数）一直带着启动时的环境。所以在两种时机收回：
//   - orynd 启动时（Run 对账之后）：旧版本不看同意一律注入，升级前已 ready 的 orb 不会再跑 setup；
//   - 同意记录变化后（PUT / DELETE ai-data-consent）：撤回不会让在跑的 orb 重新 setup。
//
// 收回 = 让 orb 重新走 connecting → resuming → ready：新的 resume.run 参数里没有被扣下的密钥，orbd 用它整体替换
// setup 环境（orbdrt runSetupLocked：setupEnv = p.Env）并重写 models.yml（占位符）；随后停掉这台 orb 上的全部 agent
// （agent.stop 结束监督器，崩溃自重启也不再沿用旧环境），orb 进入 ready 后线程服务照常重新拉起（open_session 续上原会话）。
//
// 不落库，只在内存里记两样东西（受 Service.mu 保护）：
//   - envHeld：本进程最近一次成功的 setup / resume 下发到各 orb 的降级密钥变量名。没有记录 = 不知道（升级前的旧进程或
//     本进程启动前的 setup），按「被扣下的全都在」处理——启动时会把仍在用的 orb 都过一遍（只在有被扣下的 provider 时）；
//   - envWithdraw：待收回的 provider 键。机器在跑但现在不能马上重走 setup 的 orb（link 不在、冻结、正在 setup 等）先记下，
//     由它下一次 runPhase（或 ready 状态下重新 hello 时）收回。
//
// 协调宿主不在此列：planCoordinator 丢弃降级的环境变量，orbd 的协调宿主模式也不给 agent 带 setup 环境；runner 关闭
// 环境变量降级（NoEnvFallback）；沙箱不经 setup。

// EnvWithholder 由能算出「被同意记录扣下的环境变量降级 provider」的 SetupSource 实现（DefaultSetup）。
type EnvWithholder interface {
	WithheldEnvKeys(ctx context.Context) ([]string, error)
}

// envWithdrawReason 是收回降级密钥时 orb 事件的原因。
const envWithdrawReason = "ai consent: env key withdrawn"

// envAgentStopTimeout 是收回时停一个 agent 的上限（agent.stop 给 5 秒宽限期）。
const envAgentStopTimeout = 15 * time.Second

// envAgentStopRetry / envAgentStopRetryMax 是收回时 agent 没停下的重试退避（起点与上限；测试里调小）。
var (
	envAgentStopRetry    = time.Second
	envAgentStopRetryMax = time.Minute
)

// WithdrawEnvKeys 收回 orb 环境里主人已不同意的环境变量降级密钥（见文件头）。trigger 只进日志（"startup" / "consent"）。
// 没有被扣下的 provider 时（常见情形）什么都不做；否则对机器可能还带着这些密钥的工作 orb：ready 且 link 在的立即退回
// connecting 重走 resume，其余记下待收回。可重复调用：本进程已按新同意重新下发过的 orb 不再受影响。
func (s *Service) WithdrawEnvKeys(ctx context.Context, trigger string) {
	w, ok := s.setup.(EnvWithholder)
	if !ok {
		return
	}
	keys, err := w.WithheldEnvKeys(ctx)
	if err != nil {
		slog.Warn("orbs: 计算被扣下的环境变量降级密钥失败", "trigger", trigger, "err", err)
		return
	}
	if len(keys) == 0 {
		// 没有要收回的：此前记下、还没收回的也不必再收（它们的 provider 现在已同意）
		s.mu.Lock()
		clear(s.envWithdraw)
		s.mu.Unlock()
		return
	}
	orbs, err := s.st.Q.ListLiveOrbs(ctx)
	if err != nil {
		slog.Warn("orbs: 收回环境变量降级密钥时读取 orb 失败", "trigger", trigger, "err", err)
		return
	}
	for _, orb := range orbs {
		if orb.Coordinator || IsRunner(orb) || IsSandbox(orb) {
			continue
		}
		switch orb.State {
		case StateRequested, StateStopping, StateStopped, StateDeleting:
			// 机器还没建或已停：没有在跑的进程，下次拉起时 resume 用的是新参数
			continue
		}
		hit := s.markEnvWithdraw(orb.ID, keys)
		if len(hit) == 0 {
			continue
		}
		if orb.State != StateReady || !s.Connected(orb.ID) {
			slog.Info("orbs: 记下待收回的环境变量降级密钥，orb 下次 setup / resume 后停掉 agent", "orb", orb.ID, "state", orb.State, "providers", hit, "trigger", trigger)
			continue
		}
		s.reprepareForEnv(ctx, orb.ID, hit)
	}
}

// markEnvWithdraw 记下 orb 待收回的 provider（与已记下的合并），返回这台 orb 可能持有的那部分：本进程下发过的以 envHeld
// 为准，没有记录时就是 keys 全部。
func (s *Service) markEnvWithdraw(orbID string, keys []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, known := s.envHeld[orbID]
	var hit []string
	for _, k := range keys {
		if !known || slices.Contains(held, agentcfg.EnvFallbackName(k)) {
			hit = append(hit, k)
		}
	}
	if len(hit) > 0 {
		s.envWithdraw[orbID] = mergeKeys(s.envWithdraw[orbID], hit)
	}
	return hit
}

// reprepareForEnv 把 ready 的 orb 退回 connecting，由现有 link 重走 resume（runPhase 收回记下的 provider）。状态已变时
// 不动：记下的 provider 由那次 runPhase 或之后的 hello 处理。
func (s *Service) reprepareForEnv(ctx context.Context, orbID string, providers []string) bool {
	_, moved, err := s.transition(ctx, orbID, StateReady, StateConnecting, map[string]any{"reason": envWithdrawReason, "providers": providers})
	if err != nil {
		slog.Warn("orbs: 收回环境变量降级密钥时状态更新失败", "orb", orbID, "err", err)
		return false
	}
	if moved {
		slog.Info("orbs: 收回环境变量降级密钥，orb 重走 resume 并重启 agent", "orb", orbID, "providers", providers)
		s.advance(orbID)
	}
	return moved
}

// takeEnvWithdraw 取走 orb 待收回的 provider（runPhase 在生成参数之前调用：此后的同意变化另行记下）。
func (s *Service) takeEnvWithdraw(orbID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := s.envWithdraw[orbID]
	delete(s.envWithdraw, orbID)
	return keys
}

// restoreEnvWithdraw 把没能收回的 provider 放回（runPhase 中途失败或 link 断开），由下一次 runPhase 再收。
func (s *Service) restoreEnvWithdraw(orbID string, keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envWithdraw[orbID] = mergeKeys(s.envWithdraw[orbID], keys)
}

// pendingEnvWithdraw 返回 orb 待收回的 provider（没有时为空）。
func (s *Service) pendingEnvWithdraw(orbID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.envWithdraw[orbID])
}

// noteEnvHeld 记下 setup / resume 成功后 orbd 环境里的降级密钥变量名。
func (s *Service) noteEnvHeld(orbID string, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envHeld[orbID] = slices.Clone(names)
}

// stopEnvAgents 在新的 setup / resume 替换了 orbd 的环境之后停掉这台 orb 上的 agent：归属它的线程，以及本 link 的
// hello 报告在跑的线程（没有 agent 的线程 agent.stop 是空操作）。线程服务收到 agent.status exited（不带 restartsLeft）
// 按有意停止处理，orb 进入 ready 时重新拉起。
//
// 任何一个线程停止失败（超时、RPC 错误）都不放行：那个 agent 可能还带着旧环境里的密钥直连上游。orb 留在当前阶段
// （不进 ready，线程不会往这台 orb 派新 agent），按 envAgentStopRetry 起的退避重试，直到全部停下；link 断开时返回 false
// （调用方退回 connecting，记下的 provider 放回，由新 link 重来）。
func (s *Service) stopEnvAgents(ctx context.Context, l *orbLink, providers []string) bool {
	delay := envAgentStopRetry
	for {
		ids, failed := s.stopEnvAgentsOnce(ctx, l)
		if l.ctl.Err() != nil {
			return false
		}
		if len(failed) == 0 {
			slog.Info("orbs: 已按新环境停掉 orb 上的 agent，环境变量降级密钥已收回", "orb", l.orbID, "providers", providers, "threads", ids)
			return true
		}
		slog.Warn("orbs: 收回环境变量降级密钥时有 agent 没停下，暂不进入 ready，稍后重试", "orb", l.orbID, "providers", providers, "failed", failed, "retryIn", delay)
		select {
		case <-l.ctl.Done():
			return false
		case <-time.After(delay):
		}
		delay = min(delay*2, envAgentStopRetryMax)
	}
}

// stopEnvAgentsOnce 对 orb 上的线程各发一次 agent.stop，返回涉及的线程与停止失败的线程（均已排序）。
func (s *Service) stopEnvAgentsOnce(ctx context.Context, l *orbLink) (ids, failed []string) {
	rows, err := s.st.Q.ListThreadsByOrb(ctx, &l.orbID)
	if err != nil {
		slog.Warn("orbs: 收回环境变量降级密钥时读取线程失败（只停 hello 报告在跑的）", "orb", l.orbID, "err", err)
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	for _, th := range l.hello.Threads {
		if th.AgentRunning {
			ids = append(ids, th.ThreadID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, id := range ids {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, envAgentStopTimeout)
			defer cancel()
			if err := l.ctl.Call(cctx, string(apigen.MethodAgentStop), apigen.LinkCtlAgentStopParams{ThreadID: id, GraceSeconds: 5}, nil); err != nil {
				mu.Lock()
				failed = append(failed, id)
				mu.Unlock()
				slog.Warn("orbs: 收回环境变量降级密钥时停止 agent 失败", "orb", l.orbID, "thread", id, "err", err)
			}
		})
	}
	wg.Wait()
	slices.Sort(failed)
	return ids, failed
}

// mergeKeys 合并两组 provider 键（排序、去重）。
func mergeKeys(a, b []string) []string {
	out := append(slices.Clone(a), b...)
	slices.Sort(out)
	return slices.Compact(out)
}
