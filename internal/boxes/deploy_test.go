package boxes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// ---- 假 orbd（box 一侧） ----

type boxdApp struct {
	port       int
	live       string
	releases   []string
	activating string
}

type fakeBoxd struct {
	mu       sync.Mutex
	incoming map[string][]byte
	apps     map[string]*boxdApp
	// fail 非 nil 时 activate 回它（不改状态）。
	fail *apigen.OrbBoxResult
	// holdIncoming 非 nil 时上传先关闭 arrived 再阻塞到请求取消。
	holdIncoming chan struct{}
	// pauseIncoming 非 nil 时下一次上传先关闭它，再等 resumeIncoming 关闭后照常收下。两者都只作用一次。
	pauseIncoming, resumeIncoming chan struct{}
	// pauseDelete 非 nil 时下一次下线先关闭它，再等 resumeDelete 关闭（只作用一次）。
	pauseDelete, resumeDelete chan struct{}
	activations               []apigen.OrbBoxActivateRequest
	deleted                   []string
	files                     map[string][]byte
}

func newFakeBoxd() *fakeBoxd {
	return &fakeBoxd{incoming: map[string][]byte{}, apps: map[string]*boxdApp{}, files: map[string][]byte{}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (d *fakeBoxd) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /box/incoming/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		hold, pause, resume := d.holdIncoming, d.pauseIncoming, d.resumeIncoming
		d.holdIncoming, d.pauseIncoming = nil, nil
		d.mu.Unlock()
		if hold != nil {
			close(hold)
			<-r.Context().Done()
			writeJSON(w, http.StatusBadRequest, map[string]string{"code": "canceled", "message": "client gone"})
			return
		}
		if pause != nil {
			close(pause)
			select {
			case <-resume:
			case <-r.Context().Done():
				writeJSON(w, http.StatusBadRequest, map[string]string{"code": "canceled", "message": "client gone"})
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != r.URL.Query().Get("sha256") {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "checksum_mismatch", "message": "bad sum"})
			return
		}
		d.mu.Lock()
		d.incoming[r.PathValue("id")] = body
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /box/apps", func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		st := apigen.OrbBoxAppsState{Apps: []apigen.OrbBoxAppState{}}
		for name, a := range d.apps {
			v := apigen.OrbBoxAppState{Name: name, Port: a.port, Live: a.live, Releases: slices.Clone(a.releases)}
			if a.activating != "" {
				v.Activating = new(a.activating)
			}
			st.Apps = append(st.Apps, v)
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("POST /box/apps/{app}/activate", func(w http.ResponseWriter, r *http.Request) {
		var req apigen.OrbBoxActivateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.activations = append(d.activations, req)
		if d.fail != nil {
			writeJSON(w, http.StatusOK, d.fail)
			return
		}
		if _, ok := d.incoming[req.ReleaseID]; !ok {
			writeJSON(w, http.StatusOK, apigen.OrbBoxResult{Code: new("archive_missing")})
			return
		}
		a := d.apps[r.PathValue("app")]
		if a == nil {
			a = &boxdApp{}
			d.apps[r.PathValue("app")] = a
		}
		a.port, a.live = req.Port, req.ReleaseID
		a.releases = append([]string{req.ReleaseID}, a.releases...)
		var pruned []string
		if len(a.releases) > req.Keep {
			pruned = a.releases[req.Keep:]
			a.releases = a.releases[:req.Keep]
		}
		writeJSON(w, http.StatusOK, apigen.OrbBoxResult{Ok: true, LiveReleaseID: &req.ReleaseID, Pruned: pruned})
	})
	mux.HandleFunc("POST /box/apps/{app}/rollback", func(w http.ResponseWriter, r *http.Request) {
		var req apigen.OrbBoxRollbackRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		defer d.mu.Unlock()
		a := d.apps[r.PathValue("app")]
		if a == nil || !slices.Contains(a.releases, req.ReleaseID) {
			writeJSON(w, http.StatusOK, apigen.OrbBoxResult{Code: new("release_not_found")})
			return
		}
		a.live = req.ReleaseID
		writeJSON(w, http.StatusOK, apigen.OrbBoxResult{Ok: true, LiveReleaseID: &req.ReleaseID})
	})
	mux.HandleFunc("POST /box/apps/{app}/restart", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, apigen.OrbBoxResult{Ok: true})
	})
	mux.HandleFunc("DELETE /box/apps/{app}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		pause, resume := d.pauseDelete, d.resumeDelete
		d.pauseDelete = nil
		d.mu.Unlock()
		if pause != nil {
			close(pause)
			<-resume
		}
		d.mu.Lock()
		delete(d.apps, r.PathValue("app"))
		d.deleted = append(d.deleted, r.PathValue("app")+"?"+r.URL.RawQuery)
		d.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /box/exec", func(w http.ResponseWriter, r *http.Request) {
		var req apigen.OrbBoxExecRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"s":"o","d":"cwd=`+req.Cwd+`\n"}`+"\n"+`{"s":"e","d":"warn\n"}`+"\n"+`{"exit":3}`+"\n")
	})
	mux.HandleFunc("GET /services/{name}/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "log of "+r.PathValue("name")+" tail="+r.URL.Query().Get("tail"))
	})
	fileAPI(mux, &d.mu, d.files)
	return mux
}

// fileAPI 是 orbd 的 GET / PUT /fs/file 替身。
func fileAPI(mux *http.ServeMux, mu *sync.Mutex, files map[string][]byte) {
	mux.HandleFunc("GET /fs/file", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b, ok := files[r.URL.Query().Get("path")]
		mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"code": "not_found", "message": "no such file"})
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("PUT /fs/file", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		files[r.URL.Query().Get("path")] = b
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
}

// ---- 夹具 ----

type deployEnv struct {
	*env
	box     apigen.Box
	boxd    *fakeBoxd
	src     string // 源 orb（线程的 orb）
	srcMu   sync.Mutex
	srcFile map[string][]byte
	thread  string
	dropped []string
}

// newDeployEnv 建一台就绪的 box（带 box 特性与假 orbd）和一个有线程的源 orb。
func newDeployEnv(t *testing.T) *deployEnv {
	e := &deployEnv{env: newEnv(t, at("2026-11-20T12:00:00Z")), boxd: newFakeBoxd(), srcFile: map[string][]byte{}}
	e.s.cfg.DropApp = func(app string) { e.dropped = append(e.dropped, app) }
	e.box = e.readyBox("b1", e.boxd)
	e.src = mkOrb(t, e.env, false)
	mux := http.NewServeMux()
	fileAPI(mux, &e.srcMu, e.srcFile)
	e.orbs.handlers[e.src] = mux
	e.thread = mkThread(t, e.env, e.src)
	return e
}

// readyBox 建一台 ready、有 link、有 box 特性的 box，orbd 由 d 扮演。
func (e *env) readyBox(name string, d *fakeBoxd) apigen.Box {
	b := e.box(name, orbs.StateReady)
	e.orbs.mu.Lock()
	e.orbs.connected[b.OrbID] = true
	e.orbs.features[b.OrbID] = map[string]bool{orbs.FeatureBox: true}
	e.orbs.handlers[b.OrbID] = d.handler()
	e.orbs.mu.Unlock()
	return b
}

func mkOrb(t *testing.T, e *env, automation bool) string {
	id := ids.Orb()
	p := db.CreateOrbParams{ID: id, ProviderID: e.orbs.provID, Type: "medium", State: orbs.StateReady, StateChangedAt: 1,
		TokenHash: ids.Hash(id), CreatedAt: 1}
	if automation {
		p.Automation = new(true)
	}
	require.NoError(t, e.st.Q.CreateOrb(e.ctx, p))
	return id
}

func mkThread(t *testing.T, e *env, orbID string) string {
	id := ids.Thread()
	require.NoError(t, e.st.Q.CreateThread(e.ctx, db.CreateThreadParams{
		Kind: "work", ID: id, OrbID: &orbID, TitleSource: "auto", State: "idle", SessionDir: "/s", CreatedAt: 1, UpdatedAt: 1,
	}))
	return id
}

func (e *deployEnv) by() Actor { return Actor{Kind: ActorAgent, ThreadID: e.thread} }

func pp(app, repo, root string) PrepareParams {
	return PrepareParams{Box: "b1", App: app, Repo: repo, Root: root, Branch: "main", Commit: "c0ffee", Source: true}
}

func (e *deployEnv) prepare(p PrepareParams) (PrepareResult, error) {
	return e.s.PrepareDeploy(e.ctx, e.src, p, e.by())
}

// commit 把 archive 放进源 orb 的打包落点并提交。
func (e *deployEnv) commit(prep PrepareResult, archive string) (CommitResult, error) {
	e.srcMu.Lock()
	e.srcFile[prep.ArchivePath] = []byte(archive)
	e.srcMu.Unlock()
	sum := sha256.Sum256([]byte(archive))
	return e.s.CommitDeploy(e.ctx, e.src, CommitParams{
		DeployID: prep.DeployID, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(archive)),
		Services: []apigen.OrbBoxService{{Name: "web", Command: "./run", Cwd: ""}},
	})
}

// deploy 走完 prepare + commit 并要求成功。
func (e *deployEnv) deploy(p PrepareParams, archive string) CommitResult {
	e.t.Helper()
	prep, err := e.prepare(p)
	require.NoError(e.t, err)
	res, err := e.commit(prep, archive)
	require.NoError(e.t, err)
	require.True(e.t, res.OK, "%+v", res)
	return res
}

func (e *deployEnv) appRow(name string) (db.BoxApp, bool) {
	a, err := e.st.Q.GetBoxAppByName(e.ctx, name)
	if err != nil {
		return db.BoxApp{}, false
	}
	return a, true
}

func (e *deployEnv) deployRow(id string) db.BoxDeploy {
	e.t.Helper()
	r, err := e.st.Q.GetBoxDeploy(e.ctx, id)
	require.NoError(e.t, err)
	return r
}

// ---- prepare ----

func TestPrepareRejects(t *testing.T) {
	e := newDeployEnv(t)
	_, err := e.prepare(PrepareParams{Box: "nope", App: "a", Repo: "r", Branch: "main", Commit: "c"})
	assert.Equal(t, CodeBoxNotFound, codeOf(err))
	_, err = e.prepare(pp("Bad", "r", ""))
	assert.Equal(t, CodeInvalid, codeOf(err))
	_, err = e.prepare(pp("t-0123456789ab-p80", "r", ""))
	assert.Equal(t, CodeInvalid, codeOf(err), "portal 标签形的名字")
	_, err = e.prepare(pp("a", "r", "../x"))
	assert.Equal(t, CodeInvalid, codeOf(err))

	// app 在别的 box 上
	e.deploy(pp("hello", "github.com/acme/x", ""), "v1")
	other := e.readyBox("b2", newFakeBoxd())
	p := pp("hello", "github.com/acme/x", "")
	p.Box = other.Name
	_, err = e.prepare(p)
	assert.Equal(t, CodeAppExistsElsewhere, codeOf(err))

	// 绑定的仓库或目录不同
	_, err = e.prepare(pp("hello", "github.com/acme/y", ""))
	assert.Equal(t, CodeAppRepoMismatch, codeOf(err))
	_, err = e.prepare(pp("hello", "github.com/acme/x", "sub"))
	assert.Equal(t, CodeAppRepoMismatch, codeOf(err))

	// 未结束的部署
	_, err = e.prepare(pp("hello", "github.com/acme/x", ""))
	require.NoError(t, err)
	_, err = e.prepare(pp("hello", "github.com/acme/x", ""))
	assert.Equal(t, CodeDeployInProgress, codeOf(err))

	// app 数上限
	for i := range MaxAppsPerBox - 1 {
		e.app(e.box.ID, "fill"+string(rune('a'+i)), "", true, false)
	}
	_, err = e.prepare(pp("onemore", "r", ""))
	assert.Equal(t, CodeAppLimit, codeOf(err))

	// box 停着
	_, err = e.s.Stop(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	_, err = e.prepare(pp("x", "r", ""))
	assert.Equal(t, CodeBoxStopped, codeOf(err))
}

// 有 link 却没有 box 特性 → 立即 box_unsupported；orb failed → 立即 box_not_ready（都不等假时钟）。
func TestPrepareFailsFastOnBoxState(t *testing.T) {
	e := newDeployEnv(t)
	start := e.clk.Now()
	e.orbs.mu.Lock()
	e.orbs.features[e.box.OrbID] = nil
	e.orbs.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := e.prepare(pp("a", "r", "")); done <- err }()
	select {
	case err := <-done:
		assert.Equal(t, CodeBoxUnsupported, codeOf(err))
	case <-time.After(5 * time.Second):
		t.Fatal("没有立即回 box_unsupported")
	}
	assert.Equal(t, start, e.clk.Now())

	e.orbs.mu.Lock()
	e.orbs.connected[e.box.OrbID] = false
	e.orbs.mu.Unlock()
	e.setState(e.box.OrbID, orbs.StateFailed)
	_, err := e.prepare(pp("a", "r", ""))
	assert.Equal(t, CodeBoxNotReady, codeOf(err))
}

func TestConcurrentPrepareAndCommit(t *testing.T) {
	e := newDeployEnv(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	preps := make([]PrepareResult, 2)
	for i := range 2 {
		wg.Go(func() { preps[i], errs[i] = e.prepare(pp("hello", "r", "")) })
	}
	wg.Wait()
	ok := 0
	var prep PrepareResult
	for i, err := range errs {
		if err == nil {
			ok++
			prep = preps[i]
		} else {
			assert.Equal(t, CodeDeployInProgress, codeOf(err))
		}
	}
	require.Equal(t, 1, ok, "只有一个 prepare 成功")

	e.srcFile[prep.ArchivePath] = []byte("v1")
	sum := sha256.Sum256([]byte("v1"))
	cp := CommitParams{DeployID: prep.DeployID, SHA256: hex.EncodeToString(sum[:]), SizeBytes: 2,
		Services: []apigen.OrbBoxService{{Name: "web", Command: "./run"}}}
	results := make([]CommitResult, 2)
	for i := range 2 {
		wg.Go(func() { results[i], errs[i] = e.s.CommitDeploy(e.ctx, e.src, cp) })
	}
	wg.Wait()
	ok = 0
	for i, err := range errs {
		if err == nil {
			assert.True(t, results[i].OK, "%+v", results[i])
			ok++
		} else {
			assert.Equal(t, CodeDeployNotFound, codeOf(err))
		}
	}
	assert.Equal(t, 1, ok, "只有一个 commit 认领成功")

	// 别的 orb 不能认领
	prep2, err := e.prepare(pp("other", "r", ""))
	require.NoError(t, err)
	_, err = e.s.CommitDeploy(e.ctx, mkOrb(t, e.env, false), CommitParams{DeployID: prep2.DeployID, SHA256: cp.SHA256, SizeBytes: 2, Services: cp.Services})
	assert.Equal(t, CodeDeployNotFound, codeOf(err))
	_, err = e.s.CommitDeploy(e.ctx, e.src, CommitParams{DeployID: prep2.DeployID, SHA256: cp.SHA256, SizeBytes: MaxReleaseBytes + 1, Services: cp.Services})
	assert.Equal(t, CodeReleaseTooLarge, codeOf(err))
	assert.Equal(t, deployPrepared, e.deployRow(prep2.DeployID).State, "过大时行仍是 prepared")
}

// ---- 成功 ----

func TestCommitSuccess(t *testing.T) {
	e := newDeployEnv(t)
	require.NoError(t, e.s.PutSecret(e.ctx, "b1", "hello", "API_KEY", "s3cret", true))
	prep, err := e.prepare(pp("hello", "github.com/acme/x", ""))
	require.NoError(t, err)
	assert.Equal(t, "https://hello.apps.oryn.test", prep.URL)
	assert.Equal(t, "b1", prep.Env["ORYN_DEPLOYMENT_BOX"])
	assert.Equal(t, "~/.cache/oryn/box-releases/"+prep.DeployID+".tar.gz", prep.ArchivePath)
	a, ok := e.appRow("hello")
	require.True(t, ok, "首次部署先占一行临时 app")
	assert.Nil(t, a.LiveReleaseID)
	assert.Equal(t, int64(appPortMin), a.Port)
	v, err := e.s.Get(e.ctx, "b1")
	require.NoError(t, err)
	require.Len(t, v.Apps, 1)
	assert.True(t, v.Apps[0].Deploying)
	assert.Nil(t, v.Apps[0].Live)

	res, err := e.commit(prep, "v1")
	require.NoError(t, err)
	require.True(t, res.OK, "%+v", res)
	assert.Equal(t, prep.ReleaseID, res.ReleaseID)
	assert.Equal(t, "https://hello.apps.oryn.test", res.URL)
	act := e.boxd.activations[0]
	assert.Equal(t, "s3cret", act.Env["API_KEY"])
	assert.Equal(t, "~/apps/hello/data", act.Env["ORYN_DEPLOYMENT_DATA_DIRECTORY"])
	assert.Equal(t, keepReleases, act.Keep)
	assert.Equal(t, 30000, act.Port)
	assert.Equal(t, deploySucceeded, e.deployRow(prep.DeployID).State)

	// 再部署三次：live → kept，超过 3 个的被裁剪；第二次改成私有
	p2 := pp("hello", "github.com/acme/x", "")
	p2.Public = new(false)
	r2 := e.deploy(p2, "v2")
	r3 := e.deploy(pp("hello", "github.com/acme/x", ""), "v3")
	r4 := e.deploy(pp("hello", "github.com/acme/x", ""), "v4")
	rels, err := e.s.Releases(e.ctx, "b1", "hello")
	require.NoError(t, err)
	states := map[string]apigen.BoxReleaseState{}
	for _, r := range rels {
		states[r.ID] = r.State
	}
	assert.Equal(t, map[string]apigen.BoxReleaseState{
		res.ReleaseID: apigen.BoxReleasePruned, r2.ReleaseID: apigen.BoxReleaseKept,
		r3.ReleaseID: apigen.BoxReleaseKept, r4.ReleaseID: apigen.BoxReleaseLive,
	}, states)
	a, _ = e.appRow("hello")
	assert.False(t, a.Public, "set_public 生效")
	assert.Equal(t, []string{"hello"}, e.dropped, "改成私有时断开匿名连接")

	v, err = e.s.Get(e.ctx, "b1")
	require.NoError(t, err)
	require.NotNil(t, v.Apps[0].Live)
	assert.Equal(t, r4.ReleaseID, v.Apps[0].Live.ID)
	assert.False(t, v.Apps[0].Deploying)
	require.NotNil(t, v.LastDeployAt)

	items, err := e.s.Activity(e.ctx, "b1", 100)
	require.NoError(t, err)
	var kinds []apigen.BoxActivityKind
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	assert.Equal(t, 4, count(kinds, apigen.BoxActivityDeployed))
	assert.Equal(t, 1, count(kinds, apigen.BoxActivityVisibility))
	assert.Equal(t, apigen.BoxActorAgent, items[0].Actor)

	// 回滚到上一个：live 与 kept 对调
	app, err := e.s.Rollback(e.ctx, "b1", "hello", "", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	require.NotNil(t, app.Live)
	assert.Equal(t, r3.ReleaseID, app.Live.ID)
	_, err = e.s.Rollback(e.ctx, "b1", "hello", res.ReleaseID, Actor{Kind: ActorOwner})
	assert.Equal(t, CodeReleaseNotFound, codeOf(err), "已裁剪的不能回滚")
}

func count[T comparable](xs []T, x T) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

// ---- 失败 ----

// 首次部署的三条失败路径之后临时 app 行都消失、名字可再用。
func TestFirstDeployFailuresReleaseName(t *testing.T) {
	t.Run("orbd ok=false", func(t *testing.T) {
		e := newDeployEnv(t)
		e.boxd.fail = &apigen.OrbBoxResult{Code: new("not_listening"), Message: new("port 30000 never opened"), Log: new("boom")}
		prep, err := e.prepare(pp("hello", "r", ""))
		require.NoError(t, err)
		res, err := e.commit(prep, "v1")
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, "not_listening", res.Code)
		assert.Equal(t, "boom", res.Log)
		_, ok := e.appRow("hello")
		assert.False(t, ok)
		r := e.deployRow(prep.DeployID)
		assert.Equal(t, deployFailed, r.State)
		e.boxd.fail = nil
		e.deploy(pp("hello", "other-repo", ""), "v1")
	})
	t.Run("cancel", func(t *testing.T) {
		e := newDeployEnv(t)
		prep, err := e.prepare(pp("hello", "r", ""))
		require.NoError(t, err)
		require.NoError(t, e.s.CancelDeploy(e.ctx, "b1", "hello"))
		_, ok := e.appRow("hello")
		assert.False(t, ok)
		assert.Equal(t, deployCanceled, e.deployRow(prep.DeployID).State)
		_, err = e.commit(prep, "v1")
		assert.Equal(t, CodeDeployNotFound, codeOf(err), "取消之后不能再 commit")
		e.deploy(pp("hello", "r", ""), "v1")
	})
	t.Run("expire", func(t *testing.T) {
		e := newDeployEnv(t)
		prep, err := e.prepare(pp("hello", "r", ""))
		require.NoError(t, err)
		e.clk.Add(deployExpire + time.Minute)
		e.deploy(pp("hello", "r2", ""), "v1")
		assert.Equal(t, deployCanceled, e.deployRow(prep.DeployID).State)
	})
}

// 已有 live 的 app 部署失败后 app 行原样；--move 失败不改绑定，成功才改。
func TestFailedDeployKeepsApp(t *testing.T) {
	e := newDeployEnv(t)
	first := e.deploy(pp("hello", "github.com/acme/x", ""), "v1")
	e.s.probes.Wait() // 部署成功后的后台预览探测会写 preview_checked_at，等它写完再拍快照
	before, _ := e.appRow("hello")

	e.boxd.fail = &apigen.OrbBoxResult{Code: new("not_listening")}
	prep, err := e.prepare(pp("hello", "github.com/acme/x", ""))
	require.NoError(t, err)
	res, err := e.commit(prep, "v2")
	require.NoError(t, err)
	assert.Equal(t, "not_listening", res.Code)
	after, _ := e.appRow("hello")
	assert.Equal(t, before, after)

	mv := pp("hello", "github.com/acme/new", "web")
	mv.Move = true
	prep, err = e.prepare(mv)
	require.NoError(t, err)
	res, err = e.commit(prep, "v3")
	require.NoError(t, err)
	assert.False(t, res.OK)
	after, _ = e.appRow("hello")
	assert.Equal(t, "github.com/acme/x", after.Repo, "失败的 --move 不改绑定")
	assert.Equal(t, "", after.Root)

	e.boxd.fail = nil
	e.deploy(mv, "v4")
	after, _ = e.appRow("hello")
	assert.Equal(t, "github.com/acme/new", after.Repo)
	assert.Equal(t, "web", after.Root)

	// 回滚到换仓库之前的 release：绑定一起回去，从原仓库部署不再需要 --move
	_, err = e.s.Rollback(e.ctx, "b1", "hello", first.ReleaseID, Actor{Kind: ActorOwner})
	require.NoError(t, err)
	after, _ = e.appRow("hello")
	assert.Equal(t, "github.com/acme/x", after.Repo)
	assert.Equal(t, "", after.Root)
	e.deploy(pp("hello", "github.com/acme/x", ""), "v5")

	items, err := e.s.Activity(e.ctx, "b1", 100)
	require.NoError(t, err)
	var kinds []apigen.BoxActivityKind
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	assert.Equal(t, 2, count(kinds, apigen.BoxActivityDeployFailed))
	assert.Equal(t, 1, count(kinds, apigen.BoxActivityRolledBack))
}

// 激活调用返回连接错误：按 orbd 的状态判定成败。
func TestActivateUnknownResolved(t *testing.T) {
	for _, applied := range []bool{true, false} {
		t.Run(map[bool]string{true: "orbd 已切换", false: "orbd 没有切换"}[applied], func(t *testing.T) {
			e := newDeployEnv(t)
			h := e.orbs.handlers[e.box.OrbID]
			e.orbs.doErr = func(id string, req *http.Request) error {
				if strings.HasSuffix(req.URL.Path, "/activate") {
					if applied {
						h.ServeHTTP(httptest.NewRecorder(), req)
					}
					return errors.New("link lost")
				}
				return nil
			}
			prep, err := e.prepare(pp("hello", "r", ""))
			require.NoError(t, err)
			res, err := e.commit(prep, "v1")
			require.NoError(t, err)
			assert.Equal(t, applied, res.OK, "%+v", res)
			want := deployFailed
			if applied {
				want = deploySucceeded
			}
			assert.Equal(t, want, e.deployRow(prep.DeployID).State)
			a, ok := e.appRow("hello")
			assert.Equal(t, applied, ok)
			if applied {
				require.NotNil(t, a.LiveReleaseID)
				assert.Equal(t, prep.ReleaseID, *a.LiveReleaseID)
			}
		})
	}
}

// 控制面重启：activating 的行按 orbd 的状态补完，transferring 的行判失败。
func TestRestartSweepsDeploys(t *testing.T) {
	e := newDeployEnv(t)
	act, err := e.prepare(pp("hello", "r", ""))
	require.NoError(t, err)
	xfer, err := e.prepare(pp("world", "r", ""))
	require.NoError(t, err)
	gone, err := e.prepare(pp("gone", "r", ""))
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE box_deploys SET state = 'activating', services = '[{"name":"web","command":"./run","cwd":""}]' WHERE id IN (?, ?)`, act.DeployID, gone.DeployID)
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE box_deploys SET state = 'transferring' WHERE id = ?`, xfer.DeployID)
	require.NoError(t, err)
	// orbd 已经把 hello 的新 release 确认就绪；gone 在 orbd 上不存在
	e.boxd.apps["hello"] = &boxdApp{port: 30000, live: act.ReleaseID, releases: []string{act.ReleaseID}}

	s2 := New(e.s.cfg, e.st, e.orbs, e.th)
	s2.sweepDeploys(e.ctx, true)
	assert.Equal(t, deploySucceeded, e.deployRow(act.DeployID).State)
	r := e.deployRow(xfer.DeployID)
	assert.Equal(t, deployFailed, r.State)
	require.NotNil(t, r.Error)
	assert.Equal(t, "control plane restarted", *r.Error)
	assert.Equal(t, deployFailed, e.deployRow(gone.DeployID).State)
	a, ok := e.appRow("hello")
	require.True(t, ok)
	require.NotNil(t, a.LiveReleaseID)
	_, ok = e.appRow("world")
	assert.False(t, ok)

	// orbd 仍在试运行：不动
	still, err := e.prepare(pp("again", "r", ""))
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE box_deploys SET state = 'activating' WHERE id = ?`, still.DeployID)
	require.NoError(t, err)
	e.boxd.apps["again"] = &boxdApp{activating: still.ReleaseID}
	s2.sweepDeploys(e.ctx, false)
	assert.Equal(t, deployActivating, e.deployRow(still.DeployID).State)
}

