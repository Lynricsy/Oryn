package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/providerrt"
)

type stubDriver struct{}

func (stubDriver) Info() providerrt.Info { return providerrt.Info{} }
func (stubDriver) Exec(context.Context, providerrt.Job, io.Writer) (*apigen.JobResult, error) {
	return nil, nil
}

type reporterDriver struct {
	stubDriver
	checks []providerrt.PreflightCheck
}

func (d reporterDriver) PreflightChecks(context.Context) []providerrt.PreflightCheck { return d.checks }

type preflighterDriver struct {
	stubDriver
	err error
}

func (d preflighterDriver) Preflight(context.Context) error { return d.err }

func TestRunPreflight(t *testing.T) {
	var out bytes.Buffer
	err := runPreflight(context.Background(), &out, reporterDriver{checks: []providerrt.PreflightCheck{
		{Name: "以 root 运行"},
		{Name: "宿主内核 7.3.0", Warn: "不在支持之列"},
		{Name: "nft 表 inet oryn_fc", Err: errors.New("没有这张表")},
	}})
	require.EqualError(t, err, "1 项预检失败")
	assert.Equal(t, "✓ 以 root 运行\n! 宿主内核 7.3.0: 不在支持之列\n✗ nft 表 inet oryn_fc: 没有这张表\n", out.String())

	out.Reset()
	require.NoError(t, runPreflight(context.Background(), &out, reporterDriver{checks: []providerrt.PreflightCheck{{Name: "a"}}}))
	assert.Equal(t, "✓ a\n", out.String())

	out.Reset()
	require.Error(t, runPreflight(context.Background(), &out, preflighterDriver{err: errors.New("runtime 未登记")}))
	assert.Equal(t, "✗ 预检: runtime 未登记\n", out.String())

	out.Reset()
	require.NoError(t, runPreflight(context.Background(), &out, stubDriver{}))
	assert.Equal(t, "该驱动没有启动前检查\n", out.String())
}
