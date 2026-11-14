package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

func changedSet(names ...string) func(string) bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return func(n string) bool { return m[n] }
}

func TestScheduleAddRequestBody(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "sandbox-m9"}}})
	f.json("GET /api/v1/spaces", 200, apigen.SpaceList{Spaces: []apigen.Space{{ID: "spc_S", Name: "m9"}}})
	f.json("POST /api/v1/schedules", 201, apigen.Schedule{ID: "sch_1"})
	ctx := context.Background()
	t.Setenv("TZ", "Asia/Shanghai")

	o := scheduleFlags{project: "sandbox-m9", name: "tick", every: "1m", keep: 2, prompt: "只回复 run {{run_number}}",
		space: "m9", model: "anthropic/claude-x", thinking: "high", provider: "local", orbType: "small", overlap: "allow"}
	s, err := scheduleAdd(ctx, f.client(), o, changedSet("project", "name", "every", "keep", "prompt", "space", "model", "thinking", "provider", "type", "overlap"), nil, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, "sch_1", s.ID)
	reqs := f.requests()
	last := reqs[len(reqs)-1]
	assert.Equal(t, "POST /api/v1/schedules", last.Method+" "+last.Path)
	assert.JSONEq(t, `{"mode":"new_thread","kind":"every","every":"1m","timezone":"Asia/Shanghai","prompt":"只回复 run {{run_number}}",
		"name":"tick","keepLast":2,"projectId":"prj_A","spaceId":"spc_S","model":{"provider":"anthropic","id":"claude-x"},
		"thinkingLevel":"high","provider":"local","orbType":"small","overlap":"allow"}`, string(last.Body))

	// --no-project、缺省 keep、--at +时长、--disabled
	t.Setenv("TZ", "")
	var errb bytes.Buffer
	before := time.Now()
	o = scheduleFlags{noProject: true, name: "once", at: "+15m", keep: 5, prompt: "p", disabled: true}
	_, err = scheduleAdd(ctx, f.client(), o, changedSet("no-project", "name", "at", "prompt", "disabled"), nil, &errb)
	require.NoError(t, err)
	assert.Contains(t, errb.String(), "UTC", "缺省时区提示")
	var body apigen.ScheduleInput
	reqs = f.requests()
	require.NoError(t, json.Unmarshal(reqs[len(reqs)-1].Body, &body))
	assert.Equal(t, apigen.ScheduleKindOnce, body.Kind)
	assert.Nil(t, body.ProjectID)
	assert.Equal(t, 5, *body.KeepLast)
	assert.False(t, *body.Enabled)
	assert.Equal(t, "UTC", *body.Timezone)
	assert.InDelta(t, before.Add(15*time.Minute).UnixMilli(), *body.At, 5000)

	n := len(f.requests())
	for _, bad := range []scheduleFlags{
		{name: "x", every: "1m", prompt: "p"},                                         // 缺 --project / --no-project
		{project: "sandbox-m9", noProject: true, name: "x", every: "1m", prompt: "p"}, // 两者都给
		{noProject: true, every: "1m", prompt: "p"},                                   // 缺 --name
	} {
		_, err := scheduleAdd(ctx, f.client(), bad, changedSet("every", "prompt"), nil, &bytes.Buffer{})
		var ec *exitCodeError
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, 2, ec.code)
	}
	_, err = scheduleAdd(ctx, f.client(), scheduleFlags{noProject: true, name: "x", every: "1m", cron: "* * * * *", prompt: "p"}, changedSet("every", "cron", "prompt"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "只能给一个")
	_, err = scheduleAdd(ctx, f.client(), scheduleFlags{noProject: true, name: "x", every: "1m"}, changedSet("every"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--prompt")
	assert.Len(t, f.requests(), n, "参数错误时不发请求")
}

func TestScheduleUpdateMergesAndRefs(t *testing.T) {
	f := newFakeAPI(t)
	cur := apigen.Schedule{ID: "sch_9", Mode: apigen.ScheduleModeNewThread, Name: "tick", Kind: apigen.ScheduleKindEvery, Every: new("1m0s"),
		Timezone: "Asia/Shanghai", Prompt: "p", Enabled: false, ProjectID: new("prj_A"), Overlap: apigen.ScheduleOverlapSkip, KeepLast: 5,
		Model: &apigen.ModelRef{Provider: "a", ID: "b"}}
	f.json("GET /api/v1/threads/T-1/schedule", 200, apigen.Schedule{ID: "sch_9"})
	f.json("GET /api/v1/schedules/sch_9", 200, cur)
	f.json("PUT /api/v1/schedules/sch_9", 200, cur)
	ctx := context.Background()

	_, err := scheduleUpdate(ctx, f.client(), "T-1", scheduleFlags{overlap: "allow", keep: 3, cron: "0 9 * * *"}, changedSet("overlap", "keep", "cron"), nil, &bytes.Buffer{})
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "/api/v1/threads/T-1/schedule", reqs[0].Path, "T- 先取线程内定时的 id")
	assert.JSONEq(t, `{"mode":"new_thread","name":"tick","kind":"cron","cron":"0 9 * * *","timezone":"Asia/Shanghai","prompt":"p",
		"enabled":false,"projectId":"prj_A","model":{"provider":"a","id":"b"},"overlap":"allow","keepLast":3}`, string(reqs[2].Body))

	_, err = scheduleUpdate(ctx, f.client(), "sch_9", scheduleFlags{prompt: "q", disabled: false}, changedSet("prompt"), nil, &bytes.Buffer{})
	require.NoError(t, err)
	reqs = f.requests()
	require.Len(t, reqs, 5, "sch_ 直接用")
	var body apigen.ScheduleInput
	require.NoError(t, json.Unmarshal(reqs[4].Body, &body))
	assert.Equal(t, "q", body.Prompt)
	assert.Equal(t, "1m0s", *body.Every, "未给的保留原值")
	assert.False(t, *body.Enabled, "暂停的定时改定义后仍暂停")

	_, err = resolveScheduleRef(ctx, f.client(), "tick")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)

	// 线程内定时不接受 new_thread 参数
	f.json("GET /api/v1/schedules/sch_t", 200, apigen.Schedule{ID: "sch_t", Mode: apigen.ScheduleModeThread, Kind: apigen.ScheduleKindEvery, Every: new("1h0m0s"), ThreadID: new("T-1")})
	_, err = scheduleUpdate(ctx, f.client(), "sch_t", scheduleFlags{keep: 3}, changedSet("keep"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--keep")
}

func TestScheduleListQuery(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: "prj_A", Name: "sandbox-m9"}}})
	f.json("GET /api/v1/schedules", 200, apigen.ScheduleList{Schedules: []apigen.Schedule{{ID: "sch_1"}}})
	ctx := context.Background()
	list, err := scheduleList(ctx, f.client(), "sandbox-m9", "new_thread")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	_, err = scheduleList(ctx, f.client(), "none", "")
	require.NoError(t, err)
	_, err = scheduleList(ctx, f.client(), "", "")
	require.NoError(t, err)
	var qs []string
	for _, r := range f.requests() {
		if r.Path == "/api/v1/schedules" {
			qs = append(qs, r.RawQuery)
		}
	}
	assert.Equal(t, []string{"mode=new_thread&project=prj_A", "project=none", ""}, qs)
	_, err = scheduleList(ctx, f.client(), "", "loop")
	assert.Error(t, err)
}

