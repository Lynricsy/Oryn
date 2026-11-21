package orbdrt

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

// boxAppHelper 是 box app 的测试服务（ORBD_TEST_HELPER=boxapp）：当前目录有 refuse 时不监听（一直睡）；
// 否则在 127.0.0.1:$PORT 上服务：/ 回 version.txt，/pid 回 PID，/env?k=NAME 回环境变量。
func boxAppHelper() {
	if _, err := os.Stat("refuse"); err == nil {
		time.Sleep(time.Hour)
		os.Exit(1)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", os.Getenv("PORT")))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pid", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, os.Getpid()) })
	mux.HandleFunc("/env", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, os.Getenv(r.URL.Query().Get("k"))) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := os.ReadFile("version.txt")
		_, _ = w.Write(b)
	})
	fmt.Println("box app serving on", ln.Addr())
	_ = http.Serve(ln, mux)
	os.Exit(1)
}

// ---- 夹具 ----

type boxEnv struct {
	*svcEnv
	h *orbHTTP
	b *boxManager
}

func newBoxEnv(t *testing.T, home string) *boxEnv {
	t.Helper()
	e := newSvcEnv(t, nil, home)
	require.NotNil(t, e.rt.box)
	e.rt.box.readyPoll = 20 * time.Millisecond
	be := &boxEnv{svcEnv: e, h: newOrbHTTP(t, e.cp, e.rt), b: e.rt.box}
	e.setup()
	return be
}

func relID(n int) string { return fmt.Sprintf("bxr_%026d", n) }

type boxTarEntry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func boxTarGz(t *testing.T, ents ...boxTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
			if typ == tar.TypeDir {
				mode = 0o755
			}
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.link, ModTime: time.Now(), Format: tar.FormatPAX}
		if typ == tar.TypeXGlobalHeader {
			hdr = &tar.Header{Typeflag: typ, Name: "global", PAXRecords: map[string]string{"comment": "x"}, Format: tar.FormatPAX}
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// releaseTar 是 `tar -C dir -czf x .` 形状的 release 包：./ 目录条目 + ./version.txt + extra。
func releaseTar(t *testing.T, version string, extra ...boxTarEntry) []byte {
	t.Helper()
	ents := append([]boxTarEntry{{name: "./", typ: tar.TypeDir}, {name: "./version.txt", body: version}}, extra...)
	return boxTarGz(t, ents...)
}

// tarDir 把目录（含 .git）打成 release 包。
func tarDir(t *testing.T, dir string) []byte {
	t.Helper()
	ents := []boxTarEntry{{name: "./", typ: tar.TypeDir}}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			ents = append(ents, boxTarEntry{name: "./" + rel + "/", typ: tar.TypeDir})
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			ents = append(ents, boxTarEntry{name: "./" + rel, typ: tar.TypeSymlink, link: target})
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			ents = append(ents, boxTarEntry{name: "./" + rel, body: string(b), mode: int64(info.Mode().Perm())})
		}
		return nil
	}))
	return boxTarGz(t, ents...)
}

func (e *boxEnv) uploadRaw(id string, data []byte, size int, sum string) (*http.Response, []byte) {
	return e.h.do(http.MethodPut, "/box/incoming/"+id, url.Values{"size": {strconv.Itoa(size)}, "sha256": {sum}}, bytes.NewReader(data), nil)
}

func (e *boxEnv) upload(id string, data []byte) {
	e.t.Helper()
	resp, b := e.uploadRaw(id, data, len(data), sha256Hex(data))
	require.Equal(e.t, http.StatusNoContent, resp.StatusCode, string(b))
}

func appReq(app, id string, port int) apigen.OrbBoxActivateRequest {
	return apigen.OrbBoxActivateRequest{
		ReleaseID: id, Port: port, Keep: 3, ReadyTimeoutSec: 10,
		Services: []apigen.OrbBoxService{{Name: "web", Command: helperCmd("boxapp")}},
		Env:      map[string]string{"FOO": "one", boxDataDirEnv: "~/apps/" + app + "/data"},
	}
}

func (e *boxEnv) activate(app string, req apigen.OrbBoxActivateRequest) apigen.OrbBoxResult {
	e.t.Helper()
	var res apigen.OrbBoxResult
	e.h.json(http.MethodPost, "/box/apps/"+app+"/activate", nil, req, http.StatusOK, &res)
	return res
}

// deploy 上传 data 并激活；mut 可改请求。
func (e *boxEnv) deploy(app, id string, port int, data []byte, mut ...func(*apigen.OrbBoxActivateRequest)) apigen.OrbBoxResult {
	e.t.Helper()
	e.upload(id, data)
	req := appReq(app, id, port)
	for _, m := range mut {
		m(&req)
	}
	return e.activate(app, req)
}

func (e *boxEnv) rollback(app, id string, timeout int) apigen.OrbBoxResult {
	e.t.Helper()
	var res apigen.OrbBoxResult
	e.h.json(http.MethodPost, "/box/apps/"+app+"/rollback", nil, apigen.OrbBoxRollbackRequest{ReleaseID: id, ReadyTimeoutSec: timeout}, http.StatusOK, &res)
	return res
}

