package artifacts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/library"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// agent 工具（操作）名；orb / Runner 的扩展工具与全局助手的宿主工具同名。
const (
	OpCreate          = "create_artifact"
	OpRead            = "read_artifact"
	OpUpdate          = "update_artifact"
	OpList            = "list_artifacts"
	OpDelete          = "delete_artifact"
	OpListVersions    = "list_artifact_versions"
	OpRestoreVersion  = "restore_artifact_version"
	OpReadGuide       = "read_artifact_guide"
	OpReadComments    = "read_artifact_comments"
	OpReplyComment    = "reply_artifact_comment"
	OpResolveComment  = "resolve_artifact_comment"
	OpScreenshot      = "screenshot_artifact"
	OpReadData        = "read_artifact_data"
	OpWriteData       = "write_artifact_data"
	listDefaultLimit  = 50
	listMaxLimit      = 200
	deletedThreadName = "已删除的线程"
)

// Scope 是调用方能看到的 artifact 范围；范围之外的一律 not_found。
type Scope int

const (
	// ScopeProject：同项目的全部 artifact（有项目的 orb 线程）。
	ScopeProject Scope = iota
	// ScopeThread：本线程创建或写过版本的（没有项目的 orb 线程、全部 Runner 线程）。
	ScopeThread
	// ScopeAll：全部（全局助手）。
	ScopeAll
)

// Caller 是一次 agent 操作的调用方。
type Caller struct {
	// ThreadID 是调用线程；Label 是它的标题（记进版本的作者快照）。
	ThreadID, Label string
	// ProjectID 是调用线程的项目（新建的 artifact 归它；ScopeProject 按它筛）。
	ProjectID *string
	Scope     Scope
	// Turn 是调用所处的 agent 回合（orb 扩展在 artifact.call 外壳里带的 turn）；写出的版本记在它下面。零值 = 无回合。
	Turn Turn
	// Fetch 取回 source 路径的字节（调用方负责关闭）；nil = 不接受 source。
	Fetch func(ctx context.Context, path string) (io.ReadCloser, int64, error)
	// Internal 为真表示调用来自普通 orb 的 orbd（经 link 的 artifact.call）：只有这样的调用方能用设计画布的内部操作
	// （seal_design、fetch_design_files、fetch_design_runtime）。助手、REST、Runner 都是假。
	Internal bool
}

// Image 是工具结果里的图片（Data 为 base64）。
type Image struct{ MimeType, Data string }

// Result 是 agent 操作的结果：Text 给模型看，Details 给转录卡片用。
type Result struct {
	Text    string
	Details any
	Image   *Image
}

// obj 是工具返回的 JSON 对象。
type obj = map[string]any

// Call 执行一个 agent 操作；params 是 camelCase 的 JSON 对象（未知字段报 invalid）。业务错误是 *OpError。
func (s *Service) Call(ctx context.Context, c Caller, op string, params json.RawMessage) (res Result, err error) {
	defer func() {
		if err == nil {
			s.recordActivity(ctx, c, op, params, res)
		}
	}()
	if c.Scope == ScopeProject && (c.ProjectID == nil || *c.ProjectID == "") {
		c.Scope = ScopeThread
	}
	switch op {
	case OpCreate:
		return s.agentCreate(ctx, c, params)
	case OpRead:
		return s.agentRead(ctx, c, params)
	case OpUpdate:
		return s.agentUpdate(ctx, c, params)
	case OpList:
		return s.agentList(ctx, c, params)
	case OpDelete:
		return s.agentDelete(ctx, c, params)
	case OpListVersions:
		return s.agentVersions(ctx, c, params)
	case OpRestoreVersion:
		return s.agentRestore(ctx, c, params)
	case OpReadGuide:
		return s.agentGuide(params)
	case OpReadComments:
		return s.agentReadComments(ctx, c, params)
	case OpReplyComment:
		return s.agentReplyComment(ctx, c, params)
	case OpResolveComment:
		return s.agentResolveComment(ctx, c, params)
	case OpScreenshot:
		return s.agentScreenshot(ctx, c, params)
	case OpReadData:
		return s.agentReadData(ctx, c, params)
	case OpWriteData:
		return s.agentWriteData(ctx, c, params)
	case OpCheck:
		return s.agentCheckArtifact(ctx, c, params)
	case OpDesignDirections:
		return s.agentDesignDirections(ctx, c, params)
	case OpDesignDirection:
		return s.agentDesignDirection(ctx, c, params)
	case OpDesignNotes:
		return s.agentDesignNotes(ctx, c, params)
	case OpDesignResolveNote:
		return s.agentDesignResolveNote(ctx, c, params)
	case OpSealDesign, OpFetchDesignFiles, OpFetchDesignRuntime, OpDesignHead, OpExportDesignProject:
		if c.Internal {
			return s.callInternal(ctx, c, op, params)
		}
		fallthrough
	default:
		return Result{}, opErr(CodeInvalid, "没有操作 %q。可用的操作：%s。", op,
			strings.Join([]string{OpCreate, OpRead, OpUpdate, OpList, OpDelete, OpListVersions, OpRestoreVersion, OpReadGuide,
				OpReadComments, OpReplyComment, OpResolveComment, OpScreenshot, OpReadData, OpWriteData, OpCheck,
				OpDesignDirections, OpDesignDirection, OpDesignNotes, OpDesignResolveNote}, "、"))
	}
}

