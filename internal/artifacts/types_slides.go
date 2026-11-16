package artifacts

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// 幻灯片的上限。
const (
	MaxSlides          = 200
	MaxSlideTitleRunes = 200
	MaxSlideTextRunes  = 1500
	MaxBulletRunes     = 500
	MaxBullets         = 12
	MaxSubBullets      = 8
	MaxStats           = 4
	MaxTableColumns    = 8
	MaxTableRows       = 20
	MaxCellRunes       = 300
	MaxNotesRunes      = 5000
)

// slideLayout 是一种版式的必填与可选字段（notes 对所有版式都可选，另行处理）。
type slideLayout struct{ required, optional []string }

// slideLayoutOrder 是全部版式（教程与报错按这个顺序列出）。
var slideLayoutOrder = []string{"title", "section", "bullets", "two-column", "image", "quote", "stats", "table"}

var slideLayouts = map[string]slideLayout{
	"title":      {required: []string{"title"}, optional: []string{"subtitle"}},
	"section":    {required: []string{"title"}, optional: []string{"subtitle"}},
	"bullets":    {required: []string{"title", "bullets"}, optional: []string{"image", "alt"}},
	"two-column": {required: []string{"left", "right"}, optional: []string{"title"}},
	"image":      {required: []string{"image", "alt"}, optional: []string{"title", "caption"}},
	"quote":      {required: []string{"quote"}, optional: []string{"attribution"}},
	"stats":      {required: []string{"stats"}, optional: []string{"title"}},
	"table":      {required: []string{"columns", "rows"}, optional: []string{"title"}},
}

// deckPages 返回幻灯片张数（入口已通过校验）。
func deckPages(entry []byte) int {
	var d struct {
		Slides []json.RawMessage `json:"slides"`
	}
	_ = json.Unmarshal(entry, &d)
	return len(d.Slides)
}

// validateSlides 校验幻灯片 artifact：文件集合（checkSlidesFiles）；slides.json 的结构、版式字段与上限（不允许
// 未知字段），图片引用都在版本里。
func validateSlides(files map[string][]byte) []Problem {
	const entry = EntrySlides
	var ps problems
	checkSlidesFiles(files, &ps)
	raw, ok := files[entry]
	if !ok {
		return ps
	}
	if !utf8.Valid(raw) {
		ps.add(entry, "不是合法的 UTF-8；用 UTF-8 编码重写。")
		return ps
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		ps.add(entry, "不是合法的 JSON（%v）。", err)
		return ps
	}
	if _, err := d.Token(); err != io.EOF {
		ps.add(entry, "JSON 后面还有多余的内容；整个文件只能是一个对象。")
		return ps
	}
	c := deckCheck{ps: &ps, files: files}
	c.deck(v)
	return ps
}

// checkSlidesFiles 检查幻灯片 artifact 的文件集合：入口必须在；其余文件都在 images/ 下（不限格式）。
func checkSlidesFiles(files map[string][]byte, ps *problems) {
	checkEntry(EntrySlides, files, ps)
	for _, p := range extraFiles(EntrySlides, files) {
		if !strings.HasPrefix(p, imagesDir) {
			ps.add(p, "幻灯片 artifact 只能有 %s 与 images/ 下的文件；去掉这个文件，或把它挪到 images/ 下。", EntrySlides)
		}
	}
}

// deckCheck 逐字段检查幻灯片 JSON，位置写成 slides[2].bullets[0].text 的形式。
type deckCheck struct {
	ps    *problems
	files map[string][]byte
}

func (c deckCheck) add(where, format string, args ...any) { c.ps.add(where, format, args...) }

// object 要求 v 是对象，并报告 allowed 之外的字段；不是对象时返回 nil。where 为空表示整个 slides.json。
func (c deckCheck) object(where string, v any, allowed ...string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		c.add(cmp.Or(where, EntrySlides), "应该是一个对象。")
		return nil
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(allowed, k) {
			c.add(fieldAt(where, k), "未知字段；这里只能有 %s。", strings.Join(allowed, "、"))
		}
	}
	return m
}

