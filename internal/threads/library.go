package threads

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// orynFilesKey 是客户端在助手对话的 prompt / steer / follow_up / abort_and_prompt 帧上引用文件库文件（afl_ id）的
// Oryn 扩展字段（M13 计划 4.2）：控制面删除它（omp 永远看不到），把文件绑定到本对话，并在 message 末尾追加
// 沙箱路径的附件块（格式同 orynAttachments 的附件块，前端据此渲染附件芯片）。
const orynFilesKey = "orynFiles"

// LibraryFiles 是全局助手文件库（internal/library 的 ThreadFiles 实现）。
type LibraryFiles interface {
	// BindTx 在 q 的事务里把文件绑定到助手对话，按 ids 顺序返回附件块要列出的文件（Path 为协调宿主沙箱内的路径）。
	// 业务错误为 *Error。
	BindTx(ctx context.Context, q *db.Queries, threadID string, ids []string) ([]DeliveredAttachment, error)
}

// SetLibrary 设置文件库；须在服务对外可用之前调用。
func (s *Service) SetLibrary(l LibraryFiles) { s.lib = l }

// libraryBlockTx 在 q 的事务里把文件绑定到助手对话 threadID，并把附件块追加到帧的 message 末尾。
func (s *Service) libraryBlockTx(ctx context.Context, q *db.Queries, threadID string, frame []byte, ids []string) ([]byte, error) {
	if s.lib == nil {
		return nil, errf("invalid", "library files are not supported")
	}
	d, err := s.lib.BindTx(ctx, q, threadID, ids)
	if err != nil {
		return nil, err
	}
	out, err := appendMessage(frame, attachmentBlock(d))
	if err != nil {
		return nil, errf(RouteInvalidFrame, err.Error())
	}
	return out, nil
}

// withLibraryFiles 处理协调线程客户端命令上的 orynFiles：只有全局助手对话接受。
func (s *Service) withLibraryFiles(ctx context.Context, t *thread, frame json.RawMessage, ids []string) (json.RawMessage, error) {
	if t.kind != KindAssistant {
		return nil, errf(CodeCoordinatorRestricted, "only assistant conversations accept "+orynFilesKey)
	}
	var out []byte
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		out, err = s.libraryBlockTx(ctx, q, t.id, frame, ids)
		return err
	})
	if err != nil {
		return nil, asThreadErr(err)
	}
	return out, nil
}