// decodeParams 严格解码参数：空或 null 当作 {}，未知字段与多余内容报 invalid。
func decodeParams(params json.RawMessage, v any) error {
	raw := bytes.TrimSpace(params)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return opErr(CodeInvalid, "参数解析失败（%s）。按工具说明的字段与类型重写参数。", err)
	}
	if d.More() {
		return opErr(CodeInvalid, "参数后面还有多余的内容。参数必须是单个 JSON 对象。")
	}
	return nil
}

// jsonResult 是通用结果：Text 是对象的紧凑 JSON，Details 是同一对象外加 op。
func jsonResult(op string, v obj) Result {
	b, _ := json.Marshal(v)
	d := make(obj, len(v)+1)
	for k, x := range v {
		d[k] = x
	}
	d["op"] = op
	return Result{Text: string(b), Details: d}
}

// cardResult 是写入类操作的结果：Text 是对象的紧凑 JSON，Details 是转录卡片用的 {op, id, title, kind, version, fromVersion?}。
func cardResult(op string, v obj, a Artifact, from int) Result {
	b, _ := json.Marshal(v)
	d := obj{"op": op, "id": a.ID, "title": a.Title, "kind": a.Kind, "version": a.Version}
	if from > 0 {
		d["fromVersion"] = from
	}
	return Result{Text: string(b), Details: d}
}

func stamp(t time.Time) string { return t.Format(time.RFC3339) }

// visible 报告 a 是否在调用方的范围内。
func (s *Service) visible(ctx context.Context, c Caller, a Artifact) (bool, error) {
	switch c.Scope {
	case ScopeAll:
		return true, nil
	case ScopeProject:
		return a.ProjectID == *c.ProjectID, nil
	}
	if c.ThreadID == "" {
		return false, nil
	}
	if a.ThreadID == c.ThreadID {
		return true, nil
	}
	authors, err := s.AuthorThreads(ctx, a.ID)
	if err != nil {
		return false, err
	}
	return slices.Contains(authors, c.ThreadID), nil
}

// lookup 取调用方范围内的 artifact；不存在或不在范围内 → not_found。live 为真时回收站里的报 deleted。
func (s *Service) lookup(ctx context.Context, c Caller, id string, live bool) (Artifact, error) {
	if id == "" {
		return Artifact{}, opErr(CodeInvalid, "缺少 id。先用 list_artifacts 找到 artifact 的 id。")
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	ok, err := s.visible(ctx, c, a)
	if err != nil {
		return Artifact{}, err
	}
	if !ok {
		return Artifact{}, notFound(id)
	}
	if live && a.Deleted() {
		return Artifact{}, deletedErr(id)
	}
	return a, nil
}

func (c Caller) author() Author { return Author{ThreadID: c.ThreadID, Label: c.Label} }

// fileParam 是 files 里的一项：content、source 恰好给一个，或者 remove（仅 update_artifact）。
type fileParam struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
	Source  *string `json:"source"`
	Remove  bool    `json:"remove"`
}

// pending 是一个待写的文件：data 是内联内容，source 非空时从调用方取回，remove 为真时删除。
type pending struct {
	path   string
	data   []byte
	source string
	remove bool
}

// htmlArgs 是 html 参数的两种形式：html 是内联的 index.html；htmlSource 是 orb 扩展把 html 参数写成的临时文件
// （绝对路径，经 Fetch 取回）。两者等价、至多给一个；分开传是为了让类型化 artifact 能认出「传了 html」。
type htmlArgs struct {
	HTML       *string `json:"html"`
	HTMLSource *string `json:"htmlSource"`
}

func (h htmlArgs) given() bool { return h.HTML != nil || h.HTMLSource != nil }

// typedHTMLErr 是类型化 artifact 收到 html 时的错误。
func typedHTMLErr(typ string) *OpError {
	return opErr(CodeInvalid, "文档 / 幻灯片 artifact 不接受 html：%s 类型的入口是 %s，把内容写进 files 里的 %s（改已有的可以用 edits）。格式见 read_artifact_guide 的 %s 主题。",
		typ, TypeEntry(typ), TypeEntry(typ), typ)
}

