package threads

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// ---- M12.2：/omfg 保存结果写回中央配置（契约 C5、C6）----

// ConfigSaveParams 是一次 `/omfg` 保存的回写请求。OrbID / ThreadID 来自控制面自己的线程状态，SavedPath 来自 omp 应答
// （不可信，由实现按白名单校验）。
type ConfigSaveParams struct {
	OrbID, ThreadID, SavedPath string
	// GlobalChosen 为真表示控制面在 UIResponse 里记录到用户在保存位置对话框里点的是全局选项。
	GlobalChosen bool
}

// ConfigSaveResult 是并进 omfg 应答 `data.oryn` 的同步结果（契约 C5）。
type ConfigSaveResult struct {
	Synced bool   `json:"synced"`
	Scope  string `json:"scope,omitempty"`
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ConfigSync 把 orb 里刚保存的规则写回中央配置（internal/cfgsync 实现）。
type ConfigSync interface {
	SaveRule(ctx context.Context, p ConfigSaveParams) ConfigSaveResult
}

// cfgSyncTimeout 是一次回写的上限：omfg 应答最多因此被多扣这么久（前端对 omfg 的命令超时是 10 分钟）。
const cfgSyncTimeout = 20 * time.Second

// omfgGlobalLabels / omfgOtherLabels 是 Core 保存位置对话框的选项文案（契约 C2）：新 Core 的 Oryn 文案与
// 旧 Core / 上游原文都要识别。
var omfgGlobalLabels = map[string]bool{
	"Global — all projects (Oryn central config)": true, // Core ≥ oryn-v18.19.0-8
	"Global — all projects (~/.omp/agent/rules)":  true, // 旧 Core / 上游
}

var omfgOtherLabels = map[string]bool{
	"This project (Oryn project config)": true, // Core ≥ oryn-v18.19.0-8
	"This project (.omp/rules)":          true, // 旧 Core / 上游
	"Amend with feedback…":               true,
}

// SetConfigSync 设置 /omfg 回写；nil = 不回写（应答原样交付）。须在 API 对外服务之前调用。
func (s *Service) SetConfigSync(c ConfigSync) {
	s.cfgSync = c
	s.cfgSyncTimeout = cfgSyncTimeout
}

// userOmfgPendingLocked 报告线程上是否有客户端发起、尚未应答的 omfg 命令。调用方持有 t.mu。
func userOmfgPendingLocked(t *thread) bool {
	for _, p := range t.pending {
		if userOmfg(p) {
			return true
		}
	}
	return false
}

// userOmfg 报告 p 是否为客户端（Command）发起的 omfg：自动化投递与服务内部命令都不算。
func userOmfg(p *pendingCmd) bool {
	return p.cmdType == "omfg" && p.origin == originUser && p.reply != nil
}

// noteOmfgChoiceLocked 在 extension_ui_response 已写入 / 排队之后记录用户在保存位置对话框里的选择：全局文案 → 真，
// 其它两个选项 → 假，其余值（含取消）不动。只在有客户端发起的 omfg 在途时记录，其它对话框里的同名选项不算数。
// 调用方持有 t.mu。
func noteOmfgChoiceLocked(t *thread, frame json.RawMessage) {
	if !userOmfgPendingLocked(t) {
		return
	}
	var r struct {
		Value *string `json:"value"`
	}
	if json.Unmarshal(frame, &r) != nil || r.Value == nil {
		return
	}
	switch {
	case omfgGlobalLabels[*r.Value]:
		t.omfgGlobal = true
	case omfgOtherLabels[*r.Value]:
		t.omfgGlobal = false
	}
}

// omfgSaveLocked 处理 omfg 应答：复位记录的全局选择；需要回写时在后台完成回写并交付，返回 true（调用方不再交付）。
// 调用方持有 t.mu。
func (s *Service) omfgSaveLocked(t *thread, p *pendingCmd, env omprpc.Envelope, frame json.RawMessage, r responseHead) bool {
	global := t.omfgGlobal
	t.omfgGlobal = false
	if s.cfgSync == nil || t.kind != KindWork || !userOmfg(p) || !r.Success || t.orbID == "" {
		return false
	}
	var d struct {
		State     string `json:"state"`
		SavedPath string `json:"savedPath"`
	}
	if json.Unmarshal(r.Data, &d) != nil || d.State != "saved" || d.SavedPath == "" {
		return false
	}
	sp := ConfigSaveParams{OrbID: t.orbID, ThreadID: t.id, SavedPath: d.SavedPath, GlobalChosen: global}
	// 服务正在关闭时 go_ 不执行：这条应答随进程退出丢弃
	s.go_(func(ctx context.Context) { s.finishOmfg(ctx, t, p, env, frame, r, sp) })
	return true
}

// finishOmfg 在后台回写 omfg 保存的规则（不持 t.mu 做 IO），把结果并进应答的 data.oryn 后交付并结算。
// runner 线程用的是用户机器上自己的 omp 配置：不回写，原帧交付。
func (s *Service) finishOmfg(ctx context.Context, t *thread, p *pendingCmd, env omprpc.Envelope, frame json.RawMessage, r responseHead, sp ConfigSaveParams) {
	if kind, _ := s.orbs.Features(sp.OrbID); kind != orbs.KindRunner {
		// 读 orb 文件期间算 agent 活动，避免空闲冻结与回写赛跑
		s.orbs.TouchAgent(ctx, sp.OrbID)
		cctx, cancel := context.WithTimeout(ctx, s.cfgSyncTimeout)
		res := s.cfgSync.SaveRule(cctx, sp)
		cancel()
		frame = withOrynSync(frame, res)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s.recompute(ctx, t)
	s.deliver(p, frame)
	s.settleResponseLocked(t, p, env, r)
}

// withOrynSync 把 res 写进应答的 data.oryn；任何解析失败返回原帧。
func withOrynSync(frame json.RawMessage, res ConfigSaveResult) json.RawMessage {
	var top map[string]json.RawMessage
	if json.Unmarshal(frame, &top) != nil || top == nil {
		return frame
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(top["data"], &data) != nil || data == nil {
		return frame
	}
	o, err := json.Marshal(res)
	if err != nil {
		return frame
	}
	data["oryn"] = o
	if top["data"], err = json.Marshal(data); err != nil {
		return frame
	}
	out, err := json.Marshal(top)
	if err != nil {
		return frame
	}
	return out
}
