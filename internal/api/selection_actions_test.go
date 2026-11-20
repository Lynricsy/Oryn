package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

const selectionActionsPath = "/api/v1/settings/selection-actions"

func selectionAction(id, name, prompt string) map[string]any {
	return map[string]any{"id": id, "name": name, "prompt": prompt}
}

func TestSelectionActionsAPI(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	body := func(actions ...map[string]any) map[string]any {
		if actions == nil {
			actions = []map[string]any{}
		}
		return map[string]any{"actions": actions}
	}

	assert.Equal(t, 401, e.do("GET", selectionActionsPath, h2Cred{}, nil).status)
	assert.Equal(t, 403, e.do("PUT", selectionActionsPath, h2Cred{cookie: e.webSession()}, body()).status)

	// 尚无所有者：GET 给空列表，PUT 409
	r := e.do("GET", selectionActionsPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"actions":[]}`, string(r.body))
	r = e.do("PUT", selectionActionsPath, c, body())
	assert.Equal(t, 409, r.status, string(r.body))
	assert.Equal(t, "conflict", r.code(t))

	require.NoError(t, e.st.Q.CreateOwner(context.Background(), db.CreateOwnerParams{DisplayName: "o", UserHandle: []byte("h"), CreatedAt: 1}))
	r = e.do("GET", selectionActionsPath, c, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"actions":[]}`, string(r.body), "新所有者的缺省值")

	// 写两条，名称与提示词去首尾空白后保存，顺序保留
	want := `{"actions":[{"id":"abcd1234","name":"挑毛病","prompt":"找出这段话里的漏洞"},{"id":"zz9900aa","name":"Summarize","prompt":"Sum it up."}]}`
	r = e.do("PUT", selectionActionsPath, c, body(
		selectionAction("abcd1234", "  挑毛病 ", "\n找出这段话里的漏洞  "),
		selectionAction("zz9900aa", "Summarize", "Sum it up."),
	))
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, want, string(r.body))
	r = e.do("GET", selectionActionsPath, h2Cred{cookie: e.webSession()}, nil)
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, want, string(r.body))

	thirteen := make([]map[string]any, 13)
	for i := range thirteen {
		thirteen[i] = selectionAction(fmt.Sprintf("a%07d", i), "n", "p")
	}
	for name, bad := range map[string]map[string]any{
		"13 条":    body(thirteen...),
		"id 重复":   body(selectionAction("abcd1234", "a", "p"), selectionAction("abcd1234", "b", "p")),
		"空名字":     body(selectionAction("abcd1234", "   ", "p")),
		"id 格式不对": body(selectionAction("ABCD1234", "a", "p")),
	} {
		r = e.do("PUT", selectionActionsPath, c, bad)
		assert.Equal(t, 400, r.status, "%s: %s", name, r.body)
		assert.Equal(t, "invalid_request", r.code(t), name)
	}
	r = e.do("GET", selectionActionsPath, c, nil)
	assert.JSONEq(t, want, string(r.body), "被拒的请求不写库")

	// 清空后读回空列表（不是 null）
	r = e.do("PUT", selectionActionsPath, c, body())
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"actions":[]}`, string(r.body))
	r = e.do("GET", selectionActionsPath, c, nil)
	assert.JSONEq(t, `{"actions":[]}`, string(r.body))
}
