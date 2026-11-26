package ownermem

import (
	"context"
	"fmt"
)

// RerankTexts（仅测试）用给定文本构造候选（id 为 c0、c1……）并重排。
func (s *Service) RerankTexts(ctx context.Context, query string, texts []string) rerankResult {
	cands := make([]hsFact, len(texts))
	for i, t := range texts {
		cands[i] = hsFact{ID: fmt.Sprintf("c%d", i), Text: t}
	}
	return s.rerank(ctx, query, cands)
}

// SetAfterDueCheck（仅测试）设置写入通过最后一次资格检查之后的回调。
func (s *Service) SetAfterDueCheck(fn func(threadID string)) { s.afterDueCheck = fn }
