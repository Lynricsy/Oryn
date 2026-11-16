package orbs

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// codedErr 是带错误码的领域错误（模拟 github.Error）。
type codedErr struct{ code, msg string }

func (e *codedErr) Error() string            { return e.code + ": " + e.msg }
func (e *codedErr) CtlError() *link.CtlError { return &link.CtlError{Code: e.code, Message: e.msg} }

// recGitHub 记录 github.call / github.prepare.report 的调用。
type recGitHub struct {
	stubGitHub
	mu      sync.Mutex
	calls   []string // orbID|threadID|tool
	reports []apigen.LinkCtlGithubPrepareReportParams
	callErr error
}

func (g *recGitHub) Call(_ context.Context, orbID, threadID string, p apigen.LinkCtlGithubCallParams) (apigen.LinkCtlGithubCallResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, orbID+"|"+threadID+"|"+p.Tool)
	if g.callErr != nil {
		return apigen.LinkCtlGithubCallResult{}, g.callErr
	}
	return apigen.LinkCtlGithubCallResult{Text: "done " + p.Tool, Details: map[string]any{"tool": p.Tool}}, nil
}

func (g *recGitHub) PrepareReport(_ context.Context, orbID string, p apigen.LinkCtlGithubPrepareReportParams) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reports = append(g.reports, p)
	return nil
}

// M15 5.1：github.call / github.prepare.report 只接受属于发起 link 的 orb、未归档的工作线程（同 artifact.call）；
// 校验通过才触达 GitHub 集成，结果与带码错误原样回给 orbd。
func TestGithubCtlCaller(t *testing.T) {
	drv := newFakeDriver()
	h := newHarness(t, drv, nil, nil)
	ctx := context.Background()
	gh := &recGitHub{}
	h.svc.SetGitHub(gh)
	orb, c := h.readyOrb(drv, "")
	other, _ := h.readyOrb(drv, "")

	mk := func(id, kind, orbID string, archived bool) {
		require.NoError(t, h.st.Q.CreateThread(ctx, db.CreateThreadParams{
			Kind: kind, ID: id, OrbID: &orbID, TitleSource: "auto", State: "idle", SessionDir: "/s/" + id, CreatedAt: 1, UpdatedAt: 1,
		}))
		if archived {
			_, err := h.st.DB.Exec(`UPDATE threads SET archived_at = 2 WHERE id = ?`, id)
			require.NoError(t, err)
		}
	}
	mk("T-ok", "work", orb.ID, false)
	mk("T-foreign", "work", other.ID, false)
	mk("T-space", "space", orb.ID, false)
	mk("T-archived", "work", orb.ID, true)

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sha := "0123456789abcdef0123456789abcdef01234567"
	for _, th := range []string{"", "T-missing", "T-foreign", "T-space", "T-archived"} {
		err := c.Call(cctx, string(apigen.MethodGithubCall), apigen.LinkCtlGithubCallParams{ThreadID: th, Tool: "github_task_state", Params: map[string]any{}}, nil)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce, th)
		assert.Equal(t, "forbidden", ce.Code, th)
		err = c.Call(cctx, string(apigen.MethodGithubPrepareReport), apigen.LinkCtlGithubPrepareReportParams{ThreadID: th, Sha: sha, Level: "push"}, nil)
		require.ErrorAs(t, err, &ce, th)
		assert.Equal(t, "forbidden", ce.Code, th)
	}
	assert.Empty(t, gh.calls, "未通过校验不得触达集成")
	assert.Empty(t, gh.reports)

	var res apigen.LinkCtlGithubCallResult
	require.NoError(t, c.Call(cctx, string(apigen.MethodGithubCall), apigen.LinkCtlGithubCallParams{ThreadID: "T-ok", Tool: "github_task_state", Params: map[string]any{"state": "fixing"}}, &res))
	assert.Equal(t, "done github_task_state", res.Text)
	assert.Equal(t, "github_task_state", res.Details["tool"])
	assert.Equal(t, []string{orb.ID + "|T-ok|github_task_state"}, gh.calls)

	gh.mu.Lock()
	gh.callErr = &codedErr{code: "not_implemented", msg: "github_autoclose 尚未实现"}
	gh.mu.Unlock()
	err := c.Call(cctx, string(apigen.MethodGithubCall), apigen.LinkCtlGithubCallParams{ThreadID: "T-ok", Tool: "github_autoclose", Params: map[string]any{}}, nil)
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, "not_implemented", ce.Code, "带码错误原样透传")
	assert.Equal(t, "github_autoclose 尚未实现", ce.Message)

	var ok apigen.LinkCtlOkResult
	require.NoError(t, c.Call(cctx, string(apigen.MethodGithubPrepareReport), apigen.LinkCtlGithubPrepareReportParams{ThreadID: "T-ok", Sha: sha, Level: "pr", Skipped: true, Reason: new("docs only")}, &ok))
	assert.True(t, ok.Ok)
	require.Len(t, gh.reports, 1)
	assert.Equal(t, sha, gh.reports[0].Sha)
	assert.EqualValues(t, "pr", gh.reports[0].Level)
	assert.True(t, gh.reports[0].Skipped)
}

// GitHub 集成未启用时 github.* 回 github_disabled。
func TestGithubCtlDisabled(t *testing.T) {
	s := &Service{}
	_, err := s.onGithubCall(context.Background(), "orb", json.RawMessage(`{"threadId":"T-1","tool":"github_task_state","params":{}}`))
	var ce *link.CtlError
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, CodeGitHubDisabled, ce.Code)
	_, err = s.onGithubPrepareReport(context.Background(), "orb", json.RawMessage(`{"threadId":"T-1","sha":"x","level":"push","skipped":false}`))
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, CodeGitHubDisabled, ce.Code)
}

// M15 5.1：runner 的 link 拒绝 github.call / github.prepare.report（runner_unsupported），处理器不被触达。
func TestRunnerCtlRejectsGithub(t *testing.T) {
	assert.False(t, runnerRequestAllowed(string(apigen.MethodGithubCall)))
	assert.False(t, runnerRequestAllowed(string(apigen.MethodGithubPrepareReport)))

	h := newHarnessRun(t, newFakeDriver(), fakeSetup{}, nil, false)
	captureTimers(h.svc)
	ctx := context.Background()
	r, tok, err := h.svc.CreateRunner(ctx, "laptop")
	require.NoError(t, err)
	var reached atomic.Int32
	for _, m := range []apigen.LinkCtlMethod{apigen.MethodGithubCall, apigen.MethodGithubPrepareReport} {
		h.svc.HandleRequest(string(m), func(context.Context, string, json.RawMessage) (any, error) {
			reached.Add(1)
			return map[string]any{}, nil
		})
	}
	fr := &fakeRunner{url: h.srv.URL, token: tok, features: []string{"fs"}}
	fr.connect(t)
	h.waitState(r.OrbID, StateReady, 5*time.Second)
	ctl := fr.control()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, m := range []apigen.LinkCtlMethod{apigen.MethodGithubCall, apigen.MethodGithubPrepareReport} {
		err := ctl.Call(cctx, string(m), map[string]any{"threadId": "thr_x", "tool": "github_task_state", "sha": "x", "level": "push"}, nil)
		var ce *link.CtlError
		require.ErrorAs(t, err, &ce, m)
		assert.Equal(t, CodeRunnerUnsupported, ce.Code, m)
	}
	assert.Zero(t, reached.Load(), "runner 的 github.* 不得触达处理器")
	assert.True(t, h.svc.Connected(r.OrbID), "拒绝方法不断开 link")
}
