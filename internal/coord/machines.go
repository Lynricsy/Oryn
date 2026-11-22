package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// Machines 是全局助手使用 orb 与 Runner 的入口（M19）：列出 Runner（create_thread 的 runner 参数）、经 orbd 本机 API
// 读写线程 orb 里的文件；第二层（助手的电脑）的创建、查询、唤醒、冻结、删除与 computer.* ctl 调用也经它。
// 由 OrbMachines 适配 *orbs.Service。
type Machines interface {
	ListRunners(ctx context.Context) ([]apigen.Runner, error)
	Pause(ctx context.Context, orbID, mode string) error
	Wake(ctx context.Context, orbID string) error
	Delete(ctx context.Context, orbID string) error
	EnsureUsable(ctx context.Context, orbID, feature string, wake bool, wait time.Duration) error
	Do(ctx context.Context, orbID string, req *http.Request) (*http.Response, error)
	// CreateComputer 新建助手的电脑（provider / orbType 空 = 默认解析）。
	CreateComputer(ctx context.Context, provider, orbType string) (apigen.Orb, error)
	// Computer 返回未删除的助手的电脑；没有时 ok = false。Features 只在有已 hello 的 link 时非 nil。
	Computer(ctx context.Context) (orb apigen.Orb, ok bool, err error)
	// TouchAgent 记一次 agent 活动（推迟空闲冻结）。
	TouchAgent(ctx context.Context, orbID string)
	// ComputerCall 经 orb 当前的 link 发一次 computer.* ctl 请求；没有 link 时返回 orbs.ErrOrbNotReady。
	ComputerCall(ctx context.Context, orbID, method string, params, result any) error
	// OnComputerUpdate 订阅 orbd 的 computer.update 通知（在 ctl 读循环里同步调用，fn 须尽快返回）。
	OnComputerUpdate(fn func(orbID, callID string, partial json.RawMessage))
}

// SetMachines 设置 orb 与 Runner 的操作入口，并订阅助手的电脑的流式输出；须在服务对外可用之前调用。未设置时依赖它的
// 工具回 coordinator_unavailable。
func (s *Service) SetMachines(m Machines) {
	s.machines = m
	if m != nil {
		m.OnComputerUpdate(s.onComputerUpdate)
	}
}

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

// CreateComputer 新建助手的电脑。
func (m OrbMachines) CreateComputer(ctx context.Context, provider, orbType string) (apigen.Orb, error) {
	o, err := m.Create(ctx, orbs.CreateParams{Provider: provider, Type: orbType, Computer: true})
	if err != nil {
		return apigen.Orb{}, err
	}
	return orbs.View(ctx, m.Q, o)
}

// Computer 返回未删除的助手的电脑；活动 link 的特性覆盖库里的值。
func (m OrbMachines) Computer(ctx context.Context) (apigen.Orb, bool, error) {
	o, err := m.Q.GetComputerOrb(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Orb{}, false, nil
	}
	if err != nil {
		return apigen.Orb{}, false, err
	}
	v, err := orbs.View(ctx, m.Q, o)
	if err != nil {
		return apigen.Orb{}, false, err
	}
	v.Features = nil
	if f, ok := m.LiveFeatures(o.ID); ok {
		v.Features = append([]string{}, f...)
	}
	return v, true, nil
}

// ComputerCall 经 orb 当前的 link 发一次 ctl 请求。
func (m OrbMachines) ComputerCall(ctx context.Context, orbID, method string, params, result any) error {
	ctl, ok := m.Ctl(orbID)
	if !ok {
		return orbs.ErrOrbNotReady
	}
	return ctl.Call(ctx, method, params, result)
}

// OnComputerUpdate 注册 computer.update 通知的处理器。
func (m OrbMachines) OnComputerUpdate(fn func(orbID, callID string, partial json.RawMessage)) {
	m.HandleNotify(string(apigen.MethodComputerUpdate), func(_ context.Context, orbID string, params json.RawMessage) {
		var p struct {
			CallID        string          `json:"callId"`
			PartialResult json.RawMessage `json:"partialResult"`
		}
		if json.Unmarshal(params, &p) != nil || p.CallID == "" {
			return
		}
		fn(orbID, p.CallID, p.PartialResult)
	})
}
