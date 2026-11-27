package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// 设计画布的内部操作（plan-m29 C5）：只由普通 orb 的 orbd 经 artifact.call 发来（Caller.Internal），不注册成工具，
// 也不出现在「可用的操作」提示里。
const (
	// OpSealDesign 把工作副本封存成一个版本：{id, baseVersion, files:[{path, source}|{path, remove}], check?}。
	OpSealDesign = "seal_design"
	// OpFetchDesignFiles 分页取一个版本的 src/**：{id, version?, cursor?} → orbsock.DesignFilesPage。
	OpFetchDesignFiles = "fetch_design_files"
	// OpFetchDesignRuntime 取控制面 Web 构建里的 sandbox/runtime.js 与 sandbox/probe.js：{} → orbsock.DesignRuntimeFiles。
	OpFetchDesignRuntime = "fetch_design_runtime"
	// OpDesignHead 是工作副本对账用的最新版本摘要：{id} → orbsock.DesignHead。
	OpDesignHead = "design_head"
	// OpExportDesignProject 分页导出设计画布版本的工程 zip（ExportProject）：{id, version?, cursor?} → orbsock.DesignProjectPage。
	OpExportDesignProject = "export_design_project"
)

const (
	// designFramesFile 是引擎构建写出的帧清单（C2）。
	designFramesFile            = "dist/frames.json"
	designSrcPrefix             = "src/"
	designDistPrefix            = "dist/"
	exportDesignProjectMaxBytes = 64 << 20
)

// frameIDRe 是帧 id：src/frames/<id>.tsx 的 <id>（与 canvas 的 source 规则一致）。
var frameIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// designRuntimeAssets 是 fetch_design_runtime 读取的 Web 构建产物（名字 → 构建产物里的路径）。
var designRuntimeAssets = []struct{ name, asset string }{
	{"runtime.js", "sandbox/runtime.js"},
	{"probe.js", "sandbox/probe.js"},
}

// designPath 报告 path 是否只经封存写入（C2：src/**、dist/**）。
func designPath(path string) bool {
	return strings.HasPrefix(path, designSrcPrefix) || strings.HasPrefix(path, designDistPrefix)
}

// callInternal 分派设计画布的内部操作；调用方已确认 c.Internal。
func (s *Service) callInternal(ctx context.Context, c Caller, op string, params json.RawMessage) (Result, error) {
	switch op {
	case OpSealDesign:
		return s.agentSealDesign(ctx, c, params)
	case OpFetchDesignFiles:
		return s.agentFetchDesignFiles(ctx, c, params)
	case OpDesignHead:
		return s.agentDesignHead(ctx, c, params)
	case OpExportDesignProject:
		return s.agentExportDesignProject(ctx, c, params)
	default:
		return s.agentFetchDesignRuntime(params)
	}
}

// designSourceFile 报告 path 是否属于工作副本的源码（src/ 下、不在任何一层 node_modules 里），口径同 design.SourceHash。
func designSourceFile(path string) bool {
	rest, ok := strings.CutPrefix(path, designSrcPrefix)
	if !ok || rest == "" {
		return false
	}
	return !slices.Contains(strings.Split(rest, "/"), "node_modules")
}

// isDesignCanvas 报告 a 的最新版本是不是设计画布（canvas.json 带 design）。读不到或解析失败按不是处理。
func (s *Service) isDesignCanvas(ctx context.Context, a Artifact) (bool, error) {
	if a.Kind != KindCanvas || a.Type != "" {
		return false, nil
	}
	b, _, err := s.ReadFile(ctx, a.ID, a.Version, EntryCanvas)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc, err := canvas.Parse(b)
	return err == nil && doc.Design != nil, nil
}

