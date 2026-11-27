package share

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// PublicToolCall 找公开分享里的一次工具调用（M33 的视图帧）：只在分享范围内、用清洗后的公开条目查找 id 为 callID、
// 工具名为 tool 的调用，且范围内已有它的成功结果（非 isError）；返回调用参数（转录里的原始 JSON）。
// slug 无效、已撤销、找不到调用或还没有成功结果一律 ErrNotFound。每次调用都重新走分享校验，撤销即时生效。
func (s *Service) PublicToolCall(ctx context.Context, slug, callID, tool string) (json.RawMessage, error) {
	if callID == "" {
		return nil, ErrNotFound
	}
	sh, err := s.activeBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	sc, err := s.scopeOf(ctx, sh)
	if err != nil {
		return nil, err
	}
	// 原始 payload 上的子串查询只挑候选（调用与结果都带 callID），授权以清洗后的公开内容为准
	seqs, err := s.st.Q.ListEntrySeqsContaining(ctx, db.ListEntrySeqsContainingParams{ThreadID: sc.thread.ID, UntilSeq: sc.until, Needle: []byte(callID)})
	if err != nil {
		return nil, err
	}
	var args json.RawMessage
	succeeded := false
	for _, seq := range seqs {
		if !sc.onPath[seq] {
			continue
		}
		pe, ok, err := sc.row(ctx, s.st.Q, seq)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if a, ok := transcript.ToolCallArgs(pe, callID, tool); ok && args == nil {
			args = a
		}
		if m, ok := transcript.ParseMessage(pe); ok && m.Role == "toolResult" && m.ToolCallID == callID && !m.IsError {
			succeeded = true
		}
	}
	if args == nil || !succeeded {
		return nil, ErrNotFound
	}
	return args, nil
}