// collectFiles 把 html 与 files 整理成按出现顺序的待写文件；allowRemove 为假时不接受 remove。
func collectFiles(c Caller, html htmlArgs, files []fileParam, allowRemove bool) ([]*pending, error) {
	var out []*pending
	switch {
	case html.HTML != nil && html.HTMLSource != nil:
		return nil, opErr(CodeInvalid, "html 与 htmlSource 只能给一个。")
	case html.HTML != nil:
		if len(*html.HTML) > MaxInlineBytes {
			return nil, inlineTooLarge(EntryHTML)
		}
		out = append(out, &pending{path: EntryHTML, data: []byte(*html.HTML)})
	case html.HTMLSource != nil:
		if c.Fetch == nil || *html.HTMLSource == "" {
			return nil, opErr(CodeInvalid, "这里不能用 htmlSource，请直接给 html。")
		}
		out = append(out, &pending{path: EntryHTML, source: *html.HTMLSource})
	}
	for i, f := range files {
		if f.Path == "" {
			return nil, opErr(CodeInvalid, "files[%d] 缺少 path。", i)
		}
		if f.Remove {
			if !allowRemove {
				return nil, opErr(CodeInvalid, "files[%d]（%s）：新建 artifact 时不能删除文件。", i, f.Path)
			}
			if f.Content != nil || f.Source != nil {
				return nil, opErr(CodeInvalid, "files[%d]（%s）：remove 为 true 时不能再给 content 或 source。", i, f.Path)
			}
			out = append(out, &pending{path: f.Path, remove: true})
			continue
		}
		switch {
		case (f.Content == nil) == (f.Source == nil):
			return nil, opErr(CodeInvalid, "files[%d]（%s）：content 与 source 必须恰好给一个。", i, f.Path)
		case f.Content != nil:
			if len(*f.Content) > MaxInlineBytes {
				return nil, inlineTooLarge(f.Path)
			}
			out = append(out, &pending{path: f.Path, data: []byte(*f.Content)})
		default:
			if c.Fetch == nil {
				return nil, opErr(CodeInvalid, "files[%d]（%s）：这里不能用 source，请把内容放进 content。", i, f.Path)
			}
			if *f.Source == "" {
				return nil, opErr(CodeInvalid, "files[%d]（%s）：source 是空的。给出文件的路径。", i, f.Path)
			}
			out = append(out, &pending{path: f.Path, source: *f.Source})
		}
	}
	return out, nil
}

func inlineTooLarge(path string) *OpError {
	return opErr(CodeTooLarge, "%s 的内联内容超过 %d MiB。先把文件写到你的环境里，再用 source 给出它的路径（单文件上限 %d MiB）。",
		path, MaxInlineBytes>>20, MaxFileBytes>>20)
}

func sourceTooLarge(path string) *OpError {
	return opErr(CodeTooLarge, "source %s 超过单文件上限 %d MiB。请拆小或压缩后再写入。", path, MaxFileBytes>>20)
}

// sourceReader 在第一次 Read 时才取回 source（写入是逐个文件流式落盘的，同一时刻只开一个），读完即关闭。
type sourceReader struct {
	ctx   context.Context
	fetch func(ctx context.Context, path string) (io.ReadCloser, int64, error)
	path  string
	rc    io.ReadCloser
	done  bool
}

func (r *sourceReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	if r.rc == nil {
		rc, size, err := r.fetch(r.ctx, r.path)
		if err != nil {
			r.done = true
			return 0, sourceErr(r.path, err)
		}
		if size > MaxFileBytes {
			_ = rc.Close()
			r.done = true
			return 0, sourceTooLarge(r.path)
		}
		r.rc = rc
	}
	n, err := r.rc.Read(p)
	if err != nil {
		r.done = true
		_ = r.Close()
		if err != io.EOF {
			err = sourceErr(r.path, err)
		}
	}
	return n, err
}

func (r *sourceReader) Close() error {
	if r.rc == nil {
		return nil
	}
	err := r.rc.Close()
	r.rc = nil
	return err
}

func sourceErr(path string, err error) error {
	if _, ok := AsOpError(err); ok {
		return err
	}
	return fmt.Errorf("artifacts: 取回 source %s: %w", path, err)
}

// fetchAll 把 source 整个读进内存（只用于要做 edits 的文件）。
func fetchAll(ctx context.Context, c Caller, path string) ([]byte, error) {
	rc, size, err := c.Fetch(ctx, path)
	if err != nil {
		return nil, sourceErr(path, err)
	}
	defer rc.Close()
	if size > MaxFileBytes {
		return nil, sourceTooLarge(path)
	}
	b, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
	if err != nil {
		return nil, sourceErr(path, err)
	}
	if len(b) > MaxFileBytes {
		return nil, sourceTooLarge(path)
	}
	return b, nil
}

