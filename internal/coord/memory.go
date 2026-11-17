package coord

// memory.go：M17 全局助手的主人记忆（ownermem）在协调层的接入点。

import "context"

// OwnerMemory 是协调层用到的主人记忆能力（由 ownermem.Service 实现）。
type OwnerMemory interface {
	// Brief 返回助手对话启动时追加到系统提示词的记忆段；助手记忆没有运行时为空串。
	Brief(ctx context.Context, threadID string) (string, error)
}

// SetOwnerMemory 设置主人记忆；nil 时不注入记忆段。
func (s *Service) SetOwnerMemory(m OwnerMemory) { s.mem = m }
