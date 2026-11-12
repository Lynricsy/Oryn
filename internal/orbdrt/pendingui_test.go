package orbdrt

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPendingUIReplay(t *testing.T) {
	var p pendingUI
	p.observeOut([]byte(`{"type":"extension_ui_request","id":"a","method":"select"}`))
	p.observeOut([]byte(`{"type":"extension_ui_request","id":"n","method":"notify"}`))
	p.observeOut([]byte(`{"type":"extension_ui_request","id":"b","method":"form"}`))
	p.observeOut([]byte(`{"type":"extension_ui_request","id":"c","method":"ask"}`))
	p.observeOut([]byte(`{"type":"plan_proposal","title":"x"}`))
	p.observeOut([]byte(`{"type":"extension_ui_request","id":"x","method":"cancel","targetId":"c"}`))
	p.observeIn([]byte(`{"type":"extension_ui_response","id":"a","value":"ok"}`))

	got := p.replay()
	var s []string
	for _, f := range got {
		s = append(s, string(f))
	}
	assert.Equal(t, []string{
		`{"type":"plan_proposal","title":"x"}`,
		`{"type":"extension_ui_request","id":"b","method":"form"}`,
	}, s, "已作答 / 已取消 / 仅展示的不重放；计划提案在前")

	p.observeIn([]byte(`{"type":"plan_approval","id":"ok1","approved":true}`))
	p.observeOut([]byte(`{"type":"response","id":"ok1","command":"plan_approval","success":true,"data":{}}`))
	assert.Len(t, p.replay(), 1, "成功批准后提案不再重放")
}

// 失败的 plan_approval（omp 仍保留待批提案）不能清掉重放槽；只有对应 id 的成功应答才清除。
func TestPendingUIPlanClearedOnlyOnSuccess(t *testing.T) {
	var p pendingUI
	proposal := `{"type":"plan_proposal","planFilePath":"local://PLAN.md","title":"x"}`
	p.observeOut([]byte(proposal))

	p.observeIn([]byte(`{"type":"plan_approval","id":"a1","approved":true,"option":"save","savePath":"rel.md"}`))
	assert.Equal(t, [][]byte{[]byte(proposal)}, p.replay(), "仅写出命令不算批准")
	p.observeOut([]byte(`{"type":"response","id":"a1","command":"plan_approval","success":false,"error":"Saving a plan requires an absolute savePath"}`))
	assert.Equal(t, [][]byte{[]byte(proposal)}, p.replay(), "失败的批准保留提案")

	p.observeIn([]byte(`{"type":"plan_approval","id":"a2","approved":true}`))
	p.observeOut([]byte(`{"type":"response","id":"other","command":"get_state","success":true}`))
	assert.Len(t, p.replay(), 1, "无关应答不清除")
	p.observeOut([]byte(`{"type":"response","id":"a2","command":"plan_approval","success":true,"data":{"approved":true}}`))
	assert.Empty(t, p.replay())

	p.observeOut([]byte(proposal))
	p.observeIn([]byte(`{"type":"plan_approval","id":"a3","approved":true}`))
	p.observeOut([]byte(`{"type":"response","id":"a3","command":"plan_approval","success":false,"error":"No plan is awaiting approval"}`))
	assert.Empty(t, p.replay(), "omp 已无提案时同样清除")
}
