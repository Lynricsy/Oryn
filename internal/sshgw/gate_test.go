package sshgw

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
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

// 拨号期间主机被停用：连接在登记时作废，命令不执行。
func TestRunGateHostDisabledDuringDial(t *testing.T) {
	var hook struct{ armed, entered, release chan struct{} }
	hook.armed, hook.entered, hook.release = make(chan struct{}), make(chan struct{}), make(chan struct{})
	e := newGwEnvCfg(t, func(c *Config) {
		c.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			select {
			case <-hook.armed:
				close(hook.entered)
				<-hook.release
			default:
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	}, sshgwtest.Options{})
	marker := filepath.Join(t.TempDir(), "ran")
	close(hook.armed)
	res := make(chan GateResult, 1)
	go func() {
		res <- e.svc.RunGate(context.Background(), "", false, "web", "touch "+marker, "schedule sch_1 run #1 gate", 10*time.Second)
	}()
	<-hook.entered
	_, err := e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(false)}})
	require.NoError(t, err)
	close(hook.release)
	r := <-res
	require.ErrorIs(t, r.Err, errStale)
	assert.NoFileExists(t, marker, "作废的连接不执行命令")
}

// 执行中主机被停用：上游连接被关掉，闸门按执行失败返回，不等到超时。
func TestRunGateHostDisabledDuringExec(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	marker := filepath.Join(t.TempDir(), "started")
	res := make(chan GateResult, 1)
	start := time.Now()
	go func() {
		res <- e.svc.RunGate(context.Background(), "", false, "web", "touch "+marker+"; sleep 30", "schedule sch_1 run #1 gate", time.Minute)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 5*time.Second, 10*time.Millisecond)
	_, err := e.svc.UpdateHost(context.Background(), e.host.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(false)}})
	require.NoError(t, err)
	select {
	case r := <-res:
		require.Error(t, r.Err)
		assert.Less(t, time.Since(start), 20*time.Second)
	case <-time.After(10 * time.Second):
		t.Fatal("停用主机后闸门没有断开")
	}
}
