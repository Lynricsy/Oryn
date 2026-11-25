package artifacts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
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