// changes 把待写文件变成 FileChange；返回的 closer 关闭全部还开着的 source。
func changes(ctx context.Context, c Caller, ps []*pending) ([]FileChange, func()) {
	out := make([]FileChange, 0, len(ps))
	var readers []*sourceReader
	for _, p := range ps {
		switch {
		case p.remove:
			out = append(out, FileChange{Path: p.path, Remove: true})
		case p.source != "":
			r := &sourceReader{ctx: ctx, fetch: c.Fetch, path: p.source}
			readers = append(readers, r)
			out = append(out, FileChange{Path: p.path, Data: r})
		default:
			out = append(out, FileChange{Path: p.path, Data: bytes.NewReader(p.data)})
		}
	}
	return out, func() {
		for _, r := range readers {
			_ = r.Close()
		}
	}
}

type createArgs struct {
	htmlArgs
	Title        string      `json:"title"`
	Type         string      `json:"type"`
	Files        []fileParam `json:"files"`
	Capabilities []string    `json:"capabilities"`
	Note         string      `json:"note"`
	ProjectID    *string     `json:"projectId"`
}

func (s *Service) agentCreate(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p createArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Type != "" && TypeEntry(p.Type) != "" && p.given() {
		return Result{}, typedHTMLErr(p.Type)
	}
	if !p.given() && len(p.Files) == 0 {
		return Result{}, opErr(CodeInvalid, "html 与 files 至少要给一个。单页可以只给 html；多文件用 files，并包含入口 index.html；文档与幻灯片用 files 写入口文件。")
	}
	project := c.ProjectID
	if p.ProjectID != nil {
		if c.Scope != ScopeAll {
			return Result{}, opErr(CodeInvalid, "不能指定 projectId：新建的 artifact 归你所在的线程与项目。去掉这个参数再试。")
		}
		if *p.ProjectID != "" {
			if _, err := s.st.Q.GetProject(ctx, *p.ProjectID); errors.Is(err, sql.ErrNoRows) {
				return Result{}, opErr(CodeInvalid, "项目 %s 不存在。去掉 projectId 或换一个存在的项目。", *p.ProjectID)
			} else if err != nil {
				return Result{}, err
			}
		}
		project = p.ProjectID
	}
	ps, err := collectFiles(c, p.htmlArgs, p.Files, false)
	if err != nil {
		return Result{}, err
	}
	files, closeAll := changes(ctx, c, ps)
	defer closeAll()
	a, err := s.Create(ctx, CreateParams{
		Title: p.Title, Type: p.Type, ProjectID: deref(project), ThreadID: c.ThreadID, Origin: OriginAgent,
		Author: c.author(), Turn: c.Turn, Note: p.Note, Files: files, Capabilities: p.Capabilities,
	})
	if err != nil {
		return Result{}, err
	}
	return cardResult(OpCreate, obj{
		"id": a.ID, "version": a.Version, "pages": a.Pages, "kind": a.Kind, "url": s.PublicURL() + "/artifacts/" + a.ID,
	}, a, 0), nil
}

