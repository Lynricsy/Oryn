package orbs

import (
	"context"
	"fmt"
	"slices"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

// FeatureLibrary 是协调宿主在 hello 里上报的文件库特性（M13）：认识 library.sync、library.fetch 与同步用的 library 流。
const FeatureLibrary = "library"

// OpenStream 在 orb 当前已 hello 的 link 上打开一条流（写出流头）；hello 未声明 feature 时返回 ErrOrbdOutdated。
// 不检查 orb 状态。
func (s *Service) OpenStream(ctx context.Context, orbID, feature string, h link.StreamHeader) (*link.Stream, error) {
	s.mu.Lock()
	l := s.links[orbID]
	s.mu.Unlock()
	if l == nil || !l.helloSeen() {
		return nil, fmt.Errorf("%w: orb %s has no link", ErrOrbNotReady, orbID)
	}
	if !slices.Contains(l.hello.Features, feature) {
		return nil, ErrOrbdOutdated
	}
	return l.sess.Open(ctx, h)
}

// CoordinatorSetup 返回协调 orb 当前的 setup.run 参数（omp-home 只有 coordinatorFiles，模型地址改写到协调宿主的代理端口），
// 并以同一份计划刷新它的 egress 路由。M13 4.5：协调线程每次 agent.start 之前以它重跑一次 setup.run，还原助手可能改过的受管文件。
func (s *Service) CoordinatorSetup(ctx context.Context, orbID string) (apigen.LinkCtlSetupRunParams, error) {
	orb, err := s.st.Q.GetOrb(ctx, orbID)
	if err != nil {
		return apigen.LinkCtlSetupRunParams{}, err
	}
	if !orb.Coordinator {
		return apigen.LinkCtlSetupRunParams{}, fmt.Errorf("orb %s is not the coordinator orb", orbID)
	}
	if s.setup == nil {
		return apigen.LinkCtlSetupRunParams{GitHosts: []string{}, OmpHome: []apigen.LinkCtlOmpHomeFile{}, Env: map[string]string{}}, nil
	}
	plan, err := s.setup.Plan(ctx, orb)
	if err != nil {
		return apigen.LinkCtlSetupRunParams{}, err
	}
	s.setPolicy(orbID, plan.Egress)
	return plan.Params, nil
}
