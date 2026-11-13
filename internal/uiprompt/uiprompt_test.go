package uiprompt

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// answerer 返回一个断言 err 为空并给出应答帧 JSON 文本的函数（可直接包住 AnswerOption / AnswerText 的两个返回值）。
func answerer(t *testing.T) func(json.RawMessage, error) string {
	return func(b json.RawMessage, err error) string {
		t.Helper()
		require.NoError(t, err)
		return string(b)
	}
}

func TestSelect(t *testing.T) {
	ans := answerer(t)
	f := raw(`{"type":"extension_ui_request","id":"u1","method":"select","title":"Allow  bash?\n(rm -rf)","options":["Allow","Deny","Always allow"]}`)
	p := Describe(f, Labels{})
	assert.Equal(t, Prompt{Title: "Allow bash? (rm -rf)", Options: []string{"Allow", "Deny", "Always allow"}, Supported: true}, p)

	assert.JSONEq(t, `{"type":"extension_ui_response","id":"u1","value":"Deny"}`, ans(AnswerOption(f, 1)))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"u1","value":"Always allow"}`, ans(AnswerText(f, " 3 ")))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"u1","value":"Always allow"}`, ans(AnswerText(f, "always ALLOW")))
	_, err := AnswerText(f, "maybe")
	assert.ErrorIs(t, err, ErrNeedOption)
	_, err = AnswerText(f, "4")
	assert.ErrorIs(t, err, ErrNeedOption)
	_, err = AnswerOption(f, 3)
	assert.ErrorIs(t, err, ErrNeedOption)
	_, err = AnswerOption(f, -1)
	assert.ErrorIs(t, err, ErrNeedOption)
}

func TestConfirm(t *testing.T) {
	ans := answerer(t)
	f := raw(`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","message":"This will push.\nContinue?"}`)
	assert.Equal(t, Prompt{Title: "Proceed?", Body: "This will push.\nContinue?", Options: []string{"是", "否"}, Supported: true},
		Describe(f, Labels{Yes: "是", No: "否"}))
	assert.Equal(t, []string{"Yes", "No"}, Describe(f, Labels{}).Options)

	yes := `{"type":"extension_ui_response","id":"c1","confirmed":true}`
	no := `{"type":"extension_ui_response","id":"c1","confirmed":false}`
	assert.JSONEq(t, yes, ans(AnswerOption(f, 0)))
	assert.JSONEq(t, no, ans(AnswerOption(f, 1)))
	for _, s := range []string{"1", "yes", "Y", "是", "好", "确认", " Yes "} {
		assert.JSONEq(t, yes, ans(AnswerText(f, s)), s)
	}
	for _, s := range []string{"2", "no", "N", "否", "不", "取消"} {
		assert.JSONEq(t, no, ans(AnswerText(f, s)), s)
	}
	_, err := AnswerText(f, "perhaps")
	assert.ErrorIs(t, err, ErrNeedOption)
}