// guardDesignWrite 实现 C2 末句：设计画布的 src/**、dist/** 只经封存写入，update_artifact 的 files / edits 命中这两个前缀
// 时回 invalid。edits 省略 path 时作用于入口文件（canvas.json），不受限。
func (s *Service) guardDesignWrite(ctx context.Context, a Artifact, files []fileParam, edits []editArg) error {
	var hit string
	for _, f := range files {
		if designPath(f.Path) {
			hit = f.Path
			break
		}
	}
	for _, e := range edits {
		if hit == "" && designPath(e.Path) {
			hit = e.Path
		}
	}
	if hit == "" {
		return nil
	}
	ok, err := s.isDesignCanvas(ctx, a)
	if err != nil || !ok {
		return err
	}
	return opErr(CodeInvalid, "%s 是设计画布，%s 只能由回合结束时的封存写入：直接改 orb 里的工作副本（design_open 返回的 dir 下的 src/），回合结束时会自动构建并封存。", a.ID, hit)
}

// ---- seal_design ----

type sealArgs struct {
	ID          string        `json:"id"`
	BaseVersion int           `json:"baseVersion"`
	Files       []fileParam   `json:"files"`
	Check       *design.Check `json:"check"`
}

// sealFrame 是 dist/frames.json 的一项（引擎构建生成）。
type sealFrame struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Width  float64  `json:"width"`
	Height float64  `json:"height"`
	Device string   `json:"device"`
	Links  []string `json:"links"`
}

