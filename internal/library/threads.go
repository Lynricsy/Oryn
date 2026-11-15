package library

import (
	"context"
	"errors"

	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// ThreadFiles 把文件库适配为 threads.LibraryFiles：助手对话的消息（orynFiles）与新建对话的首条消息引用库里的文件；
// 协调线程启动 agent 之前与消息引用文件之后，经 Sync 把文件库同步进协调宿主（Sync 为 nil 时不同步）。
type ThreadFiles struct {
	S    *Service
	Sync *Syncer
}

var _ threads.LibraryFiles = ThreadFiles{}

// BindTx 在 q 的事务里把文件绑定到对话，按 ids 顺序返回附件块要列出的文件（Path 为沙箱内路径）。
// 业务错误转成 *threads.Error（not_found / invalid）。
func (a ThreadFiles) BindTx(ctx context.Context, q *db.Queries, threadID string, ids []string) ([]threads.DeliveredAttachment, error) {
	files, err := a.S.BindTx(ctx, q, threadID, ids)
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrThreadNotFound):
		return nil, &threads.Error{Code: "not_found", Message: err.Error()}
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrInvalid):
		return nil, &threads.Error{Code: "invalid", Message: err.Error()}
	case err != nil:
		return nil, err
	}
	out := make([]threads.DeliveredAttachment, len(files))
	for i, f := range files {
		out[i] = threads.DeliveredAttachment{ID: f.ID, Name: f.Name, Mime: f.Mime, Path: SandboxPath(f), Size: f.Size}
	}
	return out, nil
}

// SyncHost 把文件库同步进协调宿主 orbID（宿主不支持文件库时什么也不做）。
func (a ThreadFiles) SyncHost(ctx context.Context, orbID string) error {
	if a.Sync == nil {
		return nil
	}
	return a.Sync.Sync(ctx, orbID)
}