func (e *boxEnv) apps() apigen.OrbBoxAppsState {
	e.t.Helper()
	var st apigen.OrbBoxAppsState
	e.h.json(http.MethodGet, "/box/apps", nil, nil, http.StatusOK, &st)
	return st
}

func appState(st apigen.OrbBoxAppsState, name string) (apigen.OrbBoxAppState, bool) {
	for _, a := range st.Apps {
		if a.Name == name {
			return a, true
		}
	}
	return apigen.OrbBoxAppState{}, false
}

func (e *boxEnv) appDir(app string) string { return filepath.Join(e.home, "apps", app) }

func (e *boxEnv) currentTarget(app string) string {
	l, err := os.Readlink(filepath.Join(e.appDir(app), "current"))
	if err != nil {
		return ""
	}
	return l
}

func (e *boxEnv) appServices(app string) []apigen.OrbService {
	var out []apigen.OrbService
	for _, o := range e.rt.svc.List() {
		if strings.HasPrefix(o.Name, app+"--") {
			out = append(out, o)
		}
	}
	return out
}

func tryGet(port int, path string) (string, bool) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err == nil && resp.StatusCode == http.StatusOK
}

func appGet(t *testing.T, port int, path string) string {
	t.Helper()
	s, ok := tryGet(port, path)
	require.True(t, ok, "GET :%d%s", port, path)
	return s
}

func eventuallyGet(t *testing.T, port int, path, want string) {
	t.Helper()
	require.Eventually(t, func() bool { s, ok := tryGet(port, path); return ok && s == want }, 20*time.Second, 20*time.Millisecond,
		"GET :%d%s never returned %q", port, path, want)
}

func resMsg(res apigen.OrbBoxResult) string {
	b, _ := json.Marshal(res)
	return string(b)
}

func releaseDirs(t *testing.T, appDir string) []string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(appDir, "releases"))
	require.NoError(t, err)
	var out []string
	for _, de := range des {
		out = append(out, de.Name())
	}
	return out
}

// ---- 上传 ----

func TestBoxUpload(t *testing.T) {
	e := newBoxEnv(t, "")
	id := relID(1)
	data := releaseTar(t, "v1")
	incoming := e.b.incomingDir()
	noIncoming := func(msg string) {
		des, _ := os.ReadDir(incoming)
		assert.Empty(t, des, msg)
		assert.Empty(t, tmpResidue(t, e.rt), msg)
	}

	resp, b := e.uploadRaw(id, data, len(data), strings.Repeat("0", 64))
	assert.Equal(t, "checksum_mismatch", e.h.errCode(resp, b, http.StatusUnprocessableEntity))
	noIncoming("摘要不符")

	resp, b = e.uploadRaw(id, data, len(data)+1, sha256Hex(data))
	assert.Equal(t, "size_mismatch", e.h.errCode(resp, b, http.StatusBadRequest))
	noIncoming("字节数不符")

	e.b.maxArchive = 64
	resp, b = e.uploadRaw(id, data, len(data), sha256Hex(data))
	assert.Equal(t, "too_large", e.h.errCode(resp, b, http.StatusRequestEntityTooLarge))
	resp, b = e.h.do(http.MethodPut, "/box/incoming/"+id, url.Values{"size": {"10"}, "sha256": {sha256Hex(data)}}, io.MultiReader(bytes.NewReader(data)), nil)
	assert.Equal(t, "too_large", e.h.errCode(resp, b, http.StatusRequestEntityTooLarge))
	noIncoming("超限")
	e.b.maxArchive = maxBoxArchiveBytes

	resp, b = e.uploadRaw("bxr_bad", data, len(data), sha256Hex(data))
	assert.Equal(t, "invalid_request", e.h.errCode(resp, b, http.StatusBadRequest))

	// 通用的文件接口仍然不能写 ~/.oryn/**
	resp, b = e.h.put("~/.oryn/box/incoming/x", []byte("x"), nil, nil)
	assert.Equal(t, "protected_path", e.h.errCode(resp, b, http.StatusForbidden))

	e.upload(id, data)
	got, err := os.ReadFile(filepath.Join(incoming, id+".tar.gz"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
	st, err := os.Stat(incoming)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), st.Mode().Perm())
}

// ---- 激活 ----

