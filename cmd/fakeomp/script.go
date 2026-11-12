package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// runScript 用 --script 文件替换默认的帧序列。脚本为 JSONL，每行一步（空行与 # 开头的行忽略）：
//
//	{"emit": {<帧>}}      发出帧；message_start/update/end 缺 messageId 时自动分配，
//	                      prompt_result 缺 id 时填入本次 prompt 的命令 id
//	{"entry": {<消息>}}   把消息作为 message 条目追加到会话文件（缺 timestamp 时填当前毫秒）
//	{"ask": {<请求字段>}}  发出 extension_ui_request（自动分配 id）并等待 extension_ui_response
//	{"sleep": <毫秒>}     暂停
//
// 行内占位符在解析前替换：{{prompt}}（JSON 字符串转义后的提示词，不含引号）、
// {{promptId}}、{{now}}（当前 Unix 毫秒）、{{answer}}（最近一次 ask 的应答值）。
// 脚本完全接管回合：user 条目、agent_end、prompt_result、session_settled 都需自行写出；
// 回合被 abort 时按默认方式收尾（agent_end + prompt_result{status:"aborted"} + session_settled）。
func (t *turn) runScript(p promptReq) {
	esc, _ := json.Marshal(p.message)
	prompt := string(esc[1 : len(esc)-1])
	answer := ""
	for _, raw := range t.s.script {
		r := strings.NewReplacer(
			"{{prompt}}", prompt,
			"{{promptId}}", p.id,
			"{{now}}", strconv.FormatInt(nowMs(), 10),
			"{{answer}}", jsonInner(answer),
		)
		line := r.Replace(raw)
		var step struct {
			Emit  map[string]any `json:"emit"`
			Entry map[string]any `json:"entry"`
			Ask   map[string]any `json:"ask"`
			Sleep *int           `json:"sleep"`
		}
		if err := json.Unmarshal([]byte(line), &step); err != nil {
			t.s.emit(map[string]any{"type": "notice", "level": "error", "message": "fakeomp: bad script line: " + err.Error()})
			continue
		}
		switch {
		case step.Emit != nil:
			t.prepareScripted(step.Emit)
			t.s.emitEvent(step.Emit)
		case step.Entry != nil:
			if _, ok := step.Entry["timestamp"]; !ok {
				step.Entry["timestamp"] = nowMs()
			}
			t.appendEntry(step.Entry)
		case step.Ask != nil:
			v, ok := t.ask(step.Ask)
			if !ok {
				t.finish("aborted")
				return
			}
			answer = v
		case step.Sleep != nil:
			if !t.sleep(time.Duration(*step.Sleep) * time.Millisecond) {
				t.finish("aborted")
				return
			}
		}
		if t.ctx.Err() != nil {
			t.finish("aborted")
			return
		}
	}
}

func jsonInner(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// prepareScripted 为脚本帧补齐 messageId 与 prompt_result.id。
func (t *turn) prepareScripted(f map[string]any) {
	typ, _ := f["type"].(string)
	_, has := f["messageId"]
	switch typ {
	case "message_start":
		id, _ := f["messageId"].(string)
		if !has {
			id = t.nextMessageID()
			f["messageId"] = id
		}
		t.openMsgs = append(t.openMsgs, id)
	case "message_update":
		if !has {
			if n := len(t.openMsgs); n > 0 {
				f["messageId"] = t.openMsgs[n-1]
			} else {
				f["messageId"] = t.nextMessageID()
			}
		}
	case "message_end":
		if n := len(t.openMsgs); n > 0 {
			if !has {
				f["messageId"] = t.openMsgs[n-1]
			}
			t.openMsgs = t.openMsgs[:n-1]
		} else if !has {
			f["messageId"] = t.nextMessageID()
		}
	case "prompt_result":
		if _, ok := f["id"]; !ok && t.promptID != "" {
			f["id"] = t.promptID
		}
	}
}
