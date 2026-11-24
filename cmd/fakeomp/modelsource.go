package main

import (
	"encoding/json"
	"time"
)

// 两条 Oryn 命令按 oryn-core `modes/rpc/oryn-model-source.ts` 的条目形状写会话文件：
//   - `oryn_select_model{role}`：模型不变（fakeomp 没有 role → 模型表），追加 `model_change{model, role}` + 来源记录 `source:"role"`；
//   - `oryn_select_model{provider, modelId}`：换模型，追加 `model_change{model, role:"default"}` + 来源记录 `source:"manual"`，
//     模型变了先发 `model_changed` 帧；
//   - 两种形式都接受可选 `thinkingLevel`（选完后再设，追加 `thinking_level_change`）；`startup` 忽略（fakeomp 没有计划模式，
//     开场 role 总是照常选）；
//   - `oryn_mark_session_start{explicitModel}`：会话没有 message 条目且没有来源记录时追加来源记录
//     （explicitModel 为真 → `manual`，否则 `startup, role:"default"`）；fakeomp 不写启动时的 model_change，所以不核实它。

// modelSourceData 对应 OrynModelSourceData（v1）。
type modelSourceData struct {
	V      int    `json:"v"`
	Source string `json:"source"`
	Model  string `json:"model"`
	Role   string `json:"role,omitempty"`
}

// appendModelSourceLocked 追加一条来源记录。调用方持有 s.mu 且 s.sess 非 nil。
func (s *server) appendModelSourceLocked(d modelSourceData, now time.Time) error {
	_, err := s.sess.appendEntry("custom", map[string]any{"customType": modelSourceType, "data": d}, now)
	return err
}

func (s *server) selectModel(id string, line []byte) {
	const cmd = "oryn_select_model"
	var c struct {
		Role          *string `json:"role"`
		Provider      *string `json:"provider"`
		ModelID       *string `json:"modelId"`
		ThinkingLevel string  `json:"thinkingLevel"`
	}
	_ = json.Unmarshal(line, &c)
	nonEmpty := func(p *string) bool { return p != nil && *p != "" }
	var role string
	switch {
	case nonEmpty(c.Role) && c.Provider == nil && c.ModelID == nil:
		role = *c.Role
	case nonEmpty(c.Provider) && nonEmpty(c.ModelID) && c.Role == nil:
	case c.Role == nil && (c.Provider == nil) != (c.ModelID == nil):
		s.fail(id, cmd, "provider and modelId must be given together")
		return
	default:
		s.fail(id, cmd, "oryn_select_model requires either role or provider+modelId")
		return
	}
	if err := s.ensureSession(); err != nil {
		s.fail(id, cmd, err.Error())
		return
	}
	now := time.Now()
	s.mu.Lock()
	oldProvider, oldModel := s.modelLocked()
	changeRole, source := role, "role"
	if role == "" {
		s.provider, s.modelID = *c.Provider, *c.ModelID
		changeRole, source = "default", "manual"
	} else if role == "default" {
		s.provider, s.modelID = "", ""
	}
	provider, model := s.modelLocked()
	key := provider + "/" + model
	_, err := s.sess.appendEntry("model_change", map[string]any{"model": key, "role": changeRole}, now)
	if err == nil {
		err = s.appendModelSourceLocked(modelSourceData{V: 1, Source: source, Model: key, Role: role}, now)
	}
	if c.ThinkingLevel != "" {
		s.thinking = c.ThinkingLevel
		if err == nil {
			_, err = s.sess.appendEntry("thinking_level_change", map[string]any{"thinkingLevel": c.ThinkingLevel}, now)
		}
	}
	thinking := s.thinking
	s.mu.Unlock()
	if err != nil {
		s.fail(id, cmd, err.Error())
		return
	}
	if provider != oldProvider || model != oldModel {
		s.emitEvent(map[string]any{"type": "model_changed"})
	}
	res := map[string]any{
		"model":  map[string]any{"provider": provider, "id": model, "name": model, "api": "fake"},
		"source": source,
	}
	if thinking != "" {
		res["thinkingLevel"] = thinking
	}
	if role != "" {
		res["role"] = role
	}
	s.ok(id, cmd, res)
}

func (s *server) markSessionStart(id string, line []byte) {
	const cmd = "oryn_mark_session_start"
	var c struct {
		ExplicitModel bool `json:"explicitModel"`
	}
	_ = json.Unmarshal(line, &c)
	if err := s.ensureSession(); err != nil {
		s.fail(id, cmd, err.Error())
		return
	}
	s.mu.Lock()
	if s.sess.hasMessage || s.sess.hasSource {
		s.mu.Unlock()
		s.ok(id, cmd, map[string]any{"recorded": false})
		return
	}
	provider, model := s.modelLocked()
	d := modelSourceData{V: 1, Source: "startup", Model: provider + "/" + model, Role: "default"}
	if c.ExplicitModel {
		d.Source, d.Role = "manual", ""
	}
	err := s.appendModelSourceLocked(d, time.Now())
	s.mu.Unlock()
	if err != nil {
		s.fail(id, cmd, err.Error())
		return
	}
	res := map[string]any{"recorded": true, "source": d.Source, "model": d.Model}
	if d.Role != "" {
		res["role"] = d.Role
	}
	s.ok(id, cmd, res)
}
