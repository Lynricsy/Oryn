package design

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
)

// 方案清单 src/variants.json 的解析（M34 C2）。与 internal/designengine/engine/variants.mjs 逐条相同，
// 两边共用 schema/testdata/design-variants.json 的测试向量；改规则时两边一起改。
//
//	{"groups": [{"id", "name", "variants": [{"id", "name", "note"?, "theme"?, "frames": [帧 id]}]}]}
//
// 规则同 canvas.json 的方案组（canvas 包的 MaxGroups、MinVariants 等），只是 variants 列的是帧 id 而不是画框 id；
// 不写 picked（选定只在 canvas.json）。组 id 不能与帧 id 相同：canvas.json 里组与画框共用 id 命名空间。

// VariantsFile 是方案清单在工作副本与版本里的路径。
const VariantsFile = "src/variants.json"

// Variants 是规范化的方案清单。FrameTheme 是「帧 → 主题」，只含带主题的方案的帧。
type Variants struct {
	Groups     []VariantGroup    `json:"groups"`
	FrameTheme map[string]string `json:"frameTheme"`
}

// VariantGroup 是一个方案组。
type VariantGroup struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Variants []Variant `json:"variants"`
}

// Variant 是一个方案：Theme 为空表示没有独立主题，Frames 是帧 id。
type Variant struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Note   string   `json:"note"`
	Theme  string   `json:"theme"`
	Frames []string `json:"frames"`
}

// VariantsError 是解析失败：Path 是出错位置（如 groups[0].variants[1].frames[0]），整份文件不合法时为空串。
type VariantsError struct {
	Path string
	Msg  string
}

func (e *VariantsError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + "：" + e.Msg
}

// variantsParser 记下第一处错误，之后的检查都跳过。
type variantsParser struct{ err *VariantsError }

func (p *variantsParser) fail(path, format string, args ...any) {
	if p.err == nil {
		p.err = &VariantsError{Path: path, Msg: fmt.Sprintf(format, args...)}
	}
}

func vat(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// object 要求 v 是对象且只含 keys 里的字段，并且 need 里的字段都在。
func (p *variantsParser) object(v any, path string, keys, need []string) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		p.fail(path, "必须是对象")
		return nil
	}
	extra := make([]string, 0)
	for k := range m {
		if !slices.Contains(keys, k) {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		slices.Sort(extra)
		p.fail(vat(path, extra[0]), "未知字段")
		return nil
	}
	for _, k := range need {
		if _, ok := m[k]; !ok {
			p.fail(vat(path, k), "缺少字段")
			return nil
		}
	}
	return m
}

func (p *variantsParser) text(v any, path string, max int, blankOK bool) string {
	s, ok := v.(string)
	if !ok {
		p.fail(path, "必须是字符串")
		return ""
	}
	if !blankOK && strings.TrimSpace(s) == "" {
		p.fail(path, "不能为空")
	}
	if n := utf8.RuneCountInString(s); n > max {
		p.fail(path, "不能超过 %d 个字符（现在 %d 个）", max, n)
	}
	return s
}

func (p *variantsParser) id(v any, path string) string {
	s, ok := v.(string)
	if !ok || !canvas.GroupIDPattern.MatchString(s) {
		p.fail(path, "必须匹配 %s", canvas.GroupIDPattern.String())
		return ""
	}
	return s
}

func (p *variantsParser) list(v any, path string, lo, hi int, what string) []any {
	a, ok := v.([]any)
	if !ok {
		p.fail(path, "必须是数组")
		return nil
	}
	if len(a) < lo || len(a) > hi {
		p.fail(path, "%s要有 %d–%d 个（现在 %d 个）", what, lo, hi, len(a))
		return nil
	}
	return a
}

// ParseVariants 解析 src/variants.json 的内容。frames 是 src/frames 下存在的帧 id，themes 是 src/themes 下
// 存在的主题名（文件名去掉 .css）。出错时返回 *VariantsError。
func ParseVariants(raw []byte, frames, themes []string) (Variants, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Variants{}, &VariantsError{Msg: fmt.Sprintf("不是合法的 JSON（%v）", err)}
	}
	p := &variantsParser{}
	out := p.parse(doc, frames, themes)
	if p.err != nil {
		return Variants{}, p.err
	}
	return out, nil
}