func TestBoxActivateAndFailedRelease(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	id1, id2 := relID(1), relID(2)

	res := e.deploy("shop", id1, port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res))
	assert.Equal(t, id1, *res.LiveReleaseID)
	assert.Equal(t, "v1", appGet(t, port, "/"))
	assert.Equal(t, filepath.Join("releases", id1), e.currentTarget("shop"))
	assert.Equal(t, filepath.Join(e.home, "apps", "shop", "data"), appGet(t, port, "/env?k="+boxDataDirEnv))
	assert.DirExists(t, filepath.Join(e.appDir("shop"), "data"))
	e.cp.waitNotif(t, 0, "services.state", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlServicesStateParams
		_ = json.Unmarshal(raw, &p)
		for _, s := range p.Services {
			if s.Name == "shop--web" && s.Source == apigen.OrbServiceSourceApp && s.App != nil && *s.App == "shop" && s.Status == apigen.OrbServiceStatusRunning {
				return true
			}
		}
		return false
	})
	st, ok := appState(e.apps(), "shop")
	require.True(t, ok)
	assert.Equal(t, apigen.OrbBoxAppState{Name: "shop", Port: port, Live: id1, Releases: []string{id1}}, st)
	assert.True(t, e.rt.svc.Exposed(port), "app 的端口可经 portal 拨到")
	pid1 := appGet(t, port, "/pid")

	// 第二个 release 不监听：回到第一个，服务重新在跑、环境还是第一个的
	res = e.deploy("shop", id2, port, releaseTar(t, "v2", boxTarEntry{name: "./refuse"}), func(r *apigen.OrbBoxActivateRequest) {
		r.Env["FOO"] = "two"
		r.ReadyTimeoutSec = 1
	})
	require.False(t, res.Ok)
	assert.Equal(t, "not_listening", *res.Code)
	require.NotNil(t, res.Log)
	assert.Contains(t, *res.Log, "[orbd] starting shop--web")
	assert.Equal(t, filepath.Join("releases", id1), e.currentTarget("shop"))
	assert.NoDirExists(t, filepath.Join(e.appDir("shop"), "releases", id2))
	eventuallyGet(t, port, "/", "v1")
	assert.Equal(t, "one", appGet(t, port, "/env?k=FOO"))
	assert.NotEqual(t, pid1, appGet(t, port, "/pid"), "第一个 release 的服务被重新拉起")
	st, _ = appState(e.apps(), "shop")
	assert.Equal(t, apigen.OrbBoxAppState{Name: "shop", Port: port, Live: id1, Releases: []string{id1}}, st)
}

func TestBoxNewProcessPerRelease(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	id1, id2, id3 := relID(1), relID(2), relID(3)

	res := e.deploy("shop", id1, port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res))
	pid1 := appGet(t, port, "/pid")

	// 命令与环境完全相同、只是文件不同：仍然是新进程、读到新内容
	res = e.deploy("shop", id2, port, releaseTar(t, "v2"))
	require.True(t, res.Ok, resMsg(res))
	assert.Equal(t, "v2", appGet(t, port, "/"))
	pid2 := appGet(t, port, "/pid")
	assert.NotEqual(t, pid1, pid2)

	// 回滚到上一个
	res = e.rollback("shop", id1, 10)
	require.True(t, res.Ok, resMsg(res))
	assert.Equal(t, id1, *res.LiveReleaseID)
	assert.Equal(t, "v1", appGet(t, port, "/"))
	assert.NotEqual(t, pid2, appGet(t, port, "/pid"))
	st, _ := appState(e.apps(), "shop")
	assert.Equal(t, id1, st.Live)

	// 回滚目标不监听：原来的 live 重新在跑，目标目录还在
	require.NoError(t, os.WriteFile(filepath.Join(e.appDir("shop"), "releases", id2, "refuse"), nil, 0o644))
	res = e.rollback("shop", id2, 1)
	require.False(t, res.Ok)
	assert.Equal(t, "not_listening", *res.Code)
	eventuallyGet(t, port, "/", "v1")
	assert.DirExists(t, filepath.Join(e.appDir("shop"), "releases", id2))
	st, _ = appState(e.apps(), "shop")
	assert.Equal(t, id1, st.Live)
	assert.Equal(t, []string{id2, id1}, st.Releases)

	// 已经是 live 的再回滚 / 再激活：直接 ok
	res = e.rollback("shop", id1, 10)
	assert.True(t, res.Ok, resMsg(res))
	res = e.activate("shop", appReq("shop", id1, port))
	assert.True(t, res.Ok, resMsg(res))
	res = e.rollback("shop", id3, 10)
	assert.Equal(t, "release_not_found", *res.Code)

	// 同一个 releaseId 激活两次：第二次（包已经不在 incoming）直接 ok
	res = e.deploy("shop", id3, port, releaseTar(t, "v3"))
	require.True(t, res.Ok, resMsg(res))
	res = e.activate("shop", appReq("shop", id3, port))
	assert.True(t, res.Ok, resMsg(res))
	assert.Equal(t, "v3", appGet(t, port, "/"))
}

func TestBoxPruneKeepsThree(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	var res apigen.OrbBoxResult
	for i := 1; i <= 4; i++ {
		res = e.deploy("shop", relID(i), port, releaseTar(t, fmt.Sprintf("v%d", i)))
		require.True(t, res.Ok, resMsg(res))
	}
	assert.Equal(t, []string{relID(1)}, res.Pruned)
	assert.ElementsMatch(t, []string{relID(2), relID(3), relID(4)}, releaseDirs(t, e.appDir("shop")))
	st, _ := appState(e.apps(), "shop")
	assert.Equal(t, []string{relID(4), relID(3), relID(2)}, st.Releases)
}

