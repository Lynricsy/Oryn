package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// M26 2.3：`oryn follow add` 带上调用方线程经 orbd.sock 发 follow.add，成功时打印话题 id（--json 时打印结果）。
func TestFollowAdd(t *testing.T) {
	f := startFakeOrbd(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		require.Equal(t, orbsock.MethodFollowAdd, method)
		var p map[string]any
		require.NoError(t, json.Unmarshal(params, &p))
		if p["quote"] == "没说过" {
			return nil, orbsock.Errorf("forbidden", "the owner did not say this in this thread; pass the owner's own words with --quote")
		}
		return map[string]any{"topicId": "rtp_01JX"}, nil
	})
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	t.Setenv("ORYN_API_TOKEN", "")

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(append([]string{"follow", "add"}, args...))
		t.Cleanup(func() { root.SetOut(nil); root.SetArgs(nil) })
		for _, name := range []string{"title", "question", "quote", "kind", "json"} {
			_ = followAddCmd.Flags().Set(name, followAddCmd.Flags().Lookup(name).DefValue)
		}
		err := root.ExecuteContext(context.Background())
		return out.String(), err
	}

	out, err := run("--title", "上游修复", "--question", "foo#123 是否已发布", "--quote", "跟进这个上游修复", "--kind", "interest")
	require.NoError(t, err)
	assert.Equal(t, "rtp_01JX\n", out)
	require.Len(t, f.reqs, 1)
	assert.JSONEq(t, `{"threadId":"T-caller","title":"上游修复","question":"foo#123 是否已发布","quote":"跟进这个上游修复","kind":"interest"}`,
		string(f.reqs[0].Params))

	out, err = run("--title", "T", "--question", "Q", "--quote", "跟进", "--json")
	require.NoError(t, err)
	assert.JSONEq(t, `{"topicId":"rtp_01JX"}`, out)
	assert.JSONEq(t, `{"threadId":"T-caller","title":"T","question":"Q","quote":"跟进"}`, string(f.reqs[1].Params), "省略 --kind 时不带 kind")

	// 控制面拒绝：退出码 2，错误里带错误码
	_, err = run("--title", "T", "--question", "Q", "--quote", "没说过")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
	assert.ErrorContains(t, err, "forbidden")

	// 缺参数、非法 kind：调用错误，不发请求
	n := len(f.reqs)
	for _, args := range [][]string{
		{"--title", "T", "--question", "Q"},
		{"--title", " ", "--question", "Q", "--quote", "x"},
		{"--title", "T", "--question", "Q", "--quote", "x", "--kind", "forever"},
	} {
		_, err := run(args...)
		require.ErrorAs(t, err, &ec, args)
		assert.Equal(t, 2, ec.code, args)
	}
	assert.Len(t, f.reqs, n)
}

// 不在 orb 内（或带着 API 令牌）时不可用：退出码 2。
func TestFollowAddOutsideOrb(t *testing.T) {
	t.Setenv("ORYN_ORB", "")
	_, err := followAdd(context.Background(), "T", "Q", "x", "")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
	assert.ErrorContains(t, err, "only works inside an orb thread")
}
