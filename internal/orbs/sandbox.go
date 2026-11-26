package orbs

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// sandboxSampleEvery 是沙箱 link 上 metrics.sample 的最小间隔：更密的采样直接丢弃，不让沙箱刷写数据库。
const sandboxSampleEvery = 10 * time.Second

// sandboxNotifyAllowed 报告沙箱 link 上的 ctl 通知是否处理（M30）：只收 hello 与限速后的 metrics.sample，其余丢弃。
// 只在 ctl 读循环里调用（lastSample 不加锁）。
func (l *orbLink) sandboxNotifyAllowed(method string) bool {
	switch method {
	case string(apigen.MethodHello):
		return true
	case string(apigen.MethodMetricsSample):
		now := time.Now()
		if !l.lastSample.IsZero() && now.Sub(l.lastSample) < sandboxSampleEvery {
			return false
		}
		l.lastSample = now
		return true
	}
	return false
}

// sandboxReady 是沙箱 link 的 connecting 阶段（M30）：不发 setup.run（沙箱没有项目、凭据与 setup），hello 声明了精简模式
// 就直接 ready；provider 镜像里的 orbd 太旧、没有精简模式时置 failed（它会以完整模式运行，不能当沙箱用）。
func (s *Service) sandboxReady(ctx context.Context, l *orbLink) {
	to, reason := StateReady, "sandbox link"
	if !slices.Contains(l.hello.Features, FeatureSandbox) {
		to, reason = StateFailed, "orbd in the provider image has no sandbox mode; rebuild the orb image"
	}
	if _, _, err := s.transition(ctx, l.orbID, StateConnecting, to, map[string]any{"reason": reason}); err != nil {
		slog.Warn("沙箱就绪状态更新失败", "orb", l.orbID, "to", to, "err", err)
	}
}
