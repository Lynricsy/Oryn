package transcript

import "encoding/json"

// DefaultTaskAgent 是 task 工具调用没写 agent 时 omp 使用的子 agent。
const DefaultTaskAgent = "task"

// UsedAgents 按分支顺序收集主会话 message 条目里 `task` 工具调用用到的子 agent 名（去重、按首次出现排序）。
// 同时认两种参数形状：批量 `{tasks: [{agent}]}` 与扁平 `{agent}`；没写 agent 的任务算作 DefaultTaskAgent。
// 非 message 条目、解析失败的条目一律跳过。
func UsedAgents(branch []Entry) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" {
			name = DefaultTaskAgent
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, e := range branch {
		if e.Type != "message" {
			continue
		}
		var p struct {
			Message struct {
				Content []struct {
					Type      string          `json:"type"`
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		for _, c := range p.Message.Content {
			if c.Type != "toolCall" || c.Name != "task" {
				continue
			}
			var args struct {
				Agent json.RawMessage `json:"agent"`
				Tasks []struct {
					Agent json.RawMessage `json:"agent"`
				} `json:"tasks"`
			}
			if json.Unmarshal(c.Arguments, &args) != nil {
				continue
			}
			if len(args.Tasks) == 0 {
				add(jsonString(args.Agent))
				continue
			}
			for _, t := range args.Tasks {
				// 条目级没写时退回顶层 agent（扁平与批量混写的旧形状）
				name := jsonString(t.Agent)
				if name == "" {
					name = jsonString(args.Agent)
				}
				add(name)
			}
		}
	}
	return out
}