// 试运行期间 Ensure 插进来：最终在跑的是新 release 的进程，没有旧进程残留；快照在确认就绪之前报旧 live。
// 解包卡住时 GET /box/apps 也立刻返回，activating 已经是新 release。
func TestBoxTrialEnsureAndStatus(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	id1, id2, id3 := relID(1), relID(2), relID(3)
	res := e.deploy("shop", id1, port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res))
	pid1, err := strconv.Atoi(appGet(t, port, "/pid"))
	require.NoError(t, err)

	// 钩子跑在 orbd 的处理 goroutine 里（持有管理器的锁）：不用 require，结果经 channel 交回
	during := make(chan apigen.OrbBoxAppsState, 1)
	e.b.hookTrial = func(string, string) {
		var st apigen.OrbBoxAppsState
		if resp, err := e.h.c.Get("http://orb/box/apps"); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&st)
			_ = resp.Body.Close()
		}
		during <- st
		e.rt.svc.Ensure()
	}
	res = e.deploy("shop", id2, port, releaseTar(t, "v2"), func(r *apigen.OrbBoxActivateRequest) {
		r.Services[0].Command = "sleep 0.3; " + helperCmd("boxapp")
	})
	e.b.hookTrial = nil
	require.True(t, res.Ok, resMsg(res))
	st, _ := appState(<-during, "shop")
	assert.Equal(t, id1, st.Live, "试运行中 live 仍是旧 release")
	require.NotNil(t, st.Activating)
	assert.Equal(t, id2, *st.Activating)
	st, _ = appState(e.apps(), "shop")
	assert.Equal(t, id2, st.Live)
	assert.Nil(t, st.Activating)

	svcs := e.appServices("shop")
	require.Len(t, svcs, 1)
	require.NotNil(t, svcs[0].Pid)
	assert.Equal(t, strconv.Itoa(*svcs[0].Pid), appGet(t, port, "/pid"))
	assert.Equal(t, "v2", appGet(t, port, "/"))
	requireGroupDead(t, pid1, "旧 release 的进程")

	// 解包卡住
	entered, unblock := make(chan struct{}), make(chan struct{})
	e.b.hookExtract = func(string, string) {
		close(entered)
		<-unblock
	}
	e.upload(id3, releaseTar(t, "v3"))
	done := make(chan *http.Response, 1)
	go func() {
		body, _ := json.Marshal(appReq("shop", id3, port))
		req, _ := http.NewRequest(http.MethodPost, "http://orb/box/apps/shop/activate", bytes.NewReader(body))
		resp, err := e.h.c.Do(req)
		if err != nil {
			done <- nil
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		done <- resp
	}()
	<-entered
	st, _ = appState(e.apps(), "shop")
	require.NotNil(t, st.Activating)
	assert.Equal(t, id3, *st.Activating)
	assert.Equal(t, id2, st.Live)
	close(unblock)
	resp := <-done
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	e.b.hookExtract = nil
	eventuallyGet(t, port, "/", "v3")
}

// orbd 在试运行中途退出：启动时退回 Trial.Prev，不起任何服务。
func TestBoxRecoverTrial(t *testing.T) {
	old, nu := relID(1), relID(2)
	svcs := []apigen.OrbBoxService{{Name: "web", Command: sleepCmd}}
	cases := []struct {
		name          string
		current       string // current 指向哪个 release（"" = 没有 current）
		prev, trial   func() *boxApp
		wantLive      string // "" = app 不存在
		wantGone      []string
		wantKept      []string
		wantCurrentTo string
	}{
		{
			name: "current 已指向试运行的 release", current: nu,
			prev: func() *boxApp {
				return &boxApp{Port: 30001, Live: old, Releases: []boxRelease{{ID: old, Services: svcs}}}
			},
			wantLive: old, wantGone: []string{nu}, wantKept: []string{old}, wantCurrentTo: old,
		},
		{
			name: "落盘之后、换链接之前退出", current: old,
			prev: func() *boxApp {
				return &boxApp{Port: 30001, Live: old, Releases: []boxRelease{{ID: old, Services: svcs}}}
			},
			wantLive: old, wantGone: []string{nu}, wantKept: []string{old}, wantCurrentTo: old,
		},
		{
			name: "首次激活中途退出", current: nu,
			prev: func() *boxApp { return nil },
		},
		{
			name: "回滚中途退出", current: old,
			prev: func() *boxApp {
				return &boxApp{Port: 30001, Live: nu, Releases: []boxRelease{{ID: nu, Services: svcs}, {ID: old, Services: svcs}}}
			},
			// 回滚的目标是已有的 old：退回之后它仍在
			wantLive: nu, wantKept: []string{nu, old}, wantCurrentTo: nu,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			appDir := filepath.Join(home, "apps", "shop")
			for _, id := range []string{old, nu} {
				require.NoError(t, os.MkdirAll(filepath.Join(appDir, "releases", id), 0o755))
			}
			require.NoError(t, os.MkdirAll(filepath.Join(appDir, "data"), 0o755))
			if tc.current != "" {
				require.NoError(t, os.Symlink(filepath.Join("releases", tc.current), filepath.Join(appDir, "current")))
			}
			prev := tc.prev()
			trialID := nu
			cur := &boxApp{Port: 30001, Live: nu, Releases: []boxRelease{{ID: nu, Services: svcs}}}
			if prev != nil {
				if prev.Live == nu { // 回滚：试运行 old
					trialID = old
					cur = prev.clone()
					cur.Live = old
				} else {
					cur.Releases = append(cur.Releases, prev.clone().Releases...)
				}
			}
			cur.Trial = &boxTrial{Release: trialID, Prev: prev}
			data, err := json.Marshal(boxStateFile{Version: boxStateVersion, Apps: map[string]*boxApp{"shop": cur}})
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".oryn", "box"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(home, ".oryn", "box", "apps.json"), data, 0o600))

			e := newBoxEnv(t, home)
			st, ok := appState(e.apps(), "shop")
			if tc.wantLive == "" {
				assert.False(t, ok, "首次激活的 app 不存在")
				assert.NoDirExists(t, appDir)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.wantLive, st.Live)
			assert.Nil(t, st.Activating)
			assert.Equal(t, filepath.Join("releases", tc.wantCurrentTo), e.currentTarget("shop"))
			for _, id := range tc.wantGone {
				assert.NoDirExists(t, filepath.Join(appDir, "releases", id))
			}
			for _, id := range tc.wantKept {
				assert.DirExists(t, filepath.Join(appDir, "releases", id))
			}
			saved, err := os.ReadFile(filepath.Join(home, ".oryn", "box", "apps.json"))
			require.NoError(t, err)
			assert.NotContains(t, string(saved), `"trial"`)
			if tc.name == "回滚中途退出" {
				res := e.rollback("shop", nu, 10)
				assert.True(t, res.Ok, "对已经是 live 的 release 回滚直接 ok：%s", resMsg(res))
			}
		})
	}
}

