package github

import (
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M15 R14：触发正文或跟进评论里单独成行的 `/model <别名>`、`/thinking <级别>` 覆盖本次的模型与思考级别
// （只认维护者的指令，判定在调用方）。语义移植自 robomp 的 pragmas.py。

// pragmas 是从正文里解析出的指令；未出现的字段为空串。
type pragmas struct {
	Model    string
	Thinking string
}

// parsePragmas 逐行查找 `/model <x>` 与 `/thinking <x>`：整行（去掉首尾空白）只能是指令名加一个参数，
// 同一指令出现多次时取最后一次。代码块里的行不算指令。
func parsePragmas(body string) pragmas {
	var p pragmas
	inFence := false
	for line := range strings.Lines(body) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "/model":
			p.Model = fields[1]
		case "/thinking":
			p.Thinking = fields[1]
		}
	}
	return p
}

// thinkingAliases 把思考级别的别名规范为 omp 的级别（off、low、medium、high、xhigh、max）。
var thinkingAliases = map[string]string{
	"off": "off", "none": "off", "no": "off",
	"lo": "low", "low": "low",
	"med": "medium", "medium": "medium",
	"hi": "high", "high": "high",
	"xhi": "xhigh", "xhigh": "xhigh",
	"max": "max",
}

// resolveThinking 规范思考级别别名；不认识的返回 false。
func resolveThinking(alias string) (string, bool) {
	v, ok := thinkingAliases[strings.ToLower(strings.TrimSpace(alias))]
	return v, ok
}

// resolveModel 在候选模型里按别名查找：先比完整 id（`provider/id`），再比斜杠后的短名（模型 id），最后比子串。
// 每一档只在恰好命中一个时成立；命中多个视为有歧义，返回 false，不再看下一档。比较不区分大小写。
func resolveModel(alias string, models []apigen.ModelRef) (apigen.ModelRef, bool) {
	a := strings.ToLower(strings.TrimSpace(alias))
	if a == "" {
		return apigen.ModelRef{}, false
	}
	tiers := []func(m apigen.ModelRef) bool{
		func(m apigen.ModelRef) bool { return strings.ToLower(m.Provider+"/"+m.ID) == a },
		func(m apigen.ModelRef) bool { return strings.ToLower(m.ID) == a },
		func(m apigen.ModelRef) bool { return strings.Contains(strings.ToLower(m.Provider+"/"+m.ID), a) },
	}
	for _, match := range tiers {
		var hit []apigen.ModelRef
		for _, m := range models {
			if match(m) {
				hit = append(hit, m)
			}
		}
		switch len(hit) {
		case 0:
			continue
		case 1:
			return hit[0], true
		default:
			return apigen.ModelRef{}, false
		}
	}
	return apigen.ModelRef{}, false
}

// pickModel 从规则的模型池里随机取一个（intn 返回 [0, n)，测试可注入）；池为空返回 nil。
func pickModel(pool []apigen.ModelRef, intn func(n int) int) *apigen.ModelRef {
	if len(pool) == 0 {
		return nil
	}
	m := pool[intn(len(pool))]
	return &m
}
