package artifacts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 方向发牌（M29 Q2，docs/plan-m29.md C5）：design_directions 发 3 张方向牌，design_direction 在最近一次发出的牌里
// 选定一张。两者对任意画布 artifact 可用：设计画布把选定方向写成 src/theme.css 的 @theme 块，HTML 画框画布（助手、
// Runner 只能做这种）写成页面 <style> 里的 :root 变量；HTML 页、文档、幻灯片回 invalid。
// artifact_designs.dealt 存最近 dealtKeep 张发出的牌（完整 Direction 对象的 JSON 数组，最新在后），每次发牌追加 3 张，
// 所以最后 3 张就是最近一次发出的牌；avoid 取它们的 ID。

// dealtKeep 是 dealt 保留的张数（同时是发牌的 avoid 窗口）。
const dealtKeep = 9

type directionsArgs struct {
	ID        string             `json:"id"`
	Surface   string             `json:"surface"`
	Industry  string             `json:"industry"`
	Tone      string             `json:"tone"`
	Proposals []design.Direction `json:"proposals"`
}

type directionArgs struct {
	ID   string `json:"id"`
	Pick int    `json:"pick"`
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

// agentDesignDirection 是 design_direction：在最近一次发出的 3 张牌里选第 pick 张，写进 artifact_designs.direction，
// 回可直接用的 token 文本（设计画布是 @theme，HTML 画框画布是 :root）。
func (s *Service) agentDesignDirection(ctx context.Context, c Caller, params json.RawMessage) (Result, error) {
	var p directionArgs
	if err := decodeParams(params, &p); err != nil {
		return Result{}, err
	}
	a, isDesign, err := s.directionCanvas(ctx, c, p.ID, OpDesignDirection)
	if err != nil {
		return Result{}, err
	}
	if p.Pick < 1 || p.Pick > design.Cards {
		return Result{}, opErr(CodeInvalid, "pick 是最近一次 design_directions 发出的牌的编号（1–%d），收到 %d。", design.Cards, p.Pick)
	}
	dealt, err := s.dealtCards(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	if len(dealt) < design.Cards {
		return Result{}, opErr(CodeInvalid, "%s 还没有发过方向牌。先调用 design_directions，再在发出的牌里挑。", a.ID)
	}
	d := dealt[len(dealt)-design.Cards+p.Pick-1]
	b, err := json.Marshal(d)
	if err != nil {
		return Result{}, err
	}
	row, err := s.st.Q.GetArtifactDesign(ctx, a.ID)
	if err != nil {
		return Result{}, err
	}
	direction := string(b)
	if err := s.st.Q.SetArtifactDesignDirection(ctx, db.SetArtifactDesignDirectionParams{ArtifactID: a.ID, Surface: row.Surface, Direction: &direction}); err != nil {
		return Result{}, err
	}
	var text string
	if isDesign {
		text = fmt.Sprintf("已选定方向 %d「%s」。把下面整段写进工作副本的 src/theme.css（这个文件只放这一个 @theme 块），之后用 bg-bg、text-text、bg-accent、font-display 这类 token 类，不写颜色字面量：\n\n%s\n字体文件不随 token 加载：在 src/package.json 加 @fontsource/<字体> 并在 _layout.tsx 里 import，或接受栈里的系统字体兜底。",
			p.Pick, d.Name, design.ThemeCSS(d))
	} else {
		text = fmt.Sprintf("已选定方向 %d「%s」。这是 HTML 画框画布：把下面这段放进每个画框页面的 <style>，之后用 var(--color-accent)、var(--font-display) 这类变量，不写颜色字面量：\n\n%s\n字体文件不随变量加载：按 read_artifact_guide（topic: files）列出的 CDN 加载，加载不到时栈里的系统字体兜底。",
			p.Pick, d.Name, rootCSS(d))
	}
	return Result{
		Text:    text,
		Details: obj{"op": OpDesignDirection, "id": a.ID, "title": a.Title, "kind": a.Kind, "pick": p.Pick, "design": isDesign, "direction": d},
	}, nil
}

// rootCSS 是 HTML 画框画布用的同一组 token：把 design.ThemeCSS 的 `@theme {` 换成 `:root {`，变量名与取值不变。
func rootCSS(d design.Direction) string {
	return ":root {" + strings.TrimPrefix(design.ThemeCSS(d), "@theme {")
}