func TestBoxUndeploy(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	res := e.deploy("shop", relID(1), port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res))
	require.NoError(t, os.WriteFile(filepath.Join(e.appDir("shop"), "data", "note"), []byte("x"), 0o644))

	from := e.cp.notifCount()
	resp, b := e.h.do(http.MethodDelete, "/box/apps/shop", nil, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.Empty(t, e.appServices("shop"), "下线之后监督器里立刻没有它的服务")
	e.cp.waitNotif(t, from, "services.state", func(raw json.RawMessage) bool {
		var p apigen.LinkCtlServicesStateParams
		_ = json.Unmarshal(raw, &p)
		for _, s := range p.Services {
			if strings.HasPrefix(s.Name, "shop--") {
				return false
			}
		}
		return true
	})
	_, ok := appState(e.apps(), "shop")
	assert.False(t, ok)
	assert.NoFileExists(t, filepath.Join(e.appDir("shop"), "current"))
	assert.NoDirExists(t, filepath.Join(e.appDir("shop"), "releases"))
	assert.FileExists(t, filepath.Join(e.appDir("shop"), "data", "note"), "数据目录保留")
	assert.False(t, e.rt.svc.Exposed(port))
	_, up := tryGet(port, "/")
	assert.False(t, up)

	resp, b = e.h.do(http.MethodDelete, "/box/apps/shop", url.Values{"purge": {"1"}}, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	assert.NoDirExists(t, e.appDir("shop"))
	resp, b = e.h.do(http.MethodDelete, "/box/apps/ghost", nil, nil, nil)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode, string(b))
	resp, b = e.h.do(http.MethodDelete, "/box/apps/Bad_Name", nil, nil, nil)
	assert.Equal(t, "invalid_request", e.h.errCode(resp, b, http.StatusBadRequest))
}

func TestBoxFirstActivationFails(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	res := e.deploy("shop", relID(1), port, releaseTar(t, "v1", boxTarEntry{name: "./refuse"}), func(r *apigen.OrbBoxActivateRequest) {
		r.ReadyTimeoutSec = 1
	})
	require.False(t, res.Ok)
	assert.Equal(t, "not_listening", *res.Code)
	_, ok := appState(e.apps(), "shop")
	assert.False(t, ok)
	assert.NoDirExists(t, e.appDir("shop"))
	assert.Empty(t, e.appServices("shop"))
	assert.False(t, e.rt.svc.Exposed(port))
}

func TestBoxServicesSharePortAndEnv(t *testing.T) {
	e := newBoxEnv(t, "")
	e.rt.envMu.Lock()
	e.rt.setupEnv = map[string]string{"SETUP_SECRET": "s"}
	e.rt.envMu.Unlock()
	port := freePort(t)
	res := e.deploy("shop", relID(1), port, releaseTar(t, "v1"), func(r *apigen.OrbBoxActivateRequest) {
		r.Services = append(r.Services, apigen.OrbBoxService{Name: "worker", Command: sleepCmd})
	})
	require.True(t, res.Ok, resMsg(res))
	require.Eventually(t, func() bool {
		svcs := e.appServices("shop")
		if len(svcs) != 2 {
			return false
		}
		for _, s := range svcs {
			if s.Status != apigen.OrbServiceStatusRunning || s.Port == nil || *s.Port != port {
				return false
			}
		}
		return true
	}, 20*time.Second, 20*time.Millisecond, "两个服务共用端口都进入 running：%+v", e.appServices("shop"))
	assert.Equal(t, strconv.Itoa(port), appGet(t, port, "/env?k=PORT"))
	assert.Equal(t, "one", appGet(t, port, "/env?k=FOO"))
	assert.Empty(t, appGet(t, port, "/env?k=SETUP_SECRET"), "setup 的全局 env 不进 app")
	assert.Empty(t, appGet(t, port, "/env?k=ORYN_THREAD_ID"))
	assert.Empty(t, appGet(t, port, "/env?k=PUBLIC_URL"))
}