// str 检查字符串字段：类型、必填时非空、字符数上限。
func (c deckCheck) str(where string, v any, limit int, required bool) (string, bool) {
	s, ok := v.(string)
	if !ok {
		c.add(where, "应该是字符串。")
		return "", false
	}
	if required && strings.TrimSpace(s) == "" {
		c.add(where, "不能为空。")
		return s, false
	}
	if n := utf8.RuneCountInString(s); n > limit {
		c.add(where, "有 %d 个字符，上限是 %d。", n, limit)
		return s, false
	}
	return s, true
}

// array 检查数组字段：类型与元素个数范围。
func (c deckCheck) array(where string, v any, minN, maxN int) ([]any, bool) {
	a, ok := v.([]any)
	if !ok {
		c.add(where, "应该是数组。")
		return nil, false
	}
	if len(a) < minN {
		c.add(where, "至少要有 %d 项。", minN)
		return a, false
	}
	if len(a) > maxN {
		c.add(where, "有 %d 项，上限是 %d。", len(a), maxN)
		return a, false
	}
	return a, true
}

func (c deckCheck) deck(v any) {
	m := c.object("", v, "title", "theme", "aspect", "slides")
	if m == nil {
		return
	}
	if t, ok := m["title"]; ok {
		c.str("title", t, MaxSlideTitleRunes, false)
	}
	if t, ok := m["theme"]; ok {
		if s, ok := t.(string); !ok || (s != "light" && s != "dark") {
			c.add("theme", "只能是 \"light\" 或 \"dark\"。")
		}
	}
	if a, ok := m["aspect"]; ok {
		if s, ok := a.(string); !ok || (s != "16:9" && s != "4:3") {
			c.add("aspect", "只能是 \"16:9\" 或 \"4:3\"。")
		}
	}
	raw, ok := m["slides"]
	if !ok {
		c.add("slides", "缺少必填字段 slides（幻灯片数组）。")
		return
	}
	slides, _ := c.array("slides", raw, 1, MaxSlides)
	for i, s := range slides {
		if i >= MaxSlides || len(*c.ps) >= maxProblems {
			break
		}
		c.slide(fmt.Sprintf("slides[%d]", i), s)
	}
}

func (c deckCheck) slide(where string, v any) {
	m, ok := v.(map[string]any)
	if !ok {
		c.add(where, "应该是一个对象。")
		return
	}
	lv, ok := m["layout"]
	if !ok {
		c.add(fieldAt(where, "layout"), "缺少必填字段 layout；可选 %s。", strings.Join(slideLayoutOrder, "、"))
		return
	}
	name, _ := lv.(string)
	layout, ok := slideLayouts[name]
	if !ok {
		c.add(fieldAt(where, "layout"), "未知的版式；只能是 %s。", strings.Join(slideLayoutOrder, "、"))
		return
	}
	allowed := append(append(append([]string{"layout"}, layout.required...), layout.optional...), "notes")
	for _, k := range sortedKeys(m) {
		if !slices.Contains(allowed, k) {
			c.add(fieldAt(where, k), "版式 %s 没有这个字段；它只能有 %s。", name, strings.Join(allowed, "、"))
		}
	}
	for _, k := range layout.required {
		if _, ok := m[k]; !ok {
			c.add(fieldAt(where, k), "版式 %s 缺少必填字段 %s。", name, k)
		}
	}
	for _, k := range append(append(append([]string(nil), layout.required...), layout.optional...), "notes") {
		if fv, ok := m[k]; ok {
			c.field(fieldAt(where, k), k, fv, slices.Contains(layout.required, k))
		}
	}
	if name != "table" {
		return
	}
	cols, _ := m["columns"].([]any)
	rows, _ := m["rows"].([]any)
	for i, r := range rows {
		if cells, ok := r.([]any); ok && len(cols) > 0 && len(cells) != len(cols) {
			c.add(fmt.Sprintf("%s[%d]", fieldAt(where, "rows"), i), "有 %d 格，columns 有 %d 列；每行的格数要与 columns 一致。", len(cells), len(cols))
		}
	}
}