type readArgs struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Path    string `json:"path"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
}

func (s *Service) agentRead(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p readArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Offset < 0 {
		return Result{}, opErr(CodeInvalid, "offset 不能是负数。")
	}
	switch {
	case p.Limit < 0:
		return Result{}, opErr(CodeInvalid, "limit 不能是负数。")
	case p.Limit == 0:
		p.Limit = ReadDefaultChars
	case p.Limit > ReadMaxChars:
		p.Limit = ReadMaxChars
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	if p.Version < 0 {
		return Result{}, opErr(CodeInvalid, "version 不能是负数；不给表示最新版本。")
	}
	v, err := s.Version(ctx, a.ID, p.Version)
	if err != nil {
		return Result{}, err
	}
	files, err := s.Files(ctx, a.ID, v.Version)
	if err != nil {
		return Result{}, err
	}
	if p.Path == "" {
		p.Path = a.Entry()
	}
	list := make([]obj, 0, len(files))
	for _, f := range files {
		list = append(list, obj{"path": f.Path, "size": f.Size, "mime": f.Mime})
	}
	caps := v.Capabilities
	if caps == nil {
		caps = []string{}
	}
	head := obj{
		"id": a.ID, "title": a.Title, "kind": a.Kind, "version": v.Version, "head": a.Version, "pages": v.Pages,
		"updatedAt": stamp(a.UpdatedAt), "files": list, "capabilities": caps, "path": p.Path, "offset": p.Offset,
	}
	if a.Type != "" {
		head["type"] = a.Type
		head["format"] = v.Format
	}
	if a.Kind == KindCanvas {
		if names := s.canvasViewing(ctx, a.ID); len(names) > 0 {
			head["viewing"] = names
		}
	}
	if IsBinaryType(a.Type) {
		// 二进制 Office 文件不读字节：只给清单与取回指引
		if !slices.ContainsFunc(files, func(f File) bool { return f.Path == p.Path }) {
			return Result{}, opErr(CodeInvalid, "版本 %d 里没有文件 %s。path 取 files 列表里的路径；不给表示入口文件。", v.Version, p.Path)
		}
		head["binary"] = true
		head["hint"] = "这是二进制 Office 文件；用 pull_artifact 取回到本机后再处理。"
		hb, _ := json.Marshal(head)
		details := make(obj, len(head)+1)
		for k, x := range head {
			details[k] = x
		}
		details["op"] = OpRead
		return Result{Text: string(hb), Details: details}, nil
	}
	data, _, err := s.ReadFile(ctx, a.ID, v.Version, p.Path)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return Result{}, opErr(CodeInvalid, "版本 %d 里没有文件 %s。path 取 files 列表里的路径；不给表示入口文件。", v.Version, p.Path)
	}
	if err != nil {
		return Result{}, err
	}
	var text string
	if utf8.Valid(data) {
		var next int
		text, next, err = pageRunes(data, p.Offset, p.Limit)
		if err != nil {
			return Result{}, err
		}
		if next > 0 {
			head["nextOffset"] = next
		}
	} else {
		head["binary"] = true
	}
	hb, _ := json.Marshal(head)
	details := make(obj, len(head)+1)
	for k, x := range head {
		details[k] = x
	}
	details["op"] = OpRead
	return Result{Text: string(hb) + "\n\n" + text, Details: details}, nil
}

// pageRunes 按字符（rune）取 [offset, offset+limit)；有剩余时 next 是下一页的 offset，否则为 0。
func pageRunes(b []byte, offset, limit int) (text string, next int, err error) {
	i, n := 0, 0
	for n < offset && i < len(b) {
		_, w := utf8.DecodeRune(b[i:])
		i += w
		n++
	}
	if n < offset {
		return "", 0, opErr(CodeInvalid, "offset %d 超出了文件长度（共 %d 个字符）。", offset, n)
	}
	j, k := i, 0
	for k < limit && j < len(b) {
		_, w := utf8.DecodeRune(b[j:])
		j += w
		k++
	}
	if j < len(b) {
		next = offset + k
	}
	return string(b[i:j]), next, nil
}

type editArg struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type updateArgs struct {
	htmlArgs
	ID           string          `json:"id"`
	BaseVersion  *int            `json:"baseVersion"`
	Title        *string         `json:"title"`
	Files        []fileParam     `json:"files"`
	Edits        []editArg       `json:"edits"`
	Capabilities *[]string       `json:"capabilities"`
	Canvas       json.RawMessage `json:"canvas"`
	Note         *string         `json:"note"`
}

func (s *Service) agentUpdate(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p updateArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	hasCaps, hasCanvas := p.Capabilities != nil, len(p.Canvas) > 0
	content := p.given() || len(p.Files) > 0 || len(p.Edits) > 0 || hasCaps || hasCanvas
	if p.Title != nil {
		if content || p.Note != nil {
			return Result{}, opErr(CodeInvalid, "title 不能和其它改动一起给：改名不产生版本。先单独改名，再另调一次 update_artifact 改内容。")
		}
		r, err := s.Rename(ctx, a.ID, *p.Title)
		if err != nil {
			return Result{}, err
		}
		return cardResult(OpUpdate, obj{"id": r.ID, "version": r.Version, "pages": r.Pages}, r, 0), nil
	}
	if !content {
		return Result{}, opErr(CodeInvalid, "没有任何改动。给 html、files、edits 或 capabilities；只改标题时给 title。")
	}
	if hasCaps && hasCanvas {
		return Result{}, opErr(CodeInvalid, "capabilities 不能和 canvas 操作一起给。先用 canvas 改布局，再另调一次 update_artifact 改能力声明。")
	}
	if hasCanvas && a.Kind != KindCanvas {
		return Result{}, opErr(CodeInvalid, "%s 不是画布 artifact，不能用 canvas 操作。改它的文件请用 files 或 edits。", a.ID)
	}
	if p.BaseVersion == nil {
		return Result{}, opErr(CodeInvalid, "改内容必须带 baseVersion。先 read_artifact，把返回的 version 作为 baseVersion。")
	}
	base := *p.BaseVersion
	if base < 1 || base > a.Version {
		return Result{}, &OpError{Code: CodeInvalid, State: &State{Version: a.Version},
			Message: fmt.Sprintf("baseVersion %d 不存在；%s 现在是版本 %d。先 read_artifact 拿到最新版本号。", base, a.ID, a.Version)}
	}
	if err := s.guardDesignWrite(ctx, a, p.Files, p.Edits); err != nil {
		return Result{}, err
	}
	if p.given() && a.Type != "" {
		return Result{}, typedHTMLErr(a.Type)
	}
	if len(p.Edits) > MaxEdits {
		return Result{}, opErr(CodeInvalid, "edits 最多 %d 条，这次给了 %d 条。分几次改，或者用 files 整体替换文件。", MaxEdits, len(p.Edits))
	}
	ps, err := collectFiles(c, p.htmlArgs, p.Files, true)
	if err != nil {
		return Result{}, err
	}
	if ps, err = s.applyEdits(ctx, c, a, base, ps, p.Edits); err != nil {
		return Result{}, err
	}
	if hasCanvas && slices.ContainsFunc(ps, func(f *pending) bool { return f.path == EntryCanvas }) {
		return Result{}, opErr(CodeInvalid, "同一次调用里不能既给 canvas 操作又改 canvas.json（files 或 edits）。只用 canvas 操作改布局，或者分两次调用。")
	}
	files, closeAll := changes(ctx, c, ps)
	defer closeAll()
	var r Artifact
	if hasCanvas {
		r, err = s.agentCanvas(ctx, c, a.ID, base, p.Canvas, files, deref(p.Note))
	} else {
		r, err = s.Write(ctx, WriteParams{ID: a.ID, BaseVersion: base, Author: c.author(), Turn: c.Turn, Note: deref(p.Note), Files: files, Capabilities: p.Capabilities})
	}
	if err != nil {
		return Result{}, err
	}
	return cardResult(OpUpdate, obj{"id": r.ID, "version": r.Version, "pages": r.Pages}, r, r.Version-1), nil
}

// applyEdits 依次应用 edits：每条的 oldText 必须在「前面的改动应用之后」的文本里恰好出现一次。被改的文件取本次
// files 里的新内容，没有则取 baseVersion 里的；结果作为内联内容并入 ps。
func (s *Service) applyEdits(ctx context.Context, c Caller, a Artifact, base int, ps []*pending, edits []editArg) ([]*pending, error) {
	byPath := make(map[string]*pending, len(ps))
	for _, p := range ps {
		byPath[p.path] = p
	}
	texts := map[string]string{}
	for i, e := range edits {
		path := e.Path
		if path == "" {
			path = a.Entry()
		}
		if e.OldText == "" {
			return nil, opErr(CodeInvalid, "edits[%d]：oldText 不能为空。要新增内容，就把相邻的一段原文连同新内容一起作为 newText。", i)
		}
		text, ok := texts[path]
		if !ok {
			b, err := s.editBase(ctx, c, a.ID, base, byPath[path], path, i)
			if err != nil {
				return nil, err
			}
			if !utf8.Valid(b) {
				return nil, opErr(CodeInvalid, "edits[%d]：%s 不是 UTF-8 文本，不能用 edits 改。用 files 整体替换它。", i, path)
			}
			text = string(b)
		}
		at := strings.Index(text, e.OldText)
		if at < 0 {
			return nil, opErr(CodeEditNotFound, "edits[%d]：在 %s 里找不到 oldText（按前面的 edits 改过之后的文本）。重新 read_artifact 核对原文，oldText 要逐字一致，包括空白与缩进。", i, path)
		}
		if strings.Contains(text[at+1:], e.OldText) {
			n := max(strings.Count(text, e.OldText), 2)
			return nil, opErr(CodeInvalid, "edits[%d]：oldText 在 %s 里出现了 %d 次，必须恰好一次。把 oldText 加长，带上前后能唯一定位的上下文。", i, path, n)
		}
		texts[path] = text[:at] + e.NewText + text[at+len(e.OldText):]
	}
	for path, text := range texts {
		if p := byPath[path]; p != nil {
			p.data, p.source = []byte(text), ""
			continue
		}
		ps = append(ps, &pending{path: path, data: []byte(text)})
	}
	return ps, nil
}

// editBase 返回 edits 作用的原文：本次 files 里写了的取新内容，否则取 baseVersion 里的文件。
func (s *Service) editBase(ctx context.Context, c Caller, id string, base int, p *pending, path string, i int) ([]byte, error) {
	switch {
	case p == nil:
		b, _, err := s.ReadFile(ctx, id, base, path)
		if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
			return nil, opErr(CodeInvalid, "edits[%d]：版本 %d 里没有文件 %s。新文件请用 files 写入。", i, base, path)
		}
		return b, err
	case p.remove:
		return nil, opErr(CodeInvalid, "edits[%d]：%s 在这次的 files 里被删除了，不能再改它。", i, path)
	case p.source != "":
		return fetchAll(ctx, c, p.source)
	default:
		return p.data, nil
	}
}

type listArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *Service) agentList(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p listArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	switch {
	case p.Limit < 0:
		return Result{}, opErr(CodeInvalid, "limit 不能是负数。")
	case p.Limit == 0:
		p.Limit = listDefaultLimit
	case p.Limit > listMaxLimit:
		p.Limit = listMaxLimit
	}
	var f Filter
	switch c.Scope {
	case ScopeProject:
		f.ProjectID = *c.ProjectID
	case ScopeThread:
		if c.ThreadID == "" {
			return jsonResult(OpList, obj{"artifacts": []obj{}}), nil
		}
		f.ThreadID = c.ThreadID
	}
	all, err := s.List(ctx, f)
	if err != nil {
		return Result{}, err
	}
	q := strings.ToLower(strings.TrimSpace(p.Query))
	names := map[string]string{}
	out := []obj{}
	for _, a := range all {
		if len(out) == p.Limit {
			break
		}
		if q != "" && !strings.Contains(strings.ToLower(a.Title), q) {
			continue
		}
		thread, err := s.threadName(ctx, a, names)
		if err != nil {
			return Result{}, err
		}
		out = append(out, obj{
			"id": a.ID, "title": a.Title, "kind": a.Kind, "version": a.Version, "pages": a.Pages,
			"updatedAt": stamp(a.UpdatedAt), "thread": thread,
		})
	}
	return jsonResult(OpList, obj{"artifacts": out}), nil
}

// threadName 是创建线程的标题：导入的写 user，线程已不在写「已删除的线程」。
func (s *Service) threadName(ctx context.Context, a Artifact, cache map[string]string) (string, error) {
	if a.Origin == OriginImport {
		return "user", nil
	}
	if a.ThreadID == "" {
		return deletedThreadName, nil
	}
	if n, ok := cache[a.ThreadID]; ok {
		return n, nil
	}
	n := deletedThreadName
	t, err := s.st.Q.GetThread(ctx, a.ThreadID)
	switch {
	case err == nil:
		n = t.Title
		if n == "" {
			n = t.ID
		}
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	cache[a.ThreadID] = n
	return n, nil
}

type idArgs struct {
	ID string `json:"id"`
}

func (s *Service) agentDelete(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p idArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, err := s.lookup(ctx, c, p.ID, false)
	if err != nil {
		return Result{}, err
	}
	if a, err = s.Delete(ctx, a.ID); err != nil {
		return Result{}, err
	}
	return cardResult(OpDelete, obj{"id": a.ID, "purgeAt": stamp(a.DeletedAt.Add(KeepDeleted))}, a, 0), nil
}

func (s *Service) agentVersions(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p idArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	vs, err := s.Versions(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	out := make([]obj, 0, len(vs))
	for _, v := range vs {
		author := "user"
		if v.AuthorThreadID != "" {
			author = v.AuthorLabel
			if author == "" {
				author = v.AuthorThreadID
			}
		}
		item := obj{"version": v.Version, "createdAt": stamp(v.CreatedAt), "author": author, "size": v.Size}
		if v.Note != "" {
			item["note"] = v.Note
		}
		if v.TurnID != "" {
			item["turnId"] = v.TurnID
		}
		if v.Title != "" {
			item["title"] = v.Title
		}
		out = append(out, item)
	}
	return jsonResult(OpListVersions, obj{"versions": out}), nil
}

type restoreArgs struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	BaseVersion int    `json:"baseVersion"`
}

func (s *Service) agentRestore(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p restoreArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Version < 1 || p.BaseVersion < 1 {
		return Result{}, opErr(CodeInvalid, "version 与 baseVersion 都必须给（从 1 起）。先 list_artifact_versions 选要恢复的版本，baseVersion 传当前最新版本号。")
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	r, err := s.restoreVersion(ctx, a.ID, p.Version, p.BaseVersion, c.author(), c.Turn)
	if err != nil {
		return Result{}, err
	}
	// fromVersion 是被恢复的旧版本。
	return cardResult(OpRestoreVersion, obj{"id": r.ID, "version": r.Version}, r, p.Version), nil
}

type guideArgs struct {
	Topic string `json:"topic"`
}

// agentGuide 的 Text 直接是教程正文（不包成 JSON，便于模型阅读）。
func (s *Service) agentGuide(params json.RawMessage) (Result, error) {
	var p guideArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	text, err := guideText(p.Topic)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: text, Details: obj{"op": OpReadGuide, "topic": p.Topic}}, nil
}

// OrbLinks 是 orb link 上注册 ctl 请求处理器的出口（*orbs.Service 实现）。
type OrbLinks interface {
	HandleRequest(method string, fn orbs.RequestFunc)
}

// SourceFetcher 经 library.fetch 从 orb 取回文件字节（*library.Syncer 实现）。
type SourceFetcher interface {
	Fetch(ctx context.Context, orbID, path string) (io.ReadCloser, int64, error)
}

// orbCallParams 是 artifact.call 的参数（params 保持原样，交给 Call 严格解码）。Turn 可选：orb 扩展每次调用都带
// 当前回合，进入 Caller.Turn。
type orbCallParams struct {
	ThreadID string          `json:"threadId"`
	Op       string          `json:"op"`
	Params   json.RawMessage `json:"params"`
	Turn     *struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"turn"`
}

