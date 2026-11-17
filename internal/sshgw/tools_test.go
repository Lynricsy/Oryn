package sshgw

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// toolThread 在库里建 orb（e.orb）与它的一条线程，返回线程 id。
func (e *gwEnv) toolThread(t *testing.T, id, kind string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := e.st.Q.GetOrb(ctx, e.orb.ID); err != nil {
		require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prv-t", Name: "p", TokenHash: "h", CreatedAt: 1}))
		require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{
			ID: e.orb.ID, ProviderID: "prv-t", Type: "small", State: "ready", TokenHash: "th-" + e.orb.ID, CreatedAt: 1,
		}))
	}
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: id, OrbID: &e.orb.ID, State: "idle", StateChangedAt: 1, SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1,
		Kind: kind, TitleSource: "auto",
	}))
	return id
}

func (e *gwEnv) call(t *testing.T, threadID, tool string, params map[string]any) (apigen.LinkCtlSSHCallResult, error) {
	t.Helper()
	raw, err := json.Marshal(apigen.LinkCtlSSHCallParams{ThreadID: threadID, Tool: tool, Params: params})
	require.NoError(t, err)
	res, err := e.svc.OnCall(context.Background(), e.orb.ID, raw)
	if err != nil {
		return apigen.LinkCtlSSHCallResult{}, err
	}
	return res.(apigen.LinkCtlSSHCallResult), nil
}

func requireCtlCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, code, ce.Code, ce.Message)
}

func TestExecManyCallerAndParams(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	th := e.toolThread(t, "T-ok", "work")
	sp := e.toolThread(t, "T-space", "space")

	_, err := e.call(t, "T-nope", toolExecMany, map[string]any{"hosts": []string{"web"}, "command": "true"})
	requireCtlCode(t, err, "forbidden")
	_, err = e.call(t, sp, toolExecMany, map[string]any{"hosts": []string{"web"}, "command": "true"})
	requireCtlCode(t, err, "forbidden")
	_, err = e.call(t, th, "ssh_nope", map[string]any{})
	requireCtlCode(t, err, "invalid_params")

	for name, p := range map[string]map[string]any{
		"无主机":   {"hosts": []string{}, "command": "true"},
		"空主机名":  {"hosts": []string{" "}, "command": "true"},
		"主机过多":  {"hosts": make([]string, 33), "command": "true"},
		"空命令":   {"hosts": []string{"web"}, "command": ""},
		"命令过长":  {"hosts": []string{"web"}, "command": strings.Repeat("x", 8001)},
		"超时非整数": {"hosts": []string{"web"}, "command": "true", "timeout_s": 1.5},
		"超时过大":  {"hosts": []string{"web"}, "command": "true", "timeout_s": 601},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.call(t, th, toolExecMany, p)
			requireCtlCode(t, err, "invalid_params")
		})
	}
}

func TestExecManyRunsAndAudits(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	th := e.toolThread(t, "T-ok", "work")

	res, err := e.call(t, th, toolExecMany, map[string]any{
		"hosts": []string{"web", "nope"}, "command": "echo out; echo err >&2; exit 4",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Text, "## web — exit 4（")
	assert.Contains(t, res.Text, "out\n")
	assert.Contains(t, res.Text, "err\n")
	assert.Contains(t, res.Text, `## nope — 失败：主机 "nope" 未登记`)
	assert.Less(t, strings.Index(res.Text, "## web"), strings.Index(res.Text, "## nope"), "按请求顺序")

	results := res.Details["results"].([]any)
	require.Len(t, results, 2)
	b, _ := json.Marshal(results)
	var rs []map[string]any
	require.NoError(t, json.Unmarshal(b, &rs))
	assert.Equal(t, true, rs[0]["ok"])
	assert.EqualValues(t, 4, rs[0]["exitCode"])
	assert.Equal(t, false, rs[1]["ok"])
	assert.NotEmpty(t, rs[1]["error"])

	rows := e.auditRows(t)
	var exec, deny int
	for _, r := range rows {
		if r.Source != srcTool {
			continue
		}
		require.NotNil(t, r.ThreadID)
		assert.Equal(t, th, *r.ThreadID)
		switch r.Kind {
		case "exec":
			exec++
			assert.Equal(t, "echo out; echo err >&2; exit 4", r.Detail)
			require.NotNil(t, r.ExitCode)
			assert.EqualValues(t, 4, *r.ExitCode)
		case "deny":
			deny++
			assert.Equal(t, "nope", r.HostName)
		}
	}
	assert.Equal(t, 1, exec)
	assert.Equal(t, 1, deny)
}

func TestExecManyTimeoutAndElision(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	th := e.toolThread(t, "T-ok", "work")

	res, err := e.call(t, th, toolExecMany, map[string]any{"hosts": []string{"web"}, "command": "echo started; sleep 30", "timeout_s": 1})
	require.NoError(t, err)
	assert.Contains(t, res.Text, "## web — 超时（1s）")
	assert.Contains(t, res.Text, "started")
	assert.GreaterOrEqual(t, resultDuration(t, res), int64(1000), "超时结果带耗时")

	res, err = e.call(t, th, toolExecMany, map[string]any{"hosts": []string{"web"}, "command": "sleep 0.3"})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, resultDuration(t, res), int64(300), "正常退出的结果带耗时")
	assert.NotContains(t, res.Text, "（0.0s）")

	res, err = e.call(t, th, toolExecMany, map[string]any{"hosts": []string{"web"}, "command": "head -c 10000 /dev/zero | tr '\\0' a; echo; echo END"})
	require.NoError(t, err)
	assert.Contains(t, res.Text, "## web — exit 0")
	assert.Contains(t, res.Text, "…（省略 ")
	assert.Contains(t, res.Text, "END")
	assert.Less(t, len(res.Text), 4096+200)
}

// resultDuration 取 details.results 里唯一一台主机的 durationMs。
func resultDuration(t *testing.T, res apigen.LinkCtlSSHCallResult) int64 {
	t.Helper()
	b, err := json.Marshal(res.Details["results"])
	require.NoError(t, err)
	var rs []execResult
	require.NoError(t, json.Unmarshal(b, &rs))
	require.Len(t, rs, 1)
	return rs[0].DurationMs
}

func TestHeadTail(t *testing.T) {
	var w headTail
	_, _ = w.Write([]byte(strings.Repeat("h", execOutputHalf)))
	_, _ = w.Write([]byte("MID"))
	_, _ = w.Write([]byte(strings.Repeat("t", execOutputHalf)))
	got := w.String()
	assert.Equal(t, strings.Repeat("h", execOutputHalf)+"\n…（省略 3 字节）…\n"+strings.Repeat("t", execOutputHalf), got)

	var small headTail
	_, _ = small.Write([]byte("abc"))
	_, _ = small.Write([]byte(strings.Repeat("x", execOutputHalf)))
	assert.Equal(t, "abc"+strings.Repeat("x", execOutputHalf), small.String())
}
