package threads

import (
	"context"
	"log/slog"
	"slices"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// M16：agent.start 时按需启用内置 Hindsight 记忆（计划步骤 2.3）。是否启用由 MemoryStarter（internal/memory 实现）
// 按项目判定；orbd 只在 hello 声明了 memory 特性时才收到 memory 参数。协调对话不接入记忆。

// FeatureMemory 是 orbd 支持 agent.start 的 memory 参数（普通 orbd 与 runner 上报，协调宿主不上报）。
const FeatureMemory = "memory"

// restartReasonMemory 是记忆开关变化时 apply-status 里的重启原因。
const restartReasonMemory = "memory"

// MemoryStarter 决定一次 agent.start 是否启用记忆（*memory.Service 实现）。projectID 为线程所属项目（nil = 无项目）；
// 返回 nil 表示本次不启用。
type MemoryStarter interface {
	AgentMemory(ctx context.Context, threadID string, projectID *string) (*apigen.LinkCtlAgentMemory, error)
}

// memoryProject 返回线程在记忆上的归属项目：runner 线程取线程自己的 project_id，普通 orb 取 orb 的 project_id
// （与记忆网关的判定一致）。ok 为假表示 orb 不支持记忆（未声明 memory 特性）。
func (s *Service) memoryProject(ctx context.Context, threadID, orbID string) (project *string, ok bool, err error) {
	kind, feats := s.orbs.Features(orbID)
	if !slices.Contains(feats, FeatureMemory) {
		return nil, false, nil
	}
	if kind == orbs.KindRunner {
		row, err := s.st.Q.GetThread(ctx, threadID)
		if err != nil {
			return nil, false, err
		}
		return row.ProjectID, true, nil
	}
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return nil, false, err
	}
	return orb.ProjectID, true, nil
}

// agentMemory 返回 agent.start 的 memory 参数；未配置记忆、orb 不支持或查询出错时返回 nil（出错只记日志，不挡 agent 启动）。
// 只对工作线程调用（协调对话不接入记忆）。
func (s *Service) agentMemory(ctx context.Context, threadID, orbID string) *apigen.LinkCtlAgentMemory {
	if s.cfg.Memory == nil {
		return nil
	}
	project, ok, err := s.memoryProject(ctx, threadID, orbID)
	if err != nil {
		slog.Warn("threads: 无法确定记忆归属，本次不启用记忆", "thread", threadID, "orb", orbID, "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	m, err := s.cfg.Memory.AgentMemory(ctx, threadID, project)
	if err != nil {
		slog.Warn("threads: 查询记忆开关失败，本次不启用记忆", "thread", threadID, "orb", orbID, "err", err)
		return nil
	}
	return m
}

// MarkMemoryRestart 在记忆开关变化时（memory.Config.Restart）把 agent 在跑的工作线程标为需重启，原因为 memory：
// projectID 为 nil 时作用于全部线程，否则只作用于归属该项目的线程（归属判定同 agentMemory）。orb 不支持记忆的
// 线程不受开关影响，不标记。标记由 apply-status 列出，RestartSessions 安排重启。
func (s *Service) MarkMemoryRestart(ctx context.Context, projectID *string) {
	for _, t := range s.loadedThreads() {
		t.mu.Lock()
		kind, orbID, alive := t.kind, t.orbID, t.agentAlive()
		t.mu.Unlock()
		if kind != KindWork || !alive || orbID == "" {
			continue
		}
		project, ok, err := s.memoryProject(ctx, t.id, orbID)
		if err != nil {
			slog.Warn("threads: 无法确定记忆归属，不标记重启", "thread", t.id, "orb", orbID, "err", err)
			continue
		}
		if !ok || (projectID != nil && (project == nil || *project != *projectID)) {
			continue
		}
		t.mu.Lock()
		s.applyConfigEffectLocked(t, configEffect{restart: []string{restartReasonMemory}})
		t.mu.Unlock()
	}
}
