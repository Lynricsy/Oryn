package threads

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 会换会话文件的命令：破坏线程↔会话绑定、Oryn 跟随不了的拦下；Oryn 能跟随或原地改动的放行。
func TestRouteSessionSwitchCommands(t *testing.T) {
	cases := []struct {
		frame, route string
	}{
		// 切到 / 移走 / 删除会话文件，或把旁路问答提升成新会话文件
		{`{"type":"new_session"}`, RouteManaged},
		{`{"type":"drop_session"}`, RouteManaged},
		{`{"type":"switch_session","sessionPath":"/x.jsonl"}`, RouteManaged},
		{`{"type":"open_session","sessionDir":"/x"}`, RouteManaged},
		{`{"type":"move_session","path":"/x"}`, RouteManaged},
		{`{"type":"collab_join","link":"x"}`, RouteManaged},
		{`{"type":"btw_branch"}`, RouteManaged},
		// 把历史复制进新会话文件 = 分叉
		{`{"type":"fork","entryId":"e1"}`, RouteUseOrynFork},
		{`{"type":"fork_from","entryId":"e1"}`, RouteUseOrynFork},
		{`{"type":"branch","entryId":"e1"}`, RouteUseOrynFork},
		// 原地改动，或在本目录开 Oryn 能拼接跟随的新会话文件
		{`{"type":"clear_context"}`, RoutePass},
		{`{"type":"fresh"}`, RoutePass},
		{`{"type":"handoff"}`, RoutePass},
		{`{"type":"compact"}`, RoutePass},
		{`{"type":"navigate_tree","entryId":"e1"}`, RoutePass},
		{`{"type":"switch_leaf","entryId":"e1"}`, RoutePass},
		{`{"type":"tan","work":"x"}`, RoutePass},
		{`{"type":"btw","question":"x"}`, RoutePass},
		{`{"type":"btw_inject","recordId":"r1"}`, RoutePass},
		{`{"type":"get_btw_history"}`, RoutePass},
		{`{"type":"plan_approval","approved":true,"option":"execute"}`, RoutePass},
		{`{"type":"set_loop_mode","enabled":true}`, RoutePass},
		{`{"type":"import_foreign_session","source":"claude","foreignId":"x"}`, RoutePass},
		{`{"type":"set_session_name","name":"x"}`, RoutePass},
	}
	for _, c := range cases {
		got, typ := RouteCommand(raw(c.frame))
		assert.Equal(t, c.route, got, c.frame)
		assert.NotEmpty(t, typ, c.frame)
	}
}

// prompt 正文里会换会话文件的内置斜杠命令（omp 在 RPC 里直接执行）按 omp 的解析规则拦下，cmdType 报出被拦的命令。
func TestRoutePromptSlashCommands(t *testing.T) {
	cases := []struct {
		frame, route, cmdType string
	}{
		{`{"type":"prompt","message":"/move /tmp/x"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/move"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/wt"}`, RouteManaged, "/wt"},
		{`{"type":"prompt","message":"/worktree feat"}`, RouteManaged, "/worktree"},
		{`{"type":"prompt","message":"/move:x"}`, RouteManaged, "/move"},
		{`{"type":"prompt","message":"/move\ufeffx"}`, RouteManaged, "/move"}, // JS \s 含 U+FEFF
		{`{"type":"prompt","message":"/session delete"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"/session  DELETE \n"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"/session:delete"}`, RouteManaged, "/session delete"},
		{`{"type":"prompt","message":"hi","message":"/move x"}`, RouteManaged, "/move"}, // 重复键取最后一个（同 JS）
		{`{"type":"prompt","message":"/session delete now"}`, RoutePass, "prompt"},      // omp 只在无余参时删除
		{`{"type":"prompt","message":"/session info"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"/session"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":" /move x"}`, RoutePass, "prompt"}, // omp 不去前导空白
		{`{"type":"prompt","message":"/Move x"}`, RoutePass, "prompt"},  // 命令名大小写敏感
		{`{"type":"prompt","message":"/moved"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"/move\u0085x"}`, RoutePass, "prompt"}, // U+0085 不是 JS \s
		{`{"type":"prompt","message":"/compact"}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":"please /move x"}`, RoutePass, "prompt"},
		{`{"type":"prompt","Message":"/move x"}`, RoutePass, "prompt"}, // omp 只读精确键
		{`{"type":"prompt","message":null}`, RoutePass, "prompt"},
		{`{"type":"prompt","message":7}`, RoutePass, "prompt"},
		// 只有 prompt 执行内置命令
		{`{"type":"steer","message":"/move x"}`, RoutePass, "steer"},
		{`{"type":"follow_up","message":"/move x"}`, RoutePass, "follow_up"},
		{`{"type":"abort_and_prompt","message":"/session delete"}`, RoutePass, "abort_and_prompt"},
	}
	for _, c := range cases {
		route, typ := RouteCommand(raw(c.frame))
		assert.Equal(t, c.route, route, c.frame)
		assert.Equal(t, c.cmdType, typ, c.frame)
	}
}