// CancelDeploy：prepared 与 transferring 作废（掐断在途传输），activating 回 deploy_in_progress，没有 → deploy_not_found。
func TestCancelDeployStates(t *testing.T) {
	e := newDeployEnv(t)
	assert.Equal(t, CodeDeployNotFound, codeOf(e.s.CancelDeploy(e.ctx, "b1", "hello")))

	prep, err := e.prepare(pp("hello", "r", ""))
	require.NoError(t, err)
	arrived := make(chan struct{})
	e.boxd.mu.Lock()
	e.boxd.holdIncoming = arrived
	e.boxd.mu.Unlock()
	done := make(chan CommitResult, 1)
	go func() {
		res, err := e.commit(prep, "v1")
		assert.NoError(t, err)
		done <- res
	}()
	<-arrived
	assert.Equal(t, deployTransferring, e.deployRow(prep.DeployID).State)
	require.NoError(t, e.s.CancelDeploy(e.ctx, "b1", "hello"))
	select {
	case res := <-done:
		assert.False(t, res.OK)
		assert.Equal(t, codeCanceled, res.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("取消没有掐断传输")
	}
	assert.Equal(t, deployCanceled, e.deployRow(prep.DeployID).State)
	_, ok := e.appRow("hello")
	assert.False(t, ok)

	e.boxd.mu.Lock()
	e.boxd.holdIncoming = nil
	e.boxd.mu.Unlock()
	prep, err = e.prepare(pp("hello", "r", ""))
	require.NoError(t, err)
	_, err = e.st.DB.ExecContext(e.ctx, `UPDATE box_deploys SET state = 'activating' WHERE id = ?`, prep.DeployID)
	require.NoError(t, err)
	assert.Equal(t, CodeDeployInProgress, codeOf(e.s.CancelDeploy(e.ctx, "b1", "hello")))
}

// ---- 密钥、app 操作 ----

func TestSecrets(t *testing.T) {
	e := newDeployEnv(t)
	require.NoError(t, e.s.PutSecret(e.ctx, "b1", "hello", "TOKEN", "t0k", true))
	require.NoError(t, e.s.PutSecret(e.ctx, "b1", "hello", "MODE", "prod", false))
	list, err := e.s.Secrets(e.ctx, "b1", "hello")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "MODE", list[0].Name)
	require.NotNil(t, list[0].Value)
	assert.Equal(t, "prod", *list[0].Value)
	assert.Nil(t, list[1].Value, "密文不读回")

	for _, name := range []string{"PORT", "ORYN_DEPLOYMENT_URL", "1BAD", "bad-name"} {
		assert.Equal(t, CodeInvalid, codeOf(e.s.PutSecret(e.ctx, "b1", "hello", name, "v", true)), name)
	}
	assert.Equal(t, CodeInvalid, codeOf(e.s.PutSecret(e.ctx, "b1", "hello", "BIG", strings.Repeat("x", maxSecretValue+1), true)))
	big := strings.Repeat("x", maxSecretValue)
	var err2 error
	for i := range 20 {
		if err2 = e.s.PutSecret(e.ctx, "b1", "hello", "K"+string(rune('A'+i)), big, true); err2 != nil {
			break
		}
	}
	assert.Equal(t, CodeInvalid, codeOf(err2), "合计超过 1 MiB 被拒")
	// 替换同名的旧值不重复计入
	require.NoError(t, e.s.PutSecret(e.ctx, "b1", "hello", "KA", big, true))

	require.NoError(t, e.s.DeleteSecret(e.ctx, "b1", "hello", "TOKEN"))
	list, err = e.s.Secrets(e.ctx, "b1", "hello")
	require.NoError(t, err)
	for _, s := range list {
		assert.NotEqual(t, "TOKEN", s.Name)
	}
}

