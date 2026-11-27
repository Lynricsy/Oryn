package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 方向发牌（M29 Q2，docs/plan-m29.md C5）：design_directions 发 3 张方向牌，design_direction 在最近一次发出的牌里
// 选定一张。两者对任意画布 artifact 可用：设计画布把选定方向写成 src/theme.css 的 @theme 块，HTML 画框画布（助手、
// Runner 只能做这种）写成页面 <style> 里的 :root 变量；HTML 页、文档、幻灯片回 invalid。
// artifact_designs.dealt 存最近 dealtKeep 张发出的牌（完整 Direction 对象的 JSON 数组，最新在后），每次发牌追加 3 张，
// 所以最后 3 张就是最近一次发出的牌；avoid 取它们的 ID。
// 方案组（M34 C3）：design_direction 带 theme 时把牌记成方案主题（artifact_designs.themes，见 designTheme），不动主方向；
// 只带 theme 时把记下的方案主题提升为主方向。

// dealtKeep 是 dealt 保留的张数（同时是发牌的 avoid 窗口）。
const dealtKeep = 9

// themesKeep 是 themes 保留的方案主题数，超出时淘汰最早写入的。
const themesKeep = 12

// themeNameRe 是方案主题名（与 src/variants.json 的 theme、src/themes/<名>.css 的文件名同一规则）。
var themeNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type directionsArgs struct {
	ID        string             `json:"id"`
	Surface   string             `json:"surface"`
	Industry  string             `json:"industry"`
	Tone      string             `json:"tone"`
	Proposals []design.Direction `json:"proposals"`
}

type directionArgs struct {
	ID    string `json:"id"`
	Pick  *int   `json:"pick"`
	Theme string `json:"theme"`
}

// designTheme 是 artifact_designs.themes 的一项。列存成 JSON 对象 {主题名: Direction}，键按写入先后排列（最早在前，
// 重写同名主题移到末尾），所以「最早写入」就是对象的第一个键；没有方案主题时是 '{}'。
type designTheme struct {
	Name      string
	Direction design.Direction
}

// parseThemes 按键顺序读 themes 列。
func parseThemes(raw string) ([]designTheme, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("themes 不是 JSON 对象")
	}
	var out []designTheme
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		t := designTheme{Name: tok.(string)}
		if err := dec.Decode(&t.Direction); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// themesJSON 按切片顺序写回 themes 列。
