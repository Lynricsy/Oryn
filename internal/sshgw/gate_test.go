package sshgw

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

// 定时的运行前闸门：stdout / stderr 分开收集、非 0 退出不算错误，执行与拒绝都记审计（来源 owner，带 label）。
func TestRunGate(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()

	require.NoError(t, e.svc.CheckGateHost(ctx, "", false, "web"))
	require.Error(t, e.svc.CheckGateHost(ctx, "", false, "nope"))

	res := e.svc.RunGate(ctx, "", false, "web", `echo log; echo '{"wakeAgent": false}'; echo warn >&2; exit 3`, "schedule sch_1 run #1 gate", 10*time.Second)
	require.NoError(t, res.Err)
	assert.Equal(t, 3, res.ExitCode)
	assert.Equal(t, "log\n{\"wakeAgent\": false}\n", res.Stdout)
	assert.Equal(t, "warn\n", res.Stderr)

	res = e.svc.RunGate(ctx, "", false, "nope", "true", "schedule sch_1 run #2 gate", 10*time.Second)
	require.Error(t, res.Err)

	res = e.svc.RunGate(ctx, "", false, "web", "echo started; sleep 30", "schedule sch_1 run #3 gate", time.Second)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "timed out after 1s")
	assert.Equal(t, "started\n", res.Stdout, "超时前的输出保留")

	var exec, deny int
	for _, r := range e.auditRows(t) {
		if r.Source != srcOwner {
			continue
		}
		assert.Nil(t, r.ThreadID)
		assert.True(t, strings.HasPrefix(r.Detail, "schedule sch_1 run #"), r.Detail)
		switch r.Kind {
		case "exec":
			exec++
		case "deny":
			deny++
		}
	}
	assert.Equal(t, 2, exec)
	assert.Equal(t, 1, deny)
}
