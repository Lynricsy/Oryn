package transcript

import "encoding/json"

// ModelSourceType 是 Oryn 模型来源记录的 customType（oryn-core `modes/rpc/oryn-model-source.ts` 写入；
// 条目形状与判定规则见 docs/protocol.md「M9.6：模型选择来源」）。
const ModelSourceType = "oryn-model-source"

// ModelSource 是一条合法的来源记录（data v1）。Role / Alias 可为空。
type ModelSource struct{ Source, Model, Role, Alias string }

// modelSourceSources 是合法的 data.source 取值。
var modelSourceSources = map[string]bool{"role": true, "manual": true, "startup": true}

// ParseModelSource 解析来源记录：type=custom、customType=oryn-model-source、data.v=1、data.source 合法、
// data.model 为非空字符串。不满足的条目当作不存在（ok=false）。role / alias 不是字符串时按空处理。
// 不看 File：只认主会话文件的记录由调用方保证（UsedRoles 的输入是活动分支）。
func ParseModelSource(e Entry) (ModelSource, bool) {
	if e.Type != "custom" {
		return ModelSource{}, false
	}
	var p struct {
		CustomType string `json:"customType"`
		Data       *struct {
			V      json.RawMessage `json:"v"`
			Source json.RawMessage `json:"source"`
			Model  json.RawMessage `json:"model"`
			Role   json.RawMessage `json:"role"`
			Alias  json.RawMessage `json:"alias"`
		} `json:"data"`
	}
	if json.Unmarshal(e.Payload, &p) != nil || p.CustomType != ModelSourceType || p.Data == nil {
		return ModelSource{}, false
	}
	var v float64
	if json.Unmarshal(p.Data.V, &v) != nil || v != 1 {
		return ModelSource{}, false
	}
	ms := ModelSource{Source: jsonString(p.Data.Source), Model: jsonString(p.Data.Model), Role: jsonString(p.Data.Role), Alias: jsonString(p.Data.Alias)}
	if !modelSourceSources[ms.Source] || ms.Model == "" {
		return ModelSource{}, false
	}
	return ms, true
}

// jsonString 取 JSON 字符串的值；缺失或不是字符串时返回空串。
func jsonString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// UsedRoles 按分支顺序走一遍（Type 必填；custom / model_change 须带 Payload），返回每条「生效期间出现过 message 条目」的
// role / startup 记录的候选 [role, alias]（去空、去重；两项都空的记录不出现）。
// 记录的生效期：到下一条来源记录或下一条 role 不是 fallback / prewalk 的 model_change 为止（与 web/src/lib/turn-role.ts 的轨迹一致）；
// manual 记录不计。
func UsedRoles(branch []Entry) [][]string {
	var (
		out  [][]string
		cur  *ModelSource
		used bool
	)
	flush := func() {
		if cur != nil && used && cur.Source != "manual" {
			var cands []string
			for _, r := range []string{cur.Role, cur.Alias} {
				if r != "" && (len(cands) == 0 || cands[0] != r) {
					cands = append(cands, r)
				}
			}
			if len(cands) > 0 {
				out = append(out, cands)
			}
		}
		cur, used = nil, false
	}
	for _, e := range branch {
		switch e.Type {
		case "custom":
			if ms, ok := ParseModelSource(e); ok {
				flush()
				cur = &ms
			}
		case "model_change":
			var p struct {
				Role json.RawMessage `json:"role"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if r := jsonString(p.Role); r != "fallback" && r != "prewalk" {
				flush()
			}
		case "message":
			if cur != nil {
				used = true
			}
		}
	}
	flush()
	return out
}