func TestScheduleSetInputAndPrompts(t *testing.T) {
	var in apigen.ScheduleInput
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	o := scheduleFlags{cron: "0 9 * * 1-5", tz: "Asia/Shanghai", name: "日报", until: "2027-01-01T00:00:00+08:00", promptFile: "-"}
	require.NoError(t, applyScheduleFlags(&in, o, changedSet("cron", "tz", "name", "until", "prompt-file"), true, now, strings.NewReader("写日报\n"), &bytes.Buffer{}))
	b, _ := json.Marshal(in)
	assert.JSONEq(t, `{"kind":"cron","cron":"0 9 * * 1-5","timezone":"Asia/Shanghai","name":"日报","endsAt":1798732800000,"prompt":"写日报"}`, string(b))

	_, err := parseAt("tomorrow", now)
	assert.Error(t, err)
	at, err := parseAt("2026-01-02T09:00:00+08:00", now)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 2, 1, 0, 0, 0, time.UTC).UnixMilli(), at)
}

// ---- orb 内 ----

// requests 加锁取已收到的请求。
func (f *fakeOrbd) requests() []orbsock.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]orbsock.Request(nil), f.reqs...)
}

func startOrbThreadSock(t *testing.T, h orbsock.Handler) *fakeOrbd {
	t.Helper()
	t.Setenv("ORYN_API_TOKEN", "")
	f := startFakeOrbd(t, h)
	t.Setenv("ORYN_THREAD_ID", "T-caller")
	return f
}

func TestOrbThreadCommands(t *testing.T) {
	f := startOrbThreadSock(t, func(_ context.Context, method string, params json.RawMessage, _ *orbsock.Responder) (any, error) {
		switch method {
		case orbsock.MethodThreadNew:
			return apigen.LinkCtlThreadInfo{ID: "T-child", State: apigen.ThreadStateProvisioning}, nil
		case orbsock.MethodThreadSend:
			return map[string]any{}, nil
		case orbsock.MethodThreadGet:
			return nil, orbsock.Errorf("forbidden", "not a related thread")
		}
		return nil, orbsock.Errorf(orbsock.CodeMethodNotFound, "%s", method)
	})
	require.True(t, inOrb())
	ctx := context.Background()
	info, err := orbThreadNew(ctx, "做一件事", "子任务", "small")
	require.NoError(t, err)
	assert.Equal(t, "T-child", info.ID)
	require.NoError(t, orbThreadSend(ctx, "T-child", "继续", true))
	_, err = orbThreadGet(ctx, "T-other")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)
	assert.ErrorContains(t, err, "forbidden")

	reqs := f.requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, orbsock.MethodThreadNew, reqs[0].Method)
	assert.JSONEq(t, `{"threadId":"T-caller","prompt":"做一件事","title":"子任务","type":"small"}`, string(reqs[0].Params))
	assert.JSONEq(t, `{"threadId":"T-caller","target":"T-child","message":"继续","steer":true}`, string(reqs[1].Params))

	t.Setenv("ORYN_THREAD_ID", "")
	_, err = orbThreadNew(ctx, "x", "", "")
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 2, ec.code)

	// 显式 API 凭据时不算 orb 内
	t.Setenv("ORYN_API_TOKEN", "odt_x")
	assert.False(t, inOrb())
}