func (p *variantsParser) parse(doc any, frames, themes []string) Variants {
	out := Variants{Groups: []VariantGroup{}, FrameTheme: map[string]string{}}
	m := p.object(doc, "", []string{"groups"}, []string{"groups"})
	if p.err != nil {
		return out
	}
	used := map[string]string{} // 帧 → 首次出现的位置
	groupIDs := map[string]bool{}
	for gi, g := range p.list(m["groups"], "groups", 0, canvas.MaxGroups, "方案组") {
		gp := fmt.Sprintf("groups[%d]", gi)
		gm := p.object(g, gp, []string{"id", "name", "variants"}, []string{"id", "name", "variants"})
		if p.err != nil {
			return out
		}
		gid := p.id(gm["id"], vat(gp, "id"))
		switch {
		case p.err != nil:
			return out
		case groupIDs[gid]:
			p.fail(vat(gp, "id"), "方案组 id「%s」重复", gid)
		case slices.Contains(frames, gid):
			p.fail(vat(gp, "id"), "方案组 id「%s」与帧 src/frames/%s.tsx 同名，换一个", gid, gid)
		}
		groupIDs[gid] = true
		group := VariantGroup{ID: gid, Name: p.text(gm["name"], vat(gp, "name"), canvas.MaxGroupName, false)}
		variants := p.list(gm["variants"], vat(gp, "variants"), canvas.MinVariants, canvas.MaxVariants, "方案")
		if p.err != nil {
			return out
		}
		variantIDs := map[string]bool{}
		for vi, v := range variants {
			vp := fmt.Sprintf("%s.variants[%d]", gp, vi)
			vm := p.object(v, vp, []string{"id", "name", "note", "theme", "frames"}, []string{"id", "name", "frames"})
			if p.err != nil {
				return out
			}
			vid := p.id(vm["id"], vat(vp, "id"))
			if p.err == nil && variantIDs[vid] {
				p.fail(vat(vp, "id"), "方案 id「%s」在本组重复", vid)
			}
			variantIDs[vid] = true
			variant := Variant{ID: vid, Name: p.text(vm["name"], vat(vp, "name"), canvas.MaxGroupName, false)}
			if note, ok := vm["note"]; ok {
				variant.Note = p.text(note, vat(vp, "note"), canvas.MaxVariantNote, true)
			}
			if theme, ok := vm["theme"]; ok {
				s, isStr := theme.(string)
				switch {
				case !isStr:
					p.fail(vat(vp, "theme"), "必须是字符串")
				case s != "":
					variant.Theme = p.id(s, vat(vp, "theme"))
					if p.err == nil && !slices.Contains(themes, s) {
						p.fail(vat(vp, "theme"), "没有主题文件 src/themes/%s.css", s)
					}
				}
			}
			fs := p.list(vm["frames"], vat(vp, "frames"), 1, canvas.MaxVariantPages, "帧")
			for fi, f := range fs {
				fp := fmt.Sprintf("%s.frames[%d]", vp, fi)
				s, ok := f.(string)
				switch {
				case p.err != nil:
				case !ok:
					p.fail(fp, "必须是字符串")
				case !slices.Contains(frames, s):
					p.fail(fp, "没有帧 src/frames/%s.tsx", s)
				case used[s] != "":
					p.fail(fp, "帧「%s」已经属于 %s，一个帧只能属于一个方案", s, used[s])
				}
				if p.err != nil {
					return out
				}
				used[s] = vp
				if variant.Theme != "" {
					out.FrameTheme[s] = variant.Theme
				}
				variant.Frames = append(variant.Frames, s)
			}
			if p.err != nil {
				return out
			}
			group.Variants = append(group.Variants, variant)
		}
		out.Groups = append(out.Groups, group)
	}
	return out
}
