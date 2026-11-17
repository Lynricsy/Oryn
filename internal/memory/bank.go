package memory

// bank.go：M17 主人记忆经这里访问工程记忆以外的 bank（`oryn-owner`）。连接、key 与 M16 共用；遗忘保护与删除重试
// 复用 stale.go / purge.go，按 bank 区分。工程记忆的网关与管理接口仍只碰 bank `oryn`，这里拒绝它。

import (
	"context"
	"errors"
	"net/http"
)

// Bank 是 orynd 对一个非工程记忆 bank 的操作句柄。每次使用前按当前设置重新获取（设置可能变化）。
type Bank struct {
	s *Service
	c adminClient
}

// Bank 按当前设置构造指定 bank 的句柄；未配置时返回 ErrNotConfigured。bank `oryn` 只由工程记忆使用，返回错误。
func (s *Service) Bank(ctx context.Context, bank string) (*Bank, error) {
	if bank == BankID || bank == "" {
		return nil, errors.New("bank " + BankID + " 只由工程记忆使用")
	}
	c, err := s.adminFor(ctx, bank)
	if err != nil {
		return nil, err
	}
	return &Bank{s: s, c: c}, nil
}

// ID 是 bank id。
func (b *Bank) ID() string { return b.c.bank }

// Call 访问 bank 下的端点：rel 以 `/` 开头，相对于 `/v1/default/banks/<bank>`；空串即 bank 本身。
// in 非 nil 时按 JSON 发送，out 非 nil 时解析应答；非 2xx 返回 *HTTPError。
func (b *Bank) Call(ctx context.Context, method, rel string, in, out any) error {
	return b.c.call(ctx, method, b.c.root()+rel, in, out)
}

// Ensure 创建 bank（已存在时保持不变）并用 PATCH 写入配置（mission、开关等）。
func (b *Bank) Ensure(ctx context.Context, config map[string]any) error {
	if err := b.Call(ctx, http.MethodPut, "", map[string]any{}, nil); err != nil {
		return err
	}
	if len(config) == 0 {
		return nil
	}
	return b.Call(ctx, http.MethodPatch, "/config", map[string]any{"updates": config}, nil)
}

// Stats 读 bank 的统计；bank 不存在时返回 Status 404 的 *HTTPError。
func (b *Bank) Stats(ctx context.Context) (BankStats, error) {
	return b.c.BankStats(ctx, b.c.bank)
}

// DeleteDocuments 删除 document：删除前把 bank 里全部 mental model 标为待刷新（刷新完成前 StaleModelIDs 含它们），
// 失败的进入重试队列（与工程记忆共用后台循环），有删除成功时立即提交全量刷新。返回删除数与进入队列数；
// 返回的错误只来自本地数据库。
func (b *Bank) DeleteDocuments(ctx context.Context, ids []string) (int, int, error) {
	docs := make([]tombstoneTarget, 0, len(ids))
	for _, id := range ids {
		docs = append(docs, tombstoneTarget{id: id, bank: b.c.bank})
	}
	return b.s.deleteDocumentsIn(ctx, b.c.bank, docs, true)
}

// Forget 在执行 fn（改变已有事实的操作，例如让一条事实失效）之前把 bank 里全部 mental model 标为待刷新；fn 结束后
// 释放，fn 成功时立即提交全量刷新。标记失败时不执行 fn。
func (b *Bank) Forget(ctx context.Context, fn func(context.Context) error) error {
	marks, err := b.s.markStale(ctx, b.c, forgetScopes{allModelsScope: nil})
	if err != nil {
		return err
	}
	defer b.s.releaseStale(ctx, marks)
	if err := fn(ctx); err != nil {
		return err
	}
	b.s.releaseStale(ctx, marks)
	b.s.refreshStale(ctx, b.c, marks)
	return nil
}

// StaleModelIDs 返回 bank 里遗忘后仍在等待刷新的模型 id：在刷新完成前，它们的内容不能再给出去。
func (b *Bank) StaleModelIDs(ctx context.Context) (map[string]bool, error) {
	return b.s.staleModelIDs(ctx, b.c.bank)
}

// PendingDeletes 是 bank 里删除失败等待重试的 document 数加上等待刷新的模型数。
func (b *Bank) PendingDeletes(ctx context.Context) (int, error) {
	tombs, err := b.s.cfg.Q.ListMemoryTombstones(ctx, b.c.bank)
	if err != nil {
		return 0, err
	}
	stale, err := b.s.cfg.Q.ListMemoryStaleModels(ctx, b.c.bank)
	if err != nil {
		return 0, err
	}
	return len(tombs) + len(stale), nil
}
