package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
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

// modelsOf 解析规则的模型池（JSON 数组）；解析失败按空池。
func modelsOf(t db.GithubTrigger) []apigen.ModelRef {
	var pool []apigen.ModelRef
	_ = json.Unmarshal([]byte(t.Models), &pool)
	return pool
}

// pragmaOverride 解析 text 里的指令（R14）；text 的作者 login（assoc 是其 author_association）不是 rule 认可的维护者时忽略。
// 返回要覆盖的模型（nil = 不覆盖）与思考级别（空 = 不覆盖）。模型别名在模型目录里查找；没有目录或目录不可用时在规则的
// 模型池里查找。查不到、有歧义或级别不认识的指令各自忽略。
func (s *Service) pragmaOverride(ctx context.Context, text, login, assoc string, rule driveRule, pool []apigen.ModelRef) (*apigen.ModelRef, string) {
	p := parsePragmas(text)
	if p.Model == "" && p.Thinking == "" || !rule.pragmaAllowed(login, assoc) {
		return nil, ""
	}
	var model *apigen.ModelRef
	if p.Model != "" {
		cands := pool
		if s.models != nil {
			all, err := s.models.Models(ctx)
			switch {
			case err != nil:
				slog.Warn("github: 读取模型目录失败，/model 改在规则的模型池里查找", "err", err)
			case len(all) > 0:
				cands = all
			}
		}
		if m, ok := resolveModel(p.Model, cands); ok {
			model = &m
		} else {
			slog.Info("github: /model 指令没有唯一匹配的模型，忽略", "alias", p.Model)
		}
	}
	thinking := ""
	if p.Thinking != "" {
		if lv, ok := resolveThinking(p.Thinking); ok {
			thinking = lv
		} else {
			slog.Info("github: /thinking 指令的级别不认识，忽略", "level", p.Thinking)
		}
	}
	return model, thinking
}

// applyPragmas 对已准入的跟进评论应用指令覆盖（R14）：经 Threads.SetModel 切换线程的模型与思考级别。失败只记日志。
func (s *Service) applyPragmas(ctx context.Context, threadID, text, login, assoc string, rule driveRule, pool []apigen.ModelRef) {
	m, lv := s.pragmaOverride(ctx, text, login, assoc, rule, pool)
	if m == nil && lv == "" {
		return
	}
	var ref apigen.ModelRef
	if m != nil {
		ref = *m
	}
	if err := s.th.SetModel(ctx, threadID, ref, lv); err != nil {
		slog.Warn("github: 应用 /model、/thinking 指令失败", "thread", threadID, "err", err)
	}
}
