package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

func dealtOf(t *testing.T, e *env, id string) []design.Direction {
	t.Helper()
	row, err := e.st.Q.GetArtifactDesign(context.Background(), id)
	require.NoError(t, err)
	var out []design.Direction
	require.NoError(t, json.Unmarshal([]byte(row.Dealt), &out))
	return out
}

func dealDetails(t *testing.T, r Result) []design.Direction {
	t.Helper()
	cards, ok := r.Details.(obj)["directions"].([]design.Direction)
	require.True(t, ok, "details 带结构化的 directions")
	return cards
}

// M29 Q2：landing 由方向库发 3 张并写进 dealt（完整对象、最新在后、只留 9 张）与 surface；连发三次 avoid 生效
// （9 张牌的色板、字体对两两不同）；第四次后 dealt 仍是最近 9 张。
func TestDesignDirectionsLanding(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	c := threadCaller("thr_a")

	r := mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing","industry":"saas","tone":"calm"}`)
	first := dealDetails(t, r)
	require.Len(t, first, 3)
	for _, s := range []string{"1. ", "2. ", "3. ", "色板", "字体：", "版式：", "design_direction"} {
		assert.Contains(t, r.Text, s)
	}
	assert.Equal(t, first, dealtOf(t, e, id))
	row, err := e.st.Q.GetArtifactDesign(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "landing", deref(row.Surface))

	var all []design.Direction
	all = append(all, first...)
	for range 2 {
		e.clk.add(1)
		all = append(all, dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))...)
	}
	assert.Equal(t, all, dealtOf(t, e, id))
	palettes, types := map[string]bool{}, map[string]bool{}
	for _, d := range all {
		require.NotEmpty(t, d.ID)
		palettes[d.Palette.ID] = true
		types[d.Type.ID] = true
	}
	assert.Len(t, palettes, 9, "连发三次色板不重复")
	assert.Len(t, types, 9, "连发三次字体对不重复")

	e.clk.add(1)
	fourth := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))
	got := dealtOf(t, e, id)
	require.Len(t, got, dealtKeep)
	assert.Equal(t, append(all[3:], fourth...), got)

	// landing 不收 proposals；surface 必须认识
	_, err = agentCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing","proposals":[]}`)
	assert.NoError(t, err, "空 proposals 等同不给")
	_, err = agentCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"poster"}`)
	assert.Equal(t, CodeInvalid, code(err))
}

func proposal(name, accent, display string) map[string]any {
	return map[string]any{
		"name": name,
		"palette": map[string]any{"colors": []map[string]string{
			{"role": "bg", "hex": "#ffffff"}, {"role": "surface", "hex": "#f4f4f4"}, {"role": "text", "hex": "#111111"}, {"role": "accent", "hex": accent},
		}},
		"type":   map[string]any{"display": map[string]any{"family": display}, "body": map[string]any{"family": "Inter"}},
		"layout": "侧栏导航，主区表格",
	}
}

func proposalsParams(t *testing.T, id string, ps ...map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"id": id, "surface": "app", "proposals": ps})
	require.NoError(t, err)
	return string(b)
}

// M29 Q2：app 面校验 agent 自拟的 3 张：太像（同标题字体、强调色色相差 < 30°）被拒且不写 dealt；不够 3 张、
// 缺 proposals 被拒；合规的 3 张写进 dealt。
func TestDesignDirectionsApp(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	c := threadCaller("thr_a")

	_, err := agentCall(t, e.s, c, OpDesignDirections, proposalsParams(t, id,
		proposal("蓝", "#1a4be0", "Geist"), proposal("靛", "#2a3be0", "Geist"), proposal("橙", "#e07a1a", "Geist")))
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "太像")
	_, err = e.st.Q.GetArtifactDesign(context.Background(), id)
	assert.Error(t, err, "被拒时不写 dealt")

	_, err = agentCall(t, e.s, c, OpDesignDirections, proposalsParams(t, id, proposal("蓝", "#1a4be0", "Geist")))
	assert.Equal(t, CodeInvalid, code(err))
	_, err = agentCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"app"}`)
	assert.Equal(t, CodeInvalid, code(err))

	r := mustCall(t, e.s, c, OpDesignDirections, proposalsParams(t, id,
		proposal("蓝", "#1a4be0", "Geist"), proposal("橙", "#e07a1a", "Geist"), proposal("绿", "#1ae07a", "Fraunces")))
	assert.Len(t, dealDetails(t, r), 3)
	got := dealtOf(t, e, id)
	require.Len(t, got, 3)
	assert.Equal(t, "橙", got[1].Name)
	assert.Contains(t, r.Text, "侧栏导航，主区表格")
}

