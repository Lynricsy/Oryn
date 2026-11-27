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
// 任何文件变过都算冲突），canvas.json 由服务端合并、不参与；按 dist/frames.json 与 src/variants.json 用
// canvas.ApplySeal 对齐画框与方案组（见 sealOps：新帧 add 并排位，name / width / height / device 只在新增时取用；
// 消失的帧 remove；方案组按清单 add、set、remove），整体经 writeCanvas 写成调用方回合下的一个版本。给了 check 时
// 写进新版本的 check_json（fingerprint = 新版本 src/ 的 SourceHash）。
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
	variantsRaw, err := s.sealVariantsJSON(ctx, c, a.ID, ps)
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
	has, err := s.sealPaths(ctx, a.ID, ps)
	if err != nil {
		return Result{}, err
	}
	if err := sealMissing(has, frames); err != nil {
		return Result{}, err
	}
	vs, err := parseSealVariants(variantsRaw, frames, has)
	if err != nil {
		return Result{}, err
	}
	ops, err := sealOps(doc, frames, vs)
	if err != nil {
		return Result{}, err
	}
	if err := canvas.ApplySeal(&doc, ops); err != nil {
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

// sealPaths 返回封存后版本里的路径：最新版本的文件叠加本次的写（true）与删（false）。
func (s *Service) sealPaths(ctx context.Context, id string, ps []*pending) (map[string]bool, error) {
	files, err := s.Files(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	has := make(map[string]bool, len(files)+len(ps))
	for _, f := range files {
		has[f.Path] = true
	}
	for _, p := range ps {
		has[p.path] = !p.remove
	}
	return has, nil
}

// sealMissing 检查每一帧的产物 dist/<id>.html 在封存后的版本里都在（否则 writeCanvas 会拿空白页顶上）。
func sealMissing(has map[string]bool, frames []sealFrame) error {
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

// sealVariantsJSON 取本次封存后的 src/variants.json：本次写了的取新内容（source 读进内存），没写的取最新版本里的；
// 本次删了或哪个版本都没有时返回 nil（空清单）。
func (s *Service) sealVariantsJSON(ctx context.Context, c Caller, id string, ps []*pending) ([]byte, error) {
	for _, p := range ps {
		if p.path != design.VariantsFile {
			continue
		}
		if p.remove {
			return nil, nil
		}
		if p.source != "" {
			b, err := fetchAll(ctx, c, p.source)
			if err != nil {
				return nil, err
			}
			p.data, p.source = b, ""
		}
		return nonNil(p.data), nil
	}
	b, _, err := s.ReadFile(ctx, id, 0, design.VariantsFile)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return nil, nil
	}
	return nonNil(b), err
}

// nonNil 把 nil 换成空切片：sealVariantsJSON 用 nil 表示文件不存在，空文件要照常交给解析报错。
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// parseSealVariants 按封存后的帧（dist/frames.json）与主题文件（src/themes/<名>.css）校验方案清单；raw 为 nil 是空清单。
func parseSealVariants(raw []byte, frames []sealFrame, has map[string]bool) (design.Variants, error) {
	if raw == nil {
		return design.Variants{}, nil
	}
	ids := make([]string, len(frames))
	for i, f := range frames {
		ids[i] = f.ID
	}
	var themes []string
	for p, ok := range has {
		name, under := strings.CutPrefix(p, "src/themes/")
		if name, css := strings.CutSuffix(name, ".css"); ok && under && css && !strings.Contains(name, "/") {
			themes = append(themes, name)
		}
	}
	vs, err := design.ParseVariants(raw, ids, themes)
	if err != nil {
		return design.Variants{}, opErr(CodeInvalid, "%s 不合法，%v。", design.VariantsFile, err)
	}
	return vs, nil
}

func frameSource(id string) string { return "src/frames/" + id + ".tsx" }

// sealOps 返回把 doc 对齐到本次封存的操作（frames 是 dist/frames.json，vs 是 src/variants.json），按顺序：
//  1. 带 source 的画框里不在 frames 的 remove（删除时级联把它们移出方案）；
//  2. frames 里还没有画框的 add，name / width / height / device 取自帧声明。属于方案的先加，位置由 placeVariants
//     给出；其余的后加，不给位置，接在第一个画布页现有画框的右边自动排位；
//  3. 方案组按清单同步（M34 C2，见 groupOps），引用的画框此时都已存在。
//
// 组 id 撞上画布页、标注或留下的画框时回 invalid；新画框的 id 避开所有组 id（组与画框共用 id 命名空间）。
func sealOps(doc canvas.Doc, frames []sealFrame, vs design.Variants) ([]canvas.Op, error) {
	want := make(map[string]bool, len(frames))
	for _, f := range frames {
		want[frameSource(f.ID)] = true
	}
	taken := map[string]string{} // id → 对象类型
	for _, c := range doc.Canvases {
		taken[c.ID] = "画布页"
	}
	for _, n := range doc.Notes {
		taken[n.ID] = "标注"
	}
	var ops []canvas.Op
	gone := map[string]bool{}
	pageOf := map[string]string{} // source → 画框 id（留下的与新加的）
	var layout []canvas.Page      // 留下的画框
	for _, p := range doc.Pages {
		if p.Source != "" && !want[p.Source] {
			ops = append(ops, canvas.Op{Op: canvas.OpRemove, ID: p.ID})
			gone[p.ID] = true
			continue
		}
		taken[p.ID] = "画框"
		layout = append(layout, p)
		if p.Source != "" {
			pageOf[p.Source] = p.ID
		}
	}
	for i, g := range vs.Groups {
		if kind := taken[g.ID]; kind != "" {
			return nil, opErr(CodeInvalid, "%s 不合法，groups[%d].id：方案组 id「%s」与画布上已有的%s同名，换一个。", design.VariantsFile, i, g.ID, kind)
		}
	}
	inVariant := map[string]bool{}
	for _, g := range doc.Groups {
		taken[g.ID] = "方案组"
	}
	for _, g := range vs.Groups {
		taken[g.ID] = "方案组"
		for _, v := range g.Variants {
			for _, f := range v.Frames {
				inVariant[f] = true
			}
		}
	}
	adds := map[string]map[string]any{} // 帧 id → 新画框的 add 对象
	var added []string                  // 新帧，按 frames 的顺序
	for _, f := range frames {
		src := frameSource(f.ID)
		if _, ok := pageOf[src]; ok {
			continue
		}
		id := f.ID
		for n := 2; taken[id] != ""; n++ {
			suffix := "-" + strconv.Itoa(n)
			id = f.ID[:min(len(f.ID), 64-len(suffix))] + suffix
		}
		taken[id] = "画框"
		pageOf[src] = id
		obj := map[string]any{
			"type": canvas.TypePage, "id": id, "canvas": doc.Canvases[0].ID, "name": frameName(f),
			"source": src, "width": frameSize(f.Width, canvas.DefaultPageWidth), "height": frameSize(f.Height, canvas.DefaultPageHeight),
		}
		if f.Device != "" && f.Device != canvas.DeviceNone && slices.Contains(canvas.Devices, f.Device) {
			obj["device"] = f.Device
		}
		adds[f.ID] = obj
		added = append(added, f.ID)
	}
	placeVariants(doc, layout, vs, pageOf, adds, gone)
	var rest []canvas.Op
	for _, f := range added {
		op := canvas.Op{Op: canvas.OpAdd, Object: adds[f]}
		if inVariant[f] {
			ops = append(ops, op)
		} else {
			rest = append(rest, op)
		}
	}
	ops = append(ops, rest...)
	return append(ops, groupOps(doc, vs, pageOf, gone)...), nil
}

// placeVariants 给新加进方案的帧排位（M34 C2），把 canvas、x、y 写进 adds 里对应的 add 对象：
//   - 方案里已有画框的，新帧接在该方案最下面那个画框的下方：同一画布页、左边对齐，间距 PageGap；
//   - 方案里全是新帧的，每个方案占一列：列宽取该方案最宽的帧，列间距 PageGap*2，列内按清单顺序从上往下、间距
//     PageGap。这些列接在组已有画框的右边（间距 PageGap*2，与其顶边对齐）；组在它的画布页上还没有画框时（新组），
//     放在该页现有画框的下方：左边缘与它们对齐，上边缘 = 最大下边缘 + PageGap*4（页上没有画框时从 0, 0 开始）。
//
// 组的画布页：还留在画布上的组取它的 canvas，其余取第一个画布页。layout 是留下的画框，排好的新帧依次加进去，
// 后面的组排在前面的组下方。
func placeVariants(doc canvas.Doc, layout []canvas.Page, vs design.Variants, pageOf map[string]string, adds map[string]map[string]any, gone map[string]bool) {
	at := make(map[string]canvas.Page, len(layout)) // 画框 id → 已有位置
	for _, p := range layout {
		at[p.ID] = p
	}
	put := func(frame, cv string, x, y float64) canvas.Page {
		obj := adds[frame]
		obj["canvas"], obj["x"], obj["y"] = cv, x, y
		p := canvas.Page{ID: obj["id"].(string), Canvas: cv, X: x, Y: y, Width: obj["width"].(float64), Height: obj["height"].(float64)}
		layout = append(layout, p)
		at[p.ID] = p
		return p
	}
	for _, g := range vs.Groups {
		cv := doc.Canvases[0].ID
		if i := slices.IndexFunc(doc.Groups, func(o canvas.Group) bool { return o.ID == g.ID }); i >= 0 && groupLeft(doc.Groups[i], gone) {
			cv = doc.Groups[i].Canvas
		}
		var cols [][]string
		for _, v := range g.Variants {
			var news []string
			var low *canvas.Page
			for _, f := range v.Frames {
				if _, ok := adds[f]; ok {
					news = append(news, f)
				} else if p := at[pageOf[frameSource(f)]]; low == nil || p.Y+p.Height > low.Y+low.Height {
					low = &p
				}
			}
			switch {
			case len(news) == 0:
			case low == nil:
				cols = append(cols, news)
			default:
				y := low.Y + low.Height
				for _, f := range news {
					y = put(f, low.Canvas, low.X, y+canvas.PageGap).Y + adds[f]["height"].(float64)
				}
			}
		}
		if len(cols) == 0 {
			continue
		}
		var x, y float64
		n := 0
		for _, v := range g.Variants {
			for _, f := range v.Frames {
				p, ok := at[pageOf[frameSource(f)]]
				if !ok || p.Canvas != cv {
					continue
				}
				if n == 0 || p.X+p.Width+canvas.PageGap*2 > x {
					x = p.X + p.Width + canvas.PageGap*2
				}
				if n == 0 || p.Y < y {
					y = p.Y
				}
				n++
			}
		}
		if n == 0 {
			for _, p := range layout {
				if p.Canvas != cv {
					continue
				}
				if n == 0 || p.X < x {
					x = p.X
				}
				if n == 0 || p.Y+p.Height+canvas.PageGap*4 > y {
					y = p.Y + p.Height + canvas.PageGap*4
				}
				n++
			}
		}
		for _, col := range cols {
			width, cy := 0.0, y
			for _, f := range col {
				p := put(f, cv, x, cy)
				width = max(width, p.Width)
				cy = p.Y + p.Height + canvas.PageGap
			}
			x += width + canvas.PageGap*2
		}
	}
}

// groupOps 是 sealOps 的第 3 步：清单里没有的组 remove；还在的组 set name 与 variants（canvas.ApplySeal 在 picked
// 指向的方案还在时保留 picked / pickedBy，否则两者置 null）；新的组 add 到第一个画布页。画框被删光、删除时已被级联
// 删掉的组按新组 add。picked 指向的方案还在清单里、却因画框被删光经级联丢了选定时，补回 picked 与 pickedBy。
// gone 是本次删掉的画框。
func groupOps(doc canvas.Doc, vs design.Variants, pageOf map[string]string, gone map[string]bool) []canvas.Op {
	var ops []canvas.Op
	for _, g := range doc.Groups {
		if !slices.ContainsFunc(vs.Groups, func(o design.VariantGroup) bool { return o.ID == g.ID }) {
			ops = append(ops, canvas.Op{Op: canvas.OpRemove, ID: g.ID})
		}
	}
	for _, g := range vs.Groups {
		variants := make([]any, len(g.Variants))
		for i, v := range g.Variants {
			pages := make([]any, len(v.Frames))
			for j, f := range v.Frames {
				pages[j] = pageOf[frameSource(f)]
			}
			variants[i] = map[string]any{"id": v.ID, "name": v.Name, "note": v.Note, "theme": v.Theme, "pages": pages}
		}
		i := slices.IndexFunc(doc.Groups, func(o canvas.Group) bool { return o.ID == g.ID })
		live := i >= 0 && groupLeft(doc.Groups[i], gone)
		if live {
			ops = append(ops,
				canvas.Op{Op: canvas.OpSet, ID: g.ID, Key: "name", Value: g.Name},
				canvas.Op{Op: canvas.OpSet, ID: g.ID, Key: "variants", Value: variants})
		} else {
			ops = append(ops, canvas.Op{Op: canvas.OpAdd, Object: map[string]any{
				"type": canvas.TypeGroup, "id": g.ID, "canvas": doc.Canvases[0].ID, "name": g.Name, "variants": variants,
			}})
		}
		if i < 0 || doc.Groups[i].Picked == nil {
			continue
		}
		old, picked := doc.Groups[i], *doc.Groups[i].Picked
		if !slices.ContainsFunc(g.Variants, func(v design.Variant) bool { return v.ID == picked }) {
			continue
		}
		if live && slices.ContainsFunc(old.Variants, func(v canvas.Variant) bool { return v.ID == picked && pagesLeft(v, gone) }) {
			continue
		}
		ops = append(ops, canvas.Op{Op: canvas.OpSet, ID: g.ID, Key: "picked", Value: picked})
		if old.PickedBy != nil {
			ops = append(ops, canvas.Op{Op: canvas.OpSet, ID: g.ID, Key: "pickedBy", Value: *old.PickedBy})
		}
	}
	return ops
}

// groupLeft 报告删掉 gone 里的画框之后组还在不在（级联：方案的画框删光就删方案，方案删光就删组）。
func groupLeft(g canvas.Group, gone map[string]bool) bool {
	return slices.ContainsFunc(g.Variants, func(v canvas.Variant) bool { return pagesLeft(v, gone) })
}

func pagesLeft(v canvas.Variant, gone map[string]bool) bool {
	return slices.ContainsFunc(v.Pages, func(p string) bool { return !gone[p] })
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
// （aesthetic = 已选定设计方向或记下了方案主题，surface 取 artifact_designs，与 ServeRender 注入的 window.__orynProbe 同口径）
// 与落选方案的帧（archivedFrameIDs）。只认设计画布与还没有画框的画布（design.open 接管时用它校验）。
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
	doc, err := canvas.Parse(b)
	if err != nil || (doc.Design == nil && len(doc.Pages) > 0) {
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
	head := orbsock.DesignHead{Version: v.Version, Check: v.Check, Files: []orbsock.DesignHeadFile{}, Archived: archivedFrameIDs(doc)}
	switch row, err := s.st.Q.GetArtifactDesign(ctx, a.ID); {
	case err == nil:
		head.Aesthetic, head.Surface = designAesthetic(row), deref(row.Surface)
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

// archivedFrameIDs 是 doc 里落选画框（canvas.Doc.Archived）经 source 换成的帧 id，按字典序；没有 source 的画框不计。
func archivedFrameIDs(doc canvas.Doc) []string {
	archived := doc.Archived()
	var out []string
	for _, p := range doc.Pages {
		if archived[p.ID] && p.Source != "" {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(p.Source, "src/frames/"), ".tsx"))
		}
	}
	slices.Sort(out)
	return out
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
