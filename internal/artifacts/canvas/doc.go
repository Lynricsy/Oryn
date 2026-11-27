// Package canvas 是画布 artifact 的文档模型：canvas.json 的严格解析、规范化输出与操作。
//
// 文档里 canvases 的元素在界面上叫「页面」，pages 的元素叫「画框」（每个画框是版本里的一个 HTML 文件），
// notes 是画在画框之上的标注。带顶层 design 的是设计画布：画框可以带 source（TSX 源码，file 是它的构建产物），
// design 与 source 只由封存（ApplySeal）维护。groups 是方案组：同一屏的几种方案并排比较、选定其一，
// 落选画框由 (Doc).Archived 推导。web/src/lib/artifacts/canvas-doc.ts 是逐条相同的 TS 实现，
// 两边共用 schema/testdata/canvas-ops.json 的测试向量；改规则时两边一起改。
package canvas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// 格式、上限与缺省值。
const (
	// Format 是只有 HTML 画框的画布写出的格式；带 design、source 或 device 任一字段时写出 FormatDesign，
	// 带 groups 时写出 FormatGroups。解析三种都接受，这样 HTML 画布的 canvas.json 逐字节不变，旧版本仍能读。
	Format       = 1
	FormatDesign = 2
	FormatGroups = 3
	DesignEngine = 1 // design.engine 目前唯一的取值

	MaxCanvases   = 50
	MaxPages      = 500
	MaxNotes      = 5000
	MaxPenNumbers = 30000   // 单笔的数字个数（每点 x、y、压力三个）
	MaxJSONBytes  = 5 << 20 // 整个 canvas.json
	MaxOps        = 2000    // 单次操作条数
	MaxGroups     = 20      // 方案组个数

	// 每组的方案数：add 与 set variants 时要求 MinVariants–MaxVariants；级联删除后可以只剩一个。
	MinVariants     = 2
	MaxVariants     = 4
	MaxVariantPages = 24 // 每个方案的画框数
	MaxGroupName    = 60 // 方案组与方案的名字字数
	MaxVariantNote  = 200

	MaxNameRunes     = 120
	MaxNoteTextRunes = 4000
	MaxCoord         = 1_000_000

	MinPageSize       = 64
	MaxPageSize       = 8000
	DefaultPageWidth  = 1280
	DefaultPageHeight = 800
	PageGap           = 48 // 自动排位时画框之间的间距

	MinStroke     = 0.5
	MaxStroke     = 64
	DefaultStroke = 3

	MinTextSize     = 8
	MaxTextSize     = 400
	DefaultTextSize = 24

	MinStickyWidth     = 80
	MaxStickyWidth     = 2000
	DefaultStickyWidth = 240

	maxAuthorRunes = 200
	maxFileBytes   = 512

	// DefaultCanvasID / DefaultCanvasName 是文档没写 canvases 时补上的唯一画布页。
	DefaultCanvasID   = "canvas-1"
	DefaultCanvasName = "页面 1"

	// AlternatesID / AlternatesName 是「备选方案」画布页：组选定后落选画框由 ExpandPicks 搬到这里。
	AlternatesID   = "alternates"
	AlternatesName = "备选方案"
)

// 标注种类。
const (
	KindText    = "text"
	KindSticky  = "sticky"
	KindPen     = "pen"
	KindRect    = "rect"
	KindEllipse = "ellipse"
	KindLine    = "line"
	KindArrow   = "arrow"
)

// NoteKinds 是全部标注种类。
var NoteKinds = []string{KindText, KindSticky, KindPen, KindRect, KindEllipse, KindLine, KindArrow}

// Colors 是标注可用的颜色名（界面按主题映射成具体颜色）。
var Colors = []string{"ink", "red", "orange", "yellow", "green", "blue", "violet"}

// 画框的设备外壳。
const (
	DeviceNone    = "none"
	DevicePhone   = "phone"
	DeviceTablet  = "tablet"
	DeviceBrowser = "browser"
)

// Devices 是 device 的全部取值。
var Devices = []string{DeviceNone, DevicePhone, DeviceTablet, DeviceBrowser}

