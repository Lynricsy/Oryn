package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn thread new --box [--app]`（M23）：发 executor{kind: box}；参数组合不合法时不发请求。
func TestThreadNewBox(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	ctx := context.Background()
	lastBody := func() string {
		reqs := f.requests()
		return string(reqs[len(reqs)-1].Body)
	}

	_, err := threadNew(ctx, f.client(), "hi", threadNewOptions{box: "workbench"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"box","boxId":"workbench"}}`, lastBody())
	_, err = threadNew(ctx, f.client(), "hi", threadNewOptions{box: "workbench", app: "hello"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"prompt":"hi","executor":{"kind":"box","boxId":"workbench","app":"hello"}}`, lastBody())

	n := len(f.requests())
	for _, tc := range []struct {
		name string
		o    threadNewOptions
		want string
	}{
		{"app without box", threadNewOptions{app: "hello"}, "需要与 --box 一起使用"},
		{"box with orb", threadNewOptions{box: "b", orb: "orb-x"}, "不能同时使用"},
		{"box with provider", threadNewOptions{box: "b", provider: "docker"}, "不能同时使用"},
		{"box with type", threadNewOptions{box: "b", orbType: "small"}, "不能同时使用"},
		{"box with runner", threadNewOptions{box: "b", runner: "laptop", dir: "/x"}, "不能同时使用"},
		{"box with profile", threadNewOptions{box: "b", browserProfile: "工作"}, "不能与 --box 同时使用"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := threadNew(ctx, f.client(), "hi", tc.o)
			assert.ErrorContains(t, err, tc.want, "%+v", tc.o)
		})
	}
	assert.Len(t, f.requests(), n, "参数不合法时不发请求")
}
