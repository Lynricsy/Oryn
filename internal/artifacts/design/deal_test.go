package design

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ids(ds [Cards]Direction) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.ID
	}
	return out
}

func assertDistinct(t *testing.T, ds [Cards]Direction) {
	t.Helper()
	pal, typ := map[string]bool{}, map[string]bool{}
	for _, d := range ds {
		assert.False(t, pal[d.Palette.ID], "色板重复：%s", d.Palette.ID)
		assert.False(t, typ[d.Type.ID], "字体对重复：%s", d.Type.ID)
		pal[d.Palette.ID], typ[d.Type.ID] = true, true
		require.NoError(t, d.Validate(), d.ID)
	}
}

func TestDefaultLibrary(t *testing.T) {
	lib := Default()
	assert.Len(t, lib.Palettes, 40)
	assert.Len(t, lib.Types, 20)
	assert.NotEmpty(t, lib.Source.Commit)
	for _, s := range []string{SurfaceLanding, SurfaceApp} {
		assert.True(t, slices.ContainsFunc(lib.Layouts, func(l Layout) bool { return l.Surface == s }), s)
	}
}

func TestDealDeterministic(t *testing.T) {
	lib := Default()
	a, err := Deal(lib, SurfaceLanding, "fintech", "calm", 42, []string{"white-link-blue/geist/split"})
	require.NoError(t, err)
	b, err := Deal(lib, SurfaceLanding, "fintech", "calm", 42, []string{"white-link-blue/geist/split"})
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assertDistinct(t, a)
	for _, d := range a {
		l, ok := lib.LayoutByID(d.Layout)
		require.True(t, ok)
		assert.Equal(t, SurfaceLanding, l.Surface)
	}
}

func TestDealSeedVaries(t *testing.T) {
	lib := Default()
	seen := map[string]bool{}
	for seed := range int64(20) {
		ds, err := Deal(lib, SurfaceLanding, "", "", seed, nil)
		require.NoError(t, err)
		assertDistinct(t, ds)
		seen[strings.Join(ids(ds), ",")] = true
	}
	assert.Greater(t, len(seen), 15, "不同种子应当大多发出不同的牌")
}

func TestDealAvoidThreeRounds(t *testing.T) {
	lib := Default()
	for _, c := range []struct{ surface, industry, tone string }{
		{SurfaceLanding, "", ""},
		{SurfaceLanding, "AI 编程助手", "深色 极简"},
		{SurfaceApp, "law firm", "trusted"},
		{SurfaceLanding, "wedding", "playful"}, // 候选很少，靠放宽
	} {
		t.Run(c.surface+"/"+c.industry+"/"+c.tone, func(t *testing.T) {
			var avoid []string
			usedP, usedT := map[string]bool{}, map[string]bool{}
			for round := range int64(3) {
				ds, err := Deal(lib, c.surface, c.industry, c.tone, 7+round, avoid)
				require.NoError(t, err)
				assertDistinct(t, ds)
				for _, d := range ds {
					assert.False(t, slices.Contains(avoid, d.ID), "第 %d 轮重发了 %s", round+1, d.ID)
					assert.False(t, usedP[d.Palette.ID], "第 %d 轮重用色板 %s", round+1, d.Palette.ID)
					assert.False(t, usedT[d.Type.ID], "第 %d 轮重用字体对 %s", round+1, d.Type.ID)
					usedP[d.Palette.ID], usedT[d.Type.ID] = true, true
				}
				avoid = append(avoid, ids(ds)...)
				if len(avoid) > 9 {
					avoid = avoid[len(avoid)-9:]
				}
			}
		})
	}
}

func TestDealFilters(t *testing.T) {
	lib := Default()
	for seed := range int64(10) {
		ds, err := Deal(lib, SurfaceLanding, "游戏", "暗色", seed, nil)
		require.NoError(t, err)
		for _, d := range ds {
			assert.Equal(t, "dark", d.Palette.Mode, d.ID)
		}
		ds, err = Deal(lib, SurfaceApp, "devtool", "", seed, nil)
		require.NoError(t, err)
		for _, d := range ds {
			l, _ := lib.LayoutByID(d.Layout)
			assert.Equal(t, SurfaceApp, l.Surface)
			assert.Contains(t, d.Palette.Trades, "devtool", d.ID)
			assert.GreaterOrEqual(t, d.Type.Kinds["software"]+d.Type.Kinds["tech"], 2, d.ID)
		}
	}
	// "ai" 不应命中 "retail" 里的子串。
	w := resolve(lib, "retail", "")
	assert.False(t, w.trades["ai"])
	assert.True(t, w.trades["retail"])
}

func TestDealErrors(t *testing.T) {
	_, err := Deal(Default(), "poster", "", "", 1, nil)
	assert.ErrorIs(t, err, ErrInvalid)
	_, err = Deal(Library{}, SurfaceLanding, "", "", 1, nil)
	assert.Error(t, err)
}

func proposal(name, accent, family string) Direction {
	return Direction{
		Name: name,
		Palette: Palette{Colors: []Color{
			{Role: "bg", Hex: "#ffffff"},
			{Role: "surface", Hex: "#f4f4f5"},
			{Role: "text", Hex: "#111111"},
			{Role: "muted", Hex: "#6b7280"},
			{Role: "accent", Hex: accent},
		}},
		Type:   TypePair{Display: Font{Family: family}, Body: Font{Family: "Inter"}},
		Layout: "左侧栏 + 表格",
	}
}

