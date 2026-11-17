package sshgw

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

type fakeOrbs struct {
	mu sync.Mutex
	m  map[string]db.Orb
}

func (f *fakeOrbs) Get(_ context.Context, id string) (db.Orb, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.m[id]
	if !ok {
		return db.Orb{}, sql.ErrNoRows
	}
	return o, nil
}

func (f *fakeOrbs) put(o db.Orb) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[o.ID] = o
}

type testEnv struct {
	svc     *Service
	st      *store.Store
	orbs    *fakeOrbs
	mu      sync.Mutex
	applied []string
	changed int
}

func (e *testEnv) appliedLayers() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.applied...)
}

func newEnv(t *testing.T, mod ...func(*Config)) *testEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	box, err := secrets.New(make([]byte, 32))
	require.NoError(t, err)
	e := &testEnv{st: st, orbs: &fakeOrbs{m: map[string]db.Orb{}}}
	cfg := Config{
		Box: box, DataDir: dir,
		Apply:   func(p string) { e.mu.Lock(); e.applied = append(e.applied, p); e.mu.Unlock() },
		Changed: func() { e.mu.Lock(); e.changed++; e.mu.Unlock() },
	}
	for _, m := range mod {
		m(&cfg)
	}
	e.svc, err = New(cfg, st, e.orbs)
	require.NoError(t, err)
	return e
}

func (e *testEnv) project(t *testing.T, id string) string {
	t.Helper()
	require.NoError(t, e.st.Q.CreateProject(context.Background(), db.CreateProjectParams{ID: id, Name: id, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

func (e *testEnv) key(t *testing.T, name string) apigen.SSHKey {
	t.Helper()
	k, err := e.svc.CreateKey(context.Background(), KeyInput{Name: name, Mode: "generate"})
	require.NoError(t, err)
	return k
}

// hostIn 返回一份合法的新建输入（钥匙登录）。
func hostIn(name, keyID string) apigen.SSHHostCreate {
	return apigen.SSHHostCreate{Name: name, Address: "10.0.0.1", Username: "root", AuthType: apigen.SSHAuthKey, KeyID: &keyID}
}

func (e *testEnv) mustHost(t *testing.T, in apigen.SSHHostCreate) apigen.SSHHost {
	t.Helper()
	h, err := e.svc.CreateHost(context.Background(), in)
	require.NoError(t, err)
	return h
}

func requireCode(t *testing.T, err error, code, msgPart string) {
	t.Helper()
	var se *Error
	require.True(t, errors.As(err, &se), "want *Error, got %v", err)
	require.Equal(t, code, se.Code, se.Message)
	if msgPart != "" {
		require.Contains(t, se.Message, msgPart)
	}
}