func TestAppOperations(t *testing.T) {
	e := newDeployEnv(t)
	e.deploy(pp("hello", "r", ""), "v1")

	app, err := e.s.SetPublic(e.ctx, "b1", "hello", false, Actor{Kind: ActorOwner})
	require.NoError(t, err)
	assert.False(t, app.Public)
	assert.Equal(t, []string{"hello"}, e.dropped)

	_, err = e.s.Restart(e.ctx, "b1", "hello", Actor{Kind: ActorOwner})
	require.NoError(t, err)

	var out strings.Builder
	require.NoError(t, e.s.Exec(e.ctx, "b1", apigen.BoxExecRequest{Command: "ls", App: new("hello")}, &out))
	assert.Contains(t, out.String(), `cwd=~/apps/hello/current`)
	assert.Equal(t, CodeInvalid, codeOf(e.s.Exec(e.ctx, "b1", apigen.BoxExecRequest{Command: "ls", TimeoutSec: new(3601)}, &out)))

	logs, err := e.s.Logs(e.ctx, "b1", "hello", "", 0)
	require.NoError(t, err)
	assert.Equal(t, "log of hello--web tail=65536", logs)

	e.srcFile["/home/u/note.txt"] = []byte("note")
	n, err := e.s.Copy(e.ctx, e.src, "b1", "hello", "note.txt", "/home/u/note.txt", true)
	require.NoError(t, err)
	assert.EqualValues(t, 4, n)
	assert.Equal(t, []byte("note"), e.boxd.files["~/apps/hello/data/note.txt"])
	_, err = e.s.Copy(e.ctx, e.src, "b1", "", "../x", "/home/u/note.txt", true)
	assert.Equal(t, CodeInvalid, codeOf(err))
	n, err = e.s.Copy(e.ctx, e.src, "b1", "", "back.txt", "/home/u/back.txt", false)
	assert.Equal(t, "not_found", codeOf(err), "box 上没有这个文件")
	assert.Zero(t, n)

	// 停着的 box 不做半截下线
	_, err = e.s.Stop(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	assert.Equal(t, CodeBoxStopped, codeOf(e.s.Undeploy(e.ctx, "b1", "hello", false, Actor{Kind: ActorOwner})))
	_, err = e.s.Start(e.ctx, "b1", Actor{Kind: ActorOwner})
	require.NoError(t, err)
	require.NoError(t, e.s.Undeploy(e.ctx, "b1", "hello", true, Actor{Kind: ActorOwner}))
	assert.Equal(t, []string{"hello?purge=1"}, e.boxd.deleted)
	_, ok := e.appRow("hello")
	assert.False(t, ok)
	_, err = e.s.Restart(e.ctx, "b1", "hello", Actor{Kind: ActorOwner})
	assert.Equal(t, CodeAppNotFound, codeOf(err))
}

// ---- box.call ----

func call(t *testing.T, e *deployEnv, orbID, threadID, op string, params map[string]any) (map[string]any, error) {
	t.Helper()
	raw, err := json.Marshal(apigen.LinkCtlBoxCallParams{ThreadID: threadID, Op: op, Params: params})
	require.NoError(t, err)
	res, err := e.s.OnCall(e.ctx, orbID, raw)
	if err != nil {
		return nil, err
	}
	return res.(apigen.LinkCtlBoxCallResult).Result, nil
}

func ctlCode(err error) string {
	var ce *link.CtlError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestOnCall(t *testing.T) {
	e := newDeployEnv(t)
	res, err := call(t, e, e.src, e.thread, "list", nil)
	require.NoError(t, err)
	boxes, ok := res["boxes"].([]any)
	require.True(t, ok, "list 的结果是带 boxes 键的对象：%v", res)
	assert.Len(t, boxes, 1)

	_, err = call(t, e, e.src, e.thread, "delete", map[string]any{"box": "b1"})
	assert.Equal(t, CodeForbidden, ctlCode(err))

	other := mkOrb(t, e.env, false)
	_, err = call(t, e, other, e.thread, "list", nil)
	assert.Equal(t, CodeForbidden, ctlCode(err), "线程不属于这台 orb")

	auto := mkOrb(t, e.env, true)
	_, err = call(t, e, auto, mkThread(t, e.env, auto), "list", nil)
	assert.Equal(t, CodeForbidden, ctlCode(err), "全自动的 orb 不能用 box")

	// 线路形状：prepare / commit 的字段名
	res, err = call(t, e, e.src, e.thread, "deploy.prepare", map[string]any{
		"box": "b1", "app": "hello", "repo": "r", "root": "", "branch": "main", "commit": "c", "source": true,
	})
	require.NoError(t, err)
	for _, k := range []string{"deployId", "releaseId", "box", "app", "url", "archivePath", "maxBytes", "env"} {
		assert.Contains(t, res, k)
	}
	e.srcFile[res["archivePath"].(string)] = []byte("v1")
	sum := sha256.Sum256([]byte("v1"))
	res, err = call(t, e, e.src, e.thread, "deploy.commit", map[string]any{
		"deployId": res["deployId"], "sha256": hex.EncodeToString(sum[:]), "sizeBytes": 2,
		"services": []map[string]string{{"name": "web", "command": "./run", "cwd": ""}},
	})
	require.NoError(t, err)
	assert.Equal(t, true, res["ok"], "%v", res)

	res, err = call(t, e, e.src, e.thread, "exec", map[string]any{"box": "b1", "command": "ls"})
	require.NoError(t, err)
	assert.EqualValues(t, 3, res["exit"])
	assert.Equal(t, "cwd=~\n", res["stdout"])
	assert.Equal(t, "warn\n", res["stderr"])
	_, err = call(t, e, e.src, e.thread, "exec", map[string]any{"box": "b1", "command": "ls", "timeoutSec": 1801})
	assert.Equal(t, CodeInvalid, ctlCode(err))

	res, err = call(t, e, e.src, e.thread, "secrets.set", map[string]any{"box": "b1", "app": "hello", "name": "A", "value": "1", "secret": false})
	require.NoError(t, err)
	assert.Empty(t, res)
	res, err = call(t, e, e.src, e.thread, "secrets.list", map[string]any{"box": "b1", "app": "hello"})
	require.NoError(t, err)
	assert.Len(t, res["secrets"], 1)

	_, err = call(t, e, e.src, e.thread, "deploy.cancel", map[string]any{"box": "b1", "app": "hello"})
	assert.Equal(t, CodeDeployNotFound, ctlCode(err))

	items, err := e.s.Activity(e.ctx, "b1", 100)
	require.NoError(t, err)
	assert.Equal(t, apigen.BoxActorAgent, items[0].Actor)
	require.NotNil(t, items[0].ThreadID)
	assert.Equal(t, e.thread, *items[0].ThreadID)
}

// Delete 中止这台 box 上在途的部署。
func TestDeleteAbortsDeploys(t *testing.T) {
	e := newDeployEnv(t)
	prep, err := e.prepare(pp("hello", "r", ""))
	require.NoError(t, err)
	arrived := make(chan struct{})
	e.boxd.mu.Lock()
	e.boxd.holdIncoming = arrived
	e.boxd.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.commit(prep, "v1")
	}()
	<-arrived
	require.NoError(t, e.s.Delete(context.Background(), "b1", Actor{Kind: ActorOwner}))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("删除 box 没有中止在途的部署")
	}
}

