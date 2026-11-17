package coord

import (
	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 主线（M19）：全局助手的永久对话。每段上下文很短：agent 每次启动先 clear_context，系统提示词末尾带「主线近况」
// （备忘 + 最近的对话）；主线静止一段时间或一段内回合数满了，agent 在静止时停掉，下一段重新开始。

// 主线专属工具名。
const (
	ToolUpdateNotes = "update_notes"
)

// mainToolDefs 是主线专属工具（只在主线的工具表里）。
func mainToolDefs() []apigen.OmpFrame {
	return []apigen.OmpFrame{
		essentialTool(ToolUpdateNotes, "Rewrite the main conversation's notes as a whole (at most 1500 characters). The notes are shown at the start of every episode; keep there what you must remember across episodes. Compress when it gets long.", obj{
			"text": str("The complete new notes."),
		}, "text"),
	}
}
