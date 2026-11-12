package orbdrt

import (
	"encoding/json"
	"strings"
	"sync"
)

// pendingUI 记录进程中仍在等待用户作答的交互帧（extension_ui_request、plan_proposal）。
// 这些请求只存在于 omp 内存与控制面内存中：控制面重启后会丢失，agent 则会一直等待。
// orbd 在新的 agent 流上重放它们，让新的控制面重建待答状态。
type pendingUI struct {
	mu   sync.Mutex
	seq  int
	reqs map[string]pendingFrame // id → 请求帧
	plan []byte                  // 未决的 plan_proposal 帧
	// approvals 是已写给 omp、尚未收到应答的 plan_approval 命令 id。提案只在其成功应答后清除：
	// 失败的批准（如 save 缺绝对路径）不会结束 omp 里的待批提案，重连后仍须重放。
	approvals map[string]bool
}

// errNoPendingPlan 是 omp 在没有待批提案时对 plan_approval 的错误文本（oryn-core rpc-plan.ts resolve）；
// 此时 omp 已无提案，重放槽同样清除。
const errNoPendingPlan = "No plan is awaiting approval"

type pendingFrame struct {
	seq   int
	frame []byte
}

// nonPendingMethods 是只做展示、不等待作答的 extension_ui_request 方法。
var nonPendingMethods = map[string]bool{
	"notify": true, "setStatus": true, "setWidget": true, "setTitle": true, "set_editor_text": true, "cancel": true,
}

type uiHead struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Method   string `json:"method"`
	TargetID string `json:"targetId"`
	Success  *bool  `json:"success"`
	Error    string `json:"error"`
}

// observeOut 处理 omp → 控制面的帧。
func (p *pendingUI) observeOut(frame []byte) {
	var h uiHead
	if json.Unmarshal(frame, &h) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch h.Type {
	case "extension_ui_request":
		switch {
		case h.Method == "cancel" && h.TargetID != "":
			delete(p.reqs, h.TargetID)
		case !nonPendingMethods[h.Method] && h.ID != "":
			if p.reqs == nil {
				p.reqs = map[string]pendingFrame{}
			}
			p.seq++
			p.reqs[h.ID] = pendingFrame{seq: p.seq, frame: append([]byte(nil), frame...)}
		}
	case "plan_proposal":
		p.plan = append([]byte(nil), frame...)
	case "response":
		if h.ID == "" || !p.approvals[h.ID] {
			return
		}
		delete(p.approvals, h.ID)
		if (h.Success != nil && *h.Success) || strings.Contains(h.Error, errNoPendingPlan) {
			p.plan = nil
		}
	}
}

// observeIn 处理控制面 → omp 的命令帧（在写入 omp stdin 之前调用）。
func (p *pendingUI) observeIn(line []byte) {
	var h uiHead
	if json.Unmarshal(line, &h) != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch h.Type {
	case "extension_ui_response":
		delete(p.reqs, h.ID)
	case "plan_approval":
		// 没有 id 的批准无法关联应答：保留提案（宁可多重放一次，由控制面按状态去重）。
		if h.ID != "" {
			if p.approvals == nil {
				p.approvals = map[string]bool{}
			}
			p.approvals[h.ID] = true
		}
	}
}

// replay 按到达顺序返回仍待答的帧。
func (p *pendingUI) replay() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]pendingFrame, 0, len(p.reqs))
	for _, f := range p.reqs {
		out = append(out, f)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].seq < out[j-1].seq; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	frames := make([][]byte, 0, len(out)+1)
	if p.plan != nil {
		frames = append(frames, p.plan)
	}
	for _, f := range out {
		frames = append(frames, f.frame)
	}
	return frames
}
