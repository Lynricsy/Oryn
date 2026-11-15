package threads

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/omprpc"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// ---- M12.2：/omfg 保存结果写回中央配置（契约 C5、C6）----

// ConfigSaveParams 是一次 `/omfg` 保存的回写请求。OrbID / ThreadID 来自控制面自己的线程状态，SavedPath 来自 omp 应答
// （不可信，由实现按白名单校验）。
type ConfigSaveParams struct {
	OrbID, ThreadID, SavedPath string
	// GlobalChosen 为真表示控制面记录到用户在这条 omfg 的保存位置对话框里点的是全局选项。
	GlobalChosen bool
	// Preview 是用户在保存位置对话框里点选保存位置时，omfg 预览（setWidget key `omfg`）里的规则全文；
	// 空 = 没有记录到预览。实现须拒绝写入与它不一致的内容。
	Preview string
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

// CodeOmfgInProgress：同一线程已有 omfg 在途时再发 omfg。Core 的保存位置对话框与规则预览不带命令 id，
// 并发的两条 omfg 无法把用户的选择归属到具体命令，所以每个线程同时只受理一条。
const CodeOmfgInProgress = "omfg_in_progress"

// cfgSyncTimeout 是一次回写的上限：omfg 应答最多因此被多扣这么久（前端对 omfg 的命令超时是 10 分钟）。
const cfgSyncTimeout = 20 * time.Second

// Core 保存位置对话框与规则预览的字面量（契约 C2；预览见 Core `rpc-omfg.ts` 的 createRpcOmfgPanelMirror）。
const (
	omfgSaveTitle = "Save TTSR rule where?"
	// omfgWidgetKey 是候选规则预览的 setWidget key；预览行为 [`TTSR rule — <校验状态>`, "", 规则全文按行]。
	omfgWidgetKey   = "omfg"
	omfgPreviewHead = "TTSR rule — "
)

// omfgGlobalLabels / omfgProjectLabels 是 Core 保存位置对话框的保存选项文案（契约 C2）：新 Core 的 Oryn 文案与
// 旧 Core / 上游原文都要识别。
var omfgGlobalLabels = map[string]bool{
	"Global — all projects (Oryn central config)": true, // Core ≥ oryn-v18.19.0-8
	"Global — all projects (~/.omp/agent/rules)":  true, // 旧 Core / 上游
}

var omfgProjectLabels = map[string]bool{
	"This project (Oryn project config)": true, // Core ≥ oryn-v18.19.0-8
	"This project (.omp/rules)":          true, // 旧 Core / 上游
}

// omfgChoice 是一条客户端发起的 omfg 在保存位置对话框里的最近一次作答。
type omfgChoice struct {
	// since 是受理命令时实时缓冲的帧序号：只认之后到达的对话框。
	since int
	// global 为真表示点的是全局选项；preview 是点选保存位置（全局或本项目）时的规则预览全文。
	global  bool
	preview string
}

// SetConfigSync 设置 /omfg 回写；nil = 不回写（应答原样交付）。须在 API 对外服务之前调用。
func (s *Service) SetConfigSync(c ConfigSync) {
	s.cfgSync = c
	s.cfgSyncTimeout = cfgSyncTimeout
}

// userOmfg 报告 p 是否为客户端（Command）发起的 omfg：自动化投递与服务内部命令都不算。
func userOmfg(p *pendingCmd) bool {
	return p.cmdType == "omfg" && p.origin == originUser && p.reply != nil
}

// admitOmfgLocked 在 omfg 命令登记之前调用：线程上已有 omfg 未应答（含排队）→ omfg_in_progress；客户端发起的
// 命令开始记录保存位置的作答。调用方持有 t.mu。
func admitOmfgLocked(t *thread, p *pendingCmd) error {
	for _, q := range t.pending {
		if q.cmdType == "omfg" {
			return errf(CodeOmfgInProgress, "another /omfg is still running in this thread; finish or cancel it first")
		}
	}
	if userOmfg(p) {
		p.omfg = &omfgChoice{since: t.live.Seq()}
	}
	return nil
}

// noteOmfgChoiceLocked 在 extension_ui_response 已写入 / 排队之后、移除待答请求之前，记录用户在保存位置对话框里的
// 作答。只认：线程上有客户端发起的 omfg 在途，被回答的请求是控制面在实时缓冲里见过、晚于该命令受理、方法为 select、
// 标题为保存位置的对话框。对这个对话框的任何作答（含取消、修改反馈）先作废此前的记录；值是对话框选项之一的
// 全局 / 本项目文案时，记下范围与此刻的规则预览。调用方持有 t.mu。
func noteOmfgChoiceLocked(t *thread, uiID string, frame json.RawMessage) {
	var p *pendingCmd
	for _, q := range t.pending {
		if q.omfg != nil {
			p = q
			break
		}
	}
	if p == nil {
		return
	}
	req, seq, ok := t.live.UIRequest(uiID)
	if !ok || seq <= p.omfg.since {
		return
	}
	var q struct {
		Method  string   `json:"method"`
		Title   string   `json:"title"`
		Options []string `json:"options"`
	}
	if json.Unmarshal(req, &q) != nil || q.Method != "select" || q.Title != omfgSaveTitle {
		return
	}
	p.omfg.global, p.omfg.preview = false, ""
	var r struct {
		Value *string `json:"value"`
	}
	if json.Unmarshal(frame, &r) != nil || r.Value == nil || !slices.Contains(q.Options, *r.Value) {
		return
	}
	v := *r.Value
	if !omfgGlobalLabels[v] && !omfgProjectLabels[v] {
		return
	}
	p.omfg.global = omfgGlobalLabels[v]
	p.omfg.preview = omfgPreviewLocked(t)
}

// omfgPreviewLocked 取当前 omfg 规则预览里的规则全文；没有预览或形状不对返回空串。调用方持有 t.mu。
func omfgPreviewLocked(t *thread) string {
	lines, ok := t.live.WidgetLines(omfgWidgetKey)
	if !ok || len(lines) < 3 || !strings.HasPrefix(lines[0], omfgPreviewHead) || lines[1] != "" {
		return ""
	}
	return strings.Join(lines[2:], "\n")
}

// omfgSaveLocked 处理 omfg 应答：需要回写时登记在途回写、在后台完成回写并交付，返回 true（调用方不再交付）。
// 调用方持有 t.mu。
func (s *Service) omfgSaveLocked(t *thread, p *pendingCmd, env omprpc.Envelope, frame json.RawMessage, r responseHead) bool {
	if s.cfgSync == nil || t.kind != KindWork || !userOmfg(p) || p.omfg == nil || !r.Success || t.orbID == "" {
		return false
	}
	var d struct {
		State     string `json:"state"`
		SavedPath string `json:"savedPath"`
	}
	if json.Unmarshal(r.Data, &d) != nil || d.State != "saved" || d.SavedPath == "" {
		return false
	}
	sp := ConfigSaveParams{OrbID: t.orbID, ThreadID: t.id, SavedPath: d.SavedPath, GlobalChosen: p.omfg.global, Preview: p.omfg.preview}
	// 应答已离开 pending：持锁登记在途回写，冻结闸门在 finishOmfg 撤销之前不放行
	t.cfgSyncs++
	if !s.go_(func(ctx context.Context) { s.finishOmfg(ctx, t, p, env, frame, r, sp) }) {
		// 服务正在关闭：不回写，原帧交付
		t.cfgSyncs--
		return false
	}
	return true
}

// finishOmfg 在后台回写 omfg 保存的规则（不持 t.mu 做 IO），把结果并进应答的 data.oryn 后交付、撤销在途登记并结算。
// runner 线程用的是用户机器上自己的 omp 配置：不回写，原帧交付。
func (s *Service) finishOmfg(ctx context.Context, t *thread, p *pendingCmd, env omprpc.Envelope, frame json.RawMessage, r responseHead, sp ConfigSaveParams) {
	if kind, _ := s.orbs.Features(sp.OrbID); kind != orbs.KindRunner {
		// 读 orb 文件期间算 agent 活动（冻结闸门另由 cfgSyncs 挡住）
		s.orbs.TouchAgent(ctx, sp.OrbID)
		cctx, cancel := context.WithTimeout(ctx, s.cfgSyncTimeout)
		res := s.cfgSync.SaveRule(cctx, sp)
		cancel()
		frame = withOrynSync(frame, res)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cfgSyncs--
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
