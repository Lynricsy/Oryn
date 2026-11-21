package api_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/api"
	"github.com/Lynricsy/Oryn/internal/boxes"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/threads/threadstest"
)

// boxOrbs 是 boxes.Service 的最小 orb 替身：只按库读 orb 行，其余操作都不该被调用。
type boxOrbs struct{ st *store.Store }

var errBoxOrbs = errors.New("not used")

func (o boxOrbs) Create(context.Context, orbs.CreateParams) (db.Orb, error) {
	return db.Orb{}, errBoxOrbs
}
func (o boxOrbs) Get(ctx context.Context, id string) (db.Orb, error) {
	r, err := o.st.Q.GetOrb(ctx, id)
	if err != nil {
		return r, orbs.ErrNotFound
	}
	return r, nil
}
func (boxOrbs) Delete(context.Context, string) error                      { return errBoxOrbs }
func (boxOrbs) Wake(context.Context, string) error                        { return errBoxOrbs }
func (boxOrbs) Pause(context.Context, string, string) error               { return errBoxOrbs }
func (boxOrbs) Revive(context.Context, string) error                      { return errBoxOrbs }
func (boxOrbs) Connected(string) bool                                     { return false }
func (boxOrbs) HasFeature(string, string) bool                            { return false }
func (boxOrbs) Subscribe(func(orbs.Event))                                {}
func (boxOrbs) SetBoxGate(func(string) bool)                              {}
func (boxOrbs) DialPortal(context.Context, string, int) (net.Conn, error) { return nil, errBoxOrbs }
func (boxOrbs) Do(context.Context, string, *http.Request) (*http.Response, error) {
	return nil, errBoxOrbs
}

type noBusy struct{}

func (noBusy) Busy(context.Context, string) (bool, error) { return false, nil }

// newBoxThreadsEnv 是带 boxes 服务的线程 API 环境；返回一台已停下的 box 的名字。
func newBoxThreadsEnv(t *testing.T) (*threadsEnv, string) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	o, err := threadstest.NewOrbs(st, fakeomp(t), filepath.Join(dir, "orbs"), "FAKEOMP_DELAY_MS=5")
	require.NoError(t, err)
	svc := threads.New(threads.Config{DataDir: filepath.Join(dir, "data")}, st, o)
	cfg := config.DefaultOrynd()
	cfg.DataDir = filepath.Join(dir, "data")
	s := &api.Server{Deps: api.Deps{Config: cfg, Store: st}}
	s.Threads = &api.ThreadsDeps{Service: svc}
	s.Boxes = &api.BoxesDeps{Service: boxes.New(boxes.Config{}, st, boxOrbs{st}, noBusy{})}
	srv := httptest.NewServer(api.NewRouter(s))
	t.Cleanup(func() {
		srv.Close()
		o.Close()
		svc.Close()
		_ = st.Close()
	})

	provID, orbID := ids.New("prov"), ids.Orb()
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: provID, Name: "docker", TokenHash: ids.Hash(provID), CreatedAt: 1}))
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: orbID, ProviderID: provID, Type: "medium", Cpus: 4, MemoryMb: 8192,
		State: orbs.StateStopped, StateChangedAt: 1, TokenHash: ids.Hash(orbID), CreatedAt: 1, Box: new(int64(1))}))
	require.NoError(t, st.Q.CreateBox(ctx, db.CreateBoxParams{ID: ids.New("box"), Name: "b1", OrbID: orbID,
		Desired: "stopped", DesiredBy: "owner", DesiredAt: 1, CreatedAt: 1}))
	return &threadsEnv{t: t, st: st, svc: svc, srv: srv, dir: dir}, "b1"
}

// executor.kind = box 的错误分支：缺 boxId → 400；没有 boxes 服务 → 501；ThreadTarget 的错误按 boxes 的码映射。
func TestCreateThreadBoxExecutorErrors(t *testing.T) {
	post := func(e *threadsEnv, exec map[string]any) (int, string) {
		code, body := e.do("POST", "/api/v1/threads", map[string]any{"prompt": "x", "executor": exec})
		return code, apiErr(t, body).Code
	}

	e, box := newBoxThreadsEnv(t)
	for _, tc := range []struct {
		name   string
		exec   map[string]any
		status int
		code   string
	}{
		{name: "missing boxId", exec: map[string]any{"kind": "box"}, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown box", exec: map[string]any{"kind": "box", "boxId": "nope"}, status: http.StatusNotFound, code: "box_not_found"},
		{name: "stopped box", exec: map[string]any{"kind": "box", "boxId": box, "app": "hello"}, status: http.StatusConflict, code: "box_stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, code := post(e, tc.exec)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.code, code)
		})
	}

	t.Run("no boxes service", func(t *testing.T) {
		status, code := post(newThreadsEnv(t), map[string]any{"kind": "box", "boxId": "b1"})
		assert.Equal(t, http.StatusNotImplemented, status)
		assert.Equal(t, "not_implemented", code)
	})
}
