package ownermem

// recall.go：全局助手的 recall_memory 工具（R12、R13）：在 bank `oryn-owner` 召回前 50 条，经判断模型重排分档，
// 每条带时间与出处。

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Lynricsy/Oryn/internal/aiconsent"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

const (
	recallTokens = 8192
	// partialNote、noDirectNote、unrankedNote、unconsentedNote 是结果里的标注。
	partialNote  = "部分相关，可能是旧状态或属于别的对象"
	noDirectNote = "记忆里没有能直接回答的条目。"
	unrankedNote = "判断模型不可用，以下按召回顺序给出，未经重排。"
	// unconsentedNote：主人还没同意把数据发给判断模型（AI 数据同意），没有发出重排请求。
	unconsentedNote = "主人尚未同意把数据发给判断模型（" + aiconsent.Code + "，" + aiconsent.Hint + "），以下按召回顺序给出，未经重排。"
)

// RecallHit 是 recall_memory 的一条结果。
type RecallHit struct {
	Text   string `json:"text"`
	Type   string `json:"type"`
	When   string `json:"when,omitempty"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

// RecallResult 是 recall_memory 的结果；Note 是整体标注（没有能直接回答的条目、未经重排）。
type RecallResult struct {
	Note    string      `json:"note,omitempty"`
	Results []RecallHit `json:"results"`
}

// Recall 召回、重排并给出出处。
func (s *Service) Recall(ctx context.Context, query string) (RecallResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return RecallResult{}, errInvalid("query 不能为空")
	}
	b, err := s.bank(ctx)
	if err != nil {
		return RecallResult{}, err
	}
	var resp struct {
		Results     []hsFact          `json:"results"`
		SourceFacts map[string]hsFact `json:"source_facts"`
	}
	if err := b.Call(ctx, http.MethodPost, "/memories/recall", map[string]any{
		"query": query, "types": []string{"world", "observation"}, "max_tokens": recallTokens, "budget": "mid",
		"include": map[string]any{"source_facts": map[string]any{}},
	}, &resp); err != nil {
		return RecallResult{}, upstream("召回", err)
	}
	ranked := s.rerank(ctx, query, resp.Results)
	out := RecallResult{Results: []RecallHit{}}
	switch {
	case !ranked.Reranked && aiconsent.IsDenied(ranked.Err):
		out.Note = unconsentedNote
	case !ranked.Reranked:
		out.Note = unrankedNote
	case ranked.NoDirect:
		out.Note = noDirectNote
	}
	r := s.newResolver(b, resp.SourceFacts)
	for _, it := range ranked.Items {
		h := RecallHit{Text: it.Fact.Text, Type: it.Fact.kind(), Source: sourceText(r.of(ctx, it.Fact))}
		if it.Fact.MentionedAt != nil {
			h.When = *it.Fact.MentionedAt
		}
		if it.Partial {
			h.Note = partialNote
		}
		out.Results = append(out.Results, h)
	}
	return out, nil
}

// sourceText 把出处写成给模型看的一句话（R13）：只含主人消息标「主人陈述」；含助手或 agent 消息只标位置与「角色未确认」。
func sourceText(p apigen.OwnerMemoryProvenance) string {
	switch p.Kind {
	case apigen.OwnerMemoryProvenanceExplicit:
		return "明确记忆（主人要求记住的）"
	case apigen.OwnerMemoryProvenanceUnknown:
		return "来源未确认"
	}
	where := "对话"
	if p.DocumentID != nil && strings.HasPrefix(*p.DocumentID, SourceThread+":") {
		where = "线程"
	}
	loc := where
	if p.ThreadID != nil {
		loc += " " + *p.ThreadID
	}
	if p.ThreadTitle != nil && *p.ThreadTitle != "" {
		loc += "「" + *p.ThreadTitle + "」"
	}
	if p.SeqFrom != nil && p.SeqTo != nil {
		if *p.SeqFrom == *p.SeqTo {
			loc += fmt.Sprintf(" 第 %d 条", *p.SeqFrom)
		} else {
			loc += fmt.Sprintf(" 第 %d–%d 条", *p.SeqFrom, *p.SeqTo)
		}
	}
	if p.Kind == apigen.OwnerMemoryProvenanceOwner {
		return "主人陈述（出自" + loc + "）"
	}
	return "出自" + loc + "，角色未确认"
}
