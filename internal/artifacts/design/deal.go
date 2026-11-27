// Package design 是设计画布的「方向」：内置方向库（色板、字体对、版式）、按种子发牌、
// 校验 agent 自拟的方向，以及把选定方向转成 src/theme.css 的 Tailwind v4 @theme 文本。
//
// 本包只有纯函数与内置数据，不碰数据库。library.json 的色板、字体对与落地页版式取自
// taste-skill（MIT，出处与上游 commit 见 NOTICE.md 与 library.json 的 source 字段）；
// 行业、调性的别名表与 app 面的四种版式是 Oryn 自写的。
package design

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// 发牌面。
const (
	SurfaceLanding = "landing"
	SurfaceApp     = "app"
)

// 方向库与校验的常量。
const (
	// Cards 是每次发出、每次自拟的方向张数。
	Cards = 3
	// MinHueGap 是两张方向强调色色相的最小差（度）；字体家族相同时必须达到。
	MinHueGap = 30.0
	// MinColors / MaxColors 是一个色板的颜色个数范围。
	MinColors = 4
	MaxColors = 6
	// MaxNameRunes 是方向名、字体家族名的字符上限。
	MaxNameRunes = 80
	// MaxLayoutRunes 是 agent 自拟版式说明的字符上限。
	MaxLayoutRunes = 400
	// achromaticSat 以下的强调色视为无彩色（黑、白、灰），色相无意义。
	achromaticSat = 0.12
)

// ErrInvalid 是方向不合规（数量、颜色、字体或两张太像）。调用方据此回 invalid。
var ErrInvalid = errors.New("design: invalid direction")

// RequiredRoles 是每个色板必须有的颜色角色。
var RequiredRoles = []string{"bg", "text", "accent"}

//go:embed library.json
var libraryJSON []byte

// Source 是方向库的上游出处。
type Source struct {
	Repo    string   `json:"repo"`
	Commit  string   `json:"commit"`
	License string   `json:"license"`
	Files   []string `json:"files"`
}

// Color 是色板里一个带角色的颜色；Role 成为 `--color-<role>`。
type Color struct {
	Role string `json:"role"`
	Hex  string `json:"hex"`
}

