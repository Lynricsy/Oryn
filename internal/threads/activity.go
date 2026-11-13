package threads

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// 线程活动（Thread.activity）：working 线程最近一次工具调用的意图，供侧栏显示 agent 正在做什么。
// 来源是 tool_execution_start 帧与 delta 模式 message_update 的 toolcall_end 事件（该事件带完整的
// toolCall；流式中途的参数是不完整 JSON 的增量，不取）。只在内存中维护，变化时随 index.patch 推送。

// maxActivityRunes 是活动文本的长度上限（字符数）。
const maxActivityRunes = 120

// frameActivity 从一条 omp 帧里取活动文本；帧与活动无关或取不到时返回空串。
func frameActivity(typ string, line []byte) string {
	switch typ {
	case "tool_execution_start":
		var f struct {
			ToolName string          `json:"toolName"`
			Args     json.RawMessage `json:"args"`
			Intent   string          `json:"intent"`
		}
		if json.Unmarshal(line, &f) != nil {
			return ""
		}
		return toolActivity(f.ToolName, f.Args, f.Intent)
	case "message_update":
		var f struct {
			Event struct {
				Type     string `json:"type"`
				ToolCall *struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
					Intent    string          `json:"intent"`
				} `json:"toolCall"`
			} `json:"assistantMessageEvent"`
		}
		if json.Unmarshal(line, &f) != nil || f.Event.Type != "toolcall_end" || f.Event.ToolCall == nil {
			return ""
		}
		tc := f.Event.ToolCall
		return toolActivity(tc.Name, tc.Arguments, tc.Intent)
	}
	return ""
}

// toolActivity 按 omp activity/index.ts argumentsSummary 的规则取一次工具调用的摘要：
// 意图（intent 字段或参数 i / intent）优先，否则按工具取 path / pattern / command 等，最后回退为工具名。
func toolActivity(name string, rawArgs json.RawMessage, intent string) string {
	if v := oneLine(intent); v != "" {
		return clipActivity(v)
	}
	var args map[string]any
	_ = json.Unmarshal(rawArgs, &args)
	first := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := args[k].(string); ok {
				if v := oneLine(s); v != "" {
					return v
				}
			}
		}
		return ""
	}
	v := first("i", "intent")
	if v == "" {
		switch name {
		case "read", "glob", "write", "edit":
			v = first("path")
		case "grep":
			parts := make([]string, 0, 2)
			for _, k := range []string{"pattern", "path"} {
				if s := first(k); s != "" {
					parts = append(parts, s)
				}
			}
			v = strings.Join(parts, " · ")
		case "bash":
			v = first("command")
		default:
			v = first("path", "pattern", "command", "query", "name", "task", "title")
		}
	}
	if v == "" {
		v = oneLine(name)
	}
	return clipActivity(v)
}

// oneLine 把空白（含换行）折叠为单个空格并去掉首尾空白。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clipActivity(s string) string {
	if utf8.RuneCountInString(s) <= maxActivityRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxActivityRunes-1]) + "…"
}

// activityPtr 返回线程当前活动（无则 nil，API 中省略）。
func (s *Service) activityPtr(id string) *string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.activity[id]; ok {
		return &v
	}
	return nil
}

// clearActivity 清除线程活动，返回是否有变化。
func (s *Service) clearActivity(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.activity[id]; !ok {
		return false
	}
	delete(s.activity, id)
	return true
}

// setActivityLocked 在线程 working 时记录活动；与当前值相同则不推送（同一次工具调用的 toolcall_end 与
// tool_execution_start 通常给出同一意图，只推一次）。调用方持有 t.mu。
func (s *Service) setActivityLocked(ctx context.Context, t *thread, v string) {
	if v == "" || t.state != StateWorking {
		return
	}
	s.mu.Lock()
	changed := s.activity[t.id] != v
	if changed {
		s.activity[t.id] = v
	}
	s.mu.Unlock()
	if changed {
		s.publish(ctx, t.id)
	}
}