// agentSealDesign 是 seal_design：src/**、dist/** 按 baseVersion 做文件级冲突检查（base 与最新版之间这两个前缀下
// 任何文件变过都算冲突），canvas.json 由服务端合并、不参与；按 dist/frames.json 用 canvas.ApplySeal 对齐画框
// （新帧 add 并自动排位，name / width / height / device 只在新增时取用；消失的帧 remove），整体经 writeCanvas
// 写成调用方回合下的一个版本。给了 check 时写进新版本的 check_json（fingerprint = 新版本 src/ 的 SourceHash）。
func (s *Service) agentSealDesign(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p sealArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Check != nil {
		switch p.Check.Status {
		case design.CheckClean, design.CheckIssues, design.CheckUnverified:
		default:
			return Result{}, opErr(CodeInvalid, "check.status 只能是 clean、issues、unverified 之一。")
		}
	}
	if len(p.Files) == 0 {
		return Result{}, opErr(CodeInvalid, "files 是空的：封存至少要带构建产物。")
	}
	for i, f := range p.Files {
		if !designPath(f.Path) {
			return Result{}, opErr(CodeInvalid, "files[%d]（%s）：封存只写 src/ 与 dist/ 下的文件。", i, f.Path)
		}
		if f.Content != nil {
			return Result{}, opErr(CodeInvalid, "files[%d]（%s）：封存只接受 source 或 remove。", i, f.Path)
		}
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	if a.Kind != KindCanvas || a.Type != "" {
		return Result{}, opErr(CodeInvalid, "%s 不是画布 artifact，不能封存设计稿。", a.ID)
	}
	ps, err := collectFiles(c, htmlArgs{}, p.Files, true)
	if err != nil {
		return Result{}, err
	}
	framesRaw, err := s.sealFramesJSON(ctx, c, a.ID, ps)
	if err != nil {
		return Result{}, err
	}
	frames, err := parseSealFrames(framesRaw)
	if err != nil {
		return Result{}, err
	}

	unlock := s.live.locks.lock(a.ID)
	defer unlock()
	l := s.liveOf(a.ID)
	if l != nil {
		if err := s.flushLive(ctx, a.ID, l); err != nil {
			return Result{}, err
		}
	}
	head, doc, err := s.loadCanvas(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	if doc.Design == nil && len(doc.Pages) > 0 {
		return Result{}, opErr(CodeInvalid, "%s 是 HTML 画布（已有画框、没有 design），不能封存成设计画布。新建一个设计画布再做。", a.ID)
	}
	if err := s.sealConflicts(ctx, a.ID, p.BaseVersion, head.Version, ps); err != nil {
		return Result{}, err
	}
	if err := s.sealMissing(ctx, a.ID, ps, frames); err != nil {
		return Result{}, err
	}
	if err := canvas.ApplySeal(&doc, sealOps(doc, frames)); err != nil {
		return Result{}, canvasOpsErr(err)
	}
	files, closeAll := changes(ctx, c, ps, a.Type)
	defer closeAll()
	r, err := s.writeCanvas(ctx, a.ID, head.Version, doc, nil, c.author(), c.Turn, "", files)
	if err != nil {
		return Result{}, err
	}
	if l != nil {
		l.version, l.doc, l.pending, l.copies = r.Version, doc, nil, nil
		s.broadcastDoc(a.ID, l)
	}
	if p.Check != nil {
		chk := *p.Check
		if chk.Issues == nil {
			chk.Issues = []design.Issue{}
		}
		if chk.Fingerprint, err = s.sourceHash(ctx, a.ID, r.Version); err != nil {
			return Result{}, err
		}
		if err := s.storeCheck(ctx, a.ID, Version{Version: r.Version}, chk); err != nil {
			return Result{}, err
		}
	}
	return cardResult(OpSealDesign, obj{"id": r.ID, "version": r.Version, "pages": r.Pages, "frames": len(frames)}, r, r.Version-1), nil
}

// sealFramesJSON 取本次封存后的 dist/frames.json：本次写了的取新内容（source 读进内存，之后不再取第二次），
// 删了的报错，没写的取最新版本里的。
func (s *Service) sealFramesJSON(ctx context.Context, c Caller, id string, ps []*pending) ([]byte, error) {
	for _, p := range ps {
		if p.path != designFramesFile {
			continue
		}
		switch {
		case p.remove:
			return nil, opErr(CodeInvalid, "不能删除 %s：它是封存对齐画框的依据。", designFramesFile)
		case p.source != "":
			b, err := fetchAll(ctx, c, p.source)
			if err != nil {
				return nil, err
			}
			p.data, p.source = b, ""
		}
		return p.data, nil
	}
	b, _, err := s.ReadFile(ctx, id, 0, designFramesFile)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return nil, opErr(CodeInvalid, "缺少 %s：封存要带 vite build 的完整产物。", designFramesFile)
	}
	return b, err
}

// parseSealFrames 解析并校验 dist/frames.json。
func parseSealFrames(b []byte) ([]sealFrame, error) {
	var frames []sealFrame
	if err := json.Unmarshal(b, &frames); err != nil {
		return nil, opErr(CodeInvalid, "%s 不合法（%v）。", designFramesFile, err)
	}
	seen := make(map[string]bool, len(frames))
	for i, f := range frames {
		if !frameIDRe.MatchString(f.ID) {
			return nil, opErr(CodeInvalid, "%s[%d] 的 id %q 不合法：帧 id 是 1–64 个小写字母、数字或 -，以字母或数字开头。", designFramesFile, i, f.ID)
		}
		if seen[f.ID] {
			return nil, opErr(CodeInvalid, "%s 里帧 %s 出现了两次。", designFramesFile, f.ID)
		}
		seen[f.ID] = true
	}
	if len(frames) > canvas.MaxPages {
		return nil, opErr(CodeInvalid, "最多 %d 帧，这次有 %d 帧。", canvas.MaxPages, len(frames))
	}
	return frames, nil
}

// sealConflicts 是 src/**、dist/** 的文件级冲突检查：本次写或删的路径，以及 base 与 head 两版里这两个前缀下的全部路径，
// 任何一个在 base 之后变过就 conflict（同 checkBase 的口径）。canvas.json 不参与。
func (s *Service) sealConflicts(ctx context.Context, id string, base, head int, ps []*pending) error {
	if base < 1 || base > head {
		return &OpError{Code: CodeInvalid, State: &State{Version: head},
			Message: fmt.Sprintf("baseVersion %d 不存在；%s 现在是版本 %d。", base, id, head)}
	}
	if base == head {
		return nil
	}
	set := map[string]bool{}
	for _, p := range ps {
		set[p.path] = true
	}
	for _, v := range []int{base, head} {
		files, err := s.Files(ctx, id, v)
		if err != nil {
			return err
		}
		for _, f := range files {
			if designPath(f.Path) {
				set[f.Path] = true
			}
		}
	}
	paths := make([]string, 0, len(set))
	for p := range set {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return checkBase(ctx, s.st.Q, id, base, int64(head), paths)
}

// sealMissing 检查每一帧的产物 dist/<id>.html 在封存后的版本里都在（否则 writeCanvas 会拿空白页顶上）。
func (s *Service) sealMissing(ctx context.Context, id string, ps []*pending, frames []sealFrame) error {
	files, err := s.Files(ctx, id, 0)
	if err != nil {
		return err
	}
	has := make(map[string]bool, len(files)+len(ps))
	for _, f := range files {
		has[f.Path] = true
	}
	for _, p := range ps {
		has[p.path] = !p.remove
	}
	var missing []string
	for _, f := range frames {
		if file := canvas.FrameFile(frameSource(f.ID)); !has[file] {
			missing = append(missing, file)
		}
	}
	if len(missing) > 0 {
		return opErr(CodeInvalid, "%s 列出的帧缺少产物：%s。封存要带 vite build 的完整 dist/。", designFramesFile, strings.Join(missing, "、"))
	}
	return nil
}

func frameSource(id string) string { return "src/frames/" + id + ".tsx" }

// sealOps 返回把 doc 的设计画框对齐到 frames 的操作：带 source 的画框里不在 frames 的 remove；frames 里还没有画框的
// add 到第一个画布页（不给位置，自动排位），name / width / height / device 取自帧声明。
func sealOps(doc canvas.Doc, frames []sealFrame) []canvas.Op {
	want := make(map[string]bool, len(frames))
	for _, f := range frames {
		want[frameSource(f.ID)] = true
	}
	have := map[string]bool{}
	taken := map[string]bool{}
	for _, c := range doc.Canvases {
		taken[c.ID] = true
	}
	for _, n := range doc.Notes {
		taken[n.ID] = true
	}
	var ops []canvas.Op
	for _, p := range doc.Pages {
		if p.Source != "" && !want[p.Source] {
			ops = append(ops, canvas.Op{Op: canvas.OpRemove, ID: p.ID})
			continue
		}
		taken[p.ID] = true
		have[p.Source] = true
	}
	for _, f := range frames {
		src := frameSource(f.ID)
		if have[src] {
			continue
		}
		id := f.ID
		for n := 2; taken[id]; n++ {
			suffix := "-" + strconv.Itoa(n)
			id = f.ID[:min(len(f.ID), 64-len(suffix))] + suffix
		}
		taken[id] = true
		obj := map[string]any{
			"type": canvas.TypePage, "id": id, "canvas": doc.Canvases[0].ID, "name": frameName(f),
			"source": src, "width": frameSize(f.Width, canvas.DefaultPageWidth), "height": frameSize(f.Height, canvas.DefaultPageHeight),
		}
		if f.Device != "" && f.Device != canvas.DeviceNone && slices.Contains(canvas.Devices, f.Device) {
			obj["device"] = f.Device
		}
		ops = append(ops, canvas.Op{Op: canvas.OpAdd, Object: obj})
	}
	return ops
}

func frameName(f sealFrame) string {
	name := strings.Join(strings.Fields(f.Name), " ")
	if name == "" {
		return f.ID
	}
	if utf8.RuneCountInString(name) > canvas.MaxNameRunes {
		name = string([]rune(name)[:canvas.MaxNameRunes])
	}
	return name
}

func frameSize(v, def float64) float64 {
	if v <= 0 || math.IsNaN(v) {
		return def
	}
	return math.Round(min(max(v, canvas.MinPageSize), canvas.MaxPageSize))
}

// ---- fetch_design_files ----

type fetchFilesArgs struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Cursor  string `json:"cursor"`
}

// designCursor 是 fetch_design_files 的游标 `<文件序号>:<文件内偏移>`：序号指按路径排序的 src/** 清单（版本固定，
// 清单不变）。
type designCursor struct {
	file int
	off  int64
}

func (c designCursor) String() string {
	return strconv.Itoa(c.file) + ":" + strconv.FormatInt(c.off, 10)
}

func parseDesignCursor(s string) (designCursor, bool) {
	if s == "" {
		return designCursor{}, true
	}
	a, b, ok := strings.Cut(s, ":")
	if !ok {
		return designCursor{}, false
	}
	file, err1 := strconv.Atoi(a)
	off, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || file < 0 || off < 0 {
		return designCursor{}, false
	}
	return designCursor{file: file, off: off}, true
}

// agentFetchDesignFiles 是 fetch_design_files：按游标返回版本里 src/**（不含 node_modules）的一页文件片，单个文件可以
// 跨页。整条 artifact.call 结果序列化后不超过 orbsock.DesignFilesPageBytes。
func (s *Service) agentFetchDesignFiles(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p fetchFilesArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Version < 0 {
		return Result{}, opErr(CodeInvalid, "version 不能是负数；不给表示最新版本。")
	}
	cur, ok := parseDesignCursor(p.Cursor)
	if !ok {
		return Result{}, opErr(CodeInvalid, "cursor %q 不合法：原样传上一页返回的 next。", p.Cursor)
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	v, err := s.Version(ctx, a.ID, p.Version)
	if err != nil {
		return Result{}, err
	}
	all, err := s.Files(ctx, a.ID, v.Version)
	if err != nil {
		return Result{}, err
	}
	list := slices.DeleteFunc(all, func(f File) bool { return !designSourceFile(f.Path) })
	if cur.file > len(list) || (cur.file < len(list) && cur.off > list[cur.file].Size) || (cur.file == len(list) && cur.off != 0) {
		return Result{}, opErr(CodeInvalid, "cursor %q 超出版本 %d 的文件范围：从头（不带 cursor）重新取。", p.Cursor, v.Version)
	}
	page, err := s.designFilesPage(ctx, a.ID, v.Version, list, cur, orbsock.DesignFilesPageBytes)
	if err != nil {
		return Result{}, err
	}
	return designFilesResult(a.ID, page), nil
}

// designFilesResult 是 fetch_design_files 的结果：Text 是页的 JSON，Details 只带计数。
func designFilesResult(id string, page orbsock.DesignFilesPage) Result {
	b, _ := json.Marshal(page)
	d := obj{"op": OpFetchDesignFiles, "id": id, "version": page.Version, "files": len(page.Files)}
	if page.Next != "" {
		d["next"] = page.Next
	}
	return Result{Text: string(b), Details: d}
}

// designFilesPage 从 cur 起装一页：每片的开销按「片的 JSON（data 为空）× 2」估上界（Text 作为 JSON 字符串再编码一次，
// 每个字节至多变两个），data 的 base64 不需要转义、按原长计；固定开销按 next 取最长时的空页结果实测。
func (s *Service) designFilesPage(ctx context.Context, id string, version int, list []File, cur designCursor, limit int) (orbsock.DesignFilesPage, error) {
	page := orbsock.DesignFilesPage{Version: version, Files: []orbsock.DesignFileChunk{}}
	longest := designCursor{file: math.MaxInt32, off: math.MaxInt64}.String()
	fixed := designResultBytes(id, orbsock.DesignFilesPage{Version: version, Files: []orbsock.DesignFileChunk{}, Next: longest})
	used := fixed
	for i := cur.file; i < len(list); i++ {
		f := list[i]
		off := int64(0)
		if i == cur.file {
			off = cur.off
		}
		for {
			meta, _ := json.Marshal(orbsock.DesignFileChunk{Path: f.Path, Offset: off, Size: f.Size, Sha256: f.Sha256})
			room := limit - used - 2*len(meta) - 2 // 2：两层编码下的逗号
			n := min(f.Size-off, int64(max(room, 0)/4*3))
			if n <= 0 && (f.Size > off || room < 0) {
				page.Next = designCursor{file: i, off: off}.String()
				return page, nil
			}
			data, err := s.readRange(ctx, id, version, f.Path, off, n)
			if err != nil {
				return page, err
			}
			page.Files = append(page.Files, orbsock.DesignFileChunk{
				Path: f.Path, Offset: off, Data: base64.StdEncoding.EncodeToString(data), Size: f.Size, Sha256: f.Sha256,
			})
			used += 2*len(meta) + 2 + base64.StdEncoding.EncodedLen(int(n))
			off += n
			if off >= f.Size {
				break
			}
		}
	}
	return page, nil
}

// designResultBytes 是 fetch_design_files 结果经 artifact.call 外送时的序列化大小。
func designResultBytes(id string, page orbsock.DesignFilesPage) int {
	r := designFilesResult(id, page)
	d, _ := r.Details.(obj)
	b, _ := json.Marshal(apigen.LinkCtlArtifactCallResult{Text: r.Text, Details: d})
	return len(b)
}

// readRange 读版本里一个文件的 [off, off+n)。
func (s *Service) readRange(ctx context.Context, id string, version int, path string, off, n int64) ([]byte, error) {
	rc, _, err := s.Open(ctx, id, version, path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	if _, err := rc.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(rc, buf); err != nil {
		return nil, fmt.Errorf("artifacts: 读 %s 的 [%d, %d): %w", path, off, off+n, err)
	}
	return buf, nil
}

// ---- design_head ----

// agentDesignHead 是 design_head：最新版本号、它的 check_json 原文与 src/**、dist/** 的文件摘要，以及探针的审美配置
// （aesthetic = 已选定设计方向，surface 取 artifact_designs，与 ServeRender 注入的 window.__orynProbe 同口径）。只认
// 设计画布与还没有画框的画布（design.open 接管时用它校验）。
func (s *Service) agentDesignHead(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p idArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	if a.Kind != KindCanvas || a.Type != "" {
		return Result{}, opErr(CodeInvalid, "%s 不是画布 artifact，不能作为设计稿打开。", a.ID)
	}
	b, _, err := s.ReadFile(ctx, a.ID, a.Version, EntryCanvas)
	if err != nil {
		return Result{}, err
	}
	if doc, err := canvas.Parse(b); err != nil || (doc.Design == nil && len(doc.Pages) > 0) {
		return Result{}, opErr(CodeInvalid, "%s 是 HTML 画布（已有画框、没有 design），不能作为设计稿打开。新建一个设计画布再做。", a.ID)
	}
	v, err := s.Version(ctx, a.ID, a.Version)
	if err != nil {
		return Result{}, err
	}
	files, err := s.Files(ctx, a.ID, v.Version)
	if err != nil {
		return Result{}, err
	}
	head := orbsock.DesignHead{Version: v.Version, Check: v.Check, Files: []orbsock.DesignHeadFile{}}
	switch row, err := s.st.Q.GetArtifactDesign(ctx, a.ID); {
	case err == nil:
		head.Aesthetic, head.Surface = deref(row.Direction) != "", deref(row.Surface)
	case !errors.Is(err, sql.ErrNoRows):
		return Result{}, err
	}
	for _, f := range files {
		if designSourceFile(f.Path) || strings.HasPrefix(f.Path, designDistPrefix) {
			head.Files = append(head.Files, orbsock.DesignHeadFile{Path: f.Path, Sha256: f.Sha256})
		}
	}
	text, _ := json.Marshal(head)
	return Result{Text: string(text), Details: obj{"op": OpDesignHead, "id": a.ID, "version": v.Version}}, nil
}

// ---- fetch_design_runtime ----

// agentFetchDesignRuntime 是 fetch_design_runtime：读控制面 Web 构建里的页面运行时与探针，缺哪个就不返回哪个。
func (s *Service) agentFetchDesignRuntime(params json.RawMessage) (Result, error) {
	var p struct{}
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	out := orbsock.DesignRuntimeFiles{Files: []orbsock.DesignRuntimeFile{}}
	var names []string
	for _, f := range designRuntimeAssets {
		b, ok := webui.Asset(f.asset)
		if !ok {
			continue
		}
		out.Files = append(out.Files, orbsock.DesignRuntimeFile{Name: f.name, Data: base64.StdEncoding.EncodeToString(b)})
		names = append(names, f.name)
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(out)
	return Result{Text: strings.TrimSpace(buf.String()), Details: obj{"op": OpFetchDesignRuntime, "files": names}}, nil
}

// ---- export_design_project ----

var errExportTooLarge = errors.New("export project zip exceeds limit")

// limitedBuffer 在超过 limit 字节时立即返回 errExportTooLarge，避免无界占用内存。
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int64
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if int64(w.buf.Len())+int64(len(p)) > w.limit {
		return 0, errExportTooLarge
	}
	return w.buf.Write(p)
}

// agentExportDesignProject 是 export_design_project：对调用方可见的设计画布指定版本（缺省最新）调用 ExportProject，
// 将生成的工程 zip 按字节偏移分页返回；整条 artifact.call 结果序列化后不超过 orbsock.DesignFilesPageBytes。
func (s *Service) agentExportDesignProject(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p fetchFilesArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	if p.Version < 0 {
		return Result{}, opErr(CodeInvalid, "version 不能是负数；不给表示最新版本。")
	}
	var off int64
	if p.Cursor != "" {
		var err error
		off, err = strconv.ParseInt(p.Cursor, 10, 64)
		if err != nil || off < 0 {
			return Result{}, opErr(CodeInvalid, "cursor %q 不合法：原样传上一页返回的 next。", p.Cursor)
		}
	}
	a, err := s.lookup(ctx, c, p.ID, true)
	if err != nil {
		return Result{}, err
	}
	v, err := s.Version(ctx, a.ID, p.Version)
	if err != nil {
		return Result{}, err
	}
	exp, err := s.Export(ctx, a.ID, v.Version, ExportProject)
	if err != nil {
		return Result{}, err
	}
	lw := &limitedBuffer{limit: exportDesignProjectMaxBytes}
	if err := exp.Write(lw); err != nil {
		if errors.Is(err, errExportTooLarge) {
			return Result{}, opErr(CodeTooLarge, "artifact %s 版本 %d 导出的工程 zip 超过上限 %d MiB。", a.ID, v.Version, exportDesignProjectMaxBytes>>20)
		}
		return Result{}, err
	}
	raw := lw.buf.Bytes()
	total := int64(len(raw))
	if off > total || (off == total && (total > 0 || p.Cursor != "")) {
		return Result{}, opErr(CodeInvalid, "cursor %q 超出版本 %d 工程 zip 的范围（%d 字节）：从头（不带 cursor）重新取。", p.Cursor, v.Version, total)
	}
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	page, err := designProjectPage(a.ID, v.Version, raw, sha, off, orbsock.DesignFilesPageBytes)
	if err != nil {
		return Result{}, err
	}
	return designProjectResult(a.ID, page), nil
}

// designProjectPage 从 off 起切出不超过 limit 字节（外送 JSON 总长）的一页。base64 字符不含 JSON 转义字符，
// 在 Text 二次编码后按原长计，固定开销按 next 取最长时的空片实测。
func designProjectPage(id string, version int, raw []byte, sha string, off int64, limit int) (orbsock.DesignProjectPage, error) {
	total := int64(len(raw))
	longest := strconv.FormatInt(total, 10)
	fixed := designProjectResultBytes(id, orbsock.DesignProjectPage{
		Version: version, Size: total, Sha256: sha, Offset: off, Next: longest,
	})
	room := limit - fixed
	maxRaw := int64(max(room, 0) / 4 * 3)
	if maxRaw <= 0 && total > off {
		return orbsock.DesignProjectPage{}, opErr(CodeInvalid, "分页上限 %d 字节不足以容纳导出数据。", limit)
	}
	n := min(total-off, maxRaw)
	page := orbsock.DesignProjectPage{
		Version: version,
		Size:    total,
		Sha256:  sha,
		Offset:  off,
		Data:    base64.StdEncoding.EncodeToString(raw[off : off+n]),
	}
	if off+n < total {
		page.Next = strconv.FormatInt(off+n, 10)
	}
	return page, nil
}

// designProjectResult 是 export_design_project 的结果：Text 是页的 JSON，Details 只带元信息。
func designProjectResult(id string, page orbsock.DesignProjectPage) Result {
	b, _ := json.Marshal(page)
	d := obj{"op": OpExportDesignProject, "id": id, "version": page.Version, "offset": page.Offset, "size": page.Size}
	if page.Next != "" {
		d["next"] = page.Next
	}
	return Result{Text: string(b), Details: d}
}

// designProjectResultBytes 是 export_design_project 结果经 artifact.call 外送时的序列化大小。
func designProjectResultBytes(id string, page orbsock.DesignProjectPage) int {
	r := designProjectResult(id, page)
	d, _ := r.Details.(obj)
	b, _ := json.Marshal(apigen.LinkCtlArtifactCallResult{Text: r.Text, Details: d})
	return len(b)
}