func TestBoxReservedNamesAndLimits(t *testing.T) {
	e := newBoxEnv(t, "")
	var sb strings.Builder
	sb.WriteString("services:\n  shop--web: {command: " + strconv.Quote(sleepCmd) + "}\n")
	for i := range maxServices {
		fmt.Fprintf(&sb, "  s%d: {command: %s}\n", i, strconv.Quote(sleepCmd))
	}
	e.writeServices(sb.String())
	e.rt.svc.Ensure()

	port := freePort(t)
	res := e.deploy("shop", relID(1), port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res), "普通服务已有 32 个时还能激活 app")
	var named []apigen.OrbService
	plain := 0
	for _, o := range e.rt.svc.List() {
		if o.Name == "shop--web" {
			named = append(named, o)
		} else if o.App == nil {
			plain++
		}
	}
	require.Len(t, named, 1, "文件里的 shop--web 被忽略")
	assert.Equal(t, apigen.OrbServiceSourceApp, named[0].Source)
	assert.Equal(t, maxServices, plain)

	cmd := sleepCmd
	_, err := e.rt.svc.Start(orbsock.ServiceStartParams{Name: "x--y", Command: &cmd})
	se, ok := isSvcError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, orbsock.CodeInvalidService, se.code)
	_, err = e.rt.svc.Start(orbsock.ServiceStartParams{Name: "extra", Command: &cmd})
	se, ok = isSvcError(err)
	require.True(t, ok, "%v", err)
	assert.Contains(t, se.msg, "too many services")

	_, err = e.rt.svc.Stop("shop--web", true)
	se, ok = isSvcError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, orbsock.CodeInvalidService, se.code)
	assert.Contains(t, se.msg, "undeploy")

	// 显式写了 app 端口的普通服务 → port_conflict
	e.writeServices(fmt.Sprintf("services:\n  clash: {command: %s, port: %d}\n", strconv.Quote(sleepCmd), port))
	list, _ := e.rt.svc.Ensure()
	clash := find(list, "clash")
	require.NotNil(t, clash.Error)
	assert.Equal(t, "port_conflict", *clash.Error)
}

func TestBoxChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	e := newBoxEnv(t, "")
	port := freePort(t)
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	commit := func(version string) {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "version.txt"), []byte(version), 0o644))
		git(repo, "add", "-A")
		git(repo, "commit", "-qm", version)
	}
	git(repo, "init", "-q", "-b", "main")
	commit("v1")
	source := func(r *apigen.OrbBoxActivateRequest) { r.Source = true }

	res := e.deploy("shop", relID(1), port, tarDir(t, repo), source)
	require.True(t, res.Ok, resMsg(res))
	current := filepath.Join(e.appDir("shop"), "current")
	require.NoError(t, os.WriteFile(filepath.Join(current, "version.txt"), []byte("edited on box"), 0o644))

	commit("v2")
	v2 := tarDir(t, repo)
	res = e.deploy("shop", relID(2), port, v2, source)
	require.False(t, res.Ok)
	assert.Equal(t, "box_changes", *res.Code)
	assert.Contains(t, *res.Message, "version.txt")
	assert.Contains(t, *res.Message, "--discard-box-changes")
	assert.Equal(t, "edited on box", appGet(t, port, "/"), "被拒的部署不动 live")

	res = e.activate("shop", func() apigen.OrbBoxActivateRequest {
		r := appReq("shop", relID(2), port)
		r.Source, r.DiscardBoxChanges = true, new(true)
		return r
	}())
	require.True(t, res.Ok, resMsg(res))
	assert.Equal(t, "v2", appGet(t, port, "/"))

	// box 上做了提交、而新 release 里没有它
	require.NoError(t, os.WriteFile(filepath.Join(current, "box.txt"), []byte("x"), 0o644))
	boxGit := func(args ...string) { git(current, args...) }
	boxGit("add", "-A")
	boxGit("commit", "-qm", "on box")
	head := git(current, "rev-parse", "HEAD")
	commit("v3")
	res = e.deploy("shop", relID(3), port, tarDir(t, repo), source)
	require.False(t, res.Ok)
	assert.Equal(t, "box_changes", *res.Code)
	assert.Contains(t, *res.Message, head[:12])
	assert.NoDirExists(t, filepath.Join(e.appDir("shop"), "releases", relID(3)))
}

func TestBoxRestart(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	res := e.deploy("shop", relID(1), port, releaseTar(t, "v1"))
	require.True(t, res.Ok, resMsg(res))
	pid := appGet(t, port, "/pid")
	e.h.json(http.MethodPost, "/box/apps/shop/restart", nil, apigen.OrbBoxRestartRequest{Env: map[string]string{"FOO": "two"}, ReadyTimeoutSec: 10}, http.StatusOK, &res)
	require.True(t, res.Ok, resMsg(res))
	assert.Equal(t, "two", appGet(t, port, "/env?k=FOO"))
	assert.NotEqual(t, pid, appGet(t, port, "/pid"))

	e.h.json(http.MethodPost, "/box/apps/ghost/restart", nil, apigen.OrbBoxRestartRequest{Env: map[string]string{}, ReadyTimeoutSec: 10}, http.StatusOK, &res)
	assert.False(t, res.Ok)
	assert.Equal(t, "app_not_found", *res.Code)
	resp, b := e.h.do(http.MethodPost, "/box/apps/shop/restart", nil, strings.NewReader(`{"env":{"PORT":"1"},"readyTimeoutSec":10}`), nil)
	assert.Equal(t, "invalid_request", e.h.errCode(resp, b, http.StatusBadRequest))
}