func TestValidateProposals(t *testing.T) {
	ok := []Direction{
		proposal("蓝", "#2563eb", "Geist"), // 色相 ~221°
		proposal("绿", "#16a34a", "Geist"), // ~142°
		proposal("橙", "#ea580c", "Geist"), // ~21°
	}
	require.NoError(t, ValidateProposals(ok))

	// 同色相但字体家族不同也算不同。
	require.NoError(t, ValidateProposals([]Direction{
		proposal("A", "#2563eb", "Geist"),
		proposal("B", "#2563eb", "Fraunces"),
		proposal("C", "#2563eb", "IBM Plex Sans"),
	}))

	err := ValidateProposals([]Direction{
		proposal("蓝", "#2563eb", "Geist"),
		proposal("靛", "#4f46e5", "geist"), // ~243°，差 22°，同字体（大小写不计）
		proposal("橙", "#ea580c", "Geist"),
	})
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "方向 1「蓝」与方向 2「靛」太像")

	// 两个无彩色强调色同字体视为太像。
	err = ValidateProposals([]Direction{
		proposal("黑", "#111111", "Geist"),
		proposal("灰", "#777777", "Geist"),
		proposal("橙", "#ea580c", "Geist"),
	})
	require.ErrorIs(t, err, ErrInvalid)

	err = ValidateProposals(ok[:2])
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "恰好 3 个")

	bad := slices.Clone(ok)
	bad[1].Palette.Colors = bad[1].Palette.Colors[:3]
	require.ErrorIs(t, ValidateProposals(bad), ErrInvalid)

	bad = slices.Clone(ok)
	bad[2].Type.Display.Family = `Geist"; } body { color: red`
	require.ErrorIs(t, ValidateProposals(bad), ErrInvalid)

	bad = slices.Clone(ok)
	bad[0].Palette.Colors = append(slices.Clone(bad[0].Palette.Colors[:4]), Color{Role: "brand", Hex: "#2563eb"})
	err = ValidateProposals(bad)
	require.ErrorIs(t, err, ErrInvalid)
	assert.Contains(t, err.Error(), "accent")

	// 库发出的牌必定能过校验（家族唯一）。
	ds, derr := Deal(Default(), SurfaceLanding, "", "", 3, nil)
	require.NoError(t, derr)
	require.NoError(t, ValidateProposals(ds[:]))
}

func TestAccentGap(t *testing.T) {
	p := func(hex string) Palette { return Palette{Colors: []Color{{Role: "accent", Hex: hex}}} }
	assert.InDelta(t, 20, accentGap(p("#ff0000"), p("#ff5500")), 0.5)
	assert.InDelta(t, 20, accentGap(p("#ff0000"), p("#ff0055")), 0.5) // 跨 0° 取短弧
	assert.InDelta(t, 180, accentGap(p("#111"), p("#ff0000")), 0.01)
	assert.Zero(t, accentGap(p("#000"), p("#fff")))
}

func TestThemeCSS(t *testing.T) {
	lib := Default()
	ds, err := Deal(lib, SurfaceLanding, "", "", 11, nil)
	require.NoError(t, err)
	for _, d := range ds {
		css := ThemeCSS(d)
		require.True(t, strings.HasPrefix(css, "@theme {"), css)
		assert.True(t, strings.HasSuffix(css, "}\n"))
		assert.NotContains(t, css, "@import")
		assert.Equal(t, 1, strings.Count(css, "{"))
		for _, c := range d.Palette.Colors {
			assert.Contains(t, css, fmt.Sprintf("--color-%s: %s;", c.Role, strings.ToLower(c.Hex)))
		}
		assert.Contains(t, css, `--font-display: "`+d.Type.Display.Family+`", `)
		assert.Contains(t, css, `--font-sans: "`+d.Type.Body.Family+`", `)
		if d.Type.Mono != nil {
			assert.Contains(t, css, `--font-mono: "`+d.Type.Mono.Family+`", `)
		}
	}

	css := ThemeCSS(proposal("蓝", "#2563EB", "Fraunces"))
	assert.Equal(t, `@theme {
  --color-bg: #ffffff;
  --color-surface: #f4f4f5;
  --color-text: #111111;
  --color-muted: #6b7280;
  --color-accent: #2563eb;
  --font-display: "Fraunces", ui-sans-serif, system-ui, sans-serif;
  --font-sans: "Inter", ui-sans-serif, system-ui, sans-serif;
}
`, css)
}

func TestParseLibraryRejects(t *testing.T) {
	_, err := ParseLibrary([]byte(`{"palettes":[{"id":"x","mode":"light","colors":[{"role":"bg","hex":"#fff"}]}]}`))
	assert.Error(t, err)
	_, err = ParseLibrary([]byte(`{"types":[
		{"id":"a","display":{"family":"Geist"},"body":{"family":"Geist"}},
		{"id":"b","display":{"family":"geist"},"body":{"family":"Inter"}}]}`))
	assert.Error(t, err)
	_, err = ParseLibrary([]byte(`not json`))
	assert.True(t, err != nil && !errors.Is(err, ErrInvalid))
}
