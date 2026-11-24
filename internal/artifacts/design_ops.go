package artifacts

// M29 设计画布对 agent 公开的操作名（orb 扩展、Runner 与全局助手的工具同名）。处理函数分别在 directions.go
// （发牌与选定方向）与 notes.go（便签）里；Call 的接线在 agent.go。
const (
	// OpDesignDirections 发 3 张方向牌（landing 由方向库发，app 校验 agent 自拟的 3 张）。
	OpDesignDirections = "design_directions"
	// OpDesignDirection 在最近一次发出的牌里选定一个方向，回可粘进 src/theme.css 的 @theme 文本。
	OpDesignDirection = "design_direction"
	// OpDesignNotes 列待处理的便签（未解决的评论串 + 用户贴的 sticky）与逐帧带编号的截图。
	OpDesignNotes = "design_notes"
	// OpDesignResolveNote 回复并解决一条便签（ref 是评论串 act_… 或 sticky id）。
	OpDesignResolveNote = "design_resolve_note"
)
