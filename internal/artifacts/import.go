package artifacts

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/Lynricsy/Oryn/internal/artifacts/office"
)

// 导入特有的错误码（REST：too_many 413，其余 400）。
const (
	CodeTooMany   = "too_many"
	CodeNoIndex   = "no_index"
	CodeBadCanvas = "bad_canvas"
	CodeBadName   = "bad_name"
	CodeNotUTF8   = "not_utf8"
)

// ImportBodyLimit 是导入请求体的上限：单个 Office 文件的上限，外加 multipart 头与被跳过的隐藏文件的余量
// （HTML 文件夹另受 MaxVersionBytes 的总量限制）。
const ImportBodyLimit = MaxOfficeFileBytes + 16<<20

// untitled 是既没有 <title> 也没有上传名时的标题。
const untitled = "未命名"

// ImportParams 是 Import 的参数。
type ImportParams struct {
	// ProjectID 为空表示「无项目」；给了 ThreadID 而 ProjectID 为空时取线程的项目。
	ProjectID string
	// ThreadID 非空时 artifact 归属该线程（文件面板「转为 artifact」）。
	ThreadID string
	// Name 是上传时的文件名或文件夹名（没有 <title> 时作标题）。
	Name string
}

// Import 流式读取 multipart 请求体，把每个 part 作为一个文件（字段名是相对路径）导入成新 artifact：
// origin = import、没有创建线程、作者是用户。任何一段以点开头的路径（.DS_Store、.git/…）被跳过。
func (s *Service) Import(ctx context.Context, mr *multipart.Reader, p ImportParams) (Artifact, error) {
	if p.ThreadID != "" {
		th, err := s.st.Q.GetThread(ctx, p.ThreadID)
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, opErr(CodeInvalid, "线程 %s 不存在。", p.ThreadID)
		} else if err != nil {
			return Artifact{}, err
		}
		if p.ProjectID == "" && th.ProjectID != nil {
			p.ProjectID = *th.ProjectID
		}
	}
	if p.ProjectID != "" {
		if _, err := s.st.Q.GetProject(ctx, p.ProjectID); errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, opErr(CodeInvalid, "项目 %s 不存在。", p.ProjectID)
		} else if err != nil {
			return Artifact{}, err
		}
	}
	// 从第一次落内容一直持有到 create 提交引用行之后，内容清理才不会删掉刚落下、还没被引用的文件
	release := s.holdContent()
	defer release()
	st := staged{writes: map[string]stored{}}
	paths := make([]string, 0, 8)
	var total int64
	var index []byte
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Artifact{}, importBodyErr(err)
		}
		name := part.FormName()
		var body io.Reader = part
		skip := hiddenPath(name)
		if !skip && name == EntryHTML {
			// 导出画布时补上的概览页（以 overviewMarker 开头）：查看时会重新生成，不作为 artifact 的文件
			br := bufio.NewReader(part)
			head, _ := br.Peek(len(overviewMarker) + 3)
			skip, body = isOverviewPage(head), br
		}
		if skip {
			_, err := io.Copy(io.Discard, body)
			_ = part.Close()
			if err != nil {
				return Artifact{}, importBodyErr(err)
			}
			continue
		}
		if len(paths) == MaxVersionFiles {
			return Artifact{}, opErr(CodeTooMany, "一个 artifact 最多 %d 个文件。", MaxVersionFiles)
		}
		f, err := s.importPart(body, name, &st)
		_ = part.Close()
		if err != nil {
			return Artifact{}, err
		}
		// 单个 Office 文件可以到 MaxOfficeFileBytes（importPart 已按单文件上限截断）；其余按版本总量
		if total += f.size; total > MaxVersionBytes && (len(paths) > 0 || !officeName(name)) {
			return Artifact{}, opErr(CodeTooLarge, "导入的文件总共不能超过 %d MiB。", MaxVersionBytes>>20)
		}
		// 第一个文件若是 HTML 也先留着字节：只上传了这一个文件时它就是页面本身（见下）
		if name == EntryHTML || len(paths) == 0 && IsHTML(name) {
			index = f.data
		}
		f.data = nil // 只为标题保留 index.html 的字节
		st.writes[name] = f
		paths = append(paths, name)
	}
	if len(paths) == 1 && !strings.Contains(paths[0], "/") {
		if typ := OfficeTypeByExt(paths[0]); typ != "" {
			return s.importOffice(ctx, p, typ, paths[0], st.writes[paths[0]])
		}
		if target, ok := LegacyOfficeExt(paths[0]); ok {
			e := s.officeConverter(ctx)
			if e == nil {
				return Artifact{}, opErr(CodeUnsupportedFormat, "%s 是旧版或 OpenDocument 格式。把文件交给 Agent 转成 %s，或部署 Office 引擎后再导入。", paths[0], target)
			}
			f, err := s.convertLegacy(ctx, e, paths[0], target, st.writes[paths[0]])
			if err != nil {
				return Artifact{}, err
			}
			return s.importOffice(ctx, p, target, paths[0], f)
		}
		// 单个 Markdown 文件导入成文档 artifact（例如写作块「存为 Artifact」）
		if ext := strings.ToLower(path.Ext(paths[0])); ext == ".md" || ext == ".markdown" {
			return s.importDocument(ctx, p, paths[0], st.writes[paths[0]])
		}
	}
	if c := CaseClash(paths); c != "" {
		return Artifact{}, opErr(CodeBadName, "%s 与另一个文件只差大小写。", c)
	}
	_, hasHTML := st.writes[EntryHTML]
	_, hasCanvas := st.writes[EntryCanvas]
	// 单独上传的一个 HTML 文件（不叫 index.html，例如「周报.html」）就是页面本身：存成 index.html
	if !hasHTML && !hasCanvas && len(paths) == 1 && IsHTML(paths[0]) && !strings.Contains(paths[0], "/") {
		st.writes[EntryHTML] = st.writes[paths[0]]
		delete(st.writes, paths[0])
		hasHTML = true
	}
	if !hasHTML && !hasCanvas {
		return Artifact{}, opErr(CodeNoIndex, "没有找到 index.html（画布则是 canvas.json）。")
	}
	if hasCanvas {
		b, err := s.c.read(st.writes[EntryCanvas].sha)
		if err != nil {
			return Artifact{}, err
		}
		if _, problem := canvasProblem(b, func(p string) bool { _, ok := st.writes[p]; return ok }); problem != "" {
			return Artifact{}, opErr(CodeBadCanvas, "%s", problem)
		}
	}
	return s.create(ctx, CreateParams{
		Title: importTitle(index, p.Name), ProjectID: p.ProjectID, ThreadID: p.ThreadID, Origin: OriginImport,
	}, &st)
}

