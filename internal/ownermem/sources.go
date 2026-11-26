package ownermem

// sources.go：来源列表、按来源遗忘与排除项（R6、R21）。

import (
	"context"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const defaultSourceLimit = 30

// Sources 按最近更新倒序列出来源（含写入失败的）。
func (s *Service) Sources(ctx context.Context, p apigen.ListOwnerMemorySourcesParams) (apigen.OwnerMemorySourceList, error) {
	limit, offset := defaultSourceLimit, 0
	if p.Limit != nil {
		limit = min(max(*p.Limit, 1), maxFactLimit)
	}
	if p.Offset != nil && *p.Offset > 0 {
		offset = *p.Offset
	}
	rows, err := s.cfg.Q.ListOwnerMemorySources(ctx, db.ListOwnerMemorySourcesParams{Lim: int64(limit + 1), Off: int64(offset)})
	if err != nil {
		return apigen.OwnerMemorySourceList{}, err
	}
	out := apigen.OwnerMemorySourceList{Items: make([]apigen.OwnerMemorySource, 0, len(rows))}
	if len(rows) > limit {
		rows, out.HasMore = rows[:limit], true
	}
	for _, r := range rows {
		out.Items = append(out.Items, apigen.OwnerMemorySource{
			ThreadID: r.ThreadID, Kind: apigen.OwnerMemorySourceKind(r.Kind), Chunks: int(r.Chunks), Attempts: int(r.Attempts),
			RetainedAt: r.RetainedAt, NextAttemptAt: r.NextAttemptAt, LastError: r.LastError,
			Missing: r.ThreadKind == nil, ThreadKind: r.ThreadKind, ThreadTitle: r.ThreadTitle, ProjectID: r.ThreadProjectID,
		})
	}
	return out, nil
}

// ForgetSource 按来源遗忘：删除它写入的全部 document。之后有新内容时仍会再写入（除非设了排除），
// 所以记下当前线程版本、块数 0，而不是删掉来源记录（否则下一轮扫描会把旧内容原样写回）。删除与记下版本都在
// 来源的门内完成：等门的写入拿到门后看到版本已是当前的，就不会把旧内容写回。
func (s *Service) ForgetSource(ctx context.Context, threadID string) (apigen.MemoryForgetResult, error) {
	src, err := s.cfg.Q.GetOwnerMemorySource(ctx, threadID)
	if errors.Is(err, errNoRows) {
		return apigen.MemoryForgetResult{}, errNotFound("这个对话或线程没有写入助手记忆")
	}
	if err != nil {
		return apigen.MemoryForgetResult{}, err
	}
	b, err := s.bank(ctx)
	if err != nil {
		return apigen.MemoryForgetResult{}, err
	}
	_, release := s.lockSource(threadID, true)
	defer release()
	out, err := s.forgetLocked(ctx, b, threadID)
	if err != nil {
		return out, err
	}
	t, err := s.cfg.Q.GetThread(ctx, threadID)
	if errors.Is(err, errNoRows) {
		return out, nil // 线程已删除：来源记录随之清掉
	}
	if err != nil {
		return out, err
	}
	now := s.cfg.Now().UnixMilli()
	return out, s.cfg.Q.MarkOwnerMemorySourceRetained(ctx, db.MarkOwnerMemorySourceRetainedParams{
		ThreadID: threadID, Kind: src.Kind, LastMessageAt: t.LastMessageAt, TranscriptRev: t.TranscriptRev, Now: &now,
	})
}

// Exclusions 列出排除项（带对话标题或项目名）。
func (s *Service) Exclusions(ctx context.Context) (apigen.OwnerMemoryExclusionList, error) {
	rows, err := s.cfg.Q.ListOwnerMemoryExclusions(ctx)
	if err != nil {
		return apigen.OwnerMemoryExclusionList{}, err
	}
	out := apigen.OwnerMemoryExclusionList{Items: make([]apigen.OwnerMemoryExclusion, 0, len(rows))}
	for _, r := range rows {
		e := apigen.OwnerMemoryExclusion{Kind: apigen.OwnerMemoryExclusionKind(r.Kind), TargetID: r.TargetID, CreatedAt: r.CreatedAt}
		if name, err := s.targetName(ctx, r.Kind, r.TargetID); err == nil {
			e.Name = &name
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// targetName 是排除对象的显示名；对象不存在时返回 errNoRows。
func (s *Service) targetName(ctx context.Context, kind, id string) (string, error) {
	if kind == string(apigen.OwnerMemoryExclusionProject) {
		p, err := s.cfg.Q.GetProject(ctx, id)
		return p.Name, err
	}
	t, err := s.cfg.Q.GetThread(ctx, id)
	return t.Title, err
}

// PutExclusion 设为不进助手记忆，已写入的随之遗忘（R6）。助手记忆没有运行时只记下排除项，
// 已写入的 document 在下次运行的扫描里遗忘。
func (s *Service) PutExclusion(ctx context.Context, kind apigen.OwnerMemoryExclusionKind, id string) (apigen.MemoryForgetResult, error) {
	var out apigen.MemoryForgetResult
	if !kind.Valid() {
		return out, errInvalid("kind 只能是 thread 或 project")
	}
	if _, err := s.targetName(ctx, string(kind), id); errors.Is(err, errNoRows) {
		return out, errNotFound("对象不存在")
	} else if err != nil {
		return out, err
	}
	if err := s.cfg.Q.InsertOwnerMemoryExclusion(ctx, db.InsertOwnerMemoryExclusionParams{
		Kind: string(kind), TargetID: id, CreatedAt: s.cfg.Now().UnixMilli(),
	}); err != nil {
		return out, err
	}
	if !s.Enabled(ctx) {
		return out, nil
	}
	b, err := s.bank(ctx)
	if err != nil {
		return out, err
	}
	threads := []string{id}
	if kind == apigen.OwnerMemoryExclusionProject {
		if threads, err = s.cfg.Q.ListOwnerMemorySourcesOfProject(ctx, &id); err != nil {
			return out, err
		}
	}
	for _, t := range threads {
		r, err := s.forgetSource(ctx, b, t)
		out.Deleted += r.Deleted
		out.Pending += r.Pending
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// DeleteExclusion 取消排除；之后有新内容时重新写入。
func (s *Service) DeleteExclusion(ctx context.Context, kind apigen.OwnerMemoryExclusionKind, id string) error {
	n, err := s.cfg.Q.DeleteOwnerMemoryExclusion(ctx, db.DeleteOwnerMemoryExclusionParams{Kind: string(kind), TargetID: id})
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound("没有这个排除项")
	}
	return nil
}