func TestBoxActivateValidation(t *testing.T) {
	e := newBoxEnv(t, "")
	port := freePort(t)
	bad := map[string]func(*apigen.OrbBoxActivateRequest){
		"release id":    func(r *apigen.OrbBoxActivateRequest) { r.ReleaseID = "bxr_x" },
		"port":          func(r *apigen.OrbBoxActivateRequest) { r.Port = 0 },
		"no services":   func(r *apigen.OrbBoxActivateRequest) { r.Services = nil },
		"service name":  func(r *apigen.OrbBoxActivateRequest) { r.Services[0].Name = "a--b" },
		"cwd escapes":   func(r *apigen.OrbBoxActivateRequest) { r.Services[0].Cwd = "../x" },
		"reserved env":  func(r *apigen.OrbBoxActivateRequest) { r.Env["PORT"] = "1" },
		"keep":          func(r *apigen.OrbBoxActivateRequest) { r.Keep = 0 },
		"ready timeout": func(r *apigen.OrbBoxActivateRequest) { r.ReadyTimeoutSec = 0 },
	}
	for name, mut := range bad {
		req := appReq("shop", relID(1), port)
		mut(&req)
		body, err := json.Marshal(req)
		require.NoError(t, err)
		resp, b := e.h.do(http.MethodPost, "/box/apps/shop/activate", nil, bytes.NewReader(body), nil)
		assert.Equal(t, "invalid_request", e.h.errCode(resp, b, http.StatusBadRequest), name)
	}
	res := e.activate("shop", appReq("shop", relID(1), port))
	assert.False(t, res.Ok)
	assert.Equal(t, "archive_missing", *res.Code)
	_, ok := appState(e.apps(), "shop")
	assert.False(t, ok)
}

// ---- 解包 ----

func TestBoxArchiveEntries(t *testing.T) {
	b := &boxManager{maxEntries: 100, maxUnpacked: 1 << 20}
	extract := func(ents ...boxTarEntry) (string, error) {
		dir := t.TempDir()
		return dir, b.extract(context.Background(), bytes.NewReader(boxTarGz(t, ents...)), dir)
	}
	code := func(err error) string {
		require.Error(t, err)
		return asBoxFail(err).code
	}

	dir, err := extract(
		boxTarEntry{name: "./", typ: tar.TypeDir},
		boxTarEntry{typ: tar.TypeXGlobalHeader},
		boxTarEntry{name: "./sub/", typ: tar.TypeDir},
		boxTarEntry{name: "./deep/a/b.txt", body: "b"}, // 父目录没有单独的条目
		boxTarEntry{name: "pkg/", typ: tar.TypeDir},
		boxTarEntry{name: "pkg/run.sh", body: "#!/bin/sh", mode: 0o755},
		boxTarEntry{name: "./out", typ: tar.TypeSymlink, link: "/usr/bin"}, // 指向包外的符号链接原样建出
		boxTarEntry{name: "./hl", typ: tar.TypeLink, link: "./deep/a/b.txt"},
	)
	require.NoError(t, err)
	assert.DirExists(t, filepath.Join(dir, "sub"))
	got, err := os.ReadFile(filepath.Join(dir, "deep", "a", "b.txt"))
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))
	st, err := os.Stat(filepath.Join(dir, "pkg", "run.sh"))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode().Perm()&0o100, "可执行位保留")
	target, err := os.Readlink(filepath.Join(dir, "out"))
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin", target)
	got, err = os.ReadFile(filepath.Join(dir, "hl"))
	require.NoError(t, err)
	assert.Equal(t, "b", string(got))

	outside := t.TempDir()
	_, err = extract(
		boxTarEntry{name: "evil", typ: tar.TypeSymlink, link: outside},
		boxTarEntry{name: "evil/x", body: "pwned"},
	)
	assert.Equal(t, "bad_archive", code(err))
	assert.NoFileExists(t, filepath.Join(outside, "x"), "经包里的符号链接写不到根外")

	for _, name := range []string{"../x", "/abs", "a/../b", "./../x"} {
		_, err = extract(boxTarEntry{name: name, body: "x"})
		assert.Equal(t, "bad_archive", code(err), name)
	}
	_, err = extract(boxTarEntry{name: "hl", typ: tar.TypeLink, link: "../../etc/passwd"})
	assert.Equal(t, "bad_archive", code(err))
	_, err = extract(boxTarEntry{name: "fifo", typ: tar.TypeFifo})
	assert.Equal(t, "bad_archive", code(err))
	err = b.extract(context.Background(), strings.NewReader("not gzip"), t.TempDir())
	assert.Equal(t, "bad_archive", code(err))

	b.maxEntries = 2
	_, err = extract(boxTarEntry{name: "a"}, boxTarEntry{name: "b"}, boxTarEntry{name: "c"})
	assert.Equal(t, "too_large", code(err))
	b.maxEntries, b.maxUnpacked = 100, 4
	_, err = extract(boxTarEntry{name: "a", body: "12345"})
	assert.Equal(t, "too_large", code(err))
}

// ---- exec ----

type execResult struct {
	stdout, stderr string
	exit           *int
}