// Canvas 是一个画布页。
type Canvas struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Page 是一个画框：版本里的一个 HTML 文件摆在画布页上的位置。
// 设计画布的画框带 Source（src/frames/<名>.tsx），File 固定是它的产物 dist/<名>.html。
type Page struct {
	ID     string  `json:"id"`
	Canvas string  `json:"canvas"`
	Name   string  `json:"name"`
	File   string  `json:"file"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Locked bool    `json:"locked"`
	Source string  `json:"source,omitempty"`
	Device string  `json:"device,omitempty"` // 设备外壳；空串即 none（规范化输出不写 none）
}

// Design 是设计画布的顶层信息：引擎版本与原型的起始画框（nil 表示未指定）。
type Design struct {
	Engine int     `json:"engine"`
	Start  *string `json:"start"`
}

// Variant 是方案组里的一个方案：一组画框，可以带一套方案主题（theme 为空表示没有独立主题）。
type Variant struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Note  string   `json:"note"`
	Theme string   `json:"theme"`
	Pages []string `json:"pages"`
}

// Group 是一个方案组：Canvas 是它所在的画布页（落选画框改选回来时回到这里）；
// Picked 是选定的方案 id（nil 为未选定），PickedBy 是选定者的线程 id（用户选的为 nil，由服务端盖章）。
type Group struct {
	ID       string    `json:"id"`
	Canvas   string    `json:"canvas"`
	Name     string    `json:"name"`
	Variants []Variant `json:"variants"`
	Picked   *string   `json:"picked"`
	PickedBy *string   `json:"pickedBy"`
}

// FrameFile 返回 source 对应的产物文件：src/frames/<名>.tsx → dist/<名>.html。
func FrameFile(source string) string {
	return "dist/" + strings.TrimSuffix(strings.TrimPrefix(source, "src/frames/"), ".tsx") + ".html"
}

// Note 是一个标注。哪些字段有意义由 Kind 决定：
// text 用 X、Y、Text、Size；sticky 用 X、Y、Width、Text、Done；pen 用 Points、Width、Marker；
// rect / ellipse / line / arrow 用 X1、Y1、X2、Y2、Width、From、To。
type Note struct {
	ID     string
	Canvas string
	Kind   string
	Color  string
	Locked bool
	By     *string // 画它的线程 id；用户画的为 nil

	X, Y   float64
	Text   string
	Size   float64
	Width  float64 // sticky 的宽度，或 pen / 形状的线宽
	Done   bool
	Points []float64 // x、y、压力 三个一组
	Marker bool

	X1, Y1, X2, Y2 float64
	From, To       *string // 吸附的画框 id
}

// IsShape 报告标注是否是 rect / ellipse / line / arrow（有 From / To）。
func (n Note) IsShape() bool { return isShape(n.Kind) }

func isShape(kind string) bool {
	return kind == KindRect || kind == KindEllipse || kind == KindLine || kind == KindArrow
}

type noteBase struct {
	ID     string  `json:"id"`
	Canvas string  `json:"canvas"`
	Kind   string  `json:"kind"`
	Color  string  `json:"color"`
	Locked bool    `json:"locked"`
	By     *string `json:"by"`
}

// MarshalJSON 只写出该种类的字段，顺序固定。
func (n Note) MarshalJSON() ([]byte, error) {
	b := noteBase{n.ID, n.Canvas, n.Kind, n.Color, n.Locked, n.By}
	switch n.Kind {
	case KindText:
		return json.Marshal(struct {
			noteBase
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
			Text string  `json:"text"`
			Size float64 `json:"size"`
		}{b, n.X, n.Y, n.Text, n.Size})
	case KindSticky:
		return json.Marshal(struct {
			noteBase
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
			Width float64 `json:"width"`
			Text  string  `json:"text"`
			Done  bool    `json:"done"`
		}{b, n.X, n.Y, n.Width, n.Text, n.Done})
	case KindPen:
		pts := n.Points
		if pts == nil {
			pts = []float64{}
		}
		return json.Marshal(struct {
			noteBase
			Points []float64 `json:"points"`
			Width  float64   `json:"width"`
			Marker bool      `json:"marker"`
		}{b, pts, n.Width, n.Marker})
	default:
		return json.Marshal(struct {
			noteBase
			X1    float64 `json:"x1"`
			Y1    float64 `json:"y1"`
			X2    float64 `json:"x2"`
			Y2    float64 `json:"y2"`
			Width float64 `json:"width"`
			From  *string `json:"from"`
			To    *string `json:"to"`
		}{b, n.X1, n.Y1, n.X2, n.Y2, n.Width, n.From, n.To})
	}
}

// Doc 是解析后的 canvas.json。数组顺序就是绘制顺序（先画的在下面）。
// Design 非 nil 即设计画布。Format 由内容决定（见 Format 常量），JSON 写出时重新计算。Groups 为空时不写出。
type Doc struct {
	Format   int      `json:"format"`
	Design   *Design  `json:"design,omitempty"`
	Canvases []Canvas `json:"canvases"`
	Pages    []Page   `json:"pages"`
	Notes    []Note   `json:"notes"`
	Groups   []Group  `json:"groups,omitempty"`
}

// format 返回文档该写出的格式：带 groups 时是 FormatGroups；带 design、source 或 device 任一字段时是 FormatDesign。
func (d Doc) format() int {
	switch {
	case len(d.Groups) > 0:
		return FormatGroups
	case d.Design != nil || slices.ContainsFunc(d.Pages, func(p Page) bool { return p.Source != "" || p.Device != "" }):
		return FormatDesign
	}
	return Format
}

// Archived 返回落选画框：已选定的组里其它方案的画框。只由 groups 推导，与画框当前在哪个画布页无关。
func (d Doc) Archived() map[string]bool {
	out := map[string]bool{}
	for _, g := range d.Groups {
		if g.Picked == nil {
			continue
		}
		for _, v := range g.Variants {
			if v.ID == *g.Picked {
				continue
			}
			for _, p := range v.Pages {
				out[p] = true
			}
		}
	}
	return out
}

// JSON 是规范化的 canvas.json：键序固定、两空格缩进、末尾换行，便于逐版本比较。
func (d Doc) JSON() []byte {
	out := Doc{Format: d.format(), Design: d.Design, Canvases: d.Canvases, Pages: d.Pages, Notes: d.Notes, Groups: d.Groups}
	if out.Canvases == nil {
		out.Canvases = []Canvas{}
	}
	if out.Pages == nil {
		out.Pages = []Page{}
	}
	if out.Notes == nil {
		out.Notes = []Note{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err) // 只有基本类型，编码不会失败
	}
	return buf.Bytes()
}

// PageByID 返回画框（找不到时 ok 为假）。
func (d Doc) PageByID(id string) (Page, bool) {
	for _, p := range d.Pages {
		if p.ID == id {
			return p, true
		}
	}
	return Page{}, false
}

// Error 是文档或操作不合法的原因；Where 是出错的位置，如 pages[2].canvas。
type Error struct {
	Where, Msg string
}

func (e *Error) Error() string {
	if e.Where == "" {
		return e.Msg
	}
	return e.Where + "：" + e.Msg
}

// Parse 严格解析 canvas.json：未知字段、类型不对、越界、悬空引用、重复 id 都报错。
// 只有一个画布页时画框与标注可以省略 canvas；没写 canvases 时补一个缺省画布页；
// 没写位置（x、y 都省略）的画框在所属画布页里接着已有画框排成一行。
func Parse(b []byte) (Doc, error) {
	if len(b) > MaxJSONBytes {
		return Doc{}, &Error{Msg: fmt.Sprintf("canvas.json 超过 %d MiB", MaxJSONBytes>>20)}
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return Doc{}, &Error{Msg: "canvas.json 不是合法的 JSON：" + err.Error()}
	}
	r := &reader{}
	d := r.doc(raw)
	if r.err != nil {
		return Doc{}, r.err
	}
	return d, nil
}

// MissingPageFiles 返回画框引用了、但 has 报告版本里没有的文件（去重，按首次出现的顺序）。
func MissingPageFiles(d Doc, has func(string) bool) []string {
	var out []string
	for _, p := range d.Pages {
		if !has(p.File) && !slices.Contains(out, p.File) {
			out = append(out, p.File)
		}
	}
	return out
}

// round2 保留两位小数，取整规则与 JS 的 Math.round 相同（.5 向正无穷），结果不出现 -0。
func round2(v float64) float64 { return jsRound(v*100) / 100 }

func jsRound(v float64) float64 {
	r := math.Floor(v)
	if v-r >= 0.5 {
		r++
	}
	return r + 0 // -0 → 0
}

// reader 逐字段读取解码后的 JSON 值，只记第一个错误；出错之后的读取都返回零值。
// seal 为真时是封存在改文档（ApplySeal），可以写画框的 source。
type reader struct {
	err  error
	seal bool
}

func (r *reader) fail(where, format string, args ...any) {
	if r.err == nil {
		r.err = &Error{Where: where, Msg: fmt.Sprintf(format, args...)}
	}
}

func at(where, key string) string {
	if where == "" {
		return key
	}
	return where + "." + key
}

func (r *reader) object(v any, where string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		r.fail(where, "必须是对象")
		return nil
	}
	return m
}

// only 拒绝 keys 之外的字段（按字段名排序报第一个，两边实现报同一个）。
func (r *reader) only(m map[string]any, keys []string, where string) {
	var bad []string
	for k := range m {
		if !slices.Contains(keys, k) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		r.fail(at(where, bad[0]), "未知字段（只允许 %s）", strings.Join(keys, "、"))
	}
}

// need 取必填字段。
func (r *reader) need(m map[string]any, key, where string) any {
	v, ok := m[key]
	if !ok {
		r.fail(at(where, key), "缺少这个字段")
	}
	return v
}

func (r *reader) id(v any, where string) string {
	s, ok := v.(string)
	if !ok || !validID(s) {
		r.fail(where, "id 必须是 1–64 个字母、数字、- 或 _")
		return ""
	}
	return s
}

func validID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (r *reader) text(v any, where string, max int, blankOK bool) string {
	s, ok := v.(string)
	if !ok {
		r.fail(where, "必须是字符串")
		return ""
	}
	if !blankOK && strings.TrimSpace(s) == "" {
		r.fail(where, "不能为空")
	}
	if n := utf8.RuneCountInString(s); n > max {
		r.fail(where, "不能超过 %d 个字符（现在 %d 个）", max, n)
	}
	return s
}

func (r *reader) num(v any, where string, lo, hi float64) float64 {
	n, ok := v.(float64)
	if !ok {
		r.fail(where, "必须是数字")
		return 0
	}
	if n < lo || n > hi {
		r.fail(where, "必须在 %v 到 %v 之间（现在是 %v）", lo, hi, n)
		return 0
	}
	return n
}

func (r *reader) coord(v any, where string) float64 {
	return round2(r.num(v, where, -MaxCoord, MaxCoord))
}

func (r *reader) boolean(v any, where string) bool {
	b, ok := v.(bool)
	if !ok {
		r.fail(where, "必须是 true 或 false")
	}
	return b
}

// ref 读可为 null 的 id。
func (r *reader) ref(v any, where string) *string {
	if v == nil {
		return nil
	}
	s := r.id(v, where)
	return &s
}

func (r *reader) author(v any, where string) *string {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" || utf8.RuneCountInString(s) > maxAuthorRunes {
		r.fail(where, "必须是线程 id 或 null")
		return nil
	}
	return &s
}

// pageFile 校验画框的文件：artifact 里的相对路径，扩展名 .html / .htm，不以 _oryn 开头。
// 文件是否真的在版本里由 MissingPageFiles 检查。
func (r *reader) pageFile(v any, where string) string {
	s, ok := v.(string)
	if !ok {
		r.fail(where, "必须是字符串")
		return ""
	}
	low := strings.ToLower(s)
	bad := s == "" || len(s) > maxFileBytes || strings.HasPrefix(s, "/") || strings.Contains(s, `\`) ||
		strings.HasPrefix(s, "_oryn") || !(strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".htm"))
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			bad = true
		}
	}
	if bad {
		r.fail(where, "必须是这个 artifact 里一个 .html / .htm 文件的相对路径，且不以 _oryn 开头（现在是 %q）", s)
	}
	return s
}

// sourcePattern 是设计画框的源码路径：src/frames/<名>.tsx。
var sourcePattern = regexp.MustCompile(`^src/frames/[a-z0-9][a-z0-9-]{0,63}\.tsx$`)

func (r *reader) source(v any, where string) string {
	s, ok := v.(string)
	if !ok || !sourcePattern.MatchString(s) {
		r.fail(where, "必须是 src/frames/<名>.tsx，名字是 1–64 个小写字母、数字或 -，以字母或数字开头")
		return ""
	}
	return s
}

// device 读设备外壳；none 存成空串，规范化输出时不写。
func (r *reader) device(v any, where string) string {
	s, ok := v.(string)
	if !ok || !slices.Contains(Devices, s) {
		r.fail(where, "只能是 %s 之一", strings.Join(Devices, "、"))
		return ""
	}
	if s == DeviceNone {
		return ""
	}
	return s
}

func (r *reader) design(v any, where string) *Design {
	m := r.object(v, where)
	if m == nil {
		return nil
	}
	r.only(m, designKeys, where)
	if e := r.need(m, "engine", where); e != float64(DesignEngine) {
		r.fail(at(where, "engine"), "必须是 %d", DesignEngine)
	}
	return &Design{Engine: DesignEngine, Start: opt(r, m, "start", where, nil, r.ref)}
}

// opt 返回可选字段：不存在时用缺省值。
func opt[T any](r *reader, m map[string]any, key, where string, def T, read func(any, string) T) T {
	v, ok := m[key]
	if !ok {
		return def
	}
	return read(v, at(where, key))
}

func (r *reader) list(m map[string]any, key string, max int) []any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	a, ok := v.([]any)
	if !ok {
		r.fail(key, "必须是数组")
		return nil
	}
	if len(a) > max {
		r.fail(key, "最多 %d 个（现在 %d 个）", max, len(a))
		return nil
	}
	return a
}

var (
	docKeys     = []string{"format", "design", "canvases", "pages", "notes", "groups"}
	groupKeys   = []string{"id", "canvas", "name", "variants", "picked", "pickedBy"}
	variantKeys = []string{"id", "name", "note", "theme", "pages"}
	designKeys  = []string{"engine", "start"}
	canvasKeys  = []string{"id", "name"}
	pageKeys    = []string{"id", "canvas", "name", "file", "x", "y", "width", "height", "locked", "source", "device"}
	noteCommon  = []string{"id", "canvas", "kind", "color", "locked", "by"}
	shapeKeys   = append(slices.Clone(noteCommon), "x1", "y1", "x2", "y2", "width", "from", "to")
	noteKeys    = map[string][]string{
		KindText:    append(slices.Clone(noteCommon), "x", "y", "text", "size"),
		KindSticky:  append(slices.Clone(noteCommon), "x", "y", "width", "text", "done"),
		KindPen:     append(slices.Clone(noteCommon), "points", "width", "marker"),
		KindRect:    shapeKeys,
		KindEllipse: shapeKeys,
		KindLine:    shapeKeys,
		KindArrow:   shapeKeys,
	}
)

func (r *reader) doc(raw any) Doc {
	m, ok := raw.(map[string]any)
	if !ok {
		r.fail("", "canvas.json 必须是一个 JSON 对象")
		return Doc{}
	}
	r.only(m, docKeys, "")
	if f, ok := m["format"]; ok && f != float64(Format) && f != float64(FormatDesign) && f != float64(FormatGroups) {
		r.fail("format", "必须是 %d、%d 或 %d", Format, FormatDesign, FormatGroups)
	}
	design := opt(r, m, "design", "", nil, r.design)
	var canvases []Canvas
	for i, v := range r.list(m, "canvases", MaxCanvases) {
		canvases = append(canvases, r.canvas(v, fmt.Sprintf("canvases[%d]", i)))
	}
	if len(canvases) == 0 {
		canvases = []Canvas{{ID: DefaultCanvasID, Name: DefaultCanvasName}}
	}
	sole := ""
	if len(canvases) == 1 {
		sole = canvases[0].ID
	}
	var pages []loosePage
	for i, v := range r.list(m, "pages", MaxPages) {
		pages = append(pages, r.page(withCanvas(v, sole), fmt.Sprintf("pages[%d]", i)))
	}
	var notes []Note
	for i, v := range r.list(m, "notes", MaxNotes) {
		notes = append(notes, r.note(withCanvas(v, sole), fmt.Sprintf("notes[%d]", i)))
	}
	if r.err != nil {
		return Doc{}
	}
	placed := place(pages)
	var groups []Group
	for i, v := range r.list(m, "groups", MaxGroups) {
		groups = append(groups, r.group(withCanvas(v, sole), fmt.Sprintf("groups[%d]", i), placed, false))
	}
	if r.err != nil {
		return Doc{}
	}
	d := Doc{Design: design, Canvases: canvases, Pages: placed, Notes: notes, Groups: groups}
	d.Format = d.format()
	r.links(d)
	return d
}

// withCanvas 在只有一个画布页时给省略了 canvas 的对象补上它。
func withCanvas(v any, sole string) any {
	m, ok := v.(map[string]any)
	if !ok || sole == "" {
		return v
	}
	if _, has := m["canvas"]; has {
		return v
	}
	out := make(map[string]any, len(m)+1)
	for k, x := range m {
		out[k] = x
	}
	out["canvas"] = sole
	return out
}

func (r *reader) canvas(v any, where string) Canvas {
	m := r.object(v, where)
	if m == nil {
		return Canvas{}
	}
	r.only(m, canvasKeys, where)
	return Canvas{
		ID:   r.id(r.need(m, "id", where), at(where, "id")),
		Name: r.text(r.need(m, "name", where), at(where, "name"), MaxNameRunes, false),
	}
}

// GroupIDPattern 是方案组、方案与方案主题的 id。
var GroupIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (r *reader) groupID(v any, where string) string {
	s, ok := v.(string)
	if !ok || !GroupIDPattern.MatchString(s) {
		r.fail(where, "必须是 1–32 个小写字母、数字或 -，以字母或数字开头")
		return ""
	}
	return s
}

// theme 读方案主题名：空串表示没有独立主题。
func (r *reader) theme(v any, where string) string {
	if s, ok := v.(string); ok && s == "" {
		return ""
	}
	return r.groupID(v, where)
}

// group 读一个方案组。strict 为真时（add 与 set variants）要求 MinVariants–MaxVariants 个方案，
// 否则 1–MaxVariants 个（级联删除后可以只剩一个）。省略 canvas 时取第一个方案第一个画框所在的画布页。
// 画框是否存在、是否只属于一个方案由 links 检查。
func (r *reader) group(v any, where string, pages []Page, strict bool) Group {
	m := r.object(v, where)
	if m == nil {
		return Group{}
	}
	r.only(m, groupKeys, where)
	g := Group{
		ID:       r.groupID(r.need(m, "id", where), at(where, "id")),
		Name:     r.text(r.need(m, "name", where), at(where, "name"), MaxGroupName, false),
		Picked:   opt(r, m, "picked", where, nil, r.ref),
		PickedBy: opt(r, m, "pickedBy", where, nil, r.author),
	}
	vw := at(where, "variants")
	a, ok := r.need(m, "variants", where).([]any)
	lo := 1
	if strict {
		lo = MinVariants
	}
	switch {
	case !ok:
		r.fail(vw, "必须是数组")
	case len(a) < lo || len(a) > MaxVariants:
		r.fail(vw, "每组 %d–%d 个方案（现在 %d 个）", lo, MaxVariants, len(a))
	}
	if r.err != nil {
		return Group{}
	}
	for i, x := range a {
		w := fmt.Sprintf("%s[%d]", vw, i)
		vr := r.variant(x, w)
		if slices.ContainsFunc(g.Variants, func(o Variant) bool { return o.ID == vr.ID }) {
			r.fail(at(w, "id"), "方案 id %q 在本组里重复", vr.ID)
		}
		g.Variants = append(g.Variants, vr)
	}
	if r.err != nil {
		return Group{}
	}
	if g.Picked != nil && !slices.ContainsFunc(g.Variants, func(o Variant) bool { return o.ID == *g.Picked }) {
		r.fail(at(where, "picked"), "本组没有 id 为 %q 的方案", *g.Picked)
	}
	if c, has := m["canvas"]; has {
		g.Canvas = r.id(c, at(where, "canvas"))
	} else if i := slices.IndexFunc(pages, func(p Page) bool { return p.ID == g.Variants[0].Pages[0] }); i >= 0 {
		g.Canvas = pages[i].Canvas
	} else {
		r.fail(at(where, "canvas"), "缺少这个字段")
	}
	return g
}

func (r *reader) variant(v any, where string) Variant {
	m := r.object(v, where)
	if m == nil {
		return Variant{}
	}
	r.only(m, variantKeys, where)
	vr := Variant{
		ID:   r.groupID(r.need(m, "id", where), at(where, "id")),
		Name: r.text(r.need(m, "name", where), at(where, "name"), MaxGroupName, false),
		Note: opt(r, m, "note", where, "", func(v any, w string) string {
			return r.text(v, w, MaxVariantNote, true)
		}),
		Theme: opt(r, m, "theme", where, "", r.theme),
	}
	pw := at(where, "pages")
	a, ok := r.need(m, "pages", where).([]any)
	switch {
	case !ok:
		r.fail(pw, "必须是画框 id 的数组")
	case len(a) < 1 || len(a) > MaxVariantPages:
		r.fail(pw, "每个方案 1–%d 个画框（现在 %d 个）", MaxVariantPages, len(a))
	}
	if r.err != nil {
		return Variant{}
	}
	vr.Pages = make([]string, 0, len(a))
	for i, x := range a {
		vr.Pages = append(vr.Pages, r.id(x, fmt.Sprintf("%s[%d]", pw, i)))
	}
	return vr
}

// loosePage 是可能还没有位置的画框。
type loosePage struct {
	Page
	placed bool
}

func (r *reader) size(v any, where string) float64 {
	return jsRound(r.num(v, where, MinPageSize, MaxPageSize))
}

func (r *reader) page(v any, where string) loosePage {
	m := r.object(v, where)
	if m == nil {
		return loosePage{}
	}
	r.only(m, pageKeys, where)
	p := Page{
		ID:     r.id(r.need(m, "id", where), at(where, "id")),
		Canvas: r.id(r.need(m, "canvas", where), at(where, "canvas")),
		Name:   r.text(r.need(m, "name", where), at(where, "name"), MaxNameRunes, false),
		File:   r.pageFile(r.need(m, "file", where), at(where, "file")),
		Width:  opt(r, m, "width", where, DefaultPageWidth, r.size),
		Height: opt(r, m, "height", where, DefaultPageHeight, r.size),
		Locked: opt(r, m, "locked", where, false, r.boolean),
		Source: opt(r, m, "source", where, "", r.source),
		Device: opt(r, m, "device", where, "", r.device),
	}
	_, hasX := m["x"]
	_, hasY := m["y"]
	switch {
	case hasX && hasY:
		p.X = r.coord(m["x"], at(where, "x"))
		p.Y = r.coord(m["y"], at(where, "y"))
	case hasX:
		r.fail(at(where, "y"), "x 与 y 要么都给，要么都省略（省略时自动排位）")
	case hasY:
		r.fail(at(where, "x"), "x 与 y 要么都给，要么都省略（省略时自动排位）")
	}
	return loosePage{Page: p, placed: hasX && hasY}
}

func (r *reader) color(v any, where string) string {
	s, ok := v.(string)
	if !ok || !slices.Contains(Colors, s) {
		r.fail(where, "颜色只能是 %s 之一", strings.Join(Colors, "、"))
		return ""
	}
	return s
}

func (r *reader) stroke(v any, where string) float64 {
	return round2(r.num(v, where, MinStroke, MaxStroke))
}

func (r *reader) note(v any, where string) Note {
	m := r.object(v, where)
	if m == nil {
		return Note{}
	}
	kind, _ := r.need(m, "kind", where).(string)
	keys, ok := noteKeys[kind]
	if !ok {
		r.fail(at(where, "kind"), "只能是 %s 之一", strings.Join(NoteKinds, "、"))
		return Note{}
	}
	r.only(m, keys, where)
	defColor := "ink"
	if kind == KindSticky {
		defColor = "yellow"
	}
	n := Note{
		ID:     r.id(r.need(m, "id", where), at(where, "id")),
		Canvas: r.id(r.need(m, "canvas", where), at(where, "canvas")),
		Kind:   kind,
		Color:  opt(r, m, "color", where, defColor, r.color),
		Locked: opt(r, m, "locked", where, false, r.boolean),
		By:     opt(r, m, "by", where, nil, r.author),
	}
	switch kind {
	case KindText:
		n.X = r.coord(r.need(m, "x", where), at(where, "x"))
		n.Y = r.coord(r.need(m, "y", where), at(where, "y"))
		n.Text = r.text(r.need(m, "text", where), at(where, "text"), MaxNoteTextRunes, false)
		n.Size = opt(r, m, "size", where, DefaultTextSize, func(v any, w string) float64 {
			return round2(r.num(v, w, MinTextSize, MaxTextSize))
		})
	case KindSticky:
		n.X = r.coord(r.need(m, "x", where), at(where, "x"))
		n.Y = r.coord(r.need(m, "y", where), at(where, "y"))
		n.Width = opt(r, m, "width", where, DefaultStickyWidth, func(v any, w string) float64 {
			return round2(r.num(v, w, MinStickyWidth, MaxStickyWidth))
		})
		n.Text = opt(r, m, "text", where, "", func(v any, w string) string {
			return r.text(v, w, MaxNoteTextRunes, true)
		})
		n.Done = opt(r, m, "done", where, false, r.boolean)
	case KindPen:
		n.Points = r.points(r.need(m, "points", where), at(where, "points"))
		n.Width = opt(r, m, "width", where, DefaultStroke, r.stroke)
		n.Marker = opt(r, m, "marker", where, false, r.boolean)
	default:
		n.X1 = r.coord(r.need(m, "x1", where), at(where, "x1"))
		n.Y1 = r.coord(r.need(m, "y1", where), at(where, "y1"))
		n.X2 = r.coord(r.need(m, "x2", where), at(where, "x2"))
		n.Y2 = r.coord(r.need(m, "y2", where), at(where, "y2"))
		n.Width = opt(r, m, "width", where, DefaultStroke, r.stroke)
		n.From = opt(r, m, "from", where, nil, r.ref)
		n.To = opt(r, m, "to", where, nil, r.ref)
	}
	return n
}

func (r *reader) points(v any, where string) []float64 {
	a, ok := v.([]any)
	if !ok || len(a) < 3 || len(a)%3 != 0 {
		r.fail(where, "必须是数字数组，每个点 x、y、压力三个一组，至少一个点")
		return nil
	}
	if len(a) > MaxPenNumbers {
		r.fail(where, "一笔最多 %d 个数（现在 %d 个）", MaxPenNumbers, len(a))
		return nil
	}
	out := make([]float64, len(a))
	for i, x := range a {
		w := fmt.Sprintf("%s[%d]", where, i)
		if i%3 == 2 {
			out[i] = round2(r.num(x, w, 0, 1))
		} else {
			out[i] = r.coord(x, w)
		}
		if r.err != nil {
			return nil
		}
	}
	return out
}

// links 检查 id 不重复（画布页、画框、标注、方案组共用一套 id）、画框与标注都在存在的画布页上、
// 形状吸附的是同一画布页上存在的画框、方案组在存在的画布页上且方案里的画框存在并只属于一个方案；
// 设计画布另查 source 只出现在设计画布里、file 与 source 配套、design.start 指向存在的画框。
func (r *reader) links(d Doc) {
	kinds := make(map[string]string, len(d.Canvases)+len(d.Pages)+len(d.Notes)+len(d.Groups))
	claim := func(id, kind, where string) {
		if _, dup := kinds[id]; dup {
			r.fail(where, "id %q 已被另一个对象使用", id)
			return
		}
		kinds[id] = kind
	}
	for i, c := range d.Canvases {
		claim(c.ID, "canvas", fmt.Sprintf("canvases[%d].id", i))
	}
	pageCanvas := make(map[string]string, len(d.Pages))
	for i, p := range d.Pages {
		claim(p.ID, "page", fmt.Sprintf("pages[%d].id", i))
		pageCanvas[p.ID] = p.Canvas
	}
	for i, n := range d.Notes {
		claim(n.ID, "note", fmt.Sprintf("notes[%d].id", i))
	}
	for i, g := range d.Groups {
		claim(g.ID, "group", fmt.Sprintf("groups[%d].id", i))
	}
	for i, p := range d.Pages {
		where := fmt.Sprintf("pages[%d]", i)
		if kinds[p.Canvas] != "canvas" {
			r.fail(where+".canvas", "没有 id 为 %q 的画布页", p.Canvas)
		}
		if p.Source == "" {
			continue
		}
		if d.Design == nil {
			r.fail(where+".source", "只有设计画布（带 design）的画框能有 source")
		} else if want := FrameFile(p.Source); p.File != want {
			r.fail(where+".file", "带 source 的画框的 file 必须是 %q（现在是 %q）", want, p.File)
		}
	}
	for i, n := range d.Notes {
		where := fmt.Sprintf("notes[%d]", i)
		if kinds[n.Canvas] != "canvas" {
			r.fail(where+".canvas", "没有 id 为 %q 的画布页", n.Canvas)
		}
		for _, end := range []struct {
			key string
			id  *string
		}{{"from", n.From}, {"to", n.To}} {
			if end.id == nil {
				continue
			}
			if c, ok := pageCanvas[*end.id]; !ok || c != n.Canvas {
				r.fail(where+"."+end.key, "同一画布页上没有 id 为 %q 的画框", *end.id)
			}
		}
	}
	if d.Design != nil && d.Design.Start != nil && kinds[*d.Design.Start] != "page" {
		r.fail("design.start", "没有 id 为 %q 的画框", *d.Design.Start)
	}
	owner := map[string]string{}
	for i, g := range d.Groups {
		where := fmt.Sprintf("groups[%d]", i)
		if kinds[g.Canvas] != "canvas" {
			r.fail(where+".canvas", "没有 id 为 %q 的画布页", g.Canvas)
		}
		for j, v := range g.Variants {
			for k, p := range v.Pages {
				w := fmt.Sprintf("%s.variants[%d].pages[%d]", where, j, k)
				if kinds[p] != "page" {
					r.fail(w, "没有 id 为 %q 的画框", p)
				} else if o, dup := owner[p]; dup {
					r.fail(w, "画框 %q 已属于方案 %s，一个画框最多属于一个方案", p, o)
				}
				owner[p] = g.ID + "/" + v.ID
			}
		}
	}
}

// place 给没有位置的画框排位：按出现顺序接在同一画布页已有画框的右边（间距 PageGap，顶边对齐）；
// 画布页上还没有画框时放在原点。
func place(pages []loosePage) []Page {
	out := make([]Page, len(pages))
	placed := make([]Page, 0, len(pages))
	for i, p := range pages {
		out[i] = p.Page
		if p.placed {
			placed = append(placed, p.Page)
		}
	}
	for i, p := range pages {
		if p.placed {
			continue
		}
		out[i].X, out[i].Y = nextSlot(placed, p.Canvas)
		placed = append(placed, out[i])
	}
	return out
}

// nextSlot 返回 canvas 上 pages 之后的下一个位置。
func nextSlot(pages []Page, canvas string) (x, y float64) {
	found := false
	var right, top float64
	for _, p := range pages {
		if p.Canvas != canvas {
			continue
		}
		if !found || p.X+p.Width > right {
			right = p.X + p.Width
		}
		if !found || p.Y < top {
			top = p.Y
		}
		found = true
	}
	if !found {
		return 0, 0
	}
	return round2(right + PageGap), top
}