// officeName 报告文件名是否是 Office 文件（含旧版与 OpenDocument，它们同样按 Office 单文件上限收下再判断）。
func officeName(name string) bool {
	_, legacy := LegacyOfficeExt(name)
	return legacy || OfficeTypeByExt(name) != ""
}

// importDocument 把单个 Markdown 文件 f 导入成文档 artifact：内容存为 document.md，标题取上传的文件名 name
// 去掉扩展名；校验不过的 invalid 错误原样返回。
func (s *Service) importDocument(ctx context.Context, p ImportParams, name string, f stored) (Artifact, error) {
	st := staged{writes: map[string]stored{EntryDocument: f}}
	base := path.Base(name)
	title := clipTitle(strings.TrimSuffix(base, path.Ext(base)))
	if title == "" {
		title = untitled
	}
	return s.create(ctx, CreateParams{Title: title, Type: TypeDocument, ProjectID: p.ProjectID, ThreadID: p.ThreadID, Origin: OriginImport}, &st)
}

// importOffice 把单个 Office 文件 f 导入成 typ 类型的 artifact：入口是该类型的 Entry，标题取上传的文件名 name
// 去掉扩展名；校验不过回 bad_office 并保留问题说明。
func (s *Service) importOffice(ctx context.Context, p ImportParams, typ, name string, f stored) (Artifact, error) {
	st := staged{writes: map[string]stored{TypeEntry(typ): f}}
	base := path.Base(name)
	title := clipTitle(strings.TrimSuffix(base, path.Ext(base)))
	if title == "" {
		title = untitled
	}
	a, err := s.create(ctx, CreateParams{Title: title, Type: typ, ProjectID: p.ProjectID, ThreadID: p.ThreadID, Origin: OriginImport}, &st)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeInvalid {
		return Artifact{}, &OpError{Code: CodeBadOffice, Message: oe.Message}
	}
	return a, err
}

