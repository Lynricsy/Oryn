package github

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/schedule"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19 步骤 3.3：触发规则的“交给助手”。

// 开关随规则增改存取。
func TestTriggerHandToAssistantCRUD(t *testing.T) {
	e := newTriggerEnv(t)
	tr := e.trigger(apigen.GitHubTriggerInput{Name: "triage", Event: "issue_opened", Prompt: "p", HandToAssistant: new(true)})
	assert.True(t, tr.HandToAssistant)
	up, err := e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{Prompt: new("q")})
	require.NoError(t, err)
	assert.True(t, up.HandToAssistant, "省略 = 不变")
	up, err = e.s.UpdateTrigger(e.ctx, tr.ID, apigen.GitHubTriggerPatch{HandToAssistant: new(false)})
	require.NoError(t, err)
	assert.False(t, up.HandToAssistant)
}

// 开着开关的规则线程，每个 GitHub 回合结束（任何终态）都交接；别人的标签、本地处理、没开开关的规则不交接。
func TestTriggerHandOffOnTurn(t *testing.T) {
	e := newTaskEnv(t, apigen.GitHubTriggerInput{HandToAssistant: new(true)})
	var got []schedule.HandOff
	e.s.cfg.HandOff = func(_ context.Context, h schedule.HandOff) { got = append(got, h) }
	thr := e.thread()
	obs := e.s.TurnObserver()
	turn := func(ev threads.TurnEvent) {
		ev.ThreadID = thr
		obs.TurnEnded(ev)
		e.drain()
	}
	turn(threads.TurnEvent{Tag: "srn_x", Status: threads.TurnCompleted})
	turn(threads.TurnEvent{Tag: newTurnTag(), Status: threads.TurnCompleted, Local: true})
	assert.Empty(t, got)

	turn(threads.TurnEvent{Tag: newTurnTag(), Status: threads.TurnCompleted, Text: "fixed it", HasText: true})
	turn(threads.TurnEvent{Tag: newTurnTag(), Status: threads.TurnError, Error: "rate limited"})
	require.Len(t, got, 2)
	assert.Equal(t, schedule.HandOff{Kind: "trigger", SourceID: e.rule.ID, SourceName: e.rule.Name, ThreadID: thr, Outcome: "completed", Text: "fixed it"}, got[0])
	assert.Equal(t, "failed", got[1].Outcome)
	assert.Equal(t, "rate limited", got[1].Error)

	off := newTaskEnv(t, apigen.GitHubTriggerInput{})
	var none []schedule.HandOff
	off.s.cfg.HandOff = func(_ context.Context, h schedule.HandOff) { none = append(none, h) }
	offThr := off.thread()
	off.s.TurnObserver().TurnEnded(threads.TurnEvent{ThreadID: offThr, Tag: newTurnTag(), Status: threads.TurnCompleted})
	off.drain()
	assert.Empty(t, none)
}
