package coord

import (
	"context"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Machines 是全局助手操作 orb 与 Runner 的入口（M19 第三层）：列出 Runner 与 orb、暂停 / 唤醒 / 删除 orb，
// 经 orbd 本机 API 读写线程 orb 里的文件，以及项目 Agent 配置改动后的下发。由 OrbMachines 适配 *orbs.Service。
type Machines interface {
	ListRunners(ctx context.Context) ([]apigen.Runner, error)
	ListOrbs(ctx context.Context) ([]apigen.Orb, error)
	Pause(ctx context.Context, orbID, mode string) error
	Wake(ctx context.Context, orbID string) error
	Delete(ctx context.Context, orbID string) error
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	ScheduleOmpApply(projectID string)
}

// SetMachines 设置 orb 与 Runner 的操作入口；须在服务对外可用之前调用。未设置时依赖它的工具回 coordinator_unavailable。
func (s *Service) SetMachines(m Machines) { s.machines = m }

// machinesOr 返回 Machines；未设置时返回 coordinator_unavailable。
func (s *Service) machinesOr() (Machines, error) {
	if s.machines == nil {
		return nil, errf(CodeCoordinatorUnavailable, "orbs and runners are not available")
	}
	return s.machines, nil
}

// OrbMachines 把 *orbs.Service 与库适配成 Machines。
type OrbMachines struct {
	*orbs.Service
	Q *db.Queries
}

// ListOrbs 返回未删除的 orb（新的在前），活动 link 的特性覆盖库里的值（与 REST GET orbs 同一转换）。
func (m OrbMachines) ListOrbs(ctx context.Context) ([]apigen.Orb, error) {
	rows, err := m.Q.ListOrbs(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Orb, 0, len(rows))
	for _, o := range rows {
		v, err := orbs.View(ctx, m.Q, o)
		if err != nil {
			return nil, err
		}
		if f, ok := m.LiveFeatures(o.ID); ok {
			v.Features = f
		}
		out = append(out, v)
	}
	return out, nil
}
