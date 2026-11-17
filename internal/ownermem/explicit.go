package ownermem

// explicit.go：明确记忆（R17、R22）。生效的明确记忆写成 document `explicit:<id>`（标签 `src:explicit`）；
// 待确认的只在本地，主人批准后写入。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/memory"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

const (
	explicitPending = "pending"
	explicitActive  = "active"
	// maxExplicitRunes 是一条明确记忆的长度上限。
	maxExplicitRunes = 2000
	tagExplicit      = "src:explicit"
)

func explicitDocID(id string) string { return explicitDocPrefix + id }

// cleanContent 校验并整理明确记忆的内容。
func cleanContent(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxExplicitRunes {
		return "", errInvalid("内容不能为空，且不超过 2000 字")
	}
	return s, nil
}

func (s *Service) explicitView(ctx context.Context, r db.OwnerMemoryExplicit) apigen.OwnerMemoryExplicit {
	out := apigen.OwnerMemoryExplicit{
		ID: r.ID, Content: r.Content, OwnerQuote: r.OwnerQuote, SourceThreadID: r.SourceThreadID,
		Status: apigen.OwnerMemoryExplicitStatus(r.Status), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.SourceThreadID != nil {
		if t, err := s.cfg.Q.GetThread(ctx, *r.SourceThreadID); err == nil {
			out.SourceThreadTitle = &t.Title
		}
	}
	return out
}

// retainExplicit 把一条明确记忆写成（或整体替换）它的 document。
func (s *Service) retainExplicit(ctx context.Context, b *memory.Bank, id, content string, at time.Time) error {
	item := retainItem{RetainItem: RetainItem{
		Content: "主人明确要求记住：" + s.redact(ctx)(content), Timestamp: at.UTC().Format(time.RFC3339),
		Context: "明确记忆", DocumentID: explicitDocID(id), Entities: []Entity{{Text: ownerEntity}}, Tags: []string{tagExplicit},
	}, UpdateMode: "replace"}
	if err := b.Call(ctx, http.MethodPost, "/memories", map[string]any{"items": []retainItem{item}, "async": true}, nil); err != nil {
		return upstream("写入明确记忆", err)
	}
	return nil
}

// ListExplicit 列出明确记忆（待确认的在前）。
func (s *Service) ListExplicit(ctx context.Context) (apigen.OwnerMemoryExplicitList, error) {
	rows, err := s.cfg.Q.ListOwnerMemoryExplicit(ctx)
	if err != nil {
		return apigen.OwnerMemoryExplicitList{}, err
	}
	out := apigen.OwnerMemoryExplicitList{Items: make([]apigen.OwnerMemoryExplicit, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, s.explicitView(ctx, r))
	}
	return out, nil
}

func (s *Service) getExplicit(ctx context.Context, id string) (db.OwnerMemoryExplicit, error) {
	r, err := s.cfg.Q.GetOwnerMemoryExplicit(ctx, id)
	if errors.Is(err, errNoRows) {
		return r, errNotFound("明确记忆不存在")
	}
	return r, err
}

// CreateExplicit 由主人在管理页新建一条明确记忆，直接生效。
func (s *Service) CreateExplicit(ctx context.Context, content string) (apigen.OwnerMemoryExplicit, error) {
	return s.addExplicit(ctx, content, nil, nil, true)
}

// Remember 是助手的 remember 工具（R17）：ownerQuote 在本对话活动分支上主人亲手输入的消息里逐字出现时直接生效，
// 否则进入待确认。返回的 Status 说明是否已生效。
func (s *Service) Remember(ctx context.Context, threadID, content string, ownerQuote *string) (apigen.OwnerMemoryExplicit, error) {
	quoted := false
	if ownerQuote != nil && strings.TrimSpace(*ownerQuote) != "" {
		t, err := s.cfg.Q.GetThread(ctx, threadID)
		if err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
		branch, err := s.activeBranch(ctx, t)
		if err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
		quote := strings.TrimSpace(*ownerQuote)
		for _, e := range branch {
			m, ok := transcript.ParseMessage(e)
			if ok && m.Role == "user" && !transcript.IsAutomatedUserText(m.Text) && strings.Contains(m.Text, quote) {
				quoted = true
				break
			}
		}
	}
	return s.addExplicit(ctx, content, ownerQuote, &threadID, quoted)
}

func (s *Service) addExplicit(ctx context.Context, content string, quote, thread *string, active bool) (apigen.OwnerMemoryExplicit, error) {
	content, err := cleanContent(content)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	var b *memory.Bank
	status := explicitPending
	if active {
		if b, err = s.bank(ctx); err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
		status = explicitActive
	} else if !s.Enabled(ctx) {
		return apigen.OwnerMemoryExplicit{}, errUnavailable
	}
	now := s.cfg.Now()
	id := ids.New("ome")
	if err := s.cfg.Q.InsertOwnerMemoryExplicit(ctx, db.InsertOwnerMemoryExplicitParams{
		ID: id, Content: content, OwnerQuote: quote, SourceThreadID: thread, Status: status, Now: now.UnixMilli(),
	}); err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	if active {
		if err := s.retainExplicit(ctx, b, id, content, now); err != nil {
			_, _ = s.cfg.Q.DeleteOwnerMemoryExplicit(context.WithoutCancel(ctx), id)
			return apigen.OwnerMemoryExplicit{}, err
		}
	}
	r, err := s.getExplicit(ctx, id)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	return s.explicitView(ctx, r), nil
}

// UpdateExplicit 修改内容；已生效的随之重写 document（替换旧内容属于遗忘：刷新完成前主题不进入注入）。
func (s *Service) UpdateExplicit(ctx context.Context, id, content string) (apigen.OwnerMemoryExplicit, error) {
	content, err := cleanContent(content)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	r, err := s.getExplicit(ctx, id)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	now := s.cfg.Now()
	if r.Status == explicitActive && content != r.Content {
		b, err := s.bank(ctx)
		if err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
		if err := b.Forget(ctx, func(ctx context.Context) error { return s.retainExplicit(ctx, b, id, content, now) }); err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
	}
	return s.setExplicit(ctx, r, content, r.Status, now)
}

// ApproveExplicit 批准待确认的明确记忆（可先改写内容），写入 bank。
func (s *Service) ApproveExplicit(ctx context.Context, id string, content *string) (apigen.OwnerMemoryExplicit, error) {
	r, err := s.getExplicit(ctx, id)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	if r.Status != explicitPending {
		return apigen.OwnerMemoryExplicit{}, &Error{Status: http.StatusConflict, Code: "not_pending", Message: "这条记忆已经生效"}
	}
	text := r.Content
	if content != nil {
		if text, err = cleanContent(*content); err != nil {
			return apigen.OwnerMemoryExplicit{}, err
		}
	}
	b, err := s.bank(ctx)
	if err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	now := s.cfg.Now()
	if err := s.retainExplicit(ctx, b, id, text, now); err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	return s.setExplicit(ctx, r, text, explicitActive, now)
}

func (s *Service) setExplicit(ctx context.Context, r db.OwnerMemoryExplicit, content, status string, now time.Time) (apigen.OwnerMemoryExplicit, error) {
	if _, err := s.cfg.Q.UpdateOwnerMemoryExplicit(ctx, db.UpdateOwnerMemoryExplicitParams{
		ID: r.ID, Content: content, Status: status, UpdatedAt: now.UnixMilli(),
	}); err != nil {
		return apigen.OwnerMemoryExplicit{}, err
	}
	r.Content, r.Status, r.UpdatedAt = content, status, now.UnixMilli()
	return s.explicitView(ctx, r), nil
}

// DeleteExplicit 删除明确记忆：已生效的连同 document 一起删除（遗忘保护），待确认的即拒绝。
func (s *Service) DeleteExplicit(ctx context.Context, id string) error {
	r, err := s.getExplicit(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == explicitActive {
		b, err := s.bank(ctx)
		if err != nil {
			return err
		}
		if _, _, err := b.DeleteDocuments(ctx, []string{explicitDocID(id)}); err != nil {
			return err
		}
	}
	_, err = s.cfg.Q.DeleteOwnerMemoryExplicit(ctx, id)
	return err
}
