package github

import "context"

// dropAllHooks 删除 Oryn 在各仓库上建的 webhook（删除 OAuth App 配置前调用）。
// 步骤 1.2 实现仓库 hook 管理；此处先留空实现。
func (s *Service) dropAllHooks(ctx context.Context) error { return nil }
