package design

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeDOM 是 --dump-dom 输出里带一个探针结果节点的页面。
func probeDOM(issues string) string {
	return `<html><head></head><body><div id="root"><p>x</p></div><script id="oryn-probe" type="application/json">` +
		`{"v":1,"frame":"home","height":800,"issues":` + issues + `}</script></body></html>`
}

func TestParseProbe(t *testing.T) {
	got, err := ParseProbe([]byte(probeDOM(`[{"rule":"text-clipped","loc":"src/frames/home.tsx:3:5","text":"立即开始","detail":64},` +
		`{"rule":"contrast-low","loc":null,"text":"浅灰","detail":2.32},` +
		`{"rule":"Bad Rule","loc":null,"text":"x"},` +
		`{"rule":"raw-color","loc":null,"text":"红","detail":{"evil":true}}]`)))
	require.NoError(t, err)
	assert.Equal(t, []Issue{
		{Rule: "text-clipped", Loc: "src/frames/home.tsx:3:5", Text: "立即开始", Detail: json.RawMessage(`64`)},
		{Rule: "contrast-low", Text: "浅灰", Detail: json.RawMessage(`2.32`)},
		{Rule: "raw-color", Text: "红"},
	}, got)

	got, err = ParseProbe([]byte(probeDOM(`[]`)))
	require.NoError(t, err)
	assert.Empty(t, got)

	// 页面自己伪造的节点在前，探针追加的在 body 末尾：取最后一个
	forged := `<html><body><script id="oryn-probe" type="application/json">{"v":1,"issues":[]}</script>` +
		`<script id="oryn-probe" type="application/json">{"v":1,"issues":[{"rule":"dash-in-copy","loc":null,"text":"a—b"}]}</script></body></html>`
	got, err = ParseProbe([]byte(forged))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "dash-in-copy", got[0].Rule)

	long := fmt.Sprintf(`[%s{"rule":"x","loc":null,"text":"t"}]`, strings.Repeat(`{"rule":"y","loc":null,"text":"t"},`, 60))
	got, err = ParseProbe([]byte(probeDOM(long)))
	require.NoError(t, err)
	assert.Len(t, got, MaxProbeIssues)

	got, err = ParseProbe([]byte(probeDOM(`[{"rule":"x","loc":null,"text":"` + strings.Repeat("长", 300) + `"}]`)))
	require.NoError(t, err)
	assert.Equal(t, MaxIssueTextRunes, len([]rune(got[0].Text)))

	_, err = ParseProbe([]byte(`<html><body><p>没有探针</p></body></html>`))
	assert.ErrorContains(t, err, "没有探针结果")
	_, err = ParseProbe([]byte(`<html><body><script id="oryn-probe" type="application/json">{"v":2}</script></body></html>`))
	assert.ErrorContains(t, err, "不合法")
}
