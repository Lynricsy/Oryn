package ownermem

// status.go：助手记忆的状态（管理页）。

import (
	"context"
	"net/http"
	"net/url"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/memory"
)

// Status 返回运行状态、回填进度、待确认数与 bank 统计。连不上 Hindsight 时 state 为 error，其余字段照常给出。
func (s *Service) Status(ctx context.Context) (apigen.OwnerMemoryStatus, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return apigen.OwnerMemoryStatus{}, err
	}
	out := apigen.OwnerMemoryStatus{BackfillSince: st.BackfillSince, LastCuratedAt: st.LastCuratedAt}
	if st.BackfillSince != nil {
		total, err := s.cfg.Q.CountOwnerMemoryBackfillThreads(ctx, st.BackfillSince)
		if err != nil {
			return out, err
		}
		done, err := s.cfg.Q.CountOwnerMemoryBackfillDone(ctx, st.BackfillSince)
		if err != nil {
			return out, err
		}
		out.BackfillTotal, out.BackfillDone = int(total), int(min(done, total))
	}
	failing, err := s.cfg.Q.CountOwnerMemorySourcesFailing(ctx)
	if err != nil {
		return out, err
	}
	pending, err := s.cfg.Q.CountOwnerMemoryExplicitPending(ctx)
	if err != nil {
		return out, err
	}
	out.FailingSources, out.PendingExplicit = int(failing), int(pending)

	switch {
	case !s.Available(ctx):
		out.State = apigen.OwnerMemoryStateUnavailable
		return out, nil
	case st.Enabled == 0:
		out.State = apigen.OwnerMemoryStateDisabled
		return out, nil
	}
	b, err := s.bank(ctx)
	if err == nil {
		err = s.fillBankStats(ctx, b, &out)
	}
	if err != nil {
		msg := err.Error()
		out.State, out.Error = apigen.OwnerMemoryStateError, &msg
		return out, nil
	}
	out.State = apigen.OwnerMemoryStateOk
	return out, nil
}

// llmStats 是 Hindsight `llm-requests/stats` 应答里用到的部分。
type llmStats struct {
	Buckets []struct {
		Total  int64 `json:"total"`
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
	} `json:"buckets"`
}

// fillBankStats 填 bank 的记忆数、document 数，以及 Hindsight 记录的 bank 内模型调用量（含嵌入与重排）。
func (s *Service) fillBankStats(ctx context.Context, b *memory.Bank, out *apigen.OwnerMemoryStatus) error {
	bs, err := b.Stats(ctx)
	if err != nil {
		return upstream("读取 bank 统计", err)
	}
	out.MemoryCount, out.DocumentCount = &bs.TotalNodes, &bs.TotalDocuments
	usage := func(period string) (*apigen.MemoryUsageCounts, error) {
		var st llmStats
		if err := b.Call(ctx, http.MethodGet, "/llm-requests/stats?period="+url.QueryEscape(period), nil, &st); err != nil {
			return nil, upstream("读取模型用量", err)
		}
		var c apigen.MemoryUsageCounts
		for _, bk := range st.Buckets {
			c.Calls += bk.Total
			c.InputTokens += bk.Tokens.Input
			c.OutputTokens += bk.Tokens.Output
		}
		return &c, nil
	}
	if out.UsageDay, err = usage("1d"); err != nil {
		return err
	}
	out.UsageWeek, err = usage("7d")
	return err
}