// M29 Q2：design_direction 只能在最近一次发出的 3 张里挑（pick 1–3），写进 direction；设计画布回 @theme，
// HTML 画框画布回 :root；没发过牌、pick 越界报 invalid；HTML 页、文档不是画布报 invalid。
func TestDesignDirectionPick(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	c := threadCaller("thr_a")

	_, err := agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":1}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "design_directions")

	old := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))
	e.clk.add(1)
	latest := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))
	for _, p := range []string{"0", "4", "-1"} {
		_, err := agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":`+p+`}`)
		assert.Equal(t, CodeInvalid, code(err), p)
	}
	r := mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":2}`)
	assert.Contains(t, r.Text, design.ThemeCSS(latest[1]))
	assert.Contains(t, r.Text, "src/theme.css")
	st, err := e.s.Design(context.Background(), id)
	require.NoError(t, err)
	var picked design.Direction
	require.NoError(t, json.Unmarshal(st.Direction, &picked))
	assert.Equal(t, latest[1], picked, "只在最近一次的牌里挑")
	assert.NotEqual(t, old[1].ID, picked.ID)
	row, err := e.st.Q.GetArtifactDesign(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "landing", deref(row.Surface), "选定方向不清掉 surface")

	// HTML 画框画布：同一组 token 写成 :root
	doc := canvas.Doc{Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	h, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Files: []FileChange{put(EntryCanvas, string(doc.JSON()))}})
	require.NoError(t, err)
	cards := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+h.ID+`","surface":"landing"}`))
	r = mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+h.ID+`","pick":3}`)
	assert.Contains(t, r.Text, ":root {")
	assert.NotContains(t, r.Text, "@theme")
	assert.Contains(t, r.Text, "<style>")
	assert.Contains(t, r.Text, rootCSS(cards[2]))
	assert.Contains(t, rootCSS(cards[2]), "--color-accent:")

	// 不是画布：HTML 页
	page, err := e.s.Create(context.Background(), CreateParams{Title: "页", ThreadID: "thr_a", Files: []FileChange{put(EntryHTML, "<p>x</p>")}})
	require.NoError(t, err)
	for op, params := range map[string]string{
		OpDesignDirections: `{"id":"` + page.ID + `","surface":"landing"}`,
		OpDesignDirection:  `{"id":"` + page.ID + `","pick":1}`,
	} {
		_, err := agentCall(t, e.s, c, op, params)
		require.Equal(t, CodeInvalid, code(err), op)
		assert.Contains(t, err.Error(), "不是画布", op)
	}
	// 范围外的 artifact：not_found
	mkThread(t, e.st, "thr_b", "")
	_, err = agentCall(t, e.s, threadCaller("thr_b"), OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`)
	assert.Equal(t, CodeNotFound, code(err))
}

// themesOf 按写入顺序读 artifact_designs.themes。
func themesOf(t *testing.T, e *env, id string) []designTheme {
	t.Helper()
	row, err := e.st.Q.GetArtifactDesign(context.Background(), id)
	require.NoError(t, err)
	out, err := parseThemes(row.Themes)
	require.NoError(t, err)
	return out
}

// M34 C3：pick + theme 把牌记成方案主题（作用域规则写进 src/themes/<theme>.css），不动主方向；只有方案主题时
// design_head 的 aesthetic 也为真；重写同名主题移到末尾，超过 12 项淘汰最早写入的；参数组合不对回 invalid。
func TestDesignDirectionTheme(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	c := threadCaller("thr_a")
	internal := c
	internal.Internal = true

	_, err := agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`"}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "pick 或 theme")

	cards := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))
	for _, theme := range []string{"Calm", "-calm", "calm_b", "a234567890123456789012345678901234"} {
		_, err := agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":1,"theme":"`+theme+`"}`)
		assert.Equal(t, CodeInvalid, code(err), theme)
	}
	_, err = agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":4,"theme":"calm"}`)
	assert.Equal(t, CodeInvalid, code(err))

	r := mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":1,"theme":"calm"}`)
	scoped := design.ScopedThemeCSS("calm", cards[0])
	assert.True(t, strings.HasPrefix(scoped, `:root[data-oryn-theme="calm"] {`+"\n"))
	assert.Equal(t, strings.TrimPrefix(design.ThemeCSS(cards[0]), "@theme {"), strings.TrimPrefix(scoped, `:root[data-oryn-theme="calm"] {`), "声明与 ThemeCSS 相同")
	assert.Contains(t, r.Text, scoped)
	assert.Contains(t, r.Text, "src/themes/calm.css")
	assert.Contains(t, r.Text, `theme: "calm"`, "说明之后怎么提升")
	assert.NotContains(t, r.Text, "@theme")
	st, err := e.s.Design(context.Background(), id)
	require.NoError(t, err)
	assert.Nil(t, st.Direction, "记方案主题不动主方向")
	assert.Equal(t, []designTheme{{Name: "calm", Direction: cards[0]}}, themesOf(t, e, id))

	// 只有方案主题、没有主方向：design_head 的审美规则照样打开
	head := orbsock.DesignHead{}
	require.NoError(t, json.Unmarshal([]byte(mustCall(t, e.s, internal, OpDesignHead, `{"id":"`+id+`"}`).Text), &head))
	assert.True(t, head.Aesthetic)
	assert.Equal(t, "landing", head.Surface)

	// 重写同名主题移到末尾
	mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":2,"theme":"warm"}`)
	mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":3,"theme":"calm"}`)
	assert.Equal(t, []designTheme{{Name: "warm", Direction: cards[1]}, {Name: "calm", Direction: cards[2]}}, themesOf(t, e, id))
	// 超过 12 项淘汰最早写入的
	for i := 1; i <= 11; i++ {
		mustCall(t, e.s, c, OpDesignDirection, fmt.Sprintf(`{"id":"%s","pick":1,"theme":"t%d"}`, id, i))
	}
	got := themesOf(t, e, id)
	require.Len(t, got, 12)
	assert.Equal(t, "calm", got[0].Name, "warm 最早写入，被淘汰")
	assert.Equal(t, "t11", got[11].Name)

	// HTML 画框画布：:root 文本只放进这个方案的画框
	doc := canvas.Doc{Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	h, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Files: []FileChange{put(EntryCanvas, string(doc.JSON()))}})
	require.NoError(t, err)
	hc := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+h.ID+`","surface":"landing"}`))
	r = mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+h.ID+`","pick":2,"theme":"b"}`)
	assert.Contains(t, r.Text, rootCSS(hc[1]))
	assert.Contains(t, r.Text, "这个方案每个画框")
	assert.NotContains(t, r.Text, "data-oryn-theme")
	assert.Equal(t, []designTheme{{Name: "b", Direction: hc[1]}}, themesOf(t, e, h.ID))
}

