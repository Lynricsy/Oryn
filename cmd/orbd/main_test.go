package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunArgs(t *testing.T) {
	var out, errb bytes.Buffer
	assert.Equal(t, 0, run([]string{"--version"}, &out, &errb))
	assert.Contains(t, out.String(), "orbd ")

	assert.Equal(t, 2, run(nil, &out, &errb))
	assert.Equal(t, 2, run([]string{"bogus"}, &out, &errb))

	t.Setenv("ORYN_URL", "")
	t.Setenv("ORYN_ORB_ID", "")
	t.Setenv("ORYN_ORB_TOKEN", "")
	errb.Reset()
	assert.Equal(t, 2, run([]string{"run"}, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB_TOKEN")
}

// TestRunInvalidOrbCA：ORYN_ORB_CA_PEM 解析失败时 orbd 启动失败（不退回系统根）。
func TestRunInvalidOrbCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ORYN_URL", "https://cp.invalid")
	t.Setenv("ORYN_ORB_ID", "orb-x")
	t.Setenv("ORYN_ORB_TOKEN", "oot_x")
	t.Setenv("ORYN_ORB_CA_PEM", "not a pem")
	var out, errb bytes.Buffer
	assert.Equal(t, 1, run([]string{"run"}, &out, &errb))
	assert.Contains(t, errb.String(), "ORYN_ORB_CA_PEM")
}