// 第一个 commit 卡在传输里时，同一部署的第二个 commit 被拒且不顶掉第一个的在途登记：对账不把它判成孤儿，放行后照常走完。
func TestDuplicateCommitKeepsFirstInFlight(t *testing.T) {
	e := newDeployEnv(t)
	prep, err := e.prepare(pp("hello", "r", ""))
	require.NoError(t, err)
	paused, resume := make(chan struct{}), make(chan struct{})
	e.boxd.mu.Lock()
	e.boxd.pauseIncoming, e.boxd.resumeIncoming = paused, resume
	e.boxd.mu.Unlock()
	done := make(chan CommitResult, 1)
	go func() {
		res, err := e.commit(prep, "v1")
		assert.NoError(t, err)
		done <- res
	}()
	<-paused
	require.Equal(t, deployTransferring, e.deployRow(prep.DeployID).State)

	_, err = e.commit(prep, "v1")
	assert.Equal(t, CodeDeployNotFound, codeOf(err), "同一部署的第二个 commit 被拒")
	assert.NotNil(t, e.s.flying(prep.DeployID), "第一个的在途登记还在")
	e.s.sweepDeploys(e.ctx, false)
	assert.Equal(t, deployTransferring, e.deployRow(prep.DeployID).State, "对账没有把它当孤儿")

	close(resume)
	select {
	case res := <-done:
		assert.True(t, res.OK, "%+v", res)
	case <-time.After(5 * time.Second):
		t.Fatal("第一个 commit 没有走完")
	}
	assert.Equal(t, deploySucceeded, e.deployRow(prep.DeployID).State)
	assert.Nil(t, e.s.flying(prep.DeployID), "走完后注销")
}

