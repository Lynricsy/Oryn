package apigen_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const threadID = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

// 客户端消息按 type 判别后可直接类型分派。
func TestWsClientMessageDispatch(t *testing.T) {
	cases := map[string]string{
		`{"type":"sub","threadId":"` + threadID + `","afterSeq":5}`:                                       "sub",
		`{"type":"focus","threadId":null}`:                                                                "focus",
		`{"type":"cmd","threadId":"` + threadID + `","id":"c1","frame":{"type":"prompt","message":"hi"}}`: "cmd",
		`{"type":"ui.response","threadId":"` + threadID + `","frame":{"type":"extension_ui_response"}}`:   "ui.response",
		`{"type":"ping"}`: "ping",
		`{"type":"term.watch","threadId":"` + threadID + `","on":true}`: "term.watch",
	}
	for raw, want := range cases {
		var msg apigen.WsClientMessage
		require.NoError(t, json.Unmarshal([]byte(raw), &msg))
		d, err := msg.Discriminator()
		require.NoError(t, err)
		assert.Equal(t, want, d)

		v, err := msg.ValueByDiscriminator()
		require.NoError(t, err)
		switch m := v.(type) {
		case apigen.WsClientSub:
			assert.Equal(t, threadID, m.ThreadID)
			require.NotNil(t, m.AfterSeq)
			assert.EqualValues(t, 5, *m.AfterSeq)
		case apigen.WsClientFocus:
			assert.Nil(t, m.ThreadID)
		case apigen.WsClientCmd:
			assert.Equal(t, "c1", m.ID)
			assert.Equal(t, "prompt", m.Frame["type"])
		case apigen.WsClientUIResponse:
			assert.Equal(t, "extension_ui_response", m.Frame["type"])
		case apigen.WsClientPing:
		case apigen.WsClientTermWatch:
			assert.Equal(t, threadID, m.ThreadID)
			assert.True(t, m.On)
		default:
			t.Fatalf("未分派的类型 %T", v)
		}
	}

	var bad apigen.WsClientMessage
	require.NoError(t, json.Unmarshal([]byte(`{"type":"nope"}`), &bad))
	_, err := bad.ValueByDiscriminator()
	assert.Error(t, err)
}

// 服务端消息由具体类型构造后，序列化结果自动带上 type。
func TestWsServerMessageEncode(t *testing.T) {
	var msg apigen.WsServerMessage
	require.NoError(t, msg.FromWsServerUIResolved(apigen.WsServerUIResolved{ThreadID: threadID, RequestID: "r1"}))
	b, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"ui.resolved","threadId":"`+threadID+`","requestId":"r1"}`, string(b))

	require.NoError(t, msg.FromWsServerFrame(apigen.WsServerFrame{ThreadID: threadID, Frame: apigen.OmpFrame{"type": "agent_start"}}))
	b, err = json.Marshal(msg)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"frame","threadId":"`+threadID+`","frame":{"type":"agent_start"}}`, string(b))
}

// link 流头按 t 判别。
func TestLinkStreamHeaderDispatch(t *testing.T) {
	var h apigen.LinkStreamHeader
	require.NoError(t, json.Unmarshal([]byte(`{"t":"agent","threadId":"`+threadID+`"}`), &h))
	v, err := h.ValueByDiscriminator()
	require.NoError(t, err)
	agent, ok := v.(apigen.LinkStreamAgent)
	require.True(t, ok, "got %T", v)
	assert.Equal(t, threadID, agent.ThreadID)

	require.NoError(t, json.Unmarshal([]byte(`{"t":"blob","sha256":"ab","size":3}`), &h))
	v, err = h.ValueByDiscriminator()
	require.NoError(t, err)
	blob, ok := v.(apigen.LinkStreamBlob)
	require.True(t, ok, "got %T", v)
	assert.EqualValues(t, 3, blob.Size)
}

// 新建线程的 executor 联合按 kind 判别。
func TestThreadExecutorDispatch(t *testing.T) {
	var req apigen.CreateThreadRequest
	require.NoError(t, json.Unmarshal([]byte(`{"prompt":"hi","executor":{"kind":"orb","orbId":"orb-01jcz8m3w4x5y6z7a8b9c0d1e2"}}`), &req))
	v, err := req.Executor.ValueByDiscriminator()
	require.NoError(t, err)
	e, ok := v.(apigen.ThreadExecutorOrb)
	require.True(t, ok, "got %T", v)
	assert.Equal(t, "orb-01jcz8m3w4x5y6z7a8b9c0d1e2", e.OrbID)
}

type agentCfgStub struct {
	apigen.Unimplemented
	gotPath string
}

func (s *agentCfgStub) GetAgentConfigFile(w http.ResponseWriter, _ *http.Request, path apigen.AgentConfigPath) {
	s.gotPath = path
	_, _ = w.Write([]byte("hello"))
}

// 路径参数中编码为 %2F 的 `/` 经 chi 路由后被还原为单个参数；未实现的端点返回 501。
func TestAgentConfigPathEncodedSlash(t *testing.T) {
	stub := &agentCfgStub{}
	srv := httptest.NewServer(apigen.HandlerFromMux(stub, chi.NewRouter()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/agent-config/files/skills%2Ffoo%2FSKILL.md")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, strings.TrimSpace(string(b)))
	assert.Equal(t, "hello", string(b))
	assert.Equal(t, "skills/foo/SKILL.md", stub.gotPath)

	resp2, err := http.Get(srv.URL + "/api/v1/me")
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp2.StatusCode)
}