// field 按字段名检查一张幻灯片上的字段。
func (c deckCheck) field(where, key string, v any, required bool) {
	switch key {
	case "title":
		c.str(where, v, MaxSlideTitleRunes, required)
	case "subtitle", "alt", "caption", "quote", "attribution":
		c.str(where, v, MaxSlideTextRunes, required)
	case "notes":
		c.str(where, v, MaxNotesRunes, false)
	case "image":
		if s, ok := c.str(where, v, MaxPathBytes, required); ok && !hasFile(c.files, localRef(s)) {
			c.add(where, "图片 %s 不在这个版本里；把图片放进 images/ 一起写入，或改成版本里已有的路径。", s)
		}
	case "bullets":
		c.bullets(where, v, MaxBullets, true)
	case "left", "right":
		c.column(where, v)
	case "stats":
		items, _ := c.array(where, v, 1, MaxStats)
		for i, it := range items {
			w := fmt.Sprintf("%s[%d]", where, i)
			if m := c.object(w, it, "value", "label"); m != nil {
				if val, ok := m["value"]; ok {
					c.str(fieldAt(w, "value"), val, MaxSlideTitleRunes, true)
				} else {
					c.add(fieldAt(w, "value"), "缺少必填字段 value（写成字符串，如 \"42%%\"）。")
				}
				if l, ok := m["label"]; ok {
					c.str(fieldAt(w, "label"), l, MaxBulletRunes, false)
				}
			}
		}
	case "columns":
		cols, _ := c.array(where, v, 1, MaxTableColumns)
		for i, col := range cols {
			c.str(fmt.Sprintf("%s[%d]", where, i), col, MaxCellRunes, false)
		}
	case "rows":
		rows, _ := c.array(where, v, 1, MaxTableRows)
		for i, r := range rows {
			w := fmt.Sprintf("%s[%d]", where, i)
			cells, ok := c.array(w, r, 1, MaxTableColumns)
			if !ok {
				continue
			}
			for j, cell := range cells {
				c.str(fmt.Sprintf("%s[%d]", w, j), cell, MaxCellRunes, false)
			}
		}
	}
}

// bullets 检查要点列表：元素是字符串，或 {text, bullets}（子要点只能是字符串）。
func (c deckCheck) bullets(where string, v any, maxN int, nested bool) {
	items, _ := c.array(where, v, 1, maxN)
	for i, it := range items {
		w := fmt.Sprintf("%s[%d]", where, i)
		if _, isStr := it.(string); isStr || !nested {
			c.str(w, it, MaxBulletRunes, true)
			continue
		}
		m := c.object(w, it, "text", "bullets")
		if m == nil {
			continue
		}
		if t, ok := m["text"]; ok {
			c.str(fieldAt(w, "text"), t, MaxBulletRunes, true)
		} else {
			c.add(fieldAt(w, "text"), "缺少必填字段 text。")
		}
		if sub, ok := m["bullets"]; ok {
			c.bullets(fieldAt(w, "bullets"), sub, MaxSubBullets, false)
		}
	}
}

// column 检查 two-column 的一栏 {heading?, text?, bullets?}，至少要有一项。
func (c deckCheck) column(where string, v any) {
	m := c.object(where, v, "heading", "text", "bullets")
	if m == nil {
		return
	}
	if len(m) == 0 {
		c.add(where, "至少要有 heading、text、bullets 之一。")
	}
	if h, ok := m["heading"]; ok {
		c.str(fieldAt(where, "heading"), h, MaxSlideTitleRunes, false)
	}
	if t, ok := m["text"]; ok {
		c.str(fieldAt(where, "text"), t, MaxSlideTextRunes, false)
	}
	if b, ok := m["bullets"]; ok {
		c.bullets(fieldAt(where, "bullets"), b, MaxBullets, false)
	}
}

