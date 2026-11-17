package schedule

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// M19 步骤 3.3：交给助手。

// handOffs 接管 Config.HandOff，返回已收到的交接。
func (f *fixture) handOffs() func() []HandOff {
	var mu sync.Mutex
	var got []HandOff
	f.s.cfg.HandOff = func(_ context.Context, h HandOff) {
		mu.Lock()
		got = append(got, h)
		mu.Unlock()
	}
	return func() []HandOff {
		mu.Lock()
		defer mu.Unlock()
		return append([]HandOff(nil), got...)
	}
}

// handToAssistant 只给 new_thread：thread 模式带 true 被拒；存库、进响应；更新时省略沿用现值。
func TestHandToAssistantInput(t *testing.T) {
	f := newFixture(t)
	th := f.thread()
	_, err := f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", HandToAssistant: new(true)}, "user")
	requireCode(t, err, CodeInvalidSchedule)
	_, err = f.s.PutForThread(f.ctx, th, Input{Kind: KindEvery, Every: "1h", Prompt: "p", HandToAssistant: new(false)}, "user")
	require.NoError(t, err, "false 在 thread 模式无害")

	s := f.putNew(Input{Name: "digest", Kind: KindEvery, Every: "1h", Prompt: "p", HandToAssistant: new(true)})
	assert.True(t, s.HandToAssistant)
	got, err := f.s.Put(f.ctx, s.ID, Input{Name: "digest", Kind: KindEvery, Every: "2h", Prompt: "p"}, "user")
	require.NoError(t, err)
	assert.True(t, got.HandToAssistant, "省略 = 沿用现值")
	got, err = f.s.Put(f.ctx, s.ID, Input{Name: "digest", Kind: KindEvery, Every: "2h", Prompt: "p", HandToAssistant: new(false)}, "user")
	require.NoError(t, err)
	assert.False(t, got.HandToAssistant)
}

// 开着开关的 new_thread 运行结束（完成、出错、中断）时调用 HandOff；没开开关或线程内定时不调用。
func TestHandOffOnSettle(t *testing.T) {
	f := newFixture(t)
	got := f.handOffs()
	s := f.putNew(Input{Name: "digest", Kind: KindEvery, Every: "1h", Prompt: "p", HandToAssistant: new(true)})
	f.putNew(Input{Name: "plain", Kind: KindEvery, Every: "1h", Prompt: "p"})

	f.advanceTick(time.Hour)
	runs := map[string]string{} // 定时 id → 线程
	tags := map[string]string{}
	for _, c := range f.th.Creates() {
		tags[c.ScheduleID] = c.Tag
		runs[c.ScheduleID] = *f.runs(c.ScheduleID)[0].ThreadID
	}
	require.Len(t, runs, 2)
	for id, tid := range runs {
		f.state(tid, apigen.ThreadStateWorking)
		f.s.TurnEnded(threads.TurnEvent{ThreadID: tid, Tag: tags[id], Status: threads.TurnCompleted, Text: "3 new mails", HasText: true})
		f.process()
	}
	h := got()
	require.Len(t, h, 1, "只有开着开关的定时交接")
	assert.Equal(t, HandOff{Kind: "schedule", SourceID: s.ID, SourceName: "digest", ThreadID: runs[s.ID], Outcome: "completed", Text: "3 new mails"}, h[0])

	// 出错与中断同样交接
	f.advanceTick(time.Hour)
	var tid, tag string
	for _, c := range f.th.Creates() {
		if c.ScheduleID == s.ID {
			tag = c.Tag
		}
	}
	tid = *f.runs(s.ID)[0].ThreadID
	f.state(tid, apigen.ThreadStateWorking)
	f.turn(tid, tag, threads.TurnError, "model not found")
	h = got()
	require.Len(t, h, 2)
	assert.Equal(t, "failed", h[1].Outcome)
	assert.Equal(t, "model not found", h[1].Error)
	assert.Empty(t, h[1].Text)
}
