package ownermem

// facts.go：浏览、召回主人记忆里的事实并给出出处（R13、R20），以及 world 事实的失效与恢复。

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

const (
	defaultFactLimit = 30
	maxFactLimit     = 100
	factRecallTokens = 8192
	// explicitDocPrefix 是明确记忆的 document id 前缀（R17）。
	explicitDocPrefix = "explicit:"
)

// hsFact 是 Hindsight 召回结果、记忆列表与单条记忆里用到的字段（三处字段名略有不同）。
type hsFact struct {
	ID            string         `json:"id"`
	Text          string         `json:"text"`
	Type          string         `json:"type"`
	FactType      string         `json:"fact_type"`
	MentionedAt   *string        `json:"mentioned_at"`
	DocumentID    *string        `json:"document_id"`
	Metadata      map[string]any `json:"metadata"`
	State         string         `json:"state"`
	SourceFactIDs []string       `json:"source_fact_ids"`
	SourceMemIDs  []string       `json:"source_memory_ids"`
}

func (f hsFact) kind() string {
	if f.Type != "" {
		return f.Type
	}
	return f.FactType
}

func (f hsFact) sources() []string {
	if len(f.SourceFactIDs) > 0 {
		return f.SourceFactIDs
	}
	return f.SourceMemIDs
}

func (f hsFact) meta(k string) string {
	if v, ok := f.Metadata[k].(string); ok {
		return v
	}
	return ""
}

// provResolver 解析出处；同一请求内缓存线程信息与已读取的来源事实。
type provResolver struct {
	s       *Service
	b       *memory.Bank
	facts   map[string]hsFact // 召回附带的来源事实
	threads map[string]apigen.OwnerMemoryProvenance
}

func (s *Service) newResolver(b *memory.Bank, facts map[string]hsFact) *provResolver {
	if facts == nil {
		facts = map[string]hsFact{}
	}
	return &provResolver{s: s, b: b, facts: facts, threads: map[string]apigen.OwnerMemoryProvenance{}}
}

// of 给出一条事实的出处：明确记忆；来自对话或线程的块（只含主人消息为 owner，否则 mixed）；observation 沿来源事实回溯；
// 都不行时 unknown。
func (r *provResolver) of(ctx context.Context, f hsFact) apigen.OwnerMemoryProvenance {
	if f.DocumentID != nil && *f.DocumentID != "" {
		return r.fromDocument(ctx, f)
	}
	if f.kind() == "observation" {
		srcs := f.sources()
		if len(srcs) == 0 {
			// 列表里的 observation 不带来源，读单条
			var full hsFact
			if err := r.b.Call(ctx, http.MethodGet, "/memories/"+url.PathEscape(f.ID), nil, &full); err == nil {
				srcs = full.sources()
			}
		}
		for _, id := range srcs {
			src, ok := r.facts[id]
			if !ok {
				if err := r.b.Call(ctx, http.MethodGet, "/memories/"+url.PathEscape(id), nil, &src); err != nil {
					continue
				}
				r.facts[id] = src
			}
			if src.DocumentID != nil && *src.DocumentID != "" {
				return r.fromDocument(ctx, src)
			}
		}
	}
	return apigen.OwnerMemoryProvenance{Kind: apigen.OwnerMemoryProvenanceUnknown}
}

func (r *provResolver) fromDocument(ctx context.Context, f hsFact) apigen.OwnerMemoryProvenance {
	doc := *f.DocumentID
	if strings.HasPrefix(doc, explicitDocPrefix) {
		return apigen.OwnerMemoryProvenance{Kind: apigen.OwnerMemoryProvenanceExplicit, DocumentID: &doc}
	}
	src, rest, ok := strings.Cut(doc, ":")
	threadID, _, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || (src != SourceConv && src != SourceThread) {
		return apigen.OwnerMemoryProvenance{Kind: apigen.OwnerMemoryProvenanceUnknown, DocumentID: &doc}
	}
	p := r.thread(ctx, threadID)
	p.DocumentID = &doc
	p.Kind = apigen.OwnerMemoryProvenanceMixed
	roles := strings.Split(f.meta("roles"), ",")
	if len(roles) > 0 && roles[0] != "" {
		owner := true
		for _, role := range roles {
			owner = owner && role == roleOwner
		}
		if owner {
			p.Kind = apigen.OwnerMemoryProvenanceOwner
		}
	}
	var seqs []int64
	if v := f.meta("seqs"); v != "" {
		for _, x := range strings.Split(v, ",") {
			if n, err := strconv.ParseInt(x, 10, 64); err == nil {
				seqs = append(seqs, n)
			}
		}
	} else {
		for _, k := range []string{"seq_from", "seq_to"} {
			if n, err := strconv.ParseInt(f.meta(k), 10, 64); err == nil {
				seqs = append(seqs, n)
			}
		}
	}
	if len(seqs) > 0 {
		lo, hi := seqs[0], seqs[0]
		for _, n := range seqs {
			lo, hi = min(lo, n), max(hi, n)
		}
		p.SeqFrom, p.SeqTo = &lo, &hi
	}
	return p
}