// fieldAt 返回 where 下字段 key 的位置；where 为空（顶层）时就是 key。
func fieldAt(where, key string) string {
	if where == "" {
		return key
	}
	return where + "." + key
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// guideSlides 是 slides 主题。
var guideSlides = fmt.Sprintf(`# 幻灯片 artifact

幻灯片是一份 slides.json：你只写内容和版式，排版、字体、颜色由 Oryn 的查看器统一处理。用户可以翻页、演示、就地改文字，也能导出成 PowerPoint 或 PDF。

## 创建与修改

- create_artifact 带 type: "slides"，files 里给 slides.json（入口文件）；不要给 html。
- 图片放在 images/ 下（png、jpg、gif、webp、svg 等都可以），在幻灯片里用相对路径引用，如 "image": "images/chart.png"。引用的图片必须在同一个版本里。
- 幻灯片 artifact 里只能有 slides.json 与 images/ 下的文件。
- 改之前先 read_artifact（缺省读 slides.json），再用 edits（缺省改 slides.json）做小改动，或用 files 整个替换 slides.json，带上 baseVersion。
- 格式错误时 update_artifact 会逐条列出位置（如 slides[2].title）与原因，改好再写。

## 整体结构

{
  "title": "季度回顾",            // 可选，不超过 %[1]d 字
  "theme": "light",               // 可选："light"（缺省）或 "dark"
  "aspect": "16:9",               // 可选："16:9"（缺省）或 "4:3"
  "slides": [ … ]                 // 必填，1 到 %[2]d 张
}

（真实文件里不能有注释，上面的 // 只是说明。）不允许任何未知字段。每张幻灯片有 layout 与可选的 notes（讲者备注，不超过 %[3]d 字），其余字段由版式决定。

## 八种版式

title 与 section（封面、章节页）：必填 title，可选 subtitle。
  {"layout": "title", "title": "2026 年第三季度回顾", "subtitle": "产品组 · 10 月"}
  {"layout": "section", "title": "第二部分：增长"}

bullets（要点）：必填 title、bullets，可选 image、alt（带图时图片在右半边）。要点是字符串，或 {text, bullets} 带一层子要点（子要点只能是字符串）。
  {"layout": "bullets", "title": "本季重点", "bullets": ["发布 2.0", {"text": "性能", "bullets": ["首屏 -40%%", "内存 -25%%"]}], "image": "images/launch.png", "alt": "发布会现场"}

two-column（两栏对比）：必填 left、right，可选 title。每栏是 {heading?, text?, bullets?}，至少给一项。
  {"layout": "two-column", "title": "方案对比", "left": {"heading": "方案 A", "bullets": ["便宜", "慢"]}, "right": {"heading": "方案 B", "text": "贵一些，但快三倍。"}}

image（整页图片）：必填 image、alt，可选 title、caption。
  {"layout": "image", "image": "images/chart.png", "alt": "月活走势图", "title": "月活", "caption": "数据截至 9 月 30 日"}

quote（引言）：必填 quote，可选 attribution。
  {"layout": "quote", "quote": "先做对，再做快。", "attribution": "团队守则"}

stats（关键数字）：必填 stats（1 到 %[4]d 个 {value, label?}，value 写成字符串），可选 title。
  {"layout": "stats", "title": "核心指标", "stats": [{"value": "42%%", "label": "留存"}, {"value": "1.2M", "label": "月活"}]}

table（表格）：必填 columns（表头，1 到 %[5]d 列）与 rows（1 到 %[6]d 行，每行是字符串数组），可选 title。
  {"layout": "table", "title": "排期", "columns": ["阶段", "时间"], "rows": [["设计", "10 月"], ["开发", "11 月"]]}

## 上限

- 幻灯片最多 %[2]d 张；title、heading、stats 的 value 不超过 %[1]d 字；subtitle、alt、caption、quote、attribution、两栏的 text 不超过 %[7]d 字。
- 每张最多 %[8]d 条要点、每条要点不超过 %[9]d 字、每条最多 %[10]d 条子要点；stats 最多 %[4]d 个；表格最多 %[5]d 列 × %[6]d 行、每格不超过 %[11]d 字；notes 不超过 %[3]d 字。
- 不能写位置、尺寸、字号、颜色或任何样式：版式和主题决定这些。内容放不下时拆成几张。
`, MaxSlideTitleRunes, MaxSlides, MaxNotesRunes, MaxStats, MaxTableColumns, MaxTableRows, MaxSlideTextRunes,
	MaxBullets, MaxBulletRunes, MaxSubBullets, MaxCellRunes)