// Palette 是一个色板。库内色板带 mode（light|dark，按底色亮度算）、上游的风格标签与适用行业。
type Palette struct {
	ID     string   `json:"id,omitempty"`
	Name   string   `json:"name,omitempty"`
	Mode   string   `json:"mode,omitempty"`
	Colors []Color  `json:"colors"`
	Tags   []string `json:"tags,omitempty"`
	Trades []string `json:"trades,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// Font 是一个字体家族及建议字重。
type Font struct {
	Family  string `json:"family"`
	Weights []int  `json:"weights,omitempty"`
}

// TypePair 是标题 + 正文（+ 可选等宽）字体组合。Kinds 是上游按业务大类的权重，Moods 是调性。
type TypePair struct {
	ID      string         `json:"id,omitempty"`
	Class   string         `json:"class,omitempty"`
	Display Font           `json:"display"`
	Body    Font           `json:"body"`
	Mono    *Font          `json:"mono,omitempty"`
	Suits   string         `json:"suits,omitempty"`
	Kinds   map[string]int `json:"kinds,omitempty"`
	Moods   []string       `json:"moods,omitempty"`
}

// Layout 是一种首屏/骨架版式。Kinds 为空表示不分业务大类。
type Layout struct {
	ID      string         `json:"id"`
	Surface string         `json:"surface"`
	From    string         `json:"from"`
	Name    string         `json:"name"`
	Kinds   map[string]int `json:"kinds,omitempty"`
	Note    string         `json:"note"`
}

// Industry 把自由文本行业（别名）映射到色板的 trades 与字体/版式的 kinds。
type Industry struct {
	ID      string   `json:"id"`
	Trades  []string `json:"trades"`
	Kinds   []string `json:"kinds"`
	Aliases []string `json:"aliases"`
}

// Tone 把自由文本调性映射到明暗、字体 moods 与色板 tags。
type Tone struct {
	ID      string   `json:"id"`
	Mode    string   `json:"mode,omitempty"`
	Moods   []string `json:"moods"`
	Tags    []string `json:"tags"`
	Aliases []string `json:"aliases"`
}

// Library 是方向库。
type Library struct {
	Source     Source     `json:"source"`
	Palettes   []Palette  `json:"palettes"`
	Types      []TypePair `json:"types"`
	Layouts    []Layout   `json:"layouts"`
	Industries []Industry `json:"industries"`
	Tones      []Tone     `json:"tones"`
}

// Direction 是一张方向牌：色板 + 字体对 + 版式。
// 库发的牌 ID 为 `<色板>/<字体对>/<版式>`，Layout 是版式 ID；agent 自拟的方向 ID 可空，Layout 是自由描述。
type Direction struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Palette Palette  `json:"palette"`
	Type    TypePair `json:"type"`
	Layout  string   `json:"layout"`
}

var defaultLibrary = sync.OnceValue(func() Library {
	lib, err := ParseLibrary(libraryJSON)
	if err != nil {
		panic(err)
	}
	return lib
})

// Default 返回内置方向库（library.json）。调用方不要修改返回值里的切片。
func Default() Library { return defaultLibrary() }

// ParseLibrary 解析并校验方向库。
func ParseLibrary(b []byte) (Library, error) {
	var lib Library
	if err := json.Unmarshal(b, &lib); err != nil {
		return Library{}, fmt.Errorf("design: 方向库解析失败：%w", err)
	}
	seen := map[string]bool{}
	for _, p := range lib.Palettes {
		if p.ID == "" || seen["p:"+p.ID] {
			return Library{}, fmt.Errorf("design: 色板 ID %q 为空或重复", p.ID)
		}
		seen["p:"+p.ID] = true
		if p.Mode != "light" && p.Mode != "dark" {
			return Library{}, fmt.Errorf("design: 色板 %s 的 mode 必须是 light 或 dark", p.ID)
		}
		if err := validatePalette(p); err != nil {
			return Library{}, fmt.Errorf("design: 色板 %s：%w", p.ID, err)
		}
	}
	families := map[string]bool{}
	for _, t := range lib.Types {
		if t.ID == "" || seen["t:"+t.ID] {
			return Library{}, fmt.Errorf("design: 字体对 ID %q 为空或重复", t.ID)
		}
		seen["t:"+t.ID] = true
		if err := validateType(t); err != nil {
			return Library{}, fmt.Errorf("design: 字体对 %s：%w", t.ID, err)
		}
		// 发牌只保证字体对不同；家族唯一才能让发出的牌必过 ValidateProposals 的「字体家族不同」。
		f := familyKey(t.Display.Family)
		if families[f] {
			return Library{}, fmt.Errorf("design: 字体对 %s 的标题字体 %s 与其他字体对重复", t.ID, t.Display.Family)
		}
		families[f] = true
	}
	for _, l := range lib.Layouts {
		if l.ID == "" || seen["l:"+l.ID] {
			return Library{}, fmt.Errorf("design: 版式 ID %q 为空或重复", l.ID)
		}
		seen["l:"+l.ID] = true
		if l.Surface != SurfaceLanding && l.Surface != SurfaceApp {
			return Library{}, fmt.Errorf("design: 版式 %s 的 surface 必须是 landing 或 app", l.ID)
		}
	}
	for _, t := range lib.Tones {
		if t.Mode != "" && t.Mode != "light" && t.Mode != "dark" {
			return Library{}, fmt.Errorf("design: 调性 %s 的 mode 必须是 light 或 dark", t.ID)
		}
	}
	return lib, nil
}

// LayoutByID 按 ID 找版式。
func (lib Library) LayoutByID(id string) (Layout, bool) {
	i := slices.IndexFunc(lib.Layouts, func(l Layout) bool { return l.ID == id })
	if i < 0 {
		return Layout{}, false
	}
	return lib.Layouts[i], true
}

// Deal 从库里按 surface / industry / tone 发 3 张方向牌。纯函数：同参数同种子同结果。
//
// 三张牌两两色板不同、字体对不同（库保证标题字体家族唯一），版式尽量不同。
// industry、tone 是自由文本，按库里的别名匹配（可多个）；认不出的就不设限。
// avoid 是最近发过的牌 ID（`dealt` 的最近 9 项），其中的色板、字体对、版式排到最后才用。
// 合适的候选不够时逐级放宽（先丢调性、再丢行业、再丢明暗，最后才用 avoid 里的），不报错；
// 只有 surface 不认识或库本身不够 3 个色板 / 字体对 / 1 个版式时返回错误。
func Deal(lib Library, surface, industry, tone string, seed int64, avoid []string) ([Cards]Direction, error) {
	var out [Cards]Direction
	if surface != SurfaceLanding && surface != SurfaceApp {
		return out, fmt.Errorf("%w: surface 只能是 landing 或 app，收到 %q", ErrInvalid, surface)
	}
	layouts := slices.DeleteFunc(slices.Clone(lib.Layouts), func(l Layout) bool { return l.Surface != surface })
	if len(lib.Palettes) < Cards || len(lib.Types) < Cards || len(layouts) == 0 {
		return out, fmt.Errorf("design: 方向库不足以发牌（色板 %d、字体对 %d、%s 版式 %d）",
			len(lib.Palettes), len(lib.Types), surface, len(layouts))
	}

	want := resolve(lib, industry, tone)
	av := parseAvoid(avoid)
	rng := rand.New(rand.NewPCG(uint64(seed), 0x6f72796e64657369)) // 第二个种子固定（"oryndesi"）

	pi := pick(rng, lib.Palettes, Cards, func(p Palette) bool { return av.palettes[p.ID] }, paletteLevels(want, surface))
	ti := pick(rng, lib.Types, Cards, func(t TypePair) bool { return av.types[t.ID] }, typeLevels(want))
	li := pick(rng, layouts, Cards, func(l Layout) bool { return av.layouts[l.ID] }, layoutLevels(want))
	for i := range out {
		p, t := lib.Palettes[pi[i]], lib.Types[ti[i]]
		l := layouts[li[i%len(li)]]
		out[i] = Direction{
			ID:      p.ID + "/" + t.ID + "/" + l.ID,
			Name:    p.Name + " · " + t.Display.Family,
			Palette: p,
			Type:    t,
			Layout:  l.ID,
		}
	}
	return out, nil
}

// want 是解析后的发牌条件；空集合表示不设限。
type want struct {
	trades, kinds, moods, tags map[string]bool
	mode                       string
}

// resolve 把自由文本的行业、调性匹配到库里的别名。ASCII 别名按整词匹配（避免 "ai" 命中 "retail"），
// 含非 ASCII 的别名（中文）按子串匹配。
func resolve(lib Library, industry, tone string) want {
	w := want{trades: map[string]bool{}, kinds: map[string]bool{}, moods: map[string]bool{}, tags: map[string]bool{}}
	for _, in := range lib.Industries {
		if matches(industry, in.ID, in.Aliases) {
			addAll(w.trades, in.Trades)
			addAll(w.kinds, in.Kinds)
		}
	}
	modes := map[string]bool{}
	for _, t := range lib.Tones {
		if matches(tone, t.ID, t.Aliases) {
			addAll(w.moods, t.Moods)
			addAll(w.tags, t.Tags)
			if t.Mode != "" {
				modes[t.Mode] = true
			}
		}
	}
	if len(modes) == 1 { // 同时说了深色和浅色就不按明暗筛
		for m := range modes {
			w.mode = m
		}
	}
	return w
}

func matches(text, id string, aliases []string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return false
	}
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' })
	joined := " " + strings.Join(words, " ") + " "
	for _, a := range append([]string{id}, aliases...) {
		a = strings.ToLower(a)
		if a == "" {
			continue
		}
		if isASCII(a) {
			if strings.Contains(joined, " "+a+" ") {
				return true
			}
		} else if strings.Contains(text, a) {
			return true
		}
	}
	return false
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func addAll(m map[string]bool, xs []string) {
	for _, x := range xs {
		m[x] = true
	}
}

func anyIn(m map[string]bool, xs []string) bool {
	if len(m) == 0 {
		return true
	}
	return slices.ContainsFunc(xs, func(x string) bool { return m[x] })
}

// appLoud 是 app 面尽量不用的色板标签：整块大色面适合落地页，不适合长时间操作的界面。
var appLoud = []string{"flat-loud", "single-field", "pastel-fields"}

func paletteLevels(w want, surface string) []func(Palette) bool {
	mode := func(p Palette) bool { return w.mode == "" || p.Mode == w.mode }
	trade := func(p Palette) bool { return anyIn(w.trades, p.Trades) }
	tag := func(p Palette) bool { return anyIn(w.tags, p.Tags) }
	surf := func(p Palette) bool {
		return surface != SurfaceApp || !slices.ContainsFunc(p.Tags, func(t string) bool { return slices.Contains(appLoud, t) })
	}
	return []func(Palette) bool{
		func(p Palette) bool { return mode(p) && trade(p) && tag(p) && surf(p) },
		func(p Palette) bool { return mode(p) && trade(p) && surf(p) },
		func(p Palette) bool { return mode(p) && tag(p) && surf(p) },
		func(p Palette) bool { return mode(p) && surf(p) },
		mode,
		trade,
	}
}

func typeLevels(w want) []func(TypePair) bool {
	// strong：行业对应的大类上游权重 ≥ 2；some：> 0。
	kind := func(t TypePair, min int) bool {
		if len(w.kinds) == 0 {
			return true
		}
		for k := range w.kinds {
			if t.Kinds[k] >= min {
				return true
			}
		}
		return false
	}
	mood := func(t TypePair) bool { return anyIn(w.moods, t.Moods) }
	return []func(TypePair) bool{
		func(t TypePair) bool { return kind(t, 2) && mood(t) },
		func(t TypePair) bool { return kind(t, 2) },
		func(t TypePair) bool { return kind(t, 1) && mood(t) },
		func(t TypePair) bool { return kind(t, 1) },
		mood,
	}
}

func layoutLevels(w want) []func(Layout) bool {
	return []func(Layout) bool{func(l Layout) bool {
		if len(w.kinds) == 0 || len(l.Kinds) == 0 {
			return true
		}
		for k := range w.kinds {
			if l.Kinds[k] > 0 {
				return true
			}
		}
		return false
	}}
}

// pick 从 items 里抽 n 个不同下标：先按「第一个满足的放宽层级」分档（avoid 里的整体排到所有档之后），
// 每档内用 rng 洗牌，依次取。items 不足 n 个时返回全部。
func pick[T any](rng *rand.Rand, items []T, n int, avoided func(T) bool, levels []func(T) bool) []int {
	tiers := make([][]int, 2*(len(levels)+1))
	for i, it := range items {
		tier := len(levels)
		for l, ok := range levels {
			if ok(it) {
				tier = l
				break
			}
		}
		if avoided(it) {
			tier += len(levels) + 1
		}
		tiers[tier] = append(tiers[tier], i)
	}
	out := make([]int, 0, n)
	for _, t := range tiers {
		rng.Shuffle(len(t), func(a, b int) { t[a], t[b] = t[b], t[a] })
		for _, i := range t {
			if len(out) == n {
				return out
			}
			out = append(out, i)
		}
	}
	return out
}

type avoidSet struct{ palettes, types, layouts map[string]bool }

// parseAvoid 拆开 `<色板>/<字体对>/<版式>`；只有一段的条目对三类都生效。
func parseAvoid(avoid []string) avoidSet {
	s := avoidSet{palettes: map[string]bool{}, types: map[string]bool{}, layouts: map[string]bool{}}
	for _, id := range avoid {
		parts := strings.Split(strings.TrimSpace(id), "/")
		if len(parts) == 3 {
			s.palettes[parts[0]] = true
			s.types[parts[1]] = true
			s.layouts[parts[2]] = true
			continue
		}
		for _, p := range parts {
			if p != "" {
				s.palettes[p], s.types[p], s.layouts[p] = true, true, true
			}
		}
	}
	return s
}

// ValidateProposals 校验 app 面 agent 自拟的方向：恰好 3 个，每个结构合规，
// 且两两之间强调色色相差 ≥ 30° 或标题字体家族不同。错误包着 ErrInvalid，文字是给 agent 看的中文。
func ValidateProposals(ps []Direction) error {
	if len(ps) != Cards {
		return fmt.Errorf("%w: 需要恰好 %d 个方向，收到 %d 个", ErrInvalid, Cards, len(ps))
	}
	for i, d := range ps {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("%w: 方向 %d「%s」：%v", ErrInvalid, i+1, d.Name, err)
		}
	}
	for i := range ps {
		for j := i + 1; j < len(ps); j++ {
			a, b := ps[i], ps[j]
			if familyKey(a.Type.Display.Family) != familyKey(b.Type.Display.Family) {
				continue
			}
			gap := accentGap(a.Palette, b.Palette)
			if gap >= MinHueGap {
				continue
			}
			return fmt.Errorf("%w: 方向 %d「%s」与方向 %d「%s」太像：标题字体都是 %s，强调色色相只差 %.0f°；"+
				"把其中一张的强调色换到色相差 ≥ %.0f° 的颜色，或换一个标题字体家族",
				ErrInvalid, i+1, a.Name, j+1, b.Name, a.Type.Display.Family, gap, MinHueGap)
		}
	}
	return nil
}

// Validate 检查一张方向的结构：名称、4–6 个颜色（角色是小写标识符且不重复，含 bg/text/accent，
// 值是 #rgb 或 #rrggbb）、标题与正文字体家族。ThemeCSS 只对通过校验的方向产出可用的 CSS。
func (d Direction) Validate() error {
	if err := checkText("名称", d.Name, MaxNameRunes); err != nil {
		return err
	}
	if err := validatePalette(d.Palette); err != nil {
		return err
	}
	if err := validateType(d.Type); err != nil {
		return err
	}
	if strings.TrimSpace(d.Layout) == "" {
		return errors.New("layout 不能为空")
	}
	if n := len([]rune(d.Layout)); n > MaxLayoutRunes {
		return fmt.Errorf("layout 超过 %d 字", MaxLayoutRunes)
	}
	return nil
}

var (
	roleRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	hexRe  = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
)

func validatePalette(p Palette) error {
	if n := len(p.Colors); n < MinColors || n > MaxColors {
		return fmt.Errorf("色板要 %d–%d 个颜色，收到 %d 个", MinColors, MaxColors, n)
	}
	roles := map[string]bool{}
	for _, c := range p.Colors {
		if !roleRe.MatchString(c.Role) || len(c.Role) > 32 {
			return fmt.Errorf("颜色角色 %q 不合规（小写字母、数字与连字符，如 bg、surface、text、muted、accent）", c.Role)
		}
		if roles[c.Role] {
			return fmt.Errorf("颜色角色 %s 重复", c.Role)
		}
		roles[c.Role] = true
		if !hexRe.MatchString(c.Hex) {
			return fmt.Errorf("颜色 %s 的值 %q 不是 #rgb 或 #rrggbb", c.Role, c.Hex)
		}
	}
	for _, r := range RequiredRoles {
		if !roles[r] {
			return fmt.Errorf("色板缺少 %s 角色（必须有 %s）", r, strings.Join(RequiredRoles, "、"))
		}
	}
	return nil
}

func validateType(t TypePair) error {
	if err := checkText("标题字体家族", t.Display.Family, MaxNameRunes); err != nil {
		return err
	}
	if err := checkText("正文字体家族", t.Body.Family, MaxNameRunes); err != nil {
		return err
	}
	if t.Mono != nil {
		if err := checkText("等宽字体家族", t.Mono.Family, MaxNameRunes); err != nil {
			return err
		}
	}
	return nil
}

// checkText 要求非空、不超长，且不含会破坏 CSS 的字符（引号、反斜杠、分号、花括号、控制字符）。
func checkText(what, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("%s不能为空", what)
	}
	if len([]rune(s)) > max {
		return fmt.Errorf("%s超过 %d 字", what, max)
	}
	if strings.ContainsAny(s, "\"'\\;{}<>") || strings.ContainsFunc(s, unicode.IsControl) {
		return fmt.Errorf("%s %q 含有不允许的字符", what, s)
	}
	return nil
}

func familyKey(f string) string { return strings.ToLower(strings.Join(strings.Fields(f), " ")) }

// accentGap 是两色板强调色的色相差（0–180°）。两个都是无彩色视为 0（同样是黑白灰），
// 只有一个是无彩色视为 180（有色对无色，一眼可分）。
func accentGap(a, b Palette) float64 {
	ha, sa := hueSat(roleHex(a, "accent"))
	hb, sb := hueSat(roleHex(b, "accent"))
	switch ca, cb := sa >= achromaticSat, sb >= achromaticSat; {
	case !ca && !cb:
		return 0
	case ca != cb:
		return 180
	}
	d := math.Abs(ha - hb)
	return math.Min(d, 360-d)
}

func roleHex(p Palette, role string) string {
	for _, c := range p.Colors {
		if c.Role == role {
			return c.Hex
		}
	}
	return ""
}

// hueSat 返回 HSL 的色相（度）与饱和度（0–1）。hex 已由 validatePalette 保证合规。
func hueSat(hex string) (float64, float64) {
	r, g, b := parseHex(hex)
	mx, mn := max(r, g, b), min(r, g, b)
	l := (mx + mn) / 2
	d := mx - mn
	if d == 0 {
		return 0, 0
	}
	s := d / (1 - math.Abs(2*l-1))
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s
}

func parseHex(hex string) (float64, float64, float64) {
	h := strings.TrimPrefix(hex, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	var v [3]float64
	for i := range v {
		n, _ := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		v[i] = float64(n) / 255
	}
	return v[0], v[1], v[2]
}

// ThemeCSS 生成可直接写进设计目录 src/theme.css 的 Tailwind v4 @theme 块（不含 `@import "tailwindcss"`，
// 入口在引擎 styles.css，见探针 P2）。颜色按色板顺序写成 `--color-<role>`；标题字体写 `--font-display`，
// 正文覆盖 Tailwind 缺省的 `--font-sans`，有等宽字体时覆盖 `--font-mono`。只写 token，不负责加载字体文件。
// d 应先通过 Validate。
func ThemeCSS(d Direction) string {
	return tokenBlock("@theme {", d)
}

// ScopedThemeCSS 是方案主题文件 src/themes/<name>.css 的内容：一条 `:root[data-oryn-theme="<name>"] {…}` 规则，
// 声明与 ThemeCSS 完全相同（M34 C3）。规则不在任何 layer 里，覆盖 Tailwind @layer theme 里的基础 token。
// name 应先匹配 ^[a-z0-9][a-z0-9-]{0,31}$，d 应先通过 Validate。
func ScopedThemeCSS(name string, d Direction) string {
	return tokenBlock(`:root[data-oryn-theme="`+name+`"] {`, d)
}

// tokenBlock 写 `<open>` 起头、`}` 收尾的 token 声明块，ThemeCSS 与 ScopedThemeCSS 共用。
func tokenBlock(open string, d Direction) string {
	var b strings.Builder
	b.WriteString(open + "\n")
	for _, c := range d.Palette.Colors {
		fmt.Fprintf(&b, "  --color-%s: %s;\n", c.Role, strings.ToLower(c.Hex))
	}
	display := genericFor(d.Type.Class)
	body := "sans-serif"
	if familyKey(d.Type.Body.Family) == familyKey(d.Type.Display.Family) {
		body = display
	}
	fmt.Fprintf(&b, "  --font-display: %s;\n", fontStack(d.Type.Display.Family, display))
	fmt.Fprintf(&b, "  --font-sans: %s;\n", fontStack(d.Type.Body.Family, body))
	if d.Type.Mono != nil {
		fmt.Fprintf(&b, "  --font-mono: %s;\n", fontStack(d.Type.Mono.Family, "monospace"))
	}
	b.WriteString("}\n")
	return b.String()
}

func genericFor(class string) string {
	switch class {
	case "serif", "slab":
		return "serif"
	}
	return "sans-serif"
}

var fallbacks = map[string]string{
	"sans-serif": `ui-sans-serif, system-ui, sans-serif`,
	"serif":      `ui-serif, Georgia, serif`,
	"monospace":  `ui-monospace, SFMono-Regular, Menlo, monospace`,
}

func fontStack(family, generic string) string {
	return `"` + strings.TrimSpace(family) + `", ` + fallbacks[generic]
}
