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

func TestScheduleManualKind(t *testing.T) {
	f := newFakeAPI(t)
	f.json("POST /api/v1/schedules", 201, apigen.Schedule{ID: "sch_m"})
	ctx := context.Background()
	t.Setenv("TZ", "")

	var errb bytes.Buffer
	o := scheduleFlags{noProject: true, name: "hook", manual: true, keep: 5, prompt: "处理推送"}
	_, err := scheduleAdd(ctx, f.client(), o, changedSet("no-project", "name", "manual", "prompt"), nil, &errb)
	require.NoError(t, err)
	reqs := f.requests()
	assert.JSONEq(t, `{"mode":"new_thread","kind":"manual","prompt":"处理推送","name":"hook","keepLast":5}`, string(reqs[len(reqs)-1].Body),
		"manual 不带时间字段，也不填缺省时区")
	assert.Empty(t, errb.String(), "manual 不提示缺省时区")

	n := len(f.requests())
	_, err = scheduleAdd(ctx, f.client(), scheduleFlags{noProject: true, name: "x", manual: true, every: "1m", prompt: "p"}, changedSet("manual", "every", "prompt"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--every / --manual 只能给一个")
	_, err = scheduleAdd(ctx, f.client(), scheduleFlags{noProject: true, name: "x", manual: true, until: "2027-01-01T00:00:00Z", prompt: "p"}, changedSet("manual", "until", "prompt"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--until")
	_, err = scheduleAdd(ctx, f.client(), scheduleFlags{noProject: true, name: "x", prompt: "p"}, changedSet("prompt"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--manual 之一")
	assert.Len(t, f.requests(), n, "参数错误时不发请求")

	// 线程内定时（set）不接受 --manual，缺时间时的提示也不提它
	var in apigen.ScheduleInput
	err = applyScheduleFlags(&in, scheduleFlags{manual: true, prompt: "p"}, changedSet("manual", "prompt"), true, time.Now(), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "只适用于每次新建线程的定时")
	err = applyScheduleFlags(&in, scheduleFlags{prompt: "p"}, changedSet("prompt"), true, time.Now(), nil, &bytes.Buffer{})
	assert.EqualError(t, err, "须给出 --cron、--every 或 --at 之一")

	// update：cron → manual 时去掉 cron 与截止时刻，保留其余
	cur := apigen.Schedule{ID: "sch_c", Mode: apigen.ScheduleModeNewThread, Name: "c", Kind: apigen.ScheduleKindCron, Cron: new("0 9 * * *"),
		Timezone: "Asia/Shanghai", EndsAt: new(int64(1798732800000)), Prompt: "p", Enabled: true, Overlap: apigen.ScheduleOverlapSkip, KeepLast: 5}
	f.json("GET /api/v1/schedules/sch_c", 200, cur)
	f.json("PUT /api/v1/schedules/sch_c", 200, cur)
	_, err = scheduleUpdate(ctx, f.client(), "sch_c", scheduleFlags{manual: true}, changedSet("manual"), nil, &bytes.Buffer{})
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"mode":"new_thread","name":"c","kind":"manual","timezone":"Asia/Shanghai","prompt":"p","enabled":true,"overlap":"skip","keepLast":5}`,
		string(reqs[len(reqs)-1].Body))

	f.json("GET /api/v1/schedules/sch_t", 200, apigen.Schedule{ID: "sch_t", Mode: apigen.ScheduleModeThread, Kind: apigen.ScheduleKindEvery, Every: new("1h0m0s"), ThreadID: new("T-1")})
	_, err = scheduleUpdate(ctx, f.client(), "sch_t", scheduleFlags{manual: true}, changedSet("manual"), nil, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--manual")
}

func TestScheduleManualAndWebhookDisplay(t *testing.T) {
	hook := &apigen.ScheduleWebhook{HasSecret: true, CreatedAt: 1, RotatedAt: 1}
	m := apigen.Schedule{Kind: apigen.ScheduleKindManual, Timezone: "UTC"}
	assert.Equal(t, "manual", scheduleSpec(m))
	assert.Equal(t, "manual", nextText(m))
	m.Webhook = hook
	assert.Equal(t, "manual + webhook", scheduleSpec(m))
	assert.Equal(t, "manual or webhook", nextText(m))
	m.PausedReason = new(apigen.SchedulePausedReasonUser)
	assert.Equal(t, "paused: user", nextText(m))

	e := apigen.Schedule{Kind: apigen.ScheduleKindEvery, Every: new("1h0m0s"), Timezone: "UTC", Webhook: hook}
	assert.Equal(t, "every 1h0m0s (UTC) + webhook", scheduleSpec(e))
	var out bytes.Buffer
	require.NoError(t, printSchedule(&out, e))
	assert.Contains(t, out.String(), "要求签名")
	assert.Contains(t, out.String(), "尚未被调用")
}

func TestScheduleWebhookPut(t *testing.T) {
	f := newFakeAPI(t)
	f.json("PUT /api/v1/schedules/sch_1/webhook", 200, apigen.ScheduleWebhookResult{
		URL: new("https://oryn.example/api/v1/public/hooks/owh_abc"), Webhook: apigen.ScheduleWebhook{HasSecret: true, CreatedAt: 1, RotatedAt: 1}})
	f.json("DELETE /api/v1/schedules/sch_1/webhook", 204, nil)
	ctx := context.Background()

	// 首次开启：空请求体；url 单独一行到 stdout，提示到 stderr
	r, err := scheduleWebhookPut(ctx, f.client(), "sch_1", scheduleWebhookFlags{}, changedSet(), nil)
	require.NoError(t, err)
	var out, errb bytes.Buffer
	require.NoError(t, printWebhookResult(&out, &errb, r, false))
	assert.Equal(t, "https://oryn.example/api/v1/public/hooks/owh_abc\n", out.String())
	assert.Contains(t, errb.String(), "只显示这一次")
	reqs := f.requests()
	assert.Equal(t, "PUT /api/v1/schedules/sch_1/webhook", reqs[0].Method+" "+reqs[0].Path)
	assert.JSONEq(t, `{}`, string(reqs[0].Body))

	// --rotate + --secret-stdin（去掉末尾换行）
	_, err = scheduleWebhookPut(ctx, f.client(), "sch_1", scheduleWebhookFlags{rotate: true, secretStdin: true}, changedSet("rotate", "secret-stdin"), strings.NewReader("s3cr3t\n"))
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"rotateKey":true,"secret":"s3cr3t"}`, string(reqs[len(reqs)-1].Body))

	// --secret / --clear-secret
	_, err = scheduleWebhookPut(ctx, f.client(), "sch_1", scheduleWebhookFlags{secret: "k"}, changedSet("secret"), nil)
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"secret":"k"}`, string(reqs[len(reqs)-1].Body))
	_, err = scheduleWebhookPut(ctx, f.client(), "sch_1", scheduleWebhookFlags{clearSecret: true}, changedSet("clear-secret"), nil)
	require.NoError(t, err)
	reqs = f.requests()
	assert.JSONEq(t, `{"secret":""}`, string(reqs[len(reqs)-1].Body))

	// 没有新地址：stdout 打印状态
	out.Reset()
	errb.Reset()
	require.NoError(t, printWebhookResult(&out, &errb, apigen.ScheduleWebhookResult{Webhook: apigen.ScheduleWebhook{RotatedAt: 1, LastCalledAt: new(int64(2))}}, false))
	assert.Contains(t, out.String(), "不要求签名")
	assert.Contains(t, out.String(), "最近调用")
	assert.Empty(t, errb.String())

	n := len(f.requests())
	for _, bad := range []struct {
		o       scheduleWebhookFlags
		changed []string
		stdin   string
		want    string
	}{
		{scheduleWebhookFlags{secret: "k", clearSecret: true}, []string{"secret", "clear-secret"}, "", "只能给一个"},
		{scheduleWebhookFlags{secret: "k", secretStdin: true}, []string{"secret", "secret-stdin"}, "k", "只能给一个"},
		{scheduleWebhookFlags{secret: ""}, []string{"secret"}, "", "不能为空"},
		{scheduleWebhookFlags{secretStdin: true}, []string{"secret-stdin"}, "\n", "不能为空"},
		{scheduleWebhookFlags{secret: strings.Repeat("密", 257)}, []string{"secret"}, "", "256"},
	} {
		_, err := scheduleWebhookPut(ctx, f.client(), "sch_1", bad.o, changedSet(bad.changed...), strings.NewReader(bad.stdin))
		var ec *exitCodeError
		require.ErrorAs(t, err, &ec, bad.want)
		assert.Equal(t, 2, ec.code)
		assert.ErrorContains(t, err, bad.want)
	}
	assert.Len(t, f.requests(), n, "参数错误时不发请求")

	require.NoError(t, scheduleWebhookDelete(ctx, f.client(), "sch_1"))
	reqs = f.requests()
	assert.Equal(t, "DELETE /api/v1/schedules/sch_1/webhook", reqs[len(reqs)-1].Method+" "+reqs[len(reqs)-1].Path)
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
