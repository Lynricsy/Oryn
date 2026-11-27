package share_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/share"
)

func htmlCall(id, html string) map[string]any {
	return map[string]any{"message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "toolCall", "id": id, "name": "render_html", "arguments": map[string]any{"html": html}},
	}}}
}

func toolResult(id string, isError bool) map[string]any {
	return map[string]any{"message": map[string]any{"role": "toolResult", "toolCallId": id, "toolName": "render_html", "isError": isError,
		"content": []any{map[string]any{"type": "text", "text": "Rendered."}}}}
}

// 视图帧的公开查找：只认范围内活动分支上、已有成功结果的调用；撤销后一律 404。
func TestPublicToolCall(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Views", nil)
	e.add(tid, "s1.jsonl", "u1", "", "message", msg("user", "show me"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", htmlCall("call_ok", "<p>ok</p>"))
	e.add(tid, "s1.jsonl", "r1", "a1", "message", toolResult("call_ok", false))
	e.add(tid, "s1.jsonl", "a2", "r1", "message", htmlCall("call_err", "<p>err</p>"))
	e.add(tid, "s1.jsonl", "r2", "a2", "message", toolResult("call_err", true))
	e.add(tid, "s1.jsonl", "a3", "r2", "message", htmlCall("call_pending", "<p>pending</p>"))
	// 被回退的旁支
	e.add(tid, "s1.jsonl", "a4", "u1", "message", htmlCall("call_side", "<p>side</p>"))
	e.add(tid, "s1.jsonl", "r4", "a4", "message", toolResult("call_side", false))
	// 子 agent 文件
	e.add(tid, "s1/task-1.jsonl", "a5", "", "message", htmlCall("call_sub", "<p>sub</p>"))
	e.add(tid, "s1/task-1.jsonl", "r5", "a5", "message", toolResult("call_sub", false))
	e.add(tid, "s1.jsonl", "u2", "a3", "message", msg("user", "thanks"))
	e.leaf(tid, "u2")

	for _, mode := range []apigen.ShareMode{apigen.ShareModeSnapshot, apigen.ShareModeLive} {
		sh, err := e.svc.Create(e.ctx, tid, mode, false)
		require.NoError(t, err)
		args, err := e.svc.PublicToolCall(e.ctx, sh.Slug, "call_ok", "render_html")
		require.NoError(t, err, mode)
		var p map[string]any
		require.NoError(t, json.Unmarshal(args, &p))
		assert.Equal(t, "<p>ok</p>", p["html"], mode)

		for _, id := range []string{"call_err", "call_pending", "call_side", "call_sub", "missing", ""} {
			_, err := e.svc.PublicToolCall(e.ctx, sh.Slug, id, "render_html")
			assert.ErrorIs(t, err, share.ErrNotFound, "%s（%s）", id, mode)
		}
		_, err = e.svc.PublicToolCall(e.ctx, sh.Slug, "call_ok", "render_ui")
		assert.ErrorIs(t, err, share.ErrNotFound, "工具名不符（%s）", mode)

		require.NoError(t, e.svc.Revoke(e.ctx, sh.ID))
		_, err = e.svc.PublicToolCall(e.ctx, sh.Slug, "call_ok", "render_html")
		assert.ErrorIs(t, err, share.ErrNotFound, "撤销后（%s）", mode)
	}
	_, err := e.svc.PublicToolCall(e.ctx, "nope", "call_ok", "render_html")
	assert.ErrorIs(t, err, share.ErrNotFound)
}