func TestThreadWaitExitCodes(t *testing.T) {
	var reply func() (any, error)
	f := startOrbThreadSock(t, func(_ context.Context, method string, _ json.RawMessage, _ *orbsock.Responder) (any, error) {
		return reply()
	})
	ctx := context.Background()
	cases := []struct {
		state apigen.ThreadState
		code  int
	}{{apigen.ThreadStateIdle, 0}, {apigen.ThreadStateAwaitingInput, 3}, {apigen.ThreadStateError, 4}}
	for _, c := range cases {
		reply = func() (any, error) {
			return apigen.LinkCtlThreadInfo{ID: "T-child", State: c.state, Settled: true, LastAssistantText: new("done\n")}, nil
		}
		var out, errb bytes.Buffer
		assert.Equal(t, c.code, threadWait(ctx, "T-child", 90*time.Second, false, &out, &errb), string(c.state))
		assert.Equal(t, "done\n", out.String())
	}
	reqs := f.requests()
	require.NotEmpty(t, reqs)
	assert.JSONEq(t, `{"threadId":"T-caller","target":"T-child","timeoutSeconds":90}`, string(reqs[0].Params))

	reply = func() (any, error) { return nil, orbsock.Errorf(orbsock.CodeTimeout, "not settled") }
	var out, errb bytes.Buffer
	assert.Equal(t, 124, threadWait(ctx, "T-child", time.Minute, false, &out, &errb))
	reply = func() (any, error) { return nil, orbsock.Errorf("forbidden", "no") }
	assert.Equal(t, 2, threadWait(ctx, "T-child", time.Minute, false, &out, &errb))
	assert.Equal(t, 2, threadWait(ctx, "T-child", 25*time.Hour, false, &out, &errb), "超过上限")

	t.Setenv("ORYN_ORB", "")
	assert.Equal(t, 2, threadWait(ctx, "T-child", time.Minute, false, &out, &errb), "orb 外不可用")
}

func TestOrbScheduleCommands(t *testing.T) {
	sched := apigen.Schedule{ID: "sch_1", Mode: apigen.ScheduleModeThread, Kind: apigen.ScheduleKindEvery, Every: new("30m0s")}
	f := startOrbThreadSock(t, func(_ context.Context, method string, _ json.RawMessage, _ *orbsock.Responder) (any, error) {
		switch method {
		case orbsock.MethodScheduleGet:
			return apigen.LinkCtlScheduleResult{}, nil
		case orbsock.MethodScheduleDelete:
			return apigen.LinkCtlScheduleResult{}, nil
		}
		return apigen.LinkCtlScheduleResult{Schedule: &sched}, nil
	})
	ctx := context.Background()
	in := apigen.ScheduleInput{Kind: apigen.ScheduleKindEvery, Every: new("30m"), Prompt: "p", Timezone: new("UTC")}
	s, err := orbScheduleSet(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, "sch_1", s.ID)
	got, err := orbScheduleGet(ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "没有定时")
	_, err = orbScheduleEnable(ctx, false)
	require.NoError(t, err)
	require.NoError(t, orbScheduleDelete(ctx))

	reqs := f.requests()
	require.Len(t, reqs, 4)
	assert.Equal(t, []string{orbsock.MethodScheduleSet, orbsock.MethodScheduleGet, orbsock.MethodScheduleEnable, orbsock.MethodScheduleDelete},
		[]string{reqs[0].Method, reqs[1].Method, reqs[2].Method, reqs[3].Method})
	assert.JSONEq(t, `{"threadId":"T-caller","input":{"kind":"every","every":"30m","prompt":"p","timezone":"UTC"}}`, string(reqs[0].Params))
	assert.JSONEq(t, `{"threadId":"T-caller","enabled":false}`, string(reqs[2].Params))
}

func TestThreadNewSpace(t *testing.T) {
	f := newFakeAPI(t)
	f.json("GET /api/v1/spaces", 200, apigen.SpaceList{Spaces: []apigen.Space{{ID: "spc_S", Name: "m9"}}})
	f.json("POST /api/v1/threads", 201, apigen.ThreadEnvelope{Thread: sampleThread("T-1")})
	_, err := threadNew(context.Background(), f.client(), "hi", threadNewOptions{space: "m9"})
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "archived=true", reqs[0].RawQuery, "解析名字时含已归档的 Space")
	assert.JSONEq(t, `{"prompt":"hi","spaceId":"spc_S","executor":{"kind":"new_orb"}}`, string(reqs[1].Body))
	_, err = threadNew(context.Background(), f.client(), "hi", threadNewOptions{space: "nope"})
	assert.ErrorContains(t, err, "不存在")
}
