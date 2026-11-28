package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestThreadDraftsAPI(t *testing.T) {
	e := newThreadsEnv(t)
	ctx := t.Context()
	th := ids.Thread()
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		Kind: "work", ID: th, Title: "t", TitleSource: "user", State: "idle", SessionDir: "/s/" + th, CreatedAt: 1, UpdatedAt: 1,
	}))
	_, err := e.st.Q.UpsertEntry(ctx, db.UpsertEntryParams{ThreadID: th, File: "s.jsonl", EntryID: "e1", Type: "message", Payload: []byte(`{}`)})
	require.NoError(t, err)
	base := "/api/v1/threads/" + th + "/drafts"
	in := map[string]any{"kind": "email", "title": "周五会议改期", "text": "Hi Sam,\n\nThanks"}

	code, body := e.do("GET", base, nil)
	require.Equal(t, 200, code, string(body))
	assert.JSONEq(t, `{"drafts":[]}`, string(body))

	code, body = e.do("PUT", base+"/e1/1.0", in)
	require.Equal(t, 200, code, string(body))
	var d apigen.ThreadDraft
	require.NoError(t, json.Unmarshal(body, &d))
	assert.Equal(t, apigen.ThreadDraft{EntryID: "e1", Block: "1.0", Kind: apigen.Email, Title: "周五会议改期", Text: "Hi Sam,\n\nThanks", UpdatedAt: d.UpdatedAt}, d)
	assert.Positive(t, d.UpdatedAt)
	assert.NotContains(t, string(body), "deliveredAt", "未送达时省略")

	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code)
	var list apigen.ThreadDraftList
	require.NoError(t, json.Unmarshal(body, &list))
	require.Len(t, list.Drafts, 1)
	assert.Equal(t, d, list.Drafts[0])

	// 校验与 404 分支
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		code         string
	}{
		{"GET", "/api/v1/threads/thr_missing/drafts", nil, 404, "not_found"},
		{"PUT", "/api/v1/threads/thr_missing/drafts/e1/1.0", in, 404, "not_found"},
		{"PUT", base + "/e1/x", in, 400, "invalid_draft"},
		{"PUT", base + "/e1/1.0", map[string]any{"kind": "letter", "title": "", "text": "x"}, 400, "invalid_draft"},
		{"PUT", base + "/e1/1.0", map[string]any{"kind": "doc", "title": strings.Repeat("t", 201), "text": "x"}, 400, "invalid_draft"},
		{"PUT", base + "/e1/1.0", map[string]any{"kind": "doc", "title": "", "text": strings.Repeat("x", 102401)}, 400, "invalid_draft"},
		{"PUT", base + "/e-missing/1.0", in, 404, "entry_not_found"},
		{"DELETE", base + "/e1/2.0", nil, 404, "draft_not_found"},
		{"DELETE", "/api/v1/threads/thr_missing/drafts/e1/1.0", nil, 404, "not_found"},
	} {
		code, body := e.do(c.method, c.path, c.body)
		require.Equal(t, c.status, code, "%s %s: %s", c.method, c.path, body)
		var er struct {
			Code string `json:"code"`
		}
		require.NoError(t, json.Unmarshal(body, &er))
		assert.Equal(t, c.code, er.Code, "%s %s", c.method, c.path)
	}

	code, body = e.do("DELETE", base+"/e1/1.0", nil)
	require.Equal(t, 204, code, string(body))
	code, body = e.do("GET", base, nil)
	require.Equal(t, 200, code)
	assert.JSONEq(t, `{"drafts":[]}`, string(body))
}
