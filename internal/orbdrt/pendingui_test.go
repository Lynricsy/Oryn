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

	p.observeIn([]byte(`{"type":"plan_approval","approved":true}`))
	assert.Len(t, p.replay(), 1)
}