// M34 C3：只给 theme 把记下的方案主题复制成主方向（回与 pick 相同的 token 文本）；没有记录的主题回 invalid。
func TestDesignDirectionPromote(t *testing.T) {
	e := newEnv(t)
	mkThread(t, e.st, "thr_a", "")
	id := mkEmptyDesign(t, e, "thr_a")
	c := threadCaller("thr_a")

	_, err := agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","theme":"calm"}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), "这个主题不是来自方向牌，把它的变量直接抄进 src/theme.css 即可")

	cards := dealDetails(t, mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`))
	mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","pick":2,"theme":"calm"}`)
	e.clk.add(1)
	mustCall(t, e.s, c, OpDesignDirections, `{"id":"`+id+`","surface":"landing"}`)
	_, err = agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","theme":"warm"}`)
	assert.Equal(t, CodeInvalid, code(err))

	r := mustCall(t, e.s, c, OpDesignDirection, `{"id":"`+id+`","theme":"calm"}`)
	assert.Contains(t, r.Text, design.ThemeCSS(cards[1]), "提升的是记下的那张牌，不是最近一次发的牌")
	assert.Contains(t, r.Text, "src/theme.css")
	assert.NotContains(t, r.Text, "data-oryn-theme")
	st, err := e.s.Design(context.Background(), id)
	require.NoError(t, err)
	var picked design.Direction
	require.NoError(t, json.Unmarshal(st.Direction, &picked))
	assert.Equal(t, cards[1], picked)
	assert.Equal(t, []designTheme{{Name: "calm", Direction: cards[1]}}, themesOf(t, e, id), "提升不改 themes")
	row, err := e.st.Q.GetArtifactDesign(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "landing", deref(row.Surface), "提升不清掉 surface")

	// HTML 画框画布：没有记录的主题指向 :root
	doc := canvas.Doc{Canvases: []canvas.Canvas{{ID: canvas.DefaultCanvasID, Name: canvas.DefaultCanvasName}}}
	h, err := e.s.Create(context.Background(), CreateParams{Title: "画布", ThreadID: "thr_a", Files: []FileChange{put(EntryCanvas, string(doc.JSON()))}})
	require.NoError(t, err)
	_, err = agentCall(t, e.s, c, OpDesignDirection, `{"id":"`+h.ID+`","theme":"calm"}`)
	require.Equal(t, CodeInvalid, code(err))
	assert.Contains(t, err.Error(), ":root")
}
