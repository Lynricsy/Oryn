package desktop

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假 orbd：内存里的 /desktop/* 本机 API，持有一个「Profile 目录」（这里就是一段字节） ----

type fakeOrbd struct {
	mu        sync.Mutex
	reqs      []string
	bodies    map[int][]byte // 请求序号 → PUT 体
	markerSet bool
	markerID  string
	mode      string
	content   []byte
	// exportID 非空时覆盖 X-Oryn-Profile-Id；exportTruncate 让导出体中途出错；exportBody 覆盖导出内容；
	// exportGate 非 nil 时导出在其关闭之前阻塞；putFail 非空时全量 PUT 以 400 + 该码失败。
	exportID       string
	exportTruncate bool
	exportBody     []byte
	exportGate     chan struct{}
	putFail        string
	notReady       bool
}

func (o *fakeOrbd) requests() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.reqs...)
}

// lastBody 返回最近一次全量 PUT 的请求体。
func (o *fakeOrbd) lastPut() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(o.reqs) - 1; i >= 0; i-- {
		if b, ok := o.bodies[i]; ok {
			return b
		}
	}
	return nil
}

func jsonResp(status int, v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(b))}
}

func errResp(status int, code string) *http.Response {
	return jsonResp(status, map[string]string{"code": code, "message": code})
}

type errAfter struct {
	r   io.Reader
	err error
}

func (e *errAfter) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		return n, e.err
	}
	return n, err
}

func (o *fakeOrbd) serve(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
	}
	q := req.URL.Query()
	o.mu.Lock()
	o.reqs = append(o.reqs, req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
	idx := len(o.reqs) - 1
	defer o.mu.Unlock()
	switch req.Method + " " + req.URL.Path {
	case "DELETE /desktop/profile":
		if !(o.markerSet && o.markerID == "") {
			o.content = nil
		}
		o.markerSet, o.markerID, o.mode = true, "", ""
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	case "PUT /desktop/profile":
		if q.Get("keep") == "1" {
			if !o.markerSet || o.markerID != q.Get("id") {
				return errResp(409, "profile_mismatch"), nil
			}
			o.mode = q.Get("mode")
			return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
		}
		if o.bodies == nil {
			o.bodies = map[int][]byte{}
		}
		o.bodies[idx] = body
		if o.putFail != "" {
			return errResp(400, o.putFail), nil
		}
		o.markerSet, o.markerID, o.mode, o.content = true, q.Get("id"), q.Get("mode"), body
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	case "GET /desktop/profile/export":
		if o.markerID == "" {
			return errResp(409, "no_profile"), nil
		}
		if o.mode == "readonly" {
			return errResp(409, "profile_readonly"), nil
		}
		gate := o.exportGate
		if gate != nil {
			o.mu.Unlock()
			<-gate
			o.mu.Lock()
		}
		data := o.content
		if o.exportBody != nil {
			data = o.exportBody
		}
		var r io.Reader = bytes.NewReader(append([]byte(nil), data...))
		if o.exportTruncate {
			r = &errAfter{r: r, err: io.ErrUnexpectedEOF}
		}
		id := o.markerID
		if o.exportID != "" {
			id = o.exportID
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Oryn-Profile-Id": {id}, "Content-Type": {"application/gzip"}}, Body: io.NopCloser(r)}, nil
	case "POST /desktop/browser/start", "POST /desktop/browser/stop":
		if o.notReady {
			return errResp(409, "not_ready"), nil
		}
		st := apigen.OrbBrowserStatusStopped
		if strings.HasSuffix(req.URL.Path, "start") {
			st = apigen.OrbBrowserStatusRunning
		}
		return jsonResp(200, apigen.LinkCtlDesktopStateParams{DisplayReady: true, Browser: st, Dirty: true}), nil
	}
	return errResp(404, "not_found"), nil
}

// ---- 假 orbs.Service ----

type modeCall struct {
	orbID string
	p     apigen.LinkCtlProfileModeParams
}

type fakeOrbs struct {
	st *store.Store

	mu       sync.Mutex
	hooks    orbs.DesktopHooks
	subs     []func(orbs.Event)
	orbds    map[string]*fakeOrbd
	usable   map[string]error
	dialErr  map[string]error
	touches  map[string]int
	touched  chan string
	modes    []modeCall
	applied  map[string]apigen.LinkCtlDesktopStateParams
	order    []string // 跨 orb 的请求顺序：orbID + " " + 请求
	refreshN int
}

func newFakeOrbs(st *store.Store) *fakeOrbs {
	return &fakeOrbs{
		st: st, orbds: map[string]*fakeOrbd{}, usable: map[string]error{}, dialErr: map[string]error{},
		touches: map[string]int{}, touched: make(chan string, 1024), applied: map[string]apigen.LinkCtlDesktopStateParams{},
	}
}

func (f *fakeOrbs) orbd(id string) *fakeOrbd {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.orbds[id]
	if o == nil {
		o = &fakeOrbd{}
		f.orbds[id] = o
	}
	return o
}

func (f *fakeOrbs) Get(ctx context.Context, orbID string) (db.Orb, error) {
	o, err := f.st.Q.GetOrb(ctx, orbID)
	if errors.Is(err, sql.ErrNoRows) {
		return o, orbs.ErrNotFound
	}
	return o, err
}

func (f *fakeOrbs) EnsureUsable(_ context.Context, orbID, _ string, _ bool, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usable[orbID]
}

func (f *fakeOrbs) Do(_ context.Context, orbID string, req *http.Request) (*http.Response, error) {
	o := f.orbd(orbID)
	f.mu.Lock()
	f.order = append(f.order, orbID+" "+req.Method+" "+req.URL.Path+"?"+req.URL.RawQuery)
	f.mu.Unlock()
	return o.serve(req)
}

func (f *fakeOrbs) DialDesk(_ context.Context, orbID string) (net.Conn, error) {
	f.mu.Lock()
	err := f.dialErr[orbID]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	a, b := net.Pipe()
	go func() {
		defer b.Close()
		_, _ = io.Copy(b, b)
	}()
	return a, nil
}

func (f *fakeOrbs) TouchUserCoalesced(_ context.Context, orbID string) {
	f.mu.Lock()
	f.touches[orbID]++
	f.mu.Unlock()
	f.touched <- orbID
}

func (f *fakeOrbs) ApplyDesktopState(_ context.Context, orbID string, st apigen.LinkCtlDesktopStateParams) {
	f.mu.Lock()
	f.applied[orbID] = st
	f.mu.Unlock()
}

func (f *fakeOrbs) RefreshDesktop(context.Context, string) {
	f.mu.Lock()
	f.refreshN++
	f.mu.Unlock()
}

func (f *fakeOrbs) SendProfileMode(_ context.Context, orbID string, p apigen.LinkCtlProfileModeParams) error {
	f.mu.Lock()
	f.modes = append(f.modes, modeCall{orbID, p})
	f.mu.Unlock()
	return nil
}

func (f *fakeOrbs) Subscribe(fn func(orbs.Event)) {
	f.mu.Lock()
	f.subs = append(f.subs, fn)
	f.mu.Unlock()
}

func (f *fakeOrbs) SetDesktopHooks(h orbs.DesktopHooks) {
	f.mu.Lock()
	f.hooks = h
	f.mu.Unlock()
}

func (f *fakeOrbs) emit(ev orbs.Event) {
	f.mu.Lock()
	subs := append([]func(orbs.Event){}, f.subs...)
	f.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

func (f *fakeOrbs) orderOf() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// ---- 夹具 ----

type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	fo     *fakeOrbs
	svc    *Service
	dir    string
	provID string
	// released 在释放队列处理完一个 orb 后收到其 id。
	released chan string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "data"))
	require.NoError(t, err)
	box, err := secrets.New(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	fo := newFakeOrbs(st)
	svc, err := New(Config{DataDir: filepath.Join(dir, "data"), Box: box}, st, fo, nil)
	require.NoError(t, err)
	e := &env{t: t, ctx: ctx, st: st, fo: fo, svc: svc, dir: filepath.Join(dir, "data", "browser-profiles"), released: make(chan string, 64)}
	svc.onReleased = func(id string) { e.released <- id }
	e.provID = ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: e.provID, Name: "local", TokenHash: ids.Hash(ids.Token("opv")), CreatedAt: 1}))
	t.Cleanup(func() {
		svc.Close()
		_ = st.Close()
	})
	return e
}

// orb 建一台 ready 的 orb，并（title 非空时）挂一个线程。
func (e *env) orb(title string) string {
	e.t.Helper()
	id := ids.Orb()
	require.NoError(e.t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{
		ID: id, ProviderID: e.provID, Type: "small", State: orbs.StateReady, StateChangedAt: 1, TokenHash: ids.Hash(id), CreatedAt: 1,
	}))
	if title != "" {
		tid := ids.Thread()
		require.NoError(e.t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{Kind: "work", ID: tid, OrbID: &id, Title: title, TitleSource: "user", State: "idle", SessionDir: "/s/" + tid, CreatedAt: 1, UpdatedAt: 1}))
	}
	return id
}

func (e *env) profile(name string) string {
	e.t.Helper()
	p, err := e.svc.Create(e.ctx, name)
	require.NoError(e.t, err)
	return p.ID
}

func (e *env) bind(orbID, profileID string, mode apigen.BrowserProfileMode) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.UpsertOrbDesktopProfile(e.ctx, orbs.ProfileBinding(orbID, profileID, mode, 1)))
	require.NoError(e.t, e.st.Q.UpsertOrbDesktopState(e.ctx, db.UpsertOrbDesktopStateParams{OrbID: orbID, DisplayReady: 1, Browser: "stopped", Now: 1}))
}

// dirty 模拟浏览器改了 Profile：orbd 目录内容变为 content，镜像 dirty=1。
func (e *env) dirty(orbID string, content string) {
	e.t.Helper()
	o := e.fo.orbd(orbID)
	o.mu.Lock()
	o.content = []byte(content)
	o.mu.Unlock()
	require.NoError(e.t, e.st.Q.UpsertOrbDesktopState(e.ctx, db.UpsertOrbDesktopStateParams{OrbID: orbID, DisplayReady: 1, Browser: "running", Dirty: 1, Now: 2}))
}

func (e *env) get(id string) db.BrowserProfile {
	e.t.Helper()
	p, err := e.st.Q.GetBrowserProfile(e.ctx, id)
	require.NoError(e.t, err)
	return p
}

func (e *env) desk(orbID string) db.OrbDesktop {
	e.t.Helper()
	d, err := e.st.Q.GetOrbDesktop(e.ctx, orbID)
	require.NoError(e.t, err)
	return d
}

func (e *env) view(orbID string) *apigen.OrbDesktopProfile {
	e.t.Helper()
	d, err := orbs.DesktopView(e.ctx, e.st.Q, orbID)
	require.NoError(e.t, err)
	require.NotNil(e.t, d)
	require.NotNil(e.t, d.Profile)
	return d.Profile
}

func (e *env) events(profileID string) []string {
	e.t.Helper()
	evs, err := e.st.Q.ListBrowserProfileEvents(e.ctx, db.ListBrowserProfileEventsParams{ProfileID: profileID, Lim: 100})
	require.NoError(e.t, err)
	out := []string{}
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Event)
	}
	return out
}

func (e *env) setState(orbID, state string) {
	e.t.Helper()
	require.NoError(e.t, e.st.Q.SetOrbState(e.ctx, db.SetOrbStateParams{State: state, StateChangedAt: 2, ID: orbID}))
}

// files 返回 Profile 归档目录里的文件名。
func (e *env) files(profileID string) []string {
	ents, _ := os.ReadDir(filepath.Join(e.dir, profileID))
	out := []string{}
	for _, f := range ents {
		out = append(out, f.Name())
	}
	return out
}

func last(reqs []string) string {
	if len(reqs) == 0 {
		return ""
	}
	return reqs[len(reqs)-1]
}

// ---- 同步 ----

func TestSyncWithoutProfileDeletes(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	e.svc.BeforeReady(e.ctx, o)
	assert.Equal(t, []string{"DELETE /desktop/profile?"}, e.fo.orbd(o).requests())
	// 解除绑定同样是 DELETE
	p := e.profile("work")
	e.bind(o, p, "")
	require.NoError(t, e.st.Q.UpsertOrbDesktopProfile(e.ctx, orbs.ProfileBinding(o, "", "", 2)))
	e.svc.BeforeReady(e.ctx, o)
	assert.Equal(t, "DELETE /desktop/profile?", last(e.fo.orbd(o).requests()))
	assert.EqualValues(t, -1, e.desk(o).LocalRev)
}

// 修订 0 → 空体 PUT；保存（关闭式）后修订递增、文件加密且不含明文；新 orb 装载得到原字节；本地即当前修订 → keep=1；
// profile_mismatch → 全量；内容不变不产生新修订；新修订删除旧文件。
func TestSyncCheckinRoundTrip(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	q := url.Values{"id": {p}, "mode": {"exclusive"}, "name": {"work"}}.Encode()
	assert.Equal(t, []string{"PUT /desktop/profile?" + q}, e.fo.orbd(a).requests())
	assert.Empty(t, e.fo.orbd(a).lastPut(), "修订 0：空体")
	d := e.desk(a)
	assert.EqualValues(t, 0, d.LocalRev)
	assert.EqualValues(t, 1, d.Fence)
	assert.Equal(t, a, *e.get(p).HolderOrbID)
	assert.Equal(t, apigen.OrbDesktopProfileStatusAttached, e.view(a).Status)

	secret := "SECRET-COOKIE-" + strings.Repeat("x", 100000)
	e.dirty(a, secret)
	assert.True(t, e.svc.NeedsFreezeSave(e.ctx, a))
	e.svc.BeforeShutdown(e.ctx, a)
	assert.Equal(t, "GET /desktop/profile/export?mode=closed", last(e.fo.orbd(a).requests()))
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	assert.Positive(t, got.SizeBytes)
	assert.Nil(t, got.SaveError)
	assert.Equal(t, []string{"1.oss"}, e.files(p))
	raw, err := os.ReadFile(filepath.Join(e.dir, p, "1.oss"))
	require.NoError(t, err)
	assert.Equal(t, "OSS1", string(raw[:4]))
	assert.NotContains(t, string(raw), "SECRET-COOKIE")
	st, err := os.Stat(filepath.Join(e.dir, p, "1.oss"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	assert.EqualValues(t, 1, e.desk(a).LocalRev)
	assert.Equal(t, []string{"profile.saved"}, e.events(p))

	// 内容不变：不产生新修订
	e.svc.BeforeShutdown(e.ctx, a)
	assert.EqualValues(t, 1, e.get(p).Revision)
	// 内容变了：修订 2，旧文件删除
	e.dirty(a, secret+"-v2")
	e.svc.BeforeFreeze(e.ctx, a)
	assert.Equal(t, "GET /desktop/profile/export?mode=live", last(e.fo.orbd(a).requests()))
	assert.EqualValues(t, 2, e.get(p).Revision)
	assert.Equal(t, []string{"2.oss"}, e.files(p))

	// A 离开运行态 → 释放；B 装载得到 A 保存的明文
	e.setState(a, orbs.StateFrozen)
	e.fo.emit(orbs.Event{OrbID: a, Kind: orbs.EventState, From: orbs.StateFreezing, To: orbs.StateFrozen})
	require.Equal(t, a, <-e.released)
	assert.Nil(t, e.get(p).HolderOrbID)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReleased, e.view(a).Status)

	b := e.orb("B")
	e.bind(b, p, "")
	e.svc.BeforeReady(e.ctx, b)
	assert.Equal(t, secret+"-v2", string(e.fo.orbd(b).lastPut()))
	assert.EqualValues(t, 2, e.desk(b).LocalRev)
	assert.EqualValues(t, 2, e.desk(b).Fence, "新持有者递增围栏代数")

	// 本地就是当前修订：keep=1
	n := len(e.fo.orbd(b).requests())
	e.svc.BeforeReady(e.ctx, b)
	reqs := e.fo.orbd(b).requests()
	require.Len(t, reqs, n+1)
	assert.Contains(t, reqs[n], "keep=1")
	// 本地标记不符：keep 回 profile_mismatch → 全量
	ob := e.fo.orbd(b)
	ob.mu.Lock()
	ob.markerID = "bpf_OTHER"
	ob.mu.Unlock()
	e.svc.BeforeReady(e.ctx, b)
	reqs = e.fo.orbd(b).requests()
	require.Len(t, reqs, n+3)
	assert.Contains(t, reqs[n+1], "keep=1")
	assert.NotContains(t, reqs[n+2], "keep=1")
	assert.Equal(t, secret+"-v2", string(e.fo.orbd(b).lastPut()))
}

// 被别的 orb 持有 → DELETE 且视图 in_use；装载失败 → error 且租约已释放。
func TestSyncHeldElsewhereAndLoadFailure(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a, b := e.orb("A"), e.orb("B")
	e.bind(a, p, "")
	e.bind(b, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.svc.BeforeReady(e.ctx, b)
	assert.Equal(t, "DELETE /desktop/profile?", last(e.fo.orbd(b).requests()))
	v := e.view(b)
	assert.Equal(t, apigen.OrbDesktopProfileStatusInUse, v.Status)
	assert.Equal(t, a, v.HeldBy.OrbID)
	assert.Equal(t, "A", *v.HeldBy.ThreadTitle)

	c := e.orb("C")
	e.bind(c, p, "")
	_, err := e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, a)
	require.NoError(t, err)
	e.fo.orbd(c).putFail = "invalid_archive"
	e.svc.BeforeReady(e.ctx, c)
	reqs := e.fo.orbd(c).requests()
	assert.Equal(t, "DELETE /desktop/profile?", last(reqs))
	assert.Nil(t, e.get(p).HolderOrbID, "装载失败释放租约")
	v = e.view(c)
	assert.Equal(t, apigen.OrbDesktopProfileStatusError, v.Status)
	assert.Equal(t, "invalid_archive", *v.Error)
}

// ---- 保存的失败路径 ----

func TestCheckinFailures(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "v1")
	e.svc.BeforeShutdown(e.ctx, a)
	require.EqualValues(t, 1, e.get(p).Revision)

	old := maxArchiveBytes
	maxArchiveBytes = 16
	e.dirty(a, strings.Repeat("big", 10))
	e.svc.BeforeShutdown(e.ctx, a)
	maxArchiveBytes = old
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	assert.Equal(t, "too_large", *got.SaveError)
	assert.Equal(t, []string{"1.oss"}, e.files(p), "临时文件已删除、旧修订保留")

	e.fo.orbd(a).exportTruncate = true
	e.dirty(a, "v2")
	e.svc.BeforeShutdown(e.ctx, a)
	e.fo.orbd(a).exportTruncate = false
	got = e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	assert.Equal(t, "export_failed", *got.SaveError)
	assert.Equal(t, []string{"1.oss"}, e.files(p))

	e.fo.orbd(a).exportID = "bpf_SOMEONEELSE"
	e.svc.BeforeShutdown(e.ctx, a)
	e.fo.orbd(a).exportID = ""
	assert.EqualValues(t, 1, e.get(p).Revision, "X-Oryn-Profile-Id 不符：放弃")
	assert.Equal(t, []string{"profile.saved", "profile.save_failed", "profile.save_failed"}, e.events(p))

	// 旧修订仍可装载
	_, err := e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, a)
	require.NoError(t, err)
	b := e.orb("B")
	e.bind(b, p, "")
	e.svc.BeforeReady(e.ctx, b)
	assert.Equal(t, "v1", string(e.fo.orbd(b).lastPut()))

	// 手动保存的错误映射
	e.dirty(b, strings.Repeat("big", 10))
	maxArchiveBytes = 16
	_, err = e.svc.Save(e.ctx, b)
	maxArchiveBytes = old
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_too_large", de.Code)
	assert.Equal(t, http.StatusRequestEntityTooLarge, de.Status)
	_, err = e.svc.Save(e.ctx, a)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "no_profile", de.Code)
}

// ---- 只读副本（主人验收 1：只读 orb 冻结 / 停机后版本号不变） ----

func TestReadonlyNeverWritesBack(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "saved-by-A")
	e.svc.BeforeShutdown(e.ctx, a)
	require.EqualValues(t, 1, e.get(p).Revision)

	r := e.orb("R")
	e.bind(r, p, apigen.BrowserProfileModeReadonly)
	e.svc.BeforeReady(e.ctx, r)
	assert.Equal(t, "saved-by-A", string(e.fo.orbd(r).lastPut()))
	assert.Contains(t, last(e.fo.orbd(r).requests()), "mode=readonly")
	assert.Equal(t, a, *e.get(p).HolderOrbID, "只读不取租约、不阻塞持有者")
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, e.view(r).Status)
	list, err := e.svc.List(e.ctx)
	require.NoError(t, err)
	require.Len(t, list[0].Readers, 1)
	assert.Equal(t, r, list[0].Readers[0].OrbID)

	// 只读 orb 的浏览器改了东西：冻结与停机都不导出、不回写
	e.dirty(r, "changed-in-readonly")
	assert.False(t, e.svc.NeedsFreezeSave(e.ctx, r))
	n := len(e.fo.orbd(r).requests())
	e.svc.BeforeFreeze(e.ctx, r)
	e.svc.BeforeShutdown(e.ctx, r)
	assert.Len(t, e.fo.orbd(r).requests(), n, "没有导出请求")
	assert.EqualValues(t, 1, e.get(p).Revision)
	_, err = e.svc.Save(e.ctx, r)
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_readonly", de.Code)

	// 持有者之后的新修订不推送：再次同步保留本地副本（keep=1&mode=readonly）
	e.dirty(a, "saved-by-A-v2")
	e.svc.BeforeShutdown(e.ctx, a)
	require.EqualValues(t, 2, e.get(p).Revision)
	e.svc.BeforeReady(e.ctx, r)
	assert.Contains(t, last(e.fo.orbd(r).requests()), "keep=1")
	assert.Contains(t, last(e.fo.orbd(r).requests()), "mode=readonly")
	assert.EqualValues(t, 2, e.get(p).Revision)
}

// ---- 接管（主人验收 2：原 orb 先被保存；验收 3：之后原 orb 的保存被围栏挡下） ----

func TestTakeoverSavesHolderFirst(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("线程A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "cookie-just-logged-in-on-A")

	b := e.orb("线程B")
	e.bind(b, p, apigen.BrowserProfileModeTakeover)
	e.svc.BeforeReady(e.ctx, b)

	// A 先被活体保存，B 装载到的正是它
	order := e.fo.orderOf()
	iExport, iPut := -1, -1
	for i, r := range order {
		if r == a+" GET /desktop/profile/export?mode=live" {
			iExport = i
		}
		if strings.HasPrefix(r, b+" PUT /desktop/profile?") && iPut < 0 {
			iPut = i
		}
	}
	require.GreaterOrEqual(t, iExport, 0, "原持有者被活体保存")
	assert.Less(t, iExport, iPut)
	assert.Equal(t, "cookie-just-logged-in-on-A", string(e.fo.orbd(b).lastPut()))
	got := e.get(p)
	assert.EqualValues(t, 1, got.Revision)
	assert.Equal(t, b, *got.HolderOrbID)
	assert.EqualValues(t, 2, got.Fence)
	assert.EqualValues(t, 0, e.desk(b).Takeover, "接管标记用后清零")
	assert.Equal(t, apigen.OrbDesktopProfileStatusAttached, e.view(b).Status)

	// A 降级为只读，经 ctl 收到 profile.mode
	va := e.view(a)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, va.Status)
	assert.Equal(t, apigen.BrowserProfileReadonlyReasonTakenOver, *va.ReadonlyReason)
	require.NotNil(t, va.TakenOverBy)
	assert.Equal(t, "线程B", *va.TakenOverBy.ThreadTitle)
	require.Len(t, e.fo.modes, 1)
	assert.Equal(t, a, e.fo.modes[0].orbID)
	assert.Equal(t, apigen.BrowserProfileAccessReadonly, e.fo.modes[0].p.Mode)
	assert.Equal(t, "线程B", *e.fo.modes[0].p.By)
	assert.Equal(t, []string{"profile.saved", "profile.taken_over"}, e.events(p))

	// 被接管的 A 之后冻结 / 停机不改变版本号
	e.dirty(a, "late-change-on-A")
	n := len(e.fo.orbd(a).requests())
	e.svc.BeforeFreeze(e.ctx, a)
	e.svc.BeforeShutdown(e.ctx, a)
	assert.Len(t, e.fo.orbd(a).requests(), n)
	assert.EqualValues(t, 1, e.get(p).Revision)
}

// 原持有者不可用（冻结 / 不可达）：不保存，直接换主。
func TestTakeoverHolderUnavailable(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "unsaved")
	e.fo.usable[a] = orbs.ErrOrbNotReady

	b := e.orb("B")
	e.bind(b, p, apigen.BrowserProfileModeTakeover)
	e.svc.BeforeReady(e.ctx, b)
	for _, r := range e.fo.orbd(a).requests() {
		assert.NotContains(t, r, "export")
	}
	assert.Equal(t, b, *e.get(p).HolderOrbID)
	assert.EqualValues(t, 0, e.get(p).Revision)
	assert.Nil(t, e.get(p).SaveError)
}

func TestTakeoverFencesLateSave(t *testing.T) {
	e := newEnv(t)
	oldTO := takeoverSaveTimeout
	takeoverSaveTimeout = 50 * time.Millisecond
	t.Cleanup(func() { takeoverSaveTimeout = oldTO })
	p := e.profile("work")
	a := e.orb("A")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "late")

	// A 的冻结前保存：导出写完、提交之前停住
	reached, resume := make(chan struct{}), make(chan struct{})
	e.svc.beforeSave = func(orbID string) {
		if orbID == a {
			close(reached)
			<-resume
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.svc.BeforeFreeze(e.ctx, a)
	}()
	<-reached

	// 此时 B 接管：对 A 的保存等不到 A 的锁（超时）→ takeover_unsaved，照常换主
	b := e.orb("B")
	e.bind(b, p, "")
	require.NoError(t, e.svc.SetOrbProfile(e.ctx, b, p, apigen.BrowserProfileModeTakeover))
	got := e.get(p)
	assert.Equal(t, b, *got.HolderOrbID)
	assert.Equal(t, "takeover_unsaved", *got.SaveError)
	assert.Equal(t, apigen.OrbDesktopProfileStatusReadonly, e.view(a).Status)

	// A 迟到的保存被围栏挡下：不产生修订、新文件被删、记 profile.save_fenced
	close(resume)
	<-done
	got = e.get(p)
	assert.EqualValues(t, 0, got.Revision)
	assert.Empty(t, e.files(p))
	assert.Equal(t, []string{"profile.taken_over", "profile.save_fenced"}, e.events(p))
	evs, err := e.st.Q.ListBrowserProfileEvents(e.ctx, db.ListBrowserProfileEventsParams{ProfileID: p, Lim: 1})
	require.NoError(t, err)
	assert.Equal(t, a, *evs[0].OrbID)
	assert.JSONEq(t, `{"fence":1,"currentFence":2}`, evs[0].Detail)
}

// ---- 租约释放 ----

func TestReleaseOnLeavingRunning(t *testing.T) {
	e := newEnv(t)
	for _, state := range []string{orbs.StateFrozen, orbs.StateStopped, orbs.StateFailed, orbs.StateDeleted} {
		p := e.profile("p-" + state)
		o := e.orb("")
		e.bind(o, p, "")
		e.svc.BeforeReady(e.ctx, o)
		require.Equal(t, o, *e.get(p).HolderOrbID)
		e.setState(o, state)
		e.fo.emit(orbs.Event{OrbID: o, Kind: orbs.EventState, From: orbs.StateReady, To: state})
		require.Equal(t, o, <-e.released)
		assert.Nil(t, e.get(p).HolderOrbID, state)
	}
	// ready 不释放；迟到的 frozen 事件（orb 已在唤醒）不释放
	p := e.profile("live")
	o := e.orb("")
	e.bind(o, p, "")
	e.svc.BeforeReady(e.ctx, o)
	e.fo.emit(orbs.Event{OrbID: o, Kind: orbs.EventState, From: orbs.StateResuming, To: orbs.StateReady})
	e.setState(o, orbs.StateWaking)
	e.fo.emit(orbs.Event{OrbID: o, Kind: orbs.EventState, From: orbs.StateFreezing, To: orbs.StateFrozen})
	require.Equal(t, o, <-e.released)
	assert.Equal(t, o, *e.get(p).HolderOrbID)
}

// ---- 解析、增删改、切换、浏览器启停 ----

func TestResolveForNewOrb(t *testing.T) {
	e := newEnv(t)
	p := e.profile("work")
	prj := ids.New("prj")
	require.NoError(t, e.st.Q.CreateProject(e.ctx, db.CreateProjectParams{ID: prj, Name: "demo", DefaultBrowserProfileID: &p, CreatedAt: 1, UpdatedAt: 1}))
	none := ""
	missing := "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1E2"

	id, err := e.svc.ResolveForNewOrb(e.ctx, nil, "", "")
	require.NoError(t, err)
	assert.Empty(t, id, "没有项目")
	id, err = e.svc.ResolveForNewOrb(e.ctx, nil, prj, "")
	require.NoError(t, err)
	assert.Equal(t, p, id, "项目默认")
	id, err = e.svc.ResolveForNewOrb(e.ctx, &none, prj, "")
	require.NoError(t, err)
	assert.Empty(t, id, "空串 = 不用")
	_, err = e.svc.ResolveForNewOrb(e.ctx, &missing, "", "")
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_not_found", de.Code)

	a := e.orb("正在用的线程")
	e.bind(a, p, "")
	e.svc.BeforeReady(e.ctx, a)
	_, err = e.svc.ResolveForNewOrb(e.ctx, &p, "", apigen.BrowserProfileModeExclusive)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_in_use", de.Code)
	require.NotNil(t, de.Conflict)
	assert.Equal(t, "work", de.Conflict.ProfileName)
	assert.Equal(t, a, de.Conflict.Holder.OrbID)
	assert.Equal(t, "正在用的线程", *de.Conflict.Holder.ThreadTitle)
	assert.Positive(t, de.Conflict.Holder.Since)
	assert.Equal(t, de.Conflict, de.ErrorBody())
	_, err = e.svc.ResolveForNewOrb(e.ctx, nil, prj, "")
	require.ErrorAs(t, err, &de, "项目默认同样受独占约束")
	for _, m := range []apigen.BrowserProfileMode{apigen.BrowserProfileModeReadonly, apigen.BrowserProfileModeTakeover} {
		id, err = e.svc.ResolveForNewOrb(e.ctx, &p, "", m)
		require.NoError(t, err, m)
		assert.Equal(t, p, id)
	}
	_, err = e.svc.ResolveForNewOrb(e.ctx, &p, "", "bogus")
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "invalid_request", de.Code)
}

func TestProfileCRUD(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"", "   ", strings.Repeat("长", 41), "a\nb"} {
		_, err := e.svc.Create(e.ctx, bad)
		var de *Error
		require.ErrorAs(t, err, &de, bad)
		assert.Equal(t, "invalid_request", de.Code)
	}
	p, err := e.svc.Create(e.ctx, "  工作账号 ")
	require.NoError(t, err)
	assert.Equal(t, "工作账号", p.Name)
	assert.EqualValues(t, 0, p.Revision)
	_, err = e.svc.Create(e.ctx, "工作账号")
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_name_taken", de.Code)
	q, err := e.svc.Create(e.ctx, "other")
	require.NoError(t, err)
	_, err = e.svc.Rename(e.ctx, q.ID, "工作账号")
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_name_taken", de.Code)
	r, err := e.svc.Rename(e.ctx, q.ID, "另一个")
	require.NoError(t, err)
	assert.Equal(t, "另一个", r.Name)
	_, err = e.svc.Rename(e.ctx, "bpf_01JCZ8M3W4X5Y6Z7A8B9C0D1E2", "x")
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_not_found", de.Code)

	old := maxProfiles
	maxProfiles = 2
	_, err = e.svc.Create(e.ctx, "third")
	maxProfiles = old
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_limit", de.Code)

	// 删除：被持有时拒绝；释放后成功，清项目默认值与归档目录
	prj := ids.New("prj")
	require.NoError(t, e.st.Q.CreateProject(e.ctx, db.CreateProjectParams{ID: prj, Name: "demo", DefaultBrowserProfileID: &p.ID, CreatedAt: 1, UpdatedAt: 1}))
	a := e.orb("A")
	e.bind(a, p.ID, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "data")
	e.svc.BeforeShutdown(e.ctx, a)
	require.NotEmpty(t, e.files(p.ID))
	err = e.svc.Delete(e.ctx, p.ID)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_in_use", de.Code)
	require.NotNil(t, de.Conflict)
	_, err = e.st.Q.ReleaseBrowserProfileByOrb(e.ctx, a)
	require.NoError(t, err)
	require.NoError(t, e.svc.Delete(e.ctx, p.ID))
	_, err = os.Stat(filepath.Join(e.dir, p.ID))
	assert.True(t, os.IsNotExist(err))
	got, err := e.st.Q.GetProject(e.ctx, prj)
	require.NoError(t, err)
	assert.Nil(t, got.DefaultBrowserProfileID)
	assert.Nil(t, e.desk(a).ProfileID, "绑定经外键置空")
	err = e.svc.Delete(e.ctx, p.ID)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_not_found", de.Code)
}

// SetOrbProfile：切换前对当前持有的 Profile 关闭式保存并释放；被别人持有且 exclusive → profile_in_use、不做改动。
func TestSetOrbProfileSwitch(t *testing.T) {
	e := newEnv(t)
	p1, p2 := e.profile("one"), e.profile("two")
	a, other := e.orb("A"), e.orb("Other")
	e.bind(a, p1, "")
	e.svc.BeforeReady(e.ctx, a)
	e.dirty(a, "one-data")

	require.NoError(t, e.svc.SetOrbProfile(e.ctx, a, p2, ""))
	reqs := e.fo.orbd(a).requests()
	assert.Contains(t, reqs, "GET /desktop/profile/export?mode=closed")
	assert.EqualValues(t, 1, e.get(p1).Revision)
	assert.Nil(t, e.get(p1).HolderOrbID)
	assert.Equal(t, a, *e.get(p2).HolderOrbID)
	assert.Equal(t, p2, *e.desk(a).ProfileID)

	// p1 被 other 持有：以 exclusive 选它 → 409，绑定不变
	e.bind(other, p1, "")
	e.svc.BeforeReady(e.ctx, other)
	err := e.svc.SetOrbProfile(e.ctx, a, p1, "")
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "profile_in_use", de.Code)
	assert.Equal(t, p2, *e.desk(a).ProfileID)
	assert.Equal(t, a, *e.get(p2).HolderOrbID)

	// 解除
	require.NoError(t, e.svc.SetOrbProfile(e.ctx, a, "", ""))
	assert.Nil(t, e.desk(a).ProfileID)
	assert.Nil(t, e.get(p2).HolderOrbID)
	assert.Equal(t, "DELETE /desktop/profile?", last(e.fo.orbd(a).requests()))

	// orb 不可用 → 原样返回 orb 通道错误
	e.fo.usable[a] = orbs.ErrOrbdOutdated
	assert.ErrorIs(t, e.svc.SetOrbProfile(e.ctx, a, p2, ""), orbs.ErrOrbdOutdated)
	_, err = e.svc.Save(e.ctx, a)
	assert.ErrorIs(t, err, orbs.ErrOrbdOutdated)
}

func TestSetBrowser(t *testing.T) {
	e := newEnv(t)
	o := e.orb("")
	require.NoError(t, e.svc.SetBrowser(e.ctx, o, true))
	assert.Equal(t, apigen.OrbBrowserStatusRunning, e.fo.applied[o].Browser)
	require.NoError(t, e.svc.SetBrowser(e.ctx, o, false))
	assert.Equal(t, apigen.OrbBrowserStatusStopped, e.fo.applied[o].Browser)
	e.fo.orbd(o).notReady = true
	err := e.svc.SetBrowser(e.ctx, o, true)
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "orb_unavailable", de.Code)
}

// 启动时清掉残留的临时文件。
func TestNewRemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "data"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	pd := filepath.Join(dir, "data", "browser-profiles", "bpf_X")
	require.NoError(t, os.MkdirAll(pd, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(pd, ".tmp-123"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(pd, "1.oss"), []byte("x"), 0o600))
	box, err := secrets.New(bytes.Repeat([]byte{1}, 32))
	require.NoError(t, err)
	svc, err := New(Config{DataDir: filepath.Join(dir, "data"), Box: box}, st, newFakeOrbs(st), nil)
	require.NoError(t, err)
	defer svc.Close()
	ents, err := os.ReadDir(pd)
	require.NoError(t, err)
	require.Len(t, ents, 1)
	assert.Equal(t, "1.oss", ents[0].Name())
}