// RegisterOrbMethod 注册 orb / Runner 内 agent 的 artifact 工具经 orbd 转来的 artifact.call；须在 orb 接受 link 之前调用。
func (s *Service) RegisterOrbMethod(links OrbLinks, fetch SourceFetcher) {
	links.HandleRequest(string(apigen.MethodArtifactCall), func(ctx context.Context, orbID string, params json.RawMessage) (any, error) {
		return s.onOrbCall(ctx, fetch, orbID, params)
	})
}

func (s *Service) onOrbCall(ctx context.Context, fetch SourceFetcher, orbID string, params json.RawMessage) (any, error) {
	var p orbCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &link.CtlError{Code: "invalid_params", Message: err.Error()}
	}
	c, err := s.orbCaller(ctx, fetch, orbID, p.ThreadID)
	if err != nil {
		return nil, err
	}
	if p.Turn != nil {
		c.Turn = Turn{ID: p.Turn.ID, Title: p.Turn.Title}
	}
	res, err := s.Call(ctx, c, p.Op, p.Params)
	if err != nil {
		if oe, ok := AsOpError(err); ok {
			return nil, &link.CtlError{Code: oe.Code, Message: oe.Message}
		}
		return nil, err
	}
	out := apigen.LinkCtlArtifactCallResult{Text: res.Text}
	if res.Details != nil {
		b, err := json.Marshal(res.Details)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &out.Details); err != nil {
			return nil, err
		}
	}
	if res.Image != nil {
		out.Image = &struct {
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		}{Data: res.Image.Data, MimeType: res.Image.MimeType}
	}
	return out, nil
}

