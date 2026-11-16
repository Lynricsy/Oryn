package artifacts

// 元素直接编辑（计划步骤 3.6）：用户在查看器里选中页面元素，改它的文字与七个样式属性，落成一个用户版本。
// 可编辑值的校验规则与前端 `web/src/lib/artifacts/editable.ts` 逐条相同（沙箱运行时的实时预览用那一份）。

import (
	"bytes"
	"context"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
)

// EditableProps 是可直接编辑的七个样式属性，顺序即写回 style 时追加新声明的顺序。
var EditableProps = []string{"color", "background-color", "font-size", "font-weight", "text-align", "padding", "border-radius"}

var (
	hexColorRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}([0-9a-fA-F]{2})?$`)
	pxRe         = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})(\.[0-9]{1,2})?px$`)
	fontWeightRe = regexp.MustCompile(`^[1-9]00$`)
	editTagRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// ValidStyleValue 报告 value 是否可以作为可编辑属性 prop 的值写进元素的 style；prop 不在 EditableProps 里时为假。
func ValidStyleValue(prop, value string) bool {
	switch prop {
	case "color", "background-color":
		return hexColorRe.MatchString(value)
	case "font-size":
		if !pxRe.MatchString(value) {
			return false
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(value, "px"), 64)
		return err == nil && n >= 1
	case "border-radius":
		return pxRe.MatchString(value)
	case "font-weight":
		return fontWeightRe.MatchString(value)
	case "text-align":
		return value == "left" || value == "center" || value == "right" || value == "justify"
	case "padding":
		parts := strings.Split(value, " ")
		if len(parts) > 4 {
			return false
		}
		for _, p := range parts {
			if !pxRe.MatchString(p) {
				return false
			}
		}
		return true
	}
	return false
}

// ValidEditText 报告 text 能否作为直接编辑的文字：合法 UTF-8 且不超过 MaxEditTextRunes 个码点。
func ValidEditText(text string) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= MaxEditTextRunes
}

// EditParams 是 EditElement 的参数。Offset 与 Tag 来自页面运行时回报的源码标记（BaseVersion 那一版里起始标签的
// 字节偏移与标签名）；Style 与 Text 至少给一个。
type EditParams struct {
	BaseVersion int
	Path        string
	Offset      int
	Tag         string
	Style       map[string]string
	Text        *string
}

// rawTextTags 是内容按原文解析、不解码实体的元素：写进 HTML 转义后的文字会原样显示出转义序列，所以不能改文字。
var rawTextTags = []string{"script", "style", "iframe", "noembed", "noframes", "noscript", "plaintext", "xmp"}

func (p EditParams) check() error {
	if !editTagRe.MatchString(p.Tag) {
		return opErr(CodeInvalid, "标签名 %q 不合法。", p.Tag)
	}
	if PathProblem(p.Path) != "" || !IsHTML(p.Path) {
		return opErr(CodeInvalid, "只能直接编辑 HTML 文件里的元素，%q 不是。", p.Path)
	}
	if p.Offset < 0 {
		return opErr(CodeInvalid, "元素偏移不能是负数。")
	}
	if len(p.Style) == 0 && p.Text == nil {
		return opErr(CodeInvalid, "这次编辑没有任何改动。")
	}
	for prop, v := range p.Style {
		if !slices.Contains(EditableProps, prop) {
			return opErr(CodeInvalid, "样式属性 %s 不能直接编辑；只能改 %s。", prop, strings.Join(EditableProps, "、"))
		}
		if !ValidStyleValue(prop, v) {
			return opErr(CodeInvalid, "%s 的值 %q 不合法。", prop, v)
		}
	}
	if p.Text != nil {
		if !ValidEditText(*p.Text) {
			return opErr(CodeInvalid, "文字超过 %d 个字符。", MaxEditTextRunes)
		}
		if slices.Contains(rawTextTags, p.Tag) {
			return opErr(CodeInvalid, "<%s> 元素的文字不能直接编辑。", p.Tag)
		}
	}
	return nil
}

// EditElement 改 head 里 Path 文件在 Offset 处的起始标签：Style 的各项写进这个起始标签的 style 属性（同名声明
// 替换，其余声明保留，没有 style 属性就加一个）；Text 非空时把元素内容换成 HTML 转义后的文字（只对内容全是文本的
// 元素）。偏移处不是名为 Tag 的起始标签时回 conflict。结果经 Write 以 BaseVersion 落成一个用户版本，所以文件在
// BaseVersion 之后被改过同样回 conflict。
func (s *Service) EditElement(ctx context.Context, id string, p EditParams) (Artifact, error) {
	if err := p.check(); err != nil {
		return Artifact{}, err
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	if a.Deleted() {
		return Artifact{}, deletedErr(id)
	}
	gone := func() error {
		return &OpError{
			Code:    CodeConflict,
			Message: "这个元素在最新版本里已经不在原处了。在最新版本上重新选中它再改。",
			State:   &State{Version: a.Version, Changed: []string{p.Path}},
		}
	}
	src, _, err := s.ReadFile(ctx, id, a.Version, p.Path)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return Artifact{}, gone()
	}
	if err != nil {
		return Artifact{}, err
	}
	t, ok := htmlsrc.StartTagAt(src, p.Offset)
	if !ok || t.Name != p.Tag {
		return Artifact{}, gone()
	}
	out, err := editElement(src, t, p.Style, p.Text)
	if err != nil {
		return Artifact{}, err
	}
	return s.Write(ctx, WriteParams{
		ID:          id,
		BaseVersion: p.BaseVersion,
		Files:       []FileChange{{Path: p.Path, Data: bytes.NewReader(out)}},
	})
}

