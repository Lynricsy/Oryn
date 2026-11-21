package boxes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// app 在 box 上建一个 app 行；live 为真时挂一个 live release id（只写列，不建 release 行）。
func (e *env) app(boxID, name, root string, source, live bool) {
	e.t.Helper()
	id := ids.New("bxa")
	n, err := e.st.Q.CountBoxApps(e.ctx, boxID)
	require.NoError(e.t, err)
	require.NoError(e.t, e.st.Q.CreateBoxApp(e.ctx, db.CreateBoxAppParams{
		ID: id, BoxID: boxID, Name: name, Port: 30000 + n, Public: true,
		Repo: "github.com/acme/x", Root: root, Source: source, CreatedAt: 1, UpdatedAt: 1,
	}))
	if live {
		rel := ids.New("bxr")
		_, err := e.st.Q.SetBoxAppLive(e.ctx, db.SetBoxAppLiveParams{
			LiveReleaseID: &rel, Repo: "github.com/acme/x", Root: root, Source: source, UpdatedAt: 2, ID: id,
		})
		require.NoError(e.t, err)
	}
}

func TestThreadTarget(t *testing.T) {
	e := newEnv(t, at("2026-11-20T12:00:00Z"))
	b := e.box("b1", orbs.StateReady)
	e.app(b.ID, "hello", "", true, true)
	e.app(b.ID, "crm", "crm", true, true)
	e.app(b.ID, "bin", "bin", false, true)
	e.app(b.ID, "fresh", "", true, false)

	for _, tc := range []struct {
		name, box, app, cwd, code string
	}{
		{name: "machine", box: "b1", cwd: "~"},
		{name: "machine by id", box: b.ID, cwd: "~"},
		{name: "app at repo root", box: "b1", app: "hello", cwd: "~/apps/hello/current"},
		{name: "app in subfolder", box: "b1", app: "crm", cwd: "~/apps/crm/current/crm"},
		{name: "artifact-only app ignores root", box: "b1", app: "bin", cwd: "~/apps/bin/current"},
		{name: "first deploy in flight", box: "b1", app: "fresh", code: CodeAppNotDeployed},
		{name: "unknown app", box: "b1", app: "nope", code: CodeAppNotFound},
		{name: "unknown box", box: "zz", code: CodeBoxNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orbID, cwd, err := e.s.ThreadTarget(e.ctx, tc.box, tc.app)
			if tc.code != "" {
				assert.Equal(t, tc.code, codeOf(err), "ThreadTarget(%q, %q) err = %v", tc.box, tc.app, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, b.OrbID, orbID)
			assert.Equal(t, tc.cwd, cwd)
		})
	}

	_, err := e.s.Stop(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	_, _, err = e.s.ThreadTarget(e.ctx, "b1", "")
	assert.Equal(t, CodeBoxStopped, codeOf(err), "停着的 box 不能开线程")
}
