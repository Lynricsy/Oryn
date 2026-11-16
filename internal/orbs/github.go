package orbs

import (
	"context"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// GitHubTokenState 是一台 orb 取 GitHub 令牌的结果（M14）。
type GitHubTokenState int

const (
	GitHubTokenUnmanaged GitHubTokenState = iota // 这台 orb 不归 GitHub App 管（项目未绑定，或 user 槽位未连接）：调用方可回落到 PAT
	GitHubTokenOK
	GitHubTokenUnavailable // 归 App 管但拿不到令牌（agent 槽位不可用、签发失败）：调用方不得回落
)

// GitHubSetup 是一台 orb 的 GitHub 下发参数。
type GitHubSetup struct {
	Login       string
	GitIdentity apigen.GitIdentity
	Agent       bool // 身份为 agent：GitIdentity 覆盖项目与所有者设置
	APIOrigin, GitOrigin string
}

// GitHub 是 GitHub 集成给 setup 计划与出站代理提供的能力（M14）；nil = 未启用。
type GitHub interface {
	OrbSetup(ctx context.Context, orb db.Orb, prj db.Project) (GitHubSetup, bool)
	OrbToken(ctx context.Context, orbID string) (string, GitHubTokenState)
}