// Undeploy 卡在 orbd 的 DELETE 上时并发 prepare 同一个 app：prepare 等下线做完，作为首次部署重建 app 行；不留没有 app 行的
// 未结束部署。
func TestUndeployAndPrepareSerialized(t *testing.T) {
	e := newDeployEnv(t)
	e.deploy(pp("hello", "r", ""), "v1")
	paused, resume := make(chan struct{}), make(chan struct{})
	e.boxd.mu.Lock()
	e.boxd.pauseDelete, e.boxd.resumeDelete = paused, resume
	e.boxd.mu.Unlock()
	undeployed := make(chan error, 1)
	go func() { undeployed <- e.s.Undeploy(e.ctx, "b1", "hello", false, Actor{Kind: ActorOwner}) }()
	<-paused
	prepared := make(chan error, 1)
	var prep PrepareResult
	go func() {
		var err error
		prep, err = e.prepare(pp("hello", "r2", ""))
		prepared <- err
	}()
	select {
	case err := <-prepared:
		t.Fatalf("prepare 没有等下线做完：%v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(resume)
	require.NoError(t, <-undeployed)
	require.NoError(t, <-prepared)
	a, ok := e.appRow("hello")
	require.True(t, ok, "作为首次部署重建了 app 行")
	assert.Nil(t, a.LiveReleaseID)
	assert.Equal(t, "r2", a.Repo)
	assert.True(t, e.deployRow(prep.DeployID).First)
	var orphans int
	require.NoError(t, e.st.DB.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM box_deploys d WHERE d.state IN ('prepared','transferring','activating')
		AND NOT EXISTS (SELECT 1 FROM box_apps a WHERE a.box_id = d.box_id AND a.name = d.app)`).Scan(&orphans))
	assert.Zero(t, orphans, "没有 app 行的未结束部署")
}
