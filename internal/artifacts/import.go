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
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// 导入特有的错误码（REST：too_many 413，其余 400）。
const (
	CodeTooMany   = "too_many"
	CodeNoIndex   = "no_index"
	CodeBadCanvas = "bad_canvas"
	CodeBadName   = "bad_name"
	CodeNotUTF8   = "not_utf8"
)

// ImportBodyLimit 是导入请求体的上限：一个版本的总字节，外加 multipart 头与被跳过的隐藏文件的余量。
const ImportBodyLimit = MaxVersionBytes + 16<<20

// untitled 是既没有 <title> 也没有上传名时的标题。
const untitled = "未命名"

// ImportParams 是 Import 的参数。
type ImportParams struct {
	// ProjectID 为空表示「无项目」。
	ProjectID string
	// Name 是上传时的文件名或文件夹名（没有 <title> 时作标题）。
	Name string
}

// Import 流式读取 multipart 请求体，把每个 part 作为一个文件（字段名是相对路径）导入成新 artifact：
// origin = import、没有创建线程、作者是用户。任何一段以点开头的路径（.DS_Store、.git/…）被跳过。
func (s *Service) Import(ctx context.Context, mr *multipart.Reader, p ImportParams) (Artifact, error) {
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
		if total += f.size; total > MaxVersionBytes {
			return Artifact{}, opErr(CodeTooLarge, "导入的文件总共不能超过 %d MiB。", MaxVersionBytes>>20)
		}
		if name == EntryHTML {
			index = f.data
		}
		f.data = nil // 只为标题保留 index.html 的字节
		st.writes[name] = f
		paths = append(paths, name)
	}
	if c := CaseClash(paths); c != "" {
		return Artifact{}, opErr(CodeBadName, "%s 与另一个文件只差大小写。", c)
	}
	_, hasHTML := st.writes[EntryHTML]
	_, hasCanvas := st.writes[EntryCanvas]
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
		Title: importTitle(index, p.Name), ProjectID: p.ProjectID, Origin: OriginImport,
	}, &st)
}

// importPart 校验一个 part 的路径并把内容落进内容存储；HTML 文件另做 UTF-8 校验。
func (s *Service) importPart(part io.Reader, name string, st *staged) (stored, error) {
	if pp := PathProblem(name); pp != "" {
		return stored{}, opErr(CodeBadName, "文件名 %q 不合法：%s。", name, pp)
	}
	if _, dup := st.writes[name]; dup {
		return stored{}, opErr(CodeBadName, "文件 %s 出现了两次。", name)
	}
	f, err := s.c.put(part, MaxFileBytes, IsHTML(name))
	if errors.Is(err, errFileTooLarge) {
		return stored{}, opErr(CodeTooLarge, "文件 %s 超过单文件上限 %d MiB。", name, MaxFileBytes>>20)
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
		return opErr(CodeTooLarge, "导入的内容总共不能超过 %d MiB。", MaxVersionBytes>>20)
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
