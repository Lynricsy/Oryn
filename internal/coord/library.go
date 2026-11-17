package coord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// 全局助手文件库的宿主工具与 orb 内 library.put（M13 计划 4.4 / 4.6）。助手在协调宿主沙箱里用 omp 的 read 读
// ~/library/{shared,threads/<本对话>}/，用 write 写 ~/library/out/；save_file 经 library.fetch 把 out/ 下的文件取回存库。

// LibraryHost 从 orb 取回文件（*library.Syncer 实现）：PutFrom 存入文件库，Fetch 只取回字节（artifact 的 source），
// Sync 把文件库同步进协调宿主（M19：导出、取回的新文件立刻在沙箱里可读）。
type LibraryHost interface {
	PutFrom(ctx context.Context, orbID, path string, p library.PutParams) (library.File, error)
	Fetch(ctx context.Context, orbID, path string) (io.ReadCloser, int64, error)
	Sync(ctx context.Context, orbID string) error
}

// SetLibrary 设置全局助手文件库；须在服务对外可用之前调用。未设置时文件工具与 orb 内 library.put 回 coordinator_unavailable。
func (s *Service) SetLibrary(lib *library.Service, host LibraryHost) { s.lib, s.libHost = lib, host }

func (s *Service) libraryReady() error {
	if s.lib == nil || s.libHost == nil {
		return errf(CodeCoordinatorUnavailable, "the file library is not configured")
	}
	return nil
}

// libErr 把文件库的错误映射为协调层错误；orbd 的 ctl 错误（invalid_params、not_found…）原样保留错误码。
func libErr(err error) error {
	switch {
	case errors.Is(err, library.ErrNotFound), errors.Is(err, library.ErrThreadNotFound):
		return errf(CodeNotFound, "%s", err.Error())
	case errors.Is(err, library.ErrTooLarge):
		return errf(CodeInvalid, "%s", err.Error())
	case errors.Is(err, library.ErrForbidden):
		return errf(CodeForbidden, "%s", err.Error())
	case errors.Is(err, library.ErrInvalid):
		return errf(CodeInvalid, "%s", err.Error())
	case errors.Is(err, library.ErrHostUnavailable):
		return errf(CodeCoordinatorUnavailable, "%s", err.Error())
	}
	return err
}

