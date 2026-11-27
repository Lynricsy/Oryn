package orbs

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// AI 数据同意（App Store 5.1.2(i)）：以环境变量降级的模型密钥让 omp 直连上游，出站代理管不到，只能不放进 orb 的环境。
// setup 只给已同意的 provider 注入（DefaultSetup.envConsent），但 orbd 的环境只在 setup.run / resume.run 时整体替换，
// 在跑的 agent 进程（及 orbd 崩溃自重启沿用的启动参数）一直带着启动时的环境。
//
// orbs.env_fallback 记下 orb 上可能持有的降级 provider（见迁移 0075）：新的 setup / resume 只并入本次下发的，停掉 orb 上
// 全部 agent 之后才收窄为本次下发的那组；NULL = 不知道（升级前已在跑的 orb）。收回时按这里的 provider 直接以接收方身份
// `model:<key>` 查同意（envStale），不从当前 models.yml 或密钥库反推：密钥被删、配置被改之后，已经下发的密钥仍在进程里。
//
// 收回 = 让 orb 重新走 connecting → resuming → ready：新的 resume.run 参数里没有不被同意的密钥，orbd 用它整体替换 setup
// 环境（orbdrt runSetupLocked：setupEnv = p.Env）并重写 models.yml；随后停掉这台 orb 上的全部 agent（agent.stop 结束
// 监督器，崩溃自重启也不再沿用旧环境），orb 进入 ready 后线程服务照常重新拉起（open_session 续上原会话）。触发时机：
//   - ready 且 link 在线的 orb：同意记录变化后（PUT / DELETE ai-data-consent）与 orynd 启动时（WithdrawEnvKeys）；
//   - ready 的 orb 重新 hello 时（onHello，含升级后第一次重连）；
//   - 每次 runPhase：生成参数之前按记录判定要不要停 agent，orbd 应答之后、进 ready 之前与进 ready 之后各按最新同意复查。
//
// 协调宿主不在此列：planCoordinator 丢弃降级的环境变量，orbd 的协调宿主模式也不给 agent 带 setup 环境；runner 关闭
// 环境变量降级（NoEnvFallback）；沙箱不经 setup。

// envUnknown 是 envStale 对「不知道 orb 上有哪些降级密钥」的标记。
const envUnknown = "?"

// envWithdrawReason 是收回降级密钥时 orb 事件的原因。
const envWithdrawReason = "ai consent: env key withdrawn"

// envAgentStopTimeout 是收回时停一个 agent 的上限（agent.stop 给 5 秒宽限期）。
const envAgentStopTimeout = 15 * time.Second

// envAgentStopRetry / envAgentStopRetryMax 是收回时 agent 没停下的重试退避（起点与上限；测试里调小）。
var (
	envAgentStopRetry    = time.Second
	envAgentStopRetryMax = time.Minute
)

// envTracked 报告 orb 是否走环境变量降级（工作 orb；协调宿主、runner、沙箱不经这条路）。
func envTracked(orb db.Orb) bool {
	return !orb.Coordinator && !IsRunner(orb) && !IsSandbox(orb)
}

// envHeldOf 解析 orbs.env_fallback；known 为假表示不知道（NULL 或无法解析）。
func envHeldOf(orb db.Orb) (keys []string, known bool) {
	if orb.EnvFallback == nil || json.Unmarshal([]byte(*orb.EnvFallback), &keys) != nil {
		return nil, false
	}
	return keys, true
}

// envUnconsented 返回 keys 里主人现在没有同意的第三方 provider（直接查 `model:<key>`；查询出错按未同意处理）。
func (s *Service) envUnconsented(ctx context.Context, keys []string) []string {
	var out []string
	for _, k := range keys {
		id, third := aiconsent.ModelRecipient(k, "")
		if third && aiconsent.Check(ctx, s.aiConsent, id) != nil {
			out = append(out, k)
		}
	}
	return out
}

// envStale 返回 orb 上可能持有、主人现在没有同意的降级 provider；不知道持有什么时为 [envUnknown]。
func (s *Service) envStale(ctx context.Context, orb db.Orb) []string {
	if !envTracked(orb) {
		return nil
	}
	held, known := envHeldOf(orb)
	if !known {
		return []string{envUnknown}
	}
	return s.envUnconsented(ctx, held)
}