// convertLegacy 用 Office 引擎把旧版或 OpenDocument 文件 f（上传名 name）转成 target 类型，结果落进内容存储
// （调用方持有内容读锁）。引擎拒绝或转换失败回 bad_office 带引擎的原因。
func (s *Service) convertLegacy(ctx context.Context, e *office.Engine, name, target string, f stored) (stored, error) {
	src, err := s.c.open(f.sha)
	if err != nil {
		return stored{}, err
	}
	defer src.Close()
	rc, err := e.Convert(ctx, src, path.Base(name), target)
	if err != nil {
		return stored{}, opErr(CodeBadOffice, "Office 引擎没能把 %s 转成 %s：%v", name, target, err)
	}
	defer rc.Close()
	out, err := s.c.put(rc, MaxOfficeFileBytes, false)
	if errors.Is(err, errFileTooLarge) {
		return stored{}, opErr(CodeTooLarge, "%s 转成 %s 后超过单文件上限 %d MiB。", name, target, MaxOfficeFileBytes>>20)
	}
	if err != nil {
		return stored{}, opErr(CodeBadOffice, "Office 引擎没能把 %s 转成 %s：%v", name, target, err)
	}
	return out, nil
}

// importPart 校验一个 part 的路径并把内容落进内容存储；HTML 文件另做 UTF-8 校验。
func (s *Service) importPart(part io.Reader, name string, st *staged) (stored, error) {
	if pp := PathProblem(name); pp != "" {
		return stored{}, opErr(CodeBadName, "文件名 %q 不合法：%s。", name, pp)
	}
	if _, dup := st.writes[name]; dup {
		return stored{}, opErr(CodeBadName, "文件 %s 出现了两次。", name)
	}
	limit := int64(MaxFileBytes)
	if officeName(name) {
		limit = MaxOfficeFileBytes
	}
	f, err := s.c.put(part, limit, IsHTML(name))
	if errors.Is(err, errFileTooLarge) {
		return stored{}, opErr(CodeTooLarge, "文件 %s 超过单文件上限 %d MiB。", name, limit>>20)
	}
	if err != nil {
		return stored{}, importBodyErr(err)
	}
	if IsHTML(name) && !utf8.Valid(f.data) {
		return stored{}, opErr(CodeNotUTF8, "%s 不是 UTF-8 编码。请先转成 UTF-8 再导入。", name)
	}
	return f, nil
}

// importBodyErr 把读请求体的错误规整：超过 ImportBodyLimit 的回 too_large，multipart 格式错误回 invalid。
func importBodyErr(err error) error {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return opErr(CodeTooLarge, "导入的内容超过上限（Office 文件 %d MiB，其余总共 %d MiB）。", MaxOfficeFileBytes>>20, MaxVersionBytes>>20)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || strings.HasPrefix(err.Error(), "multipart: ") {
		return opErr(CodeInvalid, "上传的内容不完整或格式不对：%v", err)
	}
	return err
}

// hiddenPath 报告路径里是否有以点开头的段。
func hiddenPath(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// importTitle 取 index.html 的 <title>；没有时取上传名（去掉 .html / .htm 扩展名），再没有用「未命名」。
func importTitle(index []byte, name string) string {
	if t := clipTitle(htmlTitle(index)); t != "" {
		return t
	}
	if IsHTML(name) {
		name = name[:strings.LastIndexByte(name, '.')]
	}
	if t := clipTitle(name); t != "" {
		return t
	}
	return untitled
}

// clipTitle 截到 MaxTitleRunes 个码点后用 CleanTitle 整理；不能用时返回空串。
func clipTitle(t string) string {
	t = strings.Join(strings.Fields(strings.ToValidUTF8(t, "")), " ")
	if r := []rune(t); len(r) > MaxTitleRunes {
		t = string(r[:MaxTitleRunes])
	}
	out, err := CleanTitle(t)
	if err != nil {
		return ""
	}
	return out
}

// htmlTitle 返回文档里第一个 <title> 的文本。
func htmlTitle(src []byte) string {
	if len(src) == 0 {
		return ""
	}
	z := html.NewTokenizer(bytes.NewReader(src))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ""
		case html.StartTagToken:
			if name, _ := z.TagName(); string(name) != "title" {
				continue
			}
			if z.Next() == html.TextToken {
				return string(z.Text())
			}
			return ""
		}
	}
}
