package artifacts

// 画框的调节参数（计划步骤 6.5）：页面在 `<script type="application/json" data-oryn-params>` 里声明一组参数
// （参数名 → 定义），用户在画布的「调节」面板里改值，写回这段 JSON 落成一个用户版本。
//
// 声明的解析与值的校验规则和前端 `web/src/lib/artifacts/params.ts` 逐条相同，两边读同一份测试向量
// `schema/testdata/artifact-params.json`。解析是宽松的：不合规的项逐个略过，其余照常，页面不会因此出错。
//
// 页面运行时（跟随最新版本的帧）把参数设成根元素的 CSS 变量与 data 属性；没有运行时的地方（渲染模式、
// 「在浏览器中打开」的顶层帧、发布页）由 ParamsPrelude 生成的一小段内联脚本做同样的事，转换规则相同。

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/artifacts/htmlsrc"
)

// 调节参数的规则；与 params.ts 相同。
const (
	ParamTextRunes  = 200
	ParamLabelRunes = 60
	ParamMaxOptions = 20
	paramMinStep    = 0.001
)

// 五种参数类型。
const (
	ParamColor   = "color"
	ParamText    = "text"
	ParamNumber  = "number"
	ParamBoolean = "boolean"
	ParamChoice  = "choice"
)

var (
	paramNameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	paramColorRe = regexp.MustCompile(`^#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)
	paramUnitRe  = regexp.MustCompile(`^[a-z%]{0,8}$`)
)

// Param 是规整后的一个参数。Value 是 string（color / text / choice）、float64（number）或 bool（boolean）；
// Min、Max、Step、Unit 只有 number 有，Options 只有 choice 有。
type Param struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Label   string   `json:"label"`
	Value   any      `json:"value"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
	Unit    *string  `json:"unit,omitempty"`
	Options []string `json:"options,omitempty"`
}

// member 是 JSON 对象的一个成员，保留在原文里的顺序。
type member struct {
	key string
	raw json.RawMessage
}

// objectMembers 按顺序取出 JSON 对象的成员；不是对象（或后面还有别的内容）时 ok 为假。重复的键同 JSON.parse：
// 留在第一次出现的位置，取最后一次的值。
func objectMembers(raw []byte) ([]member, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	var out []member
	at := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, ok := tok.(string)
		if !ok {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		if i, dup := at[key]; dup {
			out[i].raw = v
			continue
		}
		at[key] = len(out)
		out = append(out, member{key: key, raw: v})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return out, true
}

// asNumber 取有限的数字。
func asNumber(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		var err error
		if f, err = n.Float64(); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

// optNumber 取可选的数字字段：缺省时回 def；给了却不是有限数字时 ok 为假。
func optNumber(def map[string]any, key string, fallback float64) (float64, bool) {
	v, has := def[key]
	if !has {
		return fallback, true
	}
	return asNumber(v)
}

func clamp(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

// paramLabel 规整 label：字符串去掉首尾空白后取前 ParamLabelRunes 个码点；不是字符串时为空。
func paramLabel(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > ParamLabelRunes {
		s = string([]rune(s)[:ParamLabelRunes])
	}
	return s
}

// parseParam 规整一个参数定义；不合规时 ok 为假（这一项被略过）。
func parseParam(name string, raw json.RawMessage) (Param, bool) {
	var def map[string]any
	if !paramNameRe.MatchString(name) || json.Unmarshal(raw, &def) != nil || def == nil {
		return Param{}, false
	}
	typ, _ := def["type"].(string)
	p := Param{Name: name, Type: typ, Label: paramLabel(def["label"])}
	switch typ {
	case ParamNumber:
		lo, ok1 := optNumber(def, "min", 0)
		hi, ok2 := optNumber(def, "max", 100)
		step, ok3 := optNumber(def, "step", 1)
		unit, ok4 := "", true
		if u, has := def["unit"]; has {
			unit, ok4 = u.(string)
			ok4 = ok4 && paramUnitRe.MatchString(unit)
		}
		if !ok1 || !ok2 || !ok3 || !ok4 || lo > hi {
			return Param{}, false
		}
		step = math.Max(step, paramMinStep)
		p.Min, p.Max, p.Step, p.Unit = &lo, &hi, &step, &unit
	case ParamChoice:
		opts, ok := def["options"].([]any)
		if !ok || len(opts) == 0 || len(opts) > ParamMaxOptions {
			return Param{}, false
		}
		for _, o := range opts {
			s, ok := o.(string)
			if !ok {
				return Param{}, false
			}
			p.Options = append(p.Options, s)
		}
	case ParamColor, ParamText, ParamBoolean:
	default:
		return Param{}, false
	}
	v, ok := CoerceParamValue(p, def["value"])
	if !ok {
		return Param{}, false
	}
	p.Value = v
	return p, true
}

// ParseParams 规整声明（JSON 对象的原文）：按声明顺序，不合规的项略过，最多取 MaxParams 个合规的。
// 原文不是 JSON 对象时没有参数。
func ParseParams(decl []byte) []Param {
	members, ok := objectMembers(decl)
	if !ok {
		return nil
	}
	return parseMembers(members)
}

func parseMembers(members []member) []Param {
	var out []Param
	for _, m := range members {
		if len(out) == MaxParams {
			break
		}
		if p, ok := parseParam(m.key, m.raw); ok {
			out = append(out, p)
		}
	}
	return out
}

// CoerceParamValue 校验参数 p 的一个新值：color 是 #rgb / #rgba / #rrggbb / #rrggbbaa，text 不超过
// ParamTextRunes 个码点，number 是有限数字并夹回 [min, max]，boolean 是布尔，choice 必须是选项之一。
func CoerceParamValue(p Param, v any) (any, bool) {
	switch p.Type {
	case ParamColor:
		s, ok := v.(string)
		return s, ok && paramColorRe.MatchString(s)
	case ParamText:
		s, ok := v.(string)
		return s, ok && utf8.RuneCountInString(s) <= ParamTextRunes
	case ParamNumber:
		n, ok := asNumber(v)
		if !ok || p.Min == nil || p.Max == nil {
			return nil, false
		}
		return clamp(n, *p.Min, *p.Max), true
	case ParamBoolean:
		b, ok := v.(bool)
		return b, ok
	case ParamChoice:
		s, ok := v.(string)
		return s, ok && slices.Contains(p.Options, s)
	}
	return nil, false
}

// jsNumber 按 JS 的 String(number) 格式化（encoding/json 的浮点格式与它一致）。
func jsNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// jsQuote 等同 JSON.stringify(string)：只转义引号、反斜杠与控制字符。
func jsQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[r>>4])
				b.WriteByte("0123456789abcdef"[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ParamCSS 是一个参数在根元素上的两种形式：CSS 变量 `--<名>` 的值与属性 `data-<名>` 的值。
type ParamCSS struct {
	Variable  string `json:"variable"`
	Attribute string `json:"attribute"`
}

// CSSOf 返回参数值 v 的 CSS 形式：数字带单位（属性不带）、文本的变量是 JSON.stringify 的结果、其余原样。
func CSSOf(p Param, v any) ParamCSS {
	switch x := v.(type) {
	case float64:
		n := jsNumber(x)
		unit := ""
		if p.Unit != nil {
			unit = *p.Unit
		}
		return ParamCSS{Variable: n + unit, Attribute: n}
	case bool:
		s := strconv.FormatBool(x)
		return ParamCSS{Variable: s, Attribute: s}
	case string:
		if p.Type == ParamText {
			return ParamCSS{Variable: jsQuote(x), Attribute: x}
		}
		return ParamCSS{Variable: x, Attribute: x}
	}
	return ParamCSS{}
}

// paramsScript 找页面里第一个 `<script type="application/json" data-oryn-params>`，返回它内容的范围。
func paramsScript(src []byte) (start, end int, ok bool) {
	for _, t := range htmlsrc.StartTags(src) {
		if t.Name != "script" {
			continue
		}
		tag := src[t.Start:t.End]
		if _, has := findAttr(tag, len(t.Name), "data-oryn-params"); !has {
			continue
		}
		typ, _ := findAttr(tag, len(t.Name), "type")
		if strings.ToLower(strings.TrimSpace(html.UnescapeString(string(typ.value)))) != "application/json" {
			continue
		}
		return htmlsrc.TextContentRange(src, t)
	}
	return 0, 0, false
}

// PageParams 返回页面声明的参数（规整后）；没有声明时为空。
func PageParams(src []byte) []Param {
	s, e, ok := paramsScript(src)
	if !ok {
		return nil
	}
	return ParseParams(src[s:e])
}

// ParamsPrelude 返回把页面声明的参数设成根元素 CSS 变量与 data 属性的内联 `<script>`；没有合规的参数时为空。
// 名字只取通过校验的参数名，值经 json 编码（`<`、`>`、`&` 写成 \u00XX），参数值里的 `</script>` 截不断它。
func ParamsPrelude(src []byte) []byte {
	params := PageParams(src)
	if len(params) == 0 {
		return nil
	}
	vars := make([][2]string, 0, len(params))
	attrs := make([][2]string, 0, len(params))
	for _, p := range params {
		c := CSSOf(p, p.Value)
		vars = append(vars, [2]string{"--" + p.Name, c.Variable})
		attrs = append(attrs, [2]string{"data-" + p.Name, c.Attribute})
	}
	v, _ := json.Marshal(vars)
	a, _ := json.Marshal(attrs)
	var b bytes.Buffer
	// 脚本本身不含 `<`：值都在 json 编码的数组里
	b.WriteString(`<script data-oryn-params-prelude>(function(r,v,a){v.forEach(function(x){r.style.setProperty(x[0],x[1])});a.forEach(function(x){r.setAttribute(x[0],x[1])})})(document.documentElement,`)
	b.Write(v)
	b.WriteByte(',')
	b.Write(a)
	b.WriteString(`);</script>`)
	return b.Bytes()
}

// withParamsPrelude 在 InsertionPoint 处插入 ParamsPrelude；页面没有声明参数时原样返回（逐字节不变）。
func withParamsPrelude(src []byte) []byte {
	pre := ParamsPrelude(src)
	if pre == nil {
		return src
	}
	at := htmlsrc.InsertionPoint(src)
	out := make([]byte, 0, len(src)+len(pre))
	out = append(out, src[:at]...)
	out = append(out, pre...)
	return append(out, src[at:]...)
}

// SetParams 把 values（参数名 → 新值）写进 head 里 path 文件的参数声明：逐个校验（数字夹回范围），合并进声明，
// 整段声明按 JSON.stringify(x, null, 2) 的格式写回（`</` 写成 `<\/`），文件其余部分逐字节不变；结果经 Write
// 以 baseVersion 落成一个用户版本，所以文件在 baseVersion 之后被改过回 conflict。
func (s *Service) SetParams(ctx context.Context, id string, baseVersion int, path string, values map[string]any) (Artifact, error) {
	if PathProblem(path) != "" || !IsHTML(path) {
		return Artifact{}, opErr(CodeInvalid, "只有 HTML 文件能声明调节参数，%q 不是。", path)
	}
	if len(values) == 0 {
		return Artifact{}, opErr(CodeInvalid, "这次没有要改的参数。")
	}
	if len(values) > MaxParams {
		return Artifact{}, opErr(CodeInvalid, "一次最多改 %d 个参数。", MaxParams)
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	if a.Deleted() {
		return Artifact{}, deletedErr(id)
	}
	src, _, err := s.ReadFile(ctx, id, a.Version, path)
	if oe, ok := AsOpError(err); ok && oe.Code == CodeNotFound {
		return Artifact{}, &OpError{
			Code:    CodeConflict,
			Message: path + " 在最新版本里已经不在了。",
			State:   &State{Version: a.Version, Changed: []string{path}},
		}
	}
	if err != nil {
		return Artifact{}, err
	}
	out, err := setParams(src, path, values)
	if err != nil {
		return Artifact{}, err
	}
	return s.Write(ctx, WriteParams{
		ID:          id,
		BaseVersion: baseVersion,
		Files:       []FileChange{{Path: path, Data: bytes.NewReader(out)}},
	})
}

// setParams 返回把 values 合并进 src 的参数声明之后的副本。
func setParams(src []byte, path string, values map[string]any) ([]byte, error) {
	cs, ce, ok := paramsScript(src)
	if !ok {
		return nil, opErr(CodeInvalid, `%s 没有声明调节参数（<script type="application/json" data-oryn-params>）。`, path)
	}
	content := src[cs:ce]
	body := bytes.TrimSpace(content)
	lead := content[:bytes.Index(content, body)]
	trail := content[len(lead)+len(body):]
	members, ok := objectMembers(body)
	if !ok {
		return nil, opErr(CodeInvalid, "%s 的参数声明不是合法的 JSON 对象。", path)
	}
	declared := map[string]Param{}
	for _, p := range parseMembers(members) {
		declared[p.Name] = p
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	coerced := map[string]any{}
	for _, name := range names {
		p, ok := declared[name]
		if !ok {
			return nil, opErr(CodeInvalid, "%s 没有声明参数 %q（或它的声明不合规）。", path, name)
		}
		v, ok := CoerceParamValue(p, values[name])
		if !ok {
			return nil, opErr(CodeInvalid, "参数 %s 的值不合法：%s。", name, paramRule(p))
		}
		coerced[name] = v
	}

	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			compact.WriteByte(',')
		}
		writeJSONString(&compact, m.key)
		compact.WriteByte(':')
		raw := m.raw
		if v, ok := coerced[m.key]; ok {
			raw = withValue(m.raw, v)
		}
		if err := json.Compact(&compact, raw); err != nil {
			return nil, opErr(CodeInvalid, "%s 的参数声明不是合法的 JSON。", path)
		}
	}
	compact.WriteByte('}')
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, compact.Bytes(), "", "  "); err != nil {
		return nil, opErr(CodeInvalid, "%s 的参数声明不是合法的 JSON。", path)
	}
	// JSON 里 `<` 只会出现在字符串中：`</` 与 `<!--` 换成等价的转义，不会提前结束或扰乱 <script>
	decl := bytes.ReplaceAll(pretty.Bytes(), []byte("</"), []byte(`<\/`))
	decl = bytes.ReplaceAll(decl, []byte("<!--"), []byte(`\u003c!--`))

	out := make([]byte, 0, len(src)-len(body)+len(decl))
	out = append(out, src[:cs]...)
	out = append(out, lead...)
	out = append(out, decl...)
	out = append(out, trail...)
	return append(out, src[ce:]...), nil
}

// withValue 返回把定义对象 raw 的 value 换成 v 之后的 JSON（其余成员与顺序不变；没有 value 时追加）。
func withValue(raw json.RawMessage, v any) json.RawMessage {
	members, _ := objectMembers(raw) // 能走到这里的定义都已解析成功
	var b bytes.Buffer
	b.WriteByte('{')
	found := false
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, m.key)
		b.WriteByte(':')
		if m.key == "value" {
			found = true
			writeJSONValue(&b, v)
		} else {
			b.Write(m.raw)
		}
	}
	if !found {
		if len(members) > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"value":`)
		writeJSONValue(&b, v)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func writeJSONString(b *bytes.Buffer, s string) { b.WriteString(jsQuote(s)) }

func writeJSONValue(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case string:
		writeJSONString(b, x)
	case float64:
		b.WriteString(jsNumber(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	}
}

// paramRule 说明参数 p 的值要满足什么，给报错用。
func paramRule(p Param) string {
	switch p.Type {
	case ParamColor:
		return "颜色要写成 #rgb、#rgba、#rrggbb 或 #rrggbbaa"
	case ParamText:
		return "文本不能超过 " + strconv.Itoa(ParamTextRunes) + " 字"
	case ParamNumber:
		return "要是数字"
	case ParamBoolean:
		return "要是 true 或 false"
	case ParamChoice:
		return "只能是 " + strings.Join(p.Options, "、") + " 之一"
	}
	return "类型未知"
}