// editElement 返回改过起始标签 t 的 style 与（可选）内容文字之后的 src 副本；其余字节不变。
func editElement(src []byte, t htmlsrc.Tag, style map[string]string, text *string) ([]byte, error) {
	var cs, ce int
	if text != nil {
		var ok bool
		if cs, ce, ok = htmlsrc.TextContentRange(src, t); !ok {
			return nil, opErr(CodeInvalid, "<%s> 里还有别的元素，只有内容全是文字的元素能直接改文字。", t.Name)
		}
	}
	tag := src[t.Start:t.End]
	if len(style) > 0 {
		tag = setStyle(tag, len(t.Name), style)
	}
	out := make([]byte, 0, len(src)+len(tag)-(t.End-t.Start)+64)
	out = append(out, src[:t.Start]...)
	out = append(out, tag...)
	if text == nil {
		return append(out, src[t.End:]...), nil
	}
	out = append(out, src[t.End:cs]...)
	out = append(out, html.EscapeString(*text)...)
	return append(out, src[ce:]...), nil
}

// attrSpan 是起始标签原文里一个属性的位置：[start, end) 是整个属性（名字到值的结尾），value 是值的原文（未解码）。
type attrSpan struct {
	start, end int
	value      []byte
}

func isHTMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }

// findAttr 在起始标签原文 tag 里找名为 name（小写比较）的第一个属性；nameLen 是标签名的字节长度。
// 扫描规则同 HTML 分词器：属性之间的空白与 `/` 跳过，值可以是双引号、单引号或不带引号。
func findAttr(tag []byte, nameLen int, name string) (attrSpan, bool) {
	i := 1 + nameLen
	n := len(tag)
	for i < n {
		for i < n && (isHTMLSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= n || tag[i] == '>' {
			break
		}
		ns := i
		i++ // 名字的第一个字符可以是 `=`
		for i < n && !isHTMLSpace(tag[i]) && tag[i] != '/' && tag[i] != '>' && tag[i] != '=' {
			i++
		}
		an := strings.ToLower(string(tag[ns:i]))
		end, val := i, []byte(nil)
		j := i
		for j < n && isHTMLSpace(tag[j]) {
			j++
		}
		if j < n && tag[j] == '=' {
			j++
			for j < n && isHTMLSpace(tag[j]) {
				j++
			}
			switch {
			case j < n && (tag[j] == '"' || tag[j] == '\''):
				q := tag[j]
				k := bytes.IndexByte(tag[j+1:], q)
				if k < 0 {
					val, end = tag[j+1:n-1], n-1
				} else {
					val, end = tag[j+1:j+1+k], j+2+k
				}
			default:
				k := j
				for k < n && !isHTMLSpace(tag[k]) && tag[k] != '>' {
					k++
				}
				val, end = tag[j:k], k
			}
			i = end
		}
		if an == name {
			return attrSpan{start: ns, end: end, value: val}, true
		}
	}
	return attrSpan{}, false
}

// setStyle 返回把 style 的各项写进起始标签原文 tag 的 style 属性之后的副本：同名声明替换（同名的后续声明删掉，
// 免得它们盖过新值），其余声明原样保留；没有 style 属性时紧跟标签名加一个。
func setStyle(tag []byte, nameLen int, style map[string]string) []byte {
	span, ok := findAttr(tag, nameLen, "style")
	var decls []string
	if ok {
		decls = splitDecls(html.UnescapeString(string(span.value)))
	}
	for _, prop := range EditableProps {
		v, set := style[prop]
		if !set {
			continue
		}
		decl := prop + ": " + v
		replaced := false
		for i, d := range decls {
			if declName(d) == prop {
				decls[i], replaced = decl, true
				break
			}
		}
		if replaced {
			decls = dropLaterDecls(decls, prop)
		} else {
			decls = append(decls, decl)
		}
	}
	attr := `style="` + html.EscapeString(strings.Join(decls, "; ")) + `"`
	var out []byte
	if ok {
		out = make([]byte, 0, len(tag)-(span.end-span.start)+len(attr))
		out = append(out, tag[:span.start]...)
		out = append(out, attr...)
		return append(out, tag[span.end:]...)
	}
	at := 1 + nameLen
	out = make([]byte, 0, len(tag)+len(attr)+1)
	out = append(out, tag[:at]...)
	out = append(out, ' ')
	out = append(out, attr...)
	return append(out, tag[at:]...)
}

// dropLaterDecls 删掉 decls 里第一条之后名为 prop 的声明。
func dropLaterDecls(decls []string, prop string) []string {
	seen := false
	return slices.DeleteFunc(decls, func(d string) bool {
		if declName(d) != prop {
			return false
		}
		if !seen {
			seen = true
			return false
		}
		return true
	})
}

// declName 返回一条 CSS 声明的属性名（小写）；没有冒号时为空。
func declName(d string) string {
	name, _, ok := strings.Cut(d, ":")
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// splitDecls 把 style 属性的值按顶层的 `;` 拆成去掉首尾空白的声明（引号与括号里的 `;` 不拆），丢掉空声明。
func splitDecls(s string) []string {
	var out []string
	depth := 0
	var quote byte
	from := 0
	flush := func(to int) {
		if d := strings.TrimSpace(s[from:to]); d != "" {
			out = append(out, d)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			flush(i)
			from = i + 1
		}
	}
	flush(len(s))
	return out
}