// orbCaller 校验调用方线程（存在、是工作线程、未归档、属于发起 link 的 orb；否则 forbidden）并构造 Caller：
// Runner 或没有项目的线程只看得到自己的 artifact，其余看得到同项目的。
func (s *Service) orbCaller(ctx context.Context, fetch SourceFetcher, orbID, threadID string) (Caller, error) {
	forbidden := &link.CtlError{Code: CodeForbidden, Message: "调用方线程不存在、已归档，或不属于这台 orb。"}
	if threadID == "" {
		return Caller{}, forbidden
	}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, forbidden
	}
	if err != nil {
		return Caller{}, err
	}
	if t.Kind != threads.KindWork || t.ArchivedAt != nil || t.OrbID == nil || *t.OrbID != orbID {
		return Caller{}, forbidden
	}
	o, err := s.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return Caller{}, forbidden
	}
	if err != nil {
		return Caller{}, err
	}
	scope := ScopeProject
	if orbs.IsRunner(o) || t.ProjectID == nil {
		scope = ScopeThread
	}
	label := t.Title
	if label == "" {
		label = t.ID
	}
	return Caller{
		ThreadID: t.ID, Label: label, ProjectID: t.ProjectID, Scope: scope, Internal: !orbs.IsRunner(o),
		Fetch: func(ctx context.Context, path string) (io.ReadCloser, int64, error) {
			rc, size, err := fetch.Fetch(ctx, orbID, path)
			if errors.Is(err, library.ErrTooLarge) {
				return nil, 0, sourceTooLarge(path)
			}
			var ce *link.CtlError
			if errors.As(err, &ce) {
				return nil, 0, opErr(CodeInvalid, "取不到 source %s（%s）。确认它是 orb 里存在的普通文件，路径写对。", path, ce.Message)
			}
			if err != nil {
				return nil, 0, err
			}
			if size > MaxFileBytes {
				_ = rc.Close()
				return nil, 0, sourceTooLarge(path)
			}
			return rc, size, nil
		},
	}, nil
}