// fileItem 是文件工具结果里的一个文件；Path 是它在协调宿主沙箱里可读的路径。
type fileItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Mime           string `json:"mime"`
	Size           int64  `json:"size"`
	Scope          string `json:"scope"`
	Source         string `json:"source"`
	Path           string `json:"path"`
	SourceThreadID string `json:"sourceThreadId,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
}

func toFileItem(f library.File) fileItem {
	return fileItem{
		ID: f.ID, Name: f.Name, Mime: f.Mime, Size: f.Size, Scope: f.Scope, Source: f.Source, Path: library.SandboxPath(f),
		SourceThreadID: f.SourceThreadID, CreatedAt: f.CreatedAt.UnixMilli(),
	}
}

// visibleFile 取 id 对应的文件：必须属于对话 threadID 或已共享，否则按不存在处理（不暴露别的对话的文件）。
func (s *Service) visibleFile(ctx context.Context, threadID, id string) (library.File, error) {
	f, err := s.lib.Get(ctx, id)
	if err != nil {
		return library.File{}, libErr(err)
	}
	if f.ThreadID != threadID && f.Scope != library.ScopeShared {
		return library.File{}, errf(CodeNotFound, "file %s not found", id)
	}
	return f, nil
}

// outPath 预检 save_file 等取回沙箱文件的路径：不许 `..` 段；`~/` 开头时必须在 ~/library/out/ 或 ~/tmp/（M19：
// generate_image 的结果）之下。绝对路径与符号链接由协调宿主判定（library.fetch 只服务这两处的普通文件）。
func outPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errf(CodeInvalid, "path is required")
	}
	if slices.Contains(strings.Split(p, "/"), "..") {
		return "", errf(CodeInvalid, "path must not contain ..")
	}
	if strings.HasPrefix(p, "~") && !inFetchRoot(p) {
		return "", errf(CodeInvalid, "only files under ~/library/out/ or ~/tmp/ can be saved")
	}
	return p, nil
}

// inFetchRoot 报告 `~/` 路径是否在可取回的两个目录之下。
func inFetchRoot(p string) bool {
	return strings.HasPrefix(p, "~/library/out/") || strings.HasPrefix(p, "~/tmp/")
}

// conversationOrb 返回协调对话所在的协调 orb。
func (s *Service) conversationOrb(ctx context.Context, threadID string) (string, error) {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errf(CodeNotFound, "conversation %s not found", threadID)
	}
	if err != nil {
		return "", err
	}
	if t.OrbID == nil {
		return "", errf(CodeCoordinatorUnavailable, "the conversation has no coordinator orb")
	}
	return *t.OrbID, nil
}

// ---- save_file ----

type saveFileArgs struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Shared bool   `json:"shared"`
}

func (s *Service) toolSaveFile(ctx context.Context, c toolCtx) (any, error) {
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	var a saveFileArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	p, err := outPath(a.Path)
	if err != nil {
		return nil, err
	}
	orbID, err := s.conversationOrb(ctx, c.ThreadID)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = path.Base(p)
	}
	scope := library.ScopeConversation
	if a.Shared {
		scope = library.ScopeShared
	}
	f, err := s.libHost.PutFrom(ctx, orbID, p, library.PutParams{
		Name: name, ThreadID: c.ThreadID, Scope: scope, Source: library.SourceAssistant,
	})
	if err != nil {
		return nil, libErr(err)
	}
	return toFileItem(f), nil
}

// ---- list_files ----

func (s *Service) toolListFiles(ctx context.Context, c toolCtx) (any, error) {
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	var a struct {
		Scope string `json:"scope"`
	}
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	list, err := s.lib.List(ctx, library.Filter{ThreadID: c.ThreadID, Scope: a.Scope})
	if err != nil {
		return nil, libErr(err)
	}
	items := make([]fileItem, len(list))
	for i, f := range list {
		items[i] = toFileItem(f)
	}
	return map[string]any{"files": items}, nil
}

// ---- share_file ----

type shareFileArgs struct {
	ID     string `json:"id"`
	Shared *bool  `json:"shared"`
}

func (s *Service) toolShareFile(ctx context.Context, c toolCtx) (any, error) {
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	var a shareFileArgs
	if err := decodeArgs(c.Args, &a); err != nil {
		return nil, err
	}
	if a.ID == "" || a.Shared == nil {
		return nil, errf(CodeInvalid, "id and shared are required")
	}
	f, err := s.visibleFile(ctx, c.ThreadID, a.ID)
	if err != nil {
		return nil, err
	}
	scope := library.ScopeConversation
	if *a.Shared {
		scope = library.ScopeShared
	}
	if f.ThreadID != c.ThreadID && scope != f.Scope {
		// 别的对话的或不属于任何对话的共享文件：改成私有会让它从本对话（乃至所有对话）消失
		return nil, errf(CodeForbidden, "only files of this conversation can be made private")
	}
	if scope != f.Scope {
		if err := s.lib.SetScope(ctx, f.ID, scope); err != nil {
			return nil, libErr(err)
		}
		f.Scope = scope
	}
	return toFileItem(f), nil
}

// ---- create_thread{files} ----

// threadAttachments 把助手可见的库文件逐个复制成尚未绑定的附件（随后由建线程绑定到新线程）。
func (s *Service) threadAttachments(ctx context.Context, c toolCtx, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if c.Kind != threads.KindAssistant {
		return nil, errf(CodeForbidden, "files can only be attached by the global assistant")
	}
	if err := s.libraryReady(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(files))
	for _, id := range files {
		f, err := s.visibleFile(ctx, c.ThreadID, id)
		if err != nil {
			return nil, err
		}
		att, err := s.lib.CopyToAttachment(ctx, f.ID, "")
		if err != nil {
			return nil, libErr(err)
		}
		out = append(out, att)
	}
	return out, nil
}

// ---- orb 内 library.put ----

// onLibraryPut 是 orb 内 `oryn library put`：调用方须是本 orb 上未归档的工作线程；经同一 orb 的 library.fetch 取回字节，
// 以 scope=shared、source=orb、source_thread_id=调用方存入文件库。
func (s *Service) onLibraryPut(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlLibraryPutParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	c, err := s.caller(ctx, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if err := s.libraryReady(); err != nil {
		return nil, asCtlErr(err)
	}
	if !path.IsAbs(p.Path) {
		return nil, ctlErr("invalid_params", "path must be absolute")
	}
	name := path.Base(p.Path)
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		name = *p.Name
	}
	f, err := s.libHost.PutFrom(ctx, orbID, p.Path, library.PutParams{
		Name: name, Scope: library.ScopeShared, Source: library.SourceOrb, SourceThreadID: c.ID,
	})
	if err != nil {
		return nil, asCtlErr(libErr(err))
	}
	return f.API(), nil
}
