package research

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

func TestWebPromptHasNoBaseline(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp, err := f.s.CreateTopic(f.ctx, TopicInput{Title: "Subsidy 2027", Question: "Is the EV subsidy extended into 2027?",
		Baseline: "BASELINE-SECRET: yes, certainly", SourceThreadID: "T-SOURCE-THREAD"})
	require.NoError(t, err)
	f.due(tp.ID)
	f.s.step(f.ctx)
	p := f.host.lastDelivery(t).prompt
	assert.True(t, strings.HasPrefix(p, "[Oryn "), "自动化消息以 [Oryn 开头")
	assert.NotContains(t, p, "BASELINE-SECRET")
	assert.NotContains(t, p, "T-SOURCE-THREAD")
	assert.Contains(t, p, "Is the EV subsidy extended into 2027?")
	assert.Contains(t, p, "today: 2026-11-22")
	assert.Contains(t, p, "previous_report: (none")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(p), "question: Is the EV subsidy extended into 2027?"), "问题排在最末")
}

func TestWebUpdatePostsRestrictedFinding(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp, err := f.s.CreateTopic(f.ctx, TopicInput{Title: "Subsidy 2027", Question: "Q?", Baseline: "B", SourceThreadID: "T-src"})
	require.NoError(t, err)
	f.due(tp.ID)
	f.s.step(f.ctx)
	d := f.host.lastDelivery(t)
	f.reply(d.runID, `{"status":"update","summary":"  It was cut.  ","sources":[{"url":"https://gov.example/a","title":"Gov","date":"2026-11-20"},{"url":"javascript:alert(1)","title":"x"}]}`)
	require.Len(t, f.host.findings, 1)
	fd := f.host.findings[0]
	assert.True(t, fd.Restricted)
	assert.Equal(t, d.runID, fd.RunID)
	assert.Equal(t, "B", fd.Baseline)
	assert.Equal(t, "Q?", fd.Question)
	assert.Equal(t, "T-src", fd.ThreadID)
	assert.Equal(t, "It was cut.", fd.Summary)
	require.Len(t, fd.Sources, 1, "只留 http(s) 来源")
	assert.Equal(t, "https://gov.example/a", fd.Sources[0].URL)
	status, outcome, _ := f.run(d.runID)
	assert.Equal(t, "completed", status)
	assert.Equal(t, "findings:1", outcome)
	got, err := f.s.GetTopic(f.ctx, tp.ID)
	require.NoError(t, err)
	assert.Equal(t, "It was cut.", got.LastReport)
	assert.Equal(t, 1, got.RunCount)
	require.Len(t, got.LastSources, 1)

	// 下一次的提示词带上一次的报告
	f.due(tp.ID)
	f.s.step(f.ctx)
	assert.Contains(t, f.host.lastDelivery(t).prompt, "previous_report: It was cut.")
}

func TestWebNothingDoublesInterval(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	want := []time.Duration{48 * time.Hour, 96 * time.Hour, 192 * time.Hour - 24*time.Hour, webMaxInterval}
	want[2] = webMaxInterval // 96h*2 = 192h > 168h：封顶
	for i, w := range want {
		f.due(tp.ID)
		f.s.step(f.ctx)
		f.reply(f.host.lastDelivery(t).runID, `{"status":"nothing","summary":""}`)
		got, err := f.s.GetTopic(f.ctx, tp.ID)
		require.NoError(t, err)
		assert.Equal(t, int(w/time.Second), got.IntervalSeconds, "第 %d 次", i+1)
		require.NotNil(t, got.NextRunAt)
		assert.Equal(t, f.clk.Now().Add(w).UnixMilli(), *got.NextRunAt)
	}
	assert.Empty(t, f.host.findings)
}

func TestWebUpdateResetsInterval(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	for range 2 {
		f.due(tp.ID)
		f.s.step(f.ctx)
		f.reply(f.host.lastDelivery(t).runID, `{"status":"nothing"}`)
	}
	f.due(tp.ID)
	f.s.step(f.ctx)
	f.reply(f.host.lastDelivery(t).runID, `{"status":"update","summary":"new"}`)
	got, err := f.s.GetTopic(f.ctx, tp.ID)
	require.NoError(t, err)
	assert.Equal(t, int(webBaseInterval/time.Second), got.IntervalSeconds)
}

func TestQuestionExpires(t *testing.T) {
	f := newFixture(t)
	f.enable()
	q := f.topic("q", "question")
	interest, err := f.s.CreateTopic(f.ctx, TopicInput{Kind: kindInterest, Title: "i", Question: "interest"})
	require.NoError(t, err)
	f.clk.Add(questionTTL + time.Hour)
	for range 2 {
		f.s.step(f.ctx)
		f.reply(f.host.lastDelivery(t).runID, `{"status":"nothing"}`)
	}
	got, err := f.s.GetTopic(f.ctx, q.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.ResearchTopicDone, got.State)
	assert.Nil(t, got.NextRunAt)
	got, err = f.s.GetTopic(f.ctx, interest.ID)
	require.NoError(t, err)
	assert.Equal(t, apigen.ResearchTopicActive, got.State, "长期关注不结束")
	_, err = f.s.RunTopicNow(f.ctx, q.ID)
	assert.ErrorContains(t, err, "only active")
}

func TestWebSkippedWhenWebAccessOff(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	f.due(tp.ID)
	f.webOn = false
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries())
	f.webOn = true
	f.enable(func(in *apigen.ResearchSettings) { in.Web = false })
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries(), "公网跟进关着")
}

func TestUpdateTopicPauseResume(t *testing.T) {
	f := newFixture(t)
	f.enable()
	tp := f.topic("t", "q")
	paused := apigen.ResearchTopicPatchPaused
	got, err := f.s.UpdateTopic(f.ctx, tp.ID, apigen.ResearchTopicPatch{State: &paused})
	require.NoError(t, err)
	assert.Equal(t, apigen.ResearchTopicPaused, got.State)
	assert.Nil(t, got.NextRunAt)
	f.clk.Add(48 * time.Hour)
	f.s.step(f.ctx)
	assert.Zero(t, f.host.deliveries(), "暂停的不查")
	active := apigen.ResearchTopicPatchActive
	interest := apigen.ResearchTopicInterest
	got, err = f.s.UpdateTopic(f.ctx, tp.ID, apigen.ResearchTopicPatch{State: &active, Kind: &interest, Title: new("renamed")})
	require.NoError(t, err)
	assert.Equal(t, apigen.ResearchTopicActive, got.State)
	assert.Equal(t, "renamed", got.Title)
	assert.Nil(t, got.ExpiresAt)
	_, err = f.s.UpdateTopic(f.ctx, "rtp_missing", apigen.ResearchTopicPatch{})
	assert.ErrorContains(t, err, "not found")
}
