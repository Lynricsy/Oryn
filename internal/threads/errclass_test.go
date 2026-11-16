package threads

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 失败原因原文 → 归类与参数：样例取自各写入点的真实形态（provider 报错透传、orbd ctl 错误、fork、连接超时）。
func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		name   string
		kind   apigen.ThreadErrorKind
		reason string
		cause  apigen.ThreadErrorCause
		detail string
	}{
		{"docker 拉镜像被拒", apigen.OrbFailed, "docker run --name orb-1 oryn-orb:dev: exit status 125: Error response from daemon: pull access denied for oryn-orb:dev, repository does not exist", apigen.ImagePullDenied, "oryn-orb:dev"},
		{"找不到镜像", apigen.OrbFailed, "docker create x: exit status 1: Error: No such image: ghcr.io/a/b:1", apigen.ImageMissing, "ghcr.io/a/b:1"},
		{"快照镜像不存在", apigen.OrbFailed, "docker: 快照镜像 oryn-snap:abc 不存在", apigen.ImageMissing, "oryn-snap:abc"},
		{"容器命令非零退出", apigen.OrbFailed, "docker start orb-1: exit status 137: ", apigen.ProviderCommand, "137"},
		{"连接超时", apigen.OrbFailed, "no hello within connect timeout", apigen.OrbConnectTimeout, ""},
		{"fork 复制", apigen.AgentFailed, "fork copy: snapshot failed: disk full", apigen.ForkFailed, ""},
		{"目录不在 runner 服务范围", apigen.AgentFailed, `dir_not_served: "/home/x/repo" is not inside a directory served by this runner`, apigen.DirNotServed, "/home/x/repo"},
		{"启动参数无效", apigen.AgentFailed, `invalid_params: invalid threadId "x"`, apigen.InvalidParams, ""},
		{"启动超时", apigen.AgentFailed, "context deadline exceeded", apigen.AgentStartTimeout, ""},
		{"其余 agent 启动失败", apigen.AgentFailed, "failed: mkdir /x: permission denied", apigen.AgentStartFailed, ""},
		{"认不出的 orb 失败", apigen.OrbFailed, "docker: 容器 orb-1 处于 \"dead\"，无法 spawn", apigen.Unknown, ""},
		{"空原因", apigen.OrbFailed, "", apigen.Unknown, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cause, detail := classifyFailure(c.kind, c.reason)
			assert.Equal(t, c.cause, cause)
			assert.Equal(t, c.detail, detail)
		})
	}
	e := newThreadError(apigen.OrbFailed, "no hello within connect timeout")
	assert.Nil(t, e.Detail)
	assert.Equal(t, apigen.OrbConnectTimeout, *e.Cause)
}