func (e *boxEnv) exec(req apigen.OrbBoxExecRequest) execResult {
	e.t.Helper()
	body, err := json.Marshal(req)
	require.NoError(e.t, err)
	resp, b := e.h.do(http.MethodPost, "/box/exec", nil, bytes.NewReader(body), nil)
	require.Equal(e.t, http.StatusOK, resp.StatusCode, string(b))
	assert.Equal(e.t, "application/x-ndjson", resp.Header.Get("Content-Type"))
	var res execResult
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var line struct {
			S, D string
			Exit *int
		}
		require.NoError(e.t, json.Unmarshal(sc.Bytes(), &line), sc.Text())
		switch {
		case line.Exit != nil:
			res.exit = line.Exit
		case line.S == "o":
			res.stdout += line.D
		case line.S == "e":
			res.stderr += line.D
		}
	}
	return res
}

func TestBoxExec(t *testing.T) {
	e := newBoxEnv(t, "")
	require.NoError(t, os.MkdirAll(filepath.Join(e.home, "sub"), 0o755))

	res := e.exec(apigen.OrbBoxExecRequest{Command: "echo out; echo err >&2; pwd; exit 3", Cwd: "~/sub", TimeoutSec: 10})
	require.NotNil(t, res.exit)
	assert.Equal(t, 3, *res.exit)
	assert.Equal(t, "err\n", res.stderr)
	assert.Equal(t, "out\n"+filepath.Join(e.home, "sub")+"\n", res.stdout)

	res = e.exec(apigen.OrbBoxExecRequest{Command: "echo start; exec sleep 30", Cwd: "~", TimeoutSec: 1})
	require.NotNil(t, res.exit)
	assert.Equal(t, boxExecTimeoutExit, *res.exit)
	assert.Equal(t, "start\n", res.stdout)

	for _, cwd := range []string{"/etc", "~/..", "relative"} {
		body, _ := json.Marshal(apigen.OrbBoxExecRequest{Command: "true", Cwd: cwd, TimeoutSec: 10})
		resp, b := e.h.do(http.MethodPost, "/box/exec", nil, bytes.NewReader(body), nil)
		assert.Equal(t, "invalid_path", e.h.errCode(resp, b, http.StatusBadRequest), cwd)
	}
	body, _ := json.Marshal(apigen.OrbBoxExecRequest{Command: "true", Cwd: "~", TimeoutSec: 7200})
	resp, b := e.h.do(http.MethodPost, "/box/exec", nil, bytes.NewReader(body), nil)
	assert.Equal(t, "invalid_request", e.h.errCode(resp, b, http.StatusBadRequest))
}

func TestNdjsonStreamKeepsRunes(t *testing.T) {
	var buf bytes.Buffer
	out := &ndjsonOut{enc: json.NewEncoder(&buf)}
	st := &ndjsonStream{o: out, s: "o"}
	data := []byte("héllo")
	_, _ = st.Write(data[:2]) // 切在 é 中间
	_, _ = st.Write(data[2:])
	st.flush()
	var got string
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		var c boxExecChunk
		require.NoError(t, json.Unmarshal([]byte(line), &c))
		got += c.D
	}
	assert.Equal(t, "héllo", got)
}

// ---- 线程工作目录 ----

func TestFsRootsWorkdirHeader(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	o := newOrbHTTP(t, cp, rt)
	home, err := filepath.EvalSymlinks(rt.home)
	require.NoError(t, err)
	appDir := filepath.Join(rt.home, "apps", "x")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "releases", "r1", "dist"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join("releases", "r1"), filepath.Join(appDir, "current")))
	def := rt.currentWorkdir()

	roots := func(hdr string, strict bool) (*http.Response, []byte) {
		h := map[string]string{}
		if hdr != "" {
			h[WorkdirHeader] = hdr
		}
		var q url.Values
		if strict {
			q = url.Values{"strict": {"1"}}
		}
		return o.do(http.MethodGet, "/fs/roots", q, nil, h)
	}
	rootsOK := func(hdr string, strict bool) apigen.FsRoots {
		t.Helper()
		resp, b := roots(hdr, strict)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
		var r apigen.FsRoots
		require.NoError(t, json.Unmarshal(b, &r))
		return r
	}

	r := rootsOK("", false)
	assert.Equal(t, apigen.FsRoots{Home: home, Workdir: def}, r)
	r = rootsOK("~/apps/x/current", false)
	assert.True(t, strings.HasSuffix(r.Workdir, "/apps/x/current"), r.Workdir)
	assert.Equal(t, home, r.Home)
	assert.Equal(t, r, rootsOK("~/apps/x/current", true), "目录存在时 strict 与不带 strict 相同")
	for _, hdr := range []string{"/etc", "~/apps/missing/current", "~/apps/x/current/version.txt"} {
		assert.Equal(t, def, rootsOK(hdr, false).Workdir, hdr)
	}
	resp, b := roots("~/apps/missing/current", true)
	assert.Equal(t, "workdir_missing", o.errCode(resp, b, http.StatusNotFound))
	assert.Equal(t, def, rootsOK("", true).Workdir)

	// 文件接口按这个工作目录列目录
	resp, b = o.do(http.MethodGet, "/fs/list", url.Values{"path": {"~/apps/x/current"}}, nil, map[string]string{WorkdirHeader: "~/apps/x/current"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(b))
	assert.Contains(t, string(b), `"dist"`)
}