// thread 取线程的 id、种类与标题（线程已删除时只有 id）。
func (r *provResolver) thread(ctx context.Context, id string) apigen.OwnerMemoryProvenance {
	if p, ok := r.threads[id]; ok {
		return p
	}
	tid := id
	p := apigen.OwnerMemoryProvenance{ThreadID: &tid}
	if t, err := r.s.cfg.Q.GetThread(ctx, id); err == nil {
		kind, title := t.Kind, t.Title
		p.ThreadKind, p.ThreadTitle = &kind, &title
	}
	r.threads[id] = p
	return p
}

// Facts 浏览（按时间倒序分页）或召回（给了 q，按相关度，不分页）world 事实与 observation，每条带出处。
func (s *Service) Facts(ctx context.Context, p apigen.ListOwnerMemoryFactsParams) (apigen.OwnerMemoryFactList, error) {
	out := apigen.OwnerMemoryFactList{Items: []apigen.OwnerMemoryFact{}}
	b, err := s.bank(ctx)
	if err != nil {
		return out, err
	}
	limit, offset := defaultFactLimit, 0
	if p.Limit != nil {
		limit = min(max(*p.Limit, 1), maxFactLimit)
	}
	if p.Offset != nil && *p.Offset > 0 {
		offset = *p.Offset
	}
	types := []string{"world", "observation"}
	if p.Type != nil {
		types = []string{string(*p.Type)}
	}
	var facts []hsFact
	var sources map[string]hsFact
	if p.Q != nil && strings.TrimSpace(*p.Q) != "" {
		var resp struct {
			Results     []hsFact          `json:"results"`
			SourceFacts map[string]hsFact `json:"source_facts"`
		}
		if err := b.Call(ctx, http.MethodPost, "/memories/recall", map[string]any{
			"query": *p.Q, "types": types, "max_tokens": factRecallTokens, "budget": "mid",
			"include": map[string]any{"source_facts": map[string]any{}},
		}, &resp); err != nil {
			return out, upstream("召回", err)
		}
		facts, sources = resp.Results, resp.SourceFacts
		if len(facts) > limit {
			facts = facts[:limit]
		}
	} else {
		q := url.Values{"limit": {strconv.Itoa(limit + 1)}, "offset": {strconv.Itoa(offset)}}
		if p.Type != nil {
			q.Set("type", string(*p.Type))
		}
		var resp struct {
			Items []hsFact `json:"items"`
		}
		if err := b.Call(ctx, http.MethodGet, "/memories/list?"+q.Encode(), nil, &resp); err != nil {
			return out, upstream("列出记忆", err)
		}
		facts = resp.Items
		if len(facts) > limit {
			facts, out.HasMore = facts[:limit], true
		}
	}
	r := s.newResolver(b, sources)
	for _, f := range facts {
		kind := f.kind()
		if kind != "world" && kind != "observation" {
			continue
		}
		state := apigen.OwnerMemoryFactValid
		if f.State == string(apigen.OwnerMemoryFactInvalidated) {
			state = apigen.OwnerMemoryFactInvalidated
		}
		out.Items = append(out.Items, apigen.OwnerMemoryFact{
			ID: f.ID, Text: f.Text, Type: kind, MentionedAt: f.MentionedAt, State: state, Provenance: r.of(ctx, f),
		})
	}
	return out, nil
}

// SetFactState 让一条 world 事实失效或恢复（R20）。改动前把全部主题标为待刷新，完成后刷新：刷新完成前它们不进入注入。
func (s *Service) SetFactState(ctx context.Context, id string, in apigen.OwnerMemoryFactStateUpdate) error {
	if in.State != apigen.Invalidated && in.State != apigen.Valid {
		return errInvalid("state 只能是 invalidated 或 valid")
	}
	b, err := s.bank(ctx)
	if err != nil {
		return err
	}
	var f hsFact
	if err := b.Call(ctx, http.MethodGet, "/memories/"+url.PathEscape(id), nil, &f); err != nil {
		if hsStatus(err) == http.StatusNotFound {
			return errNotFound("事实不存在")
		}
		return upstream("读取事实", err)
	}
	if f.kind() != "world" {
		return errInvalid("只有 world 事实可以直接失效；observation 请失效它的来源事实")
	}
	body := map[string]any{"state": string(in.State)}
	if in.Reason != nil {
		body["reason"] = *in.Reason
	}
	err = b.Forget(ctx, func(ctx context.Context) error {
		return b.Call(ctx, http.MethodPatch, "/memories/"+url.PathEscape(id), body, nil)
	})
	var me *Error
	if err != nil && !errors.As(err, &me) {
		return upstream("修改事实状态", err)
	}
	return err
}
