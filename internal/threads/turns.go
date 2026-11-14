package threads

import "context"

// 带标签投递的终态（契约 C1）。
const (
	TurnCompleted = "completed"
	TurnAborted   = "aborted"
	TurnError     = "error"
	TurnUnknown   = "unknown"
)

// TurnEvent 是一次带标签的自动化投递（AutoMessage.Tag / CreateParams.Tag）的终态。
// 标签约定：定时用运行 id（srn_…），协调回报用登记代号（rep_…）；标签为空 = 不跟踪。
type TurnEvent struct {
	ThreadID, Tag string
	Status        string // TurnCompleted / TurnAborted / TurnError / TurnUnknown
	Local         bool   // omp 本地处理，没有开启回合（Status = completed）
	Error         string // Status == error 时的原因
	// Text 是所属回合最后一条有文本的 assistant 消息（至多 8000 字符）；
	// HasText 为假表示控制面没有看到该回合的 agent_end（消费方退回读库）。
	Text    string
	HasText bool
}

// RecoveredOutcome 从已入库的转录推断一次没有标签关联的投递（重启前已下发）的终态（补充契约 C1.6）。
// anchorPrefix 是该投递首条用户消息正文的开头（定时运行的来源头行前缀，见 C9.3）；since 是投递时间（毫秒）。
// 规则：在活动分支上找 ts >= since、正文以 anchorPrefix 开头的第一条 user 消息作锚点；
// 取锚点之后、下一条 user 消息之前，最后一条「结束回合」的 assistant 消息（stopReason 不是 toolUse / 空）：
//
//	stopReason == "error"   → TurnEvent{Status: TurnError, Error: errorMessage 或 "provider request failed"}
//	stopReason == "aborted" → TurnEvent{Status: TurnAborted}
//	其它                     → TurnEvent{Status: TurnCompleted}
//
// Text / HasText 取该消息的文本（同 C1 规则 4）。
// ok=false：锚点不存在，或锚点之后还没有结束回合的 assistant 消息（转录复制尚未赶上，或回合未结束）。
//
// S0 只落声明：恒返回 ok=false（实现归切片 A）。
func (s *Service) RecoveredOutcome(ctx context.Context, threadID, anchorPrefix string, since int64) (ev TurnEvent, ok bool, err error) {
	return TurnEvent{}, false, nil
}