func TestInputEditor(t *testing.T) {
	ans := answerer(t)
	f := raw(`{"type":"extension_ui_request","id":"i1","method":"input","title":"Branch name","placeholder":"main"}`)
	assert.Equal(t, Prompt{Title: "Branch name", FreeText: true, Supported: true}, Describe(f, Labels{}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"i1","value":"feat/x"}`, ans(AnswerText(f, "feat/x")))
	_, err := AnswerOption(f, 0)
	assert.ErrorIs(t, err, ErrNeedOption)

	e := raw(`{"type":"extension_ui_request","id":"e1","method":"editor","title":"Commit message","prefill":"x"}`)
	assert.True(t, Describe(e, Labels{}).Supported)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"e1","value":"line1\nline2"}`, ans(AnswerText(e, "line1\nline2")))

	for _, extra := range []string{`"secret":true`, `"authCodeFor":"o1"`, `"loginPromptFor":"o1"`} {
		s := raw(`{"type":"extension_ui_request","id":"i2","method":"input","title":"API key",` + extra + `}`)
		p := Describe(s, Labels{})
		assert.False(t, p.Supported, extra)
		assert.Equal(t, "API key", p.Title)
		_, err := AnswerText(s, "sk-…")
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}

func TestForm(t *testing.T) {
	ans := answerer(t)
	// ask 工具的单选题：字段无 label，问题取前面最近的 text 块；其余 text / list / pre 进 Body；other → FreeText。
	f := raw(`{"type":"extension_ui_request","id":"f1","method":"form","sections":[{"title":[{"text":"Pick"}],"blocks":[
		{"kind":"text","spans":[{"text":"Context first."}],"heading":false},
		{"kind":"list","items":[[{"text":"a"}],[{"text":"b"}]],"ordered":true},
		{"kind":"text","spans":[{"text":"Which "},{"text":"database","style":"strong"},{"text":"?"}],"heading":false},
		{"kind":"field","field":{"kind":"choice","name":"db","multi":false,"select":false,"other":true,"label":[],"required":true,
			"options":[{"value":"pg","label":[{"text":"Postgres"}],"checked":false,"disabled":false},
			           {"value":"sqlite","label":[{"text":"SQLite"}],"checked":false,"disabled":false}]}},
		{"kind":"pre","text":"note\n"},
		{"kind":"table","rows":[],"headerRows":0}
	]}]}`)
	assert.Equal(t, Prompt{
		Title: "Which database?", Body: "Context first.\n1. a\n2. b\nnote",
		Options: []string{"Postgres", "SQLite"}, FreeText: true, Supported: true,
	}, Describe(f, Labels{}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"f1","formData":{"db":"sqlite"}}`, ans(AnswerOption(f, 1)))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"f1","formData":{"db":"pg"}}`, ans(AnswerText(f, "postgres")))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"f1","formData":{"db":"mysql"}}`, ans(AnswerText(f, " mysql ")))

	// 字段自带 label；不接受其它答案。
	g := raw(`{"type":"extension_ui_request","id":"f2","method":"form","sections":[{"title":[],"blocks":[
		{"kind":"field","field":{"kind":"choice","name":"env","multi":false,"other":false,"label":[{"text":"Environment"}],
			"options":[{"value":"prod","label":[{"text":"Production"}]},{"value":"dev","label":[{"text":"Dev"}]}]}}]}]}`)
	p := Describe(g, Labels{})
	assert.Equal(t, "Environment", p.Title)
	assert.False(t, p.FreeText)
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"f2","formData":{"env":"dev"}}`, ans(AnswerText(g, "2")))
	_, err := AnswerText(g, "staging")
	assert.ErrorIs(t, err, ErrNeedOption)

	// 单个文本字段。
	h := raw(`{"type":"extension_ui_request","id":"f3","method":"form","sections":[{"title":[{"text":"Name"}],"blocks":[
		{"kind":"field","field":{"kind":"text","name":"name","type":"text","multiline":false,"label":[{"text":"Project name"}],"value":"","placeholder":"","required":true,"rows":1}}]}]}`)
	assert.Equal(t, Prompt{Title: "Project name", FreeText: true, Supported: true}, Describe(h, Labels{}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"f3","formData":{"name":"oryn"}}`, ans(AnswerText(h, "oryn")))

	// 多字段 / 多选：不支持。
	for _, s := range []string{
		`{"type":"extension_ui_request","id":"f4","method":"form","sections":[{"title":[],"blocks":[
			{"kind":"field","field":{"kind":"text","name":"a","label":[]}}]},{"title":[],"blocks":[
			{"kind":"field","field":{"kind":"text","name":"b","label":[]}}]}]}`,
		`{"type":"extension_ui_request","id":"f5","method":"form","sections":[{"title":[],"blocks":[
			{"kind":"field","field":{"kind":"choice","name":"a","multi":true,"label":[],"options":[{"value":"x","label":[{"text":"X"}]}]}}]}]}`,
		`{"type":"extension_ui_request","id":"f6","method":"form","sections":[{"title":[],"blocks":[{"kind":"text","spans":[{"text":"no fields"}]}]}]}`,
	} {
		assert.False(t, Describe(raw(s), Labels{}).Supported)
		_, err := AnswerOption(raw(s), 0)
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}

func TestAsk(t *testing.T) {
	ans := answerer(t)
	f := raw(`{"type":"extension_ui_request","id":"a1","method":"ask","questions":[{"id":"q1","question":"A or B?","options":[{"label":"A"},{"label":"B","description":"second"}],"recommended":0}]}`)
	assert.Equal(t, Prompt{Title: "A or B?", Options: []string{"A", "B"}, FreeText: true, Supported: true}, Describe(f, Labels{}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"a1","answers":[{"id":"q1","selectedOptions":["B"]}]}`, ans(AnswerOption(f, 1)))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"a1","answers":[{"id":"q1","selectedOptions":["A"]}]}`, ans(AnswerText(f, "1")))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"a1","answers":[{"id":"q1","selectedOptions":[],"customInput":"neither"}]}`, ans(AnswerText(f, "neither ")))

	for _, s := range []string{
		`{"type":"extension_ui_request","id":"a2","method":"ask","questions":[{"id":"q1","question":"x","options":[]},{"id":"q2","question":"y","options":[]}]}`,
		`{"type":"extension_ui_request","id":"a3","method":"ask","questions":[{"id":"q1","question":"x","multi":true,"options":[{"label":"A"}]}]}`,
	} {
		assert.False(t, Describe(raw(s), Labels{}).Supported)
		_, err := AnswerText(raw(s), "A")
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}