// setEnvHeld 写 orbs.env_fallback（known 为假写 NULL）。
func (s *Service) setEnvHeld(ctx context.Context, orbID string, keys []string, known bool) {
	var v *string
	if known {
		keys = slices.Compact(slices.Sorted(slices.Values(keys)))
		if keys == nil {
			keys = []string{}
		}
		b, _ := json.Marshal(keys)
		v = new(string(b))
	}
	if err := s.st.Q.SetOrbEnvFallback(ctx, db.SetOrbEnvFallbackParams{EnvFallback: v, ID: orbID}); err != nil {
		slog.Warn("orbs: 记录 orb 持有的环境变量降级密钥失败", "orb", orbID, "err", err)
	}
}

// WithdrawEnvKeys 收回 orb 上主人已不同意的环境变量降级密钥（见文件头）。trigger 只进日志（"startup" / "consent"）。
// ready 且 link 在线、可能持有不被同意密钥的工作 orb 立即退回 connecting 重走 resume；其余由它们下一次 hello 或
// runPhase 按 orbs.env_fallback 处理。
func (s *Service) WithdrawEnvKeys(ctx context.Context, trigger string) {
	orbs, err := s.st.Q.ListLiveOrbs(ctx)
	if err != nil {
		slog.Warn("orbs: 收回环境变量降级密钥时读取 orb 失败", "trigger", trigger, "err", err)
		return
	}
	for _, orb := range orbs {
		if orb.State != StateReady || !s.Connected(orb.ID) {
			continue
		}
		if stale := s.envStale(ctx, orb); len(stale) > 0 {
			slog.Info("orbs: orb 可能持有未同意的环境变量降级密钥，重走 resume", "orb", orb.ID, "providers", stale, "trigger", trigger)
			s.reprepareForEnv(ctx, orb.ID, stale)
		}
	}
}

// reprepareForEnv 把 ready 的 orb 退回 connecting，由现有 link 重走 resume（runPhase 按 env_fallback 停 agent）。
// 状态已变时不动：由那次 runPhase 或之后的 hello 处理。
func (s *Service) reprepareForEnv(ctx context.Context, orbID string, providers []string) bool {
	_, moved, err := s.transition(ctx, orbID, StateReady, StateConnecting, map[string]any{"reason": envWithdrawReason, "providers": providers})
	if err != nil {
		slog.Warn("orbs: 收回环境变量降级密钥时状态更新失败", "orb", orbID, "err", err)
		return false
	}
	if moved {
		s.advance(orbID)
	}
	return moved
}

// stopEnvOnFailure 在 setup / resume 失败（参数生成失败、orbd 报错）、orb 将进入 failed 时调用：orbd 的环境没能按最新同意
// 替换，orb 上还带着不被同意密钥的 agent 至少要停掉（failed 不进 ready，线程不会再拉起）。env_fallback 不收窄。
func (s *Service) stopEnvOnFailure(ctx context.Context, l *orbLink, tracked bool, held []string, known bool) {
	if !tracked {
		return
	}
	stale := []string{envUnknown}
	if known {
		stale = s.envUnconsented(ctx, held)
	}
	if len(stale) > 0 {
		slog.Warn("orbs: setup / resume 失败，先停掉可能带着未同意降级密钥的 agent", "orb", l.orbID, "providers", stale)
		s.stopEnvAgents(ctx, l, stale)
	}
}

// stopEnvAgents 在新的 setup / resume 替换了 orbd 的环境之后停掉这台 orb 上的 agent：归属它的线程，以及本 link 的
// hello 报告在跑的线程（没有 agent 的线程 agent.stop 是空操作）。线程服务收到 agent.status exited（不带 restartsLeft）
// 按有意停止处理，orb 进入 ready 时重新拉起。
//
// 任何一个线程停止失败（超时、RPC 错误）都不放行：那个 agent 可能还带着旧环境里的密钥直连上游。orb 留在当前阶段
// （不进 ready，线程不会往这台 orb 派新 agent），按 envAgentStopRetry 起的退避重试，直到全部停下；link 断开时返回 false
// （调用方退回 connecting；env_fallback 没有收窄，由新 link 重来）。
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