func themesJSON(ts []designTheme) (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, t := range ts {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(t.Name)
		v, err := json.Marshal(t.Direction)
		if err != nil {
			return "", err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.String(), nil
}

// designAesthetic 是探针的审美开关：选定了主方向，或记下了方案主题（withProbe 与 design_head 同口径）。
func designAesthetic(row db.ArtifactDesign) bool {
	return deref(row.Direction) != "" || row.Themes != "{}"
}

// directionCanvas 取调用方范围内的画布 artifact（回收站里的报 deleted），并报告它是不是设计画布。
func (s *Service) directionCanvas(ctx context.Context, c Caller, id, op string) (Artifact, bool, error) {
	a, err := s.lookup(ctx, c, id, true)
	if err != nil {
		return Artifact{}, false, err
	}
	if a.Kind != KindCanvas || a.Type != "" {
		return Artifact{}, false, opErr(CodeInvalid, "%s 不是画布，%s 只用于画布 artifact（设计画布或 HTML 画框画布）。", a.ID, op)
	}
	isDesign, err := s.isDesignCanvas(ctx, a)
	if err != nil {
		return Artifact{}, false, err
	}
	return a, isDesign, nil
}

// dealtCards 读 artifact_designs.dealt（没有行时为空）。
func (s *Service) dealtCards(ctx context.Context, id string) ([]design.Direction, error) {
	row, err := s.st.Q.GetArtifactDesign(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []design.Direction
	if err := json.Unmarshal([]byte(row.Dealt), &out); err != nil {
		return nil, fmt.Errorf("artifacts: %s 的 dealt 解析失败: %w", id, err)
	}
	return out, nil
}

// agentDesignDirections 是 design_directions：landing 由方向库发 3 张（避开最近发过的），app 校验 agent 自拟的 3 张；
// 发出的牌追加进 dealt（只留最近 9 张）并记下 surface。
func (s *Service) agentDesignDirections(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p directionsArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, _, err := s.directionCanvas(ctx, c, p.ID, OpDesignDirections)
	if err != nil {
		return Result{}, err
	}
	dealt, err := s.dealtCards(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	var cards []design.Direction
	switch p.Surface {
	case design.SurfaceLanding:
		if len(p.Proposals) > 0 {
			return Result{}, opErr(CodeInvalid, "surface 为 landing 时由方向库发牌，不收 proposals；要自拟方向用 surface: \"app\"。")
		}
		avoid := make([]string, 0, len(dealt))
		for _, d := range dealt {
			avoid = append(avoid, d.ID)
		}
		got, err := design.Deal(design.Default(), p.Surface, p.Industry, p.Tone, s.now().UnixNano(), avoid)
		if err != nil {
			return Result{}, err
		}
		cards = got[:]
	case design.SurfaceApp:
		if len(p.Proposals) == 0 {
			return Result{}, opErr(CodeInvalid, "surface 为 app 时要你自拟恰好 %d 个方向放进 proposals（每个 {name, palette: {colors: [{role, hex}]}, type: {display: {family}, body: {family}}, layout}），由 Oryn 校验它们够不够不同。", design.Cards)
		}
		if err := design.ValidateProposals(p.Proposals); err != nil {
			return Result{}, opErr(CodeInvalid, "%s。改好后重新调用 design_directions。", strings.TrimPrefix(err.Error(), design.ErrInvalid.Error()+": "))
		}
		cards = p.Proposals
	default:
		return Result{}, opErr(CodeInvalid, "surface 只能是 landing（落地页、品牌页、发布页）或 app（产品界面、后台、工具），收到 %q。", p.Surface)
	}
	dealt = append(dealt, cards...)
	if n := len(dealt); n > dealtKeep {
		dealt = dealt[n-dealtKeep:]
	}
	b, err := json.Marshal(dealt)
	if err != nil {
		return Result{}, err
	}
	surface := p.Surface
	if err := s.st.Q.SetArtifactDesignDealt(ctx, db.SetArtifactDesignDealtParams{ArtifactID: a.ID, Surface: &surface, Dealt: string(b)}); err != nil {
		return Result{}, err
	}
	return Result{
		Text:    directionsText(p.Surface, cards),
		Details: obj{"op": OpDesignDirections, "id": a.ID, "title": a.Title, "kind": a.Kind, "surface": p.Surface, "directions": cards},
	}, nil
}

// directionsText 把 3 张牌写成给 agent 的文本：编号、名称、色板、字体、版式、一句说明，末尾说明下一步。
func directionsText(surface string, cards []design.Direction) string {
	lib := design.Default()
	var b strings.Builder
	fmt.Fprintf(&b, "发出 %d 张方向牌（%s）：\n", len(cards), surface)
	for i, d := range cards {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, d.Name)
		colors := make([]string, 0, len(d.Palette.Colors))
		for _, c := range d.Palette.Colors {
			colors = append(colors, c.Role+" "+strings.ToLower(c.Hex))
		}
		mode := ""
		if d.Palette.Mode != "" {
			mode = "（" + d.Palette.Mode + "）"
		}
		fmt.Fprintf(&b, "   色板%s：%s\n", mode, strings.Join(colors, "、"))
		fonts := "标题 " + d.Type.Display.Family + " / 正文 " + d.Type.Body.Family
		if d.Type.Mono != nil {
			fonts += " / 等宽 " + d.Type.Mono.Family
		}
		fmt.Fprintf(&b, "   字体：%s\n", fonts)
		if l, ok := lib.LayoutByID(d.Layout); ok && surface == design.SurfaceLanding {
			fmt.Fprintf(&b, "   版式：%s（%s）\n", l.Name, firstSentence(l.Note))
		} else {
			fmt.Fprintf(&b, "   版式：%s\n", d.Layout)
		}
		if note := firstSentence(d.Palette.Note); note != "" {
			fmt.Fprintf(&b, "   说明：%s\n", note)
		} else if d.Type.Suits != "" {
			fmt.Fprintf(&b, "   说明：字体适合 %s\n", d.Type.Suits)
		}
	}
	b.WriteString("\n把这几张牌讲给用户：推荐其中一张并说明理由；用户选定（或说「你定」）后调用 design_direction {id, pick: 编号}。只能在这次发出的牌里挑，都不合适就再发一次。")
	return b.String()
}

// firstSentence 取说明的第一句（英文按 ". "、中文按「。」断开）。
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	if i := strings.Index(s, "。"); i >= 0 {
		s = s[:i+len("。")]
	}
	return s
}

// agentDesignDirection 是 design_direction（M34 C3 三种用法）：只给 pick 在最近一次发出的 3 张牌里选第 pick 张，写进
// artifact_designs.direction；pick + theme 把这张牌记成方案主题 themes[theme]，不动 direction；只给 theme 把记下的方案主题
// 复制成 direction（提升）。都回可直接用的 token 文本（设计画布是 @theme 或作用域规则，HTML 画框画布是 :root）。
func (s *Service) agentDesignDirection(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p directionArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, isDesign, err := s.directionCanvas(ctx, c, p.ID, OpDesignDirection)
	if err != nil {
		return Result{}, err
	}
	if p.Pick == nil && p.Theme == "" {
		return Result{}, opErr(CodeInvalid, "design_direction 要给 pick 或 theme：只给 pick 选定主方向；pick + theme 把这张牌记成方案主题（方案组里各方案各用一张牌时）；只给 theme 把记下的方案主题提升为主方向。")
	}
	if p.Theme != "" && !themeNameRe.MatchString(p.Theme) {
		return Result{}, opErr(CodeInvalid, "theme 是方案主题名，要匹配 ^[a-z0-9][a-z0-9-]{0,31}$（与 src/variants.json 里方案的 theme 相同），收到 %q。", p.Theme)
	}
	var row db.ArtifactDesign
	themes := []designTheme{}
	switch got, err := s.st.Q.GetArtifactDesign(ctx, a.ID); {
	case err == nil:
		row = got
		if themes, err = parseThemes(row.Themes); err != nil {
			return Result{}, fmt.Errorf("artifacts: %s 的 themes 解析失败: %w", a.ID, err)
		}
	case !errors.Is(err, sql.ErrNoRows):
		return Result{}, err
	}
	details := obj{"op": OpDesignDirection, "id": a.ID, "title": a.Title, "kind": a.Kind, "design": isDesign}

	// 只给 theme：提升
	if p.Pick == nil {
		i := slices.IndexFunc(themes, func(t designTheme) bool { return t.Name == p.Theme })
		if i < 0 {
			if isDesign {
				return Result{}, opErr(CodeInvalid, "方案主题 %q 没有记录：这个主题不是来自方向牌，把它的变量直接抄进 src/theme.css 即可。", p.Theme)
			}
			return Result{}, opErr(CodeInvalid, "方案主题 %q 没有记录：这个主题不是来自方向牌，把它的变量直接抄进每个画框页面 <style> 的 :root 即可。", p.Theme)
		}
		d := themes[i].Direction
		if err := s.setDirection(ctx, a.ID, row.Surface, d); err != nil {
			return Result{}, err
		}
		details["theme"], details["direction"] = p.Theme, d
		return Result{Text: pickedText(isDesign, fmt.Sprintf("已把方案主题 %s（方向「%s」）提升为主方向。", p.Theme, d.Name), d), Details: details}, nil
	}

	pick := *p.Pick
	if pick < 1 || pick > design.Cards {
		return Result{}, opErr(CodeInvalid, "pick 是最近一次 design_directions 发出的牌的编号（1–%d），收到 %d。", design.Cards, pick)
	}
	dealt, err := s.dealtCards(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	if len(dealt) < design.Cards {
		return Result{}, opErr(CodeInvalid, "%s 还没有发过方向牌。先调用 design_directions，再在发出的牌里挑。", a.ID)
	}
	d := dealt[len(dealt)-design.Cards+pick-1]
	details["pick"], details["direction"] = pick, d

	// 只给 pick：选定主方向
	if p.Theme == "" {
		if err := s.setDirection(ctx, a.ID, row.Surface, d); err != nil {
			return Result{}, err
		}
		return Result{Text: pickedText(isDesign, fmt.Sprintf("已选定方向 %d「%s」。", pick, d.Name), d), Details: details}, nil
	}

	// pick + theme：记成方案主题（重写同名主题移到末尾，超出 themesKeep 淘汰最早写入的），不动主方向
	themes = slices.DeleteFunc(themes, func(t designTheme) bool { return t.Name == p.Theme })
	themes = append(themes, designTheme{Name: p.Theme, Direction: d})
	if n := len(themes); n > themesKeep {
		themes = themes[n-themesKeep:]
	}
	raw, err := themesJSON(themes)
	if err != nil {
		return Result{}, err
	}
	if err := s.st.Q.SetArtifactDesignThemes(ctx, db.SetArtifactDesignThemesParams{ArtifactID: a.ID, Themes: raw}); err != nil {
		return Result{}, err
	}
	details["theme"] = p.Theme
	var text string
	if isDesign {
		text = fmt.Sprintf("已把方向 %d「%s」记为方案主题 %s，主方向不变。把下面整段写进工作副本的 src/themes/%s.css（这个文件只放这一条规则），并在 src/variants.json 里把这个方案的 theme 写成 %q；src/theme.css 仍是基础主题，不要改。方案的画框照用 bg-bg、text-text、bg-accent、font-display 这类 token 类，不写颜色字面量：\n\n%s\n字体文件不随 token 加载：在 src/package.json 加 @fontsource/<字体> 并在 _layout.tsx 里 import，或接受栈里的系统字体兜底。用户选定方案后，调用 design_direction {id, theme: %q} 把它提升为主方向。",
			pick, d.Name, p.Theme, p.Theme, p.Theme, design.ScopedThemeCSS(p.Theme, d), p.Theme)
	} else {
		text = fmt.Sprintf("已把方向 %d「%s」记为方案主题 %s，主方向不变。这是 HTML 画框画布：把下面这段放进这个方案每个画框页面的 <style>（其他方案的画框不放），之后用 var(--color-accent)、var(--font-display) 这类变量，不写颜色字面量：\n\n%s\n字体文件不随变量加载：按 read_artifact_guide（topic: files）列出的 CDN 加载，加载不到时栈里的系统字体兜底。用户选定方案后，调用 design_direction {id, theme: %q} 把它提升为主方向。",
			pick, d.Name, p.Theme, rootCSS(d), p.Theme)
	}
	return Result{Text: text, Details: details}, nil
}

// setDirection 把 d 写成画布的主方向（surface 不变）。
func (s *Service) setDirection(ctx context.Context, id string, surface *string, d design.Direction) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	direction := string(b)
	return s.st.Q.SetArtifactDesignDirection(ctx, db.SetArtifactDesignDirectionParams{ArtifactID: id, Surface: surface, Direction: &direction})
}

// pickedText 是主方向写定后（选定或提升）的回复：lead 之后是可直接用的 token 文本，设计画布写进 src/theme.css，
// HTML 画框画布放进每个画框页面的 <style>。
func pickedText(isDesign bool, lead string, d design.Direction) string {
	if isDesign {
		return lead + fmt.Sprintf("把下面整段写进工作副本的 src/theme.css（这个文件只放这一个 @theme 块），之后用 bg-bg、text-text、bg-accent、font-display 这类 token 类，不写颜色字面量：\n\n%s\n字体文件不随 token 加载：在 src/package.json 加 @fontsource/<字体> 并在 _layout.tsx 里 import，或接受栈里的系统字体兜底。",
			design.ThemeCSS(d))
	}
	return lead + fmt.Sprintf("这是 HTML 画框画布：把下面这段放进每个画框页面的 <style>，之后用 var(--color-accent)、var(--font-display) 这类变量，不写颜色字面量：\n\n%s\n字体文件不随变量加载：按 read_artifact_guide（topic: files）列出的 CDN 加载，加载不到时栈里的系统字体兜底。",
		rootCSS(d))
}

// rootCSS 是 HTML 画框画布用的同一组 token：把 design.ThemeCSS 的 `@theme {` 换成 `:root {`，变量名与取值不变。
func rootCSS(d design.Direction) string {
	return ":root {" + strings.TrimPrefix(design.ThemeCSS(d), "@theme {")
}