func TestAskDialog(t *testing.T) {
	ans := answerer(t)
	f := raw(`{"type":"extension_ui_request","id":"d1","method":"askDialog","questions":[{"id":"q1","question":"Ship it?","options":[{"label":"Ship"},{"label":"Wait"}]}]}`)
	assert.Equal(t, Prompt{Title: "Ship it?", Options: []string{"Ship", "Wait"}, FreeText: true, Supported: true}, Describe(f, Labels{}))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"d1","askDialog":{"kind":"submit","results":[
		{"id":"q1","question":"Ship it?","options":["Ship","Wait"],"multi":false,"selectedOptions":["Ship"]}]}}`, ans(AnswerOption(f, 0)))
	assert.JSONEq(t, `{"type":"extension_ui_response","id":"d1","askDialog":{"kind":"submit","results":[
		{"id":"q1","question":"Ship it?","options":["Ship","Wait"],"multi":false,"selectedOptions":[],"customInput":"after lunch"}]}}`, ans(AnswerText(f, "after lunch")))

	multi := raw(`{"type":"extension_ui_request","id":"d2","method":"askDialog","questions":[{"id":"q1","question":"x","multi":true,"options":[]}]}`)
	assert.False(t, Describe(multi, Labels{}).Supported)
}

func TestUnsupported(t *testing.T) {
	for _, tc := range []struct{ frame, title string }{
		{`{"type":"plan_proposal","title":"Refactor auth","plan":"…"}`, "Refactor auth"},
		{`{"type":"extension_ui_request","id":"o1","method":"open_url","url":"https://x"}`, ""},
		{`{"type":"extension_ui_request","id":"n1","method":"notify","message":"hi"}`, ""},
		{`{"type":"extension_ui_request","id":"t1","method":"setTitle","title":"T"}`, "T"},
		{`not json`, ""},
	} {
		p := Describe(raw(tc.frame), Labels{})
		assert.Equal(t, Prompt{Title: tc.title}, p, tc.frame)
		_, err := AnswerOption(raw(tc.frame), 0)
		assert.ErrorIs(t, err, ErrUnsupported)
		_, err = AnswerText(raw(tc.frame), "x")
		assert.ErrorIs(t, err, ErrUnsupported)
	}
}
