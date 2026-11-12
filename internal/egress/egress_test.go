package egress

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeR struct {
	routes  map[string]string
	secrets map[string][2]string // id → value, 允许主机（逗号分隔）
	git     map[string]string
}

func (f fakeR) LLMRoute(k string) (string, bool) { v, ok := f.routes[k]; return v, ok }
func (f fakeR) SecretByID(id string) (string, []string, bool) {
	v, ok := f.secrets[id]
	if !ok {
		return "", nil, false
	}
	var hosts []string
	if v[1] != "" {
		hosts = strings.Split(v[1], ",")
	}
	return v[0], hosts, true
}
func (f fakeR) GitToken(h string) (string, bool) { v, ok := f.git[h]; return v, ok }

func TestProxyInjectsSecretAndStreams(t *testing.T) {
	release := make(chan struct{})
	var gotAuth, gotPath, gotQuery string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotQuery = r.Header.Get("Authorization"), r.URL.Path, r.URL.Query().Get("key")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: two\n\n")
	}))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")

	h := NewHandler("orb-x", fakeR{
		routes:  map[string]string{"custom": up.URL},
		secrets: map[string][2]string{"s1": {"REAL-KEY", upHost}, "s2": {"OTHER", "nope.example.com"}},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/llm/custom/v1/messages?key=oryn-ph-s1", nil)
	req.Header.Set("Authorization", "Bearer oryn-ph-s1")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	// 第一块在上游尚未结束时就到达 = 未被缓冲
	assert.Equal(t, "data: one\n", line)
	close(release)
	assert.Equal(t, "Bearer REAL-KEY", gotAuth)
	assert.Equal(t, "/v1/messages", gotPath)
	assert.Equal(t, "REAL-KEY", gotQuery)

	// 占位符绑定的主机不匹配 → 拒绝
	req2, _ := http.NewRequest("POST", srv.URL+"/llm/custom/v1", nil)
	req2.Header.Set("x-api-key", "oryn-ph-s2")
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode)

	// 未知路由
	resp3, err := http.Get(srv.URL + "/llm/missing/x")
	require.NoError(t, err)
	resp3.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp3.StatusCode)
}

func TestGitRoute(t *testing.T) {
	h := NewHandler("orb-x", fakeR{git: map[string]string{"github.com": "tok"}})
	u, auth, ok := h.resolve("/git/github.com/owner/repo.git/info/refs")
	require.True(t, ok)
	assert.Equal(t, "https://github.com/owner/repo.git/info/refs", u.String())
	assert.Equal(t, "Basic eC1hY2Nlc3MtdG9rZW46dG9r", auth)
	_, _, ok = h.resolve("/git/gitlab.com/x")
	assert.False(t, ok, "无令牌的 git 主机不放行")
}
