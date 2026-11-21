package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxes"
	"github.com/Lynricsy/Oryn/internal/config"
)

// M23 §8.1：Boxes 全栈——process 驱动 + 真实 orbd + 真实 `oryn boxes deploy`（orb 内经 orbd.sock 的 box.call）+ fakeapp。
//
// box 的 orbd 与它拉起的 app 都是宿主进程，app 端口（30000 起）在回环上全进程共享，而栈的清理只杀 orbd
// （SIGKILL，app 在自己的进程组里会活下来）。所以每个建 box 的测试经 newM23Stack 在 newStack 之后登记收尾
// （先于栈的清理执行）：还在的 box 停掉、已删的等 orb 到 deleted，再确认 app 端口都已释放。这些测试都不并行。

const m23Domain = "apps.test"

var m23Idle = config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)}

// newM23Stack 起一套启用 portal（app 主机 <app>.apps.test）的栈，并登记 box 收尾。
func newM23Stack(t *testing.T, idle config.Idle) *stack {
	t.Helper()
	s := newStack(t, idle, func(c *config.Orynd) { c.PortalDomain = m23Domain })
	t.Cleanup(func() {
		s.t = t
		s.releaseBoxes()
	})
	return s
}

// releaseBoxes 停掉还在的 box、等已删 box 的 orb 回收完，然后确认 app 端口段上没有监听者。
func (s *stack) releaseBoxes() {
	t := s.t
	ctx := context.Background()
	all := true
	orbs, err := s.srv.Store.Q.ListOrbs(ctx, &all)
	require.NoError(t, err)
	for _, o := range orbs {
		if o.Box != 1 || o.State == "deleted" {
			continue
		}
		done := map[string]bool{"deleted": true}
		if b, err := s.srv.Store.Q.GetBoxByOrb(ctx, o.ID); err == nil {
			if _, err := s.srv.Boxes.Stop(ctx, b.ID, boxes.Actor{Kind: boxes.ActorOwner}); err != nil {
				t.Logf("停止 box %s: %v", b.Name, err)
			}
			done["stopped"], done["failed"] = true, true
		}
		deadline := time.Now().Add(60 * time.Second)
		for {
			cur, err := s.srv.Store.Q.GetOrb(ctx, o.ID)
			require.NoError(t, err)
			if done[cur.State] {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("收尾：box 的 orb %s 停在 %s", o.ID, cur.State)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for port := 30000; port < 30016; port++ {
		for listening(port) {
			if time.Now().After(deadline) {
				t.Errorf("收尾：app 端口 %d 仍有监听者", port)
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// listening 报告回环上的 port 是否有监听者。
func listening(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// ---------------------------------------------------------------- 夹具

// boxSrc 是部署的源头：一个普通线程，它的 orb HOME 里有一个 git 仓库。
type boxSrc struct {
	s      *stack
	thread string
	orb    string
	home   string
	repo   string
}

// boxFixture 建一个普通线程，在它的 orb HOME 里建仓库 ~/repo：source: false、artifacts: [dist]，
// 服务 web 是 dist/fakeapp（cwd dist），dist/version.txt = v1，已提交。
func boxFixture(t *testing.T, s *stack) *boxSrc {
	t.Helper()
	th := s.newThread(map[string]any{"prompt": "hi"})
	home := s.drv.HomeDir(th.OrbID)
	b := &boxSrc{s: s, thread: th.ID, orb: th.OrbID, home: home, repo: filepath.Join(home, "repo")}
	writeTree(t, b.repo, map[string]string{
		".oryn/box.yaml":   "source: false\nartifacts: [dist]\nservices:\n  web:\n    command: ./fakeapp\n    cwd: dist\n",
		"dist/version.txt": "v1",
	})
	copyFakeapp(t, filepath.Join(b.repo, "dist"))
	gitE2E(t, b.repo, "init", "-q", "-b", "main")
	commitAll(t, b.repo, "v1")
	return b
}

// writeTree 在 dir 下写入相对路径 → 内容。
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

// copyFakeapp 把构建出的 fakeapp 拷到 dir/fakeapp（可执行）。
func copyFakeapp(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(binDir, "fakeapp"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fakeapp"), b, 0o755))
}

// commitAll 提交 dir 所在仓库的全部改动（部署的是 HEAD 提交）。
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitE2E(t, dir, "add", "-A")
	gitE2E(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
}

// oryn 以源线程在 orb 里的身份（ORYN_ORB=1、ORYN_THREAD_ID、orb 的 HOME）在 dir 里跑真实的 oryn，返回退出码与合并输出。
func (b *boxSrc) oryn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(binDir, "oryn"), args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + b.home, "PATH=" + os.Getenv("PATH"), "ORYN_ORB=1", "ORYN_THREAD_ID=" + b.thread}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	}
	require.NoError(t, err, "oryn %v: %s", args, out)
	return 0, ""
}

// deploy 在 dir 里跑 `oryn boxes deploy args…`。
func (b *boxSrc) deploy(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	return b.oryn(t, dir, append([]string{"boxes", "deploy"}, args...)...)
}

// mustDeploy 部署并要求成功。
func (b *boxSrc) mustDeploy(t *testing.T, dir string, args ...string) string {
	t.Helper()
	code, out := b.deploy(t, dir, args...)
	require.Equal(t, 0, code, "deploy %v: %s", args, out)
	return out
}

// appGet 不跟随重定向地向 app 主机 <app>.apps.test 发 GET；kv 是附加的请求头。
func (s *stack) appGet(app, path string, kv ...string) (int, http.Header, string) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.http.URL+path, nil)
	require.NoError(s.t, err)
	req.Host = app + "." + m23Domain
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	require.NoError(s.t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(s.t, err)
	return resp.StatusCode, resp.Header, string(body)
}

// ownerGet 带设备令牌对 app 主机 GET，要求 200（服务刚换过进程时短暂不通，至多等 30 秒），返回响应体。
func (s *stack) ownerGet(app, path string) string {
	s.t.Helper()
	var code int
	var body string
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, _, body = s.appGet(app, path, "Authorization", "Bearer "+s.token)
		if code == http.StatusOK || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.Equal(s.t, http.StatusOK, code, "GET %s%s: %s", app, path, body)
	return body
}

// waitAppBody 等带令牌的 GET 回 want（换 release / 重启之后）。
func (s *stack) waitAppBody(app, path, want string) {
	s.t.Helper()
	var got string
	require.Eventually(s.t, func() bool {
		got = s.ownerGet(app, path)
		return got == want
	}, 60*time.Second, 200*time.Millisecond, "GET %s%s 期望 %q", app, path, want)
}

func (s *stack) box(ref string) apigen.Box {
	s.t.Helper()
	var b apigen.Box
	s.rest("GET", "/api/v1/boxes/"+ref, nil, &b, 200)
	return b
}

// waitBox 等 box 进入 state。
func (s *stack) waitBox(ref string, state apigen.BoxState) apigen.Box {
	s.t.Helper()
	var b apigen.Box
	deadline := time.Now().Add(90 * time.Second)
	for {
		b = s.box(ref)
		if b.State == state {
			return b
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("box %s 没有进入 %s（当前 %s）", ref, state, b.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// boxApp 返回 box 上名为 app 的 app；不存在时测试失败。
func (s *stack) boxApp(ref, app string) apigen.BoxApp {
	s.t.Helper()
	for _, a := range s.box(ref).Apps {
		if a.Name == app {
			return a
		}
	}
	s.t.Fatalf("box %s 上没有 app %s", ref, app)
	return apigen.BoxApp{}
}

func (s *stack) createBox(name string) apigen.Box {
	s.t.Helper()
	var b apigen.Box
	s.rest("POST", "/api/v1/boxes", map[string]any{"name": name}, &b, 201)
	return s.waitBox(b.ID, apigen.BoxStateRunning)
}

// boxThread 用 box 执行位置建线程（app 为空 = 机器），返回线程 id 与 cwd。
func (s *stack) boxThread(box, app string) (string, string) {
	s.t.Helper()
	ex := map[string]any{"kind": "box", "boxId": box}
	if app != "" {
		ex["app"] = app
	}
	var created struct {
		Thread struct {
			ID  string  `json:"id"`
			Cwd *string `json:"cwd"`
		} `json:"thread"`
	}
	s.rest("POST", "/api/v1/threads", map[string]any{"prompt": "hi", "executor": ex}, &created, 201)
	cwd := ""
	if created.Thread.Cwd != nil {
		cwd = *created.Thread.Cwd
	}
	return created.Thread.ID, cwd
}

// restErr 发请求，要求状态码 want，返回错误码。
func (s *stack) restErr(method, path string, body any, want int) string {
	s.t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	s.rest(method, path, body, &e, want)
	return e.Code
}

// countEvents 数 orb 事件里 from>to 的条数（from 为空 = 任意来源）。
func (s *stack) countEvents(orbID, from, to string) int {
	s.t.Helper()
	n := 0
	for _, e := range s.orbEvents(orbID) {
		f, tt, _ := strings.Cut(e, ">")
		if tt == to && (from == "" || f == from) {
			n++
		}
	}
	return n
}

// orbdPID 扫 /proc 找跑着 orbID 的 `orbd run` 进程；找不到时返回 0。
func orbdPID(orbID string) int {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(args) != 2 || filepath.Base(args[0]) != "orbd" || args[1] != "run" {
			continue
		}
		env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue
		}
		for kv := range strings.SplitSeq(string(env), "\x00") {
			if kv == "ORYN_ORB_ID="+orbID {
				return pid
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------- TestE2EM23

type m23Env struct {
	*stack
	src     *boxSrc
	dump    string // fakeomp 的转储目录
	boxID   string
	boxOrb  string
	boxHome string
	port    int    // hello 的端口
	v1, v2  string // R2 / R4 的 release id
	thread  string // R9 开在 hello 上的线程
}

func TestE2EM23(t *testing.T) {
	dump := t.TempDir()
	t.Setenv("FAKEOMP_DUMP_DIR", dump)
	s := newM23Stack(t, m23Idle)
	e := &m23Env{stack: s, dump: dump}
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"R1建box", e.stepCreate},
		{"R2首次部署", e.stepFirstDeploy},
		{"R3匿名访问", e.stepAnon},
		{"R4私有", e.stepPrivate},
		{"R5失败回滚", e.stepRefuse},
		{"R6保留三个release", e.stepPrune},
		{"R7密钥与重启", e.stepSecret},
		{"R8回滚", e.stepRollback},
		{"R9box上的线程", e.stepThread},
		{"R10停机", e.stepStop},
		{"R11启动", e.stepStart},
		{"R12exec与cp", e.stepExecCp},
		{"R13预览", e.stepPreview},
		{"R14删除", e.stepDelete},
	}
	for _, st := range steps {
		ok := t.Run(st.name, func(sub *testing.T) {
			e.stack.t = sub
			defer func() { e.stack.t = t }()
			st.fn(sub)
		})
		if !ok {
			t.Fatalf("%s 失败，后续步骤依赖它，停止", st.name)
		}
	}
}

// R1：建 box，等到 running；orb 行带 box 标记。
func (e *m23Env) stepCreate(t *testing.T) {
	b := e.createBox("b1")
	e.boxID, e.boxOrb = b.ID, string(b.OrbID)
	e.boxHome = e.drv.HomeDir(e.boxOrb)
	o, err := e.srv.Store.Q.GetOrb(context.Background(), e.boxOrb)
	require.NoError(t, err)
	assert.EqualValues(t, 1, o.Box)
	e.src = boxFixture(t, e.stack)
}

// R2：v1 首次部署成功；仓库里记下部署目标；部署行 succeeded。
func (e *m23Env) stepFirstDeploy(t *testing.T) {
	out := e.src.mustDeploy(t, e.src.repo, "--on", "b1", "--name", "hello")
	assert.Contains(t, out, "deployed hello to b1")
	assert.FileExists(t, filepath.Join(e.src.repo, ".oryn", "box-deploy.json"))
	rows, err := e.srv.Store.Q.ListBoxDeploysByState(context.Background(), "succeeded")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "hello", rows[0].App)
	app := e.boxApp("b1", "hello")
	require.NotNil(t, app.Live)
	e.v1, e.port = app.Live.ID, app.Port
	assert.True(t, app.Public)
}

// R3：匿名访问公开 app：200 v1，保留 app 自己的 X-Frame-Options。
func (e *m23Env) stepAnon(t *testing.T) {
	code, hdr, body := e.appGet("hello", "/")
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "v1", body)
	assert.Equal(t, "DENY", hdr.Get("X-Frame-Options"))
}

// R4：v2 以 --private 部署：匿名导航被带去登录、非导航 401，设备令牌 200 v2。
func (e *m23Env) stepPrivate(t *testing.T) {
	writeTree(t, e.src.repo, map[string]string{"dist/version.txt": "v2"})
	commitAll(t, e.src.repo, "v2")
	e.src.mustDeploy(t, e.src.repo, "--private")
	code, hdr, _ := e.appGet("hello", "/", "Sec-Fetch-Mode", "navigate")
	require.Equal(t, http.StatusFound, code)
	assert.Contains(t, hdr.Get("Location"), "/portal/auth?h=hello")
	code, _, _ = e.appGet("hello", "/")
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "v2", e.ownerGet("hello", "/"))
	app := e.boxApp("b1", "hello")
	assert.False(t, app.Public)
	require.NotNil(t, app.Live)
	e.v2 = app.Live.ID
}

// R5：起不来的 release：部署失败（not_listening），线上仍是 v2，没有半截部署。
func (e *m23Env) stepRefuse(t *testing.T) {
	writeTree(t, e.src.repo, map[string]string{"dist/refuse": "x"})
	commitAll(t, e.src.repo, "refuse")
	code, out := e.src.deploy(t, e.src.repo)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "not_listening")
	assert.Equal(t, "v2", e.ownerGet("hello", "/"))
	var rl apigen.BoxReleaseList
	e.rest("GET", "/api/v1/boxes/b1/apps/hello/releases", nil, &rl, 200)
	live := ""
	for _, r := range rl.Releases {
		if r.State == apigen.BoxReleaseLive {
			live = r.ID
		}
	}
	assert.Equal(t, e.v2, live)
	assert.False(t, e.boxApp("b1", "hello").Deploying)
}

// R6：再部署 v3、v4：box 上恰好留 3 个 release 目录，最旧的 v1 记为 pruned。
func (e *m23Env) stepPrune(t *testing.T) {
	require.NoError(t, os.Remove(filepath.Join(e.src.repo, "dist", "refuse")))
	for _, v := range []string{"v3", "v4"} {
		writeTree(t, e.src.repo, map[string]string{"dist/version.txt": v})
		commitAll(t, e.src.repo, v)
		e.src.mustDeploy(t, e.src.repo)
		assert.Equal(t, v, e.ownerGet("hello", "/"))
	}
	ents, err := os.ReadDir(filepath.Join(e.boxHome, "apps", "hello", "releases"))
	require.NoError(t, err)
	var names []string
	for _, d := range ents {
		names = append(names, d.Name())
	}
	assert.Len(t, names, 3, "%v", names)
	assert.NotContains(t, names, e.v1)
	var rl apigen.BoxReleaseList
	e.rest("GET", "/api/v1/boxes/b1/apps/hello/releases", nil, &rl, 200)
	states := map[string]apigen.BoxReleaseState{}
	for _, r := range rl.Releases {
		states[r.ID] = r.State
	}
	assert.Equal(t, apigen.BoxReleasePruned, states[e.v1])
}

// R7：设密钥后重启：进程环境里有它，数据目录存在。
func (e *m23Env) stepSecret(t *testing.T) {
	e.rest("PUT", "/api/v1/boxes/b1/apps/hello/secrets/GREETING", map[string]any{"value": "hi"}, nil, 204)
	e.rest("POST", "/api/v1/boxes/b1/apps/hello/restart", nil, nil, 200)
	e.waitAppBody("hello", "/env?k=GREETING", "hi")
	assert.Equal(t, "yes", e.ownerGet("hello", "/data"))
}

// R8：回滚到上一个 release：v3。
func (e *m23Env) stepRollback(t *testing.T) {
	e.rest("POST", "/api/v1/boxes/b1/apps/hello/rollback", map[string]any{}, nil, 200)
	assert.Equal(t, "v3", e.ownerGet("hello", "/"))
}

// R9：开在 hello 上的线程：agent 的 cwd 与文件面板的根都在 app 目录；executor.kind = orb 指向 box 的 orb 被拒。
func (e *m23Env) stepThread(t *testing.T) {
	id, cwd := e.boxThread("b1", "hello")
	e.thread = id
	assert.Equal(t, "~/apps/hello/current", cwd)
	e.waitThread(id, 60*time.Second, func(v threadView) bool { return v.State == "idle" })

	raw, err := os.ReadFile(filepath.Join(e.dump, id+".json"))
	require.NoError(t, err)
	var rec struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal(raw, &rec))
	got, err := filepath.EvalSymlinks(rec.Cwd)
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(filepath.Join(e.boxHome, "apps", "hello", "current"))
	require.NoError(t, err)
	assert.Equal(t, want, got)

	var roots apigen.FsRoots
	e.rest("GET", "/api/v1/orbs/"+e.boxOrb+"/fs/roots?thread="+id, nil, &roots, 200)
	assert.True(t, strings.HasSuffix(roots.Workdir, "/apps/hello/current"), roots.Workdir)
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	e.rest("GET", "/api/v1/orbs/"+e.boxOrb+"/fs/list?thread="+id+"&path="+url.QueryEscape(roots.Workdir), nil, &listing, 200)
	var names []string
	for _, en := range listing.Entries {
		names = append(names, en.Name)
	}
	assert.Contains(t, names, "dist")

	code := e.restErr("POST", "/api/v1/threads", map[string]any{
		"prompt": "hi", "executor": map[string]any{"kind": "orb", "orbId": e.boxOrb},
	}, http.StatusConflict)
	assert.Equal(t, "orb_is_box", code)
}

// R10：停机：app 503 且不唤醒；线程的消息、wake、在 box 上建线程都同步回 box_stopped。
func (e *m23Env) stepStop(t *testing.T) {
	e.rest("POST", "/api/v1/boxes/b1/stop", nil, nil, 202)
	e.waitBox("b1", apigen.BoxStateStopped)
	e.waitOrbState(e.boxOrb, "stopped", 30*time.Second)
	wakes := e.countEvents(e.boxOrb, "", "waking")

	code, _, body := e.appGet("hello", "/", "Authorization", "Bearer "+e.token)
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.Contains(t, body, "app_unavailable")

	a := e.dial()
	a.send(map[string]any{"type": "cmd", "threadId": e.thread, "id": "s1", "frame": map[string]any{"type": "prompt", "message": "while stopped"}})
	a.until(10*time.Second, func(m wsMsg) bool {
		if m.Type == "res" && m.ThreadID == e.thread {
			t.Fatalf("停机的 box 上的线程受理了命令")
		}
		return m.Type == "error" && m.ThreadID == e.thread && m.Code == "box_stopped"
	})

	assert.Equal(t, "box_stopped", e.restErr("POST", "/api/v1/threads/"+e.thread+"/wake", nil, http.StatusConflict))
	assert.Equal(t, "box_stopped", e.restErr("POST", "/api/v1/threads", map[string]any{
		"prompt": "hi", "executor": map[string]any{"kind": "box", "boxId": "b1", "app": "hello"},
	}, http.StatusConflict))

	time.Sleep(time.Second)
	assert.Equal(t, wakes, e.countEvents(e.boxOrb, "", "waking"), "停机期间 orb 被唤醒")
	for _, txt := range e.userTexts(e.thread) {
		assert.NotContains(t, txt, "while stopped", "被拒的消息进了线程")
	}
	assert.Equal(t, apigen.OrbState("stopped"), e.orb(e.boxOrb).State)
}

// R11：启动：服务随 resume 拉起，线上仍是 v3。
func (e *m23Env) stepStart(t *testing.T) {
	e.rest("POST", "/api/v1/boxes/b1/start", nil, nil, 202)
	e.waitBox("b1", apigen.BoxStateRunning)
	e.waitAppBody("hello", "/", "v3")
}

// R12：orb 里的 exec 与 cp。
func (e *m23Env) stepExecCp(t *testing.T) {
	code, out := e.src.oryn(t, e.src.repo, "boxes", "exec", "b1", "--app", "hello", "--", "cat", "dist/version.txt")
	require.Equal(t, 0, code, out)
	assert.Equal(t, "v3", strings.TrimSpace(out))
	writeTree(t, e.src.repo, map[string]string{"note.txt": "a note"})
	code, out = e.src.oryn(t, e.src.repo, "boxes", "cp", "./note.txt", "b1:note.txt", "--app", "hello")
	require.Equal(t, 0, code, out)
	b, err := os.ReadFile(filepath.Join(e.boxHome, "apps", "hello", "data", "note.txt"))
	require.NoError(t, err)
	assert.Equal(t, "a note", string(b))
}

// R13：带 oryn-preview 标签的 release：部署后 app 的 preview 变为 true。
func (e *m23Env) stepPreview(t *testing.T) {
	writeTree(t, e.src.repo, map[string]string{
		"dist/preview.html": `<!doctype html><html><head><meta name="oryn-preview" content="v1; enabled; iframe"><title>hello</title></head><body>preview</body></html>`,
	})
	commitAll(t, e.src.repo, "preview")
	e.src.mustDeploy(t, e.src.repo)
	require.Eventually(t, func() bool { return e.boxApp("b1", "hello").Preview }, 60*time.Second, 200*time.Millisecond, "preview 变为 true")
}

// R14：box 的 orb 不能经 orbs 接口删；删 box 之后列表为空、app 主机 404、orb 回收完、端口释放。
func (e *m23Env) stepDelete(t *testing.T) {
	assert.Equal(t, "orb_is_box", e.restErr("DELETE", "/api/v1/orbs/"+e.boxOrb, nil, http.StatusConflict))
	e.rest("DELETE", "/api/v1/boxes/b1", nil, nil, 204)
	var list apigen.BoxList
	e.rest("GET", "/api/v1/boxes", nil, &list, 200)
	assert.Empty(t, list.Boxes)
	code, _, _ := e.appGet("hello", "/")
	assert.Equal(t, http.StatusNotFound, code)
	e.waitOrbState(e.boxOrb, "deleted", 60*time.Second)
	require.Eventually(t, func() bool { return !listening(e.port) }, 30*time.Second, 100*time.Millisecond, "端口 %d 释放", e.port)
}

// ---------------------------------------------------------------- 常驻

// box 的 orb 不冻结、不随线程回收：空闲阈值 1 秒下，对照的普通 orb 冻结了，box 的 orb 删掉最后一个线程后仍是 ready。
func TestE2EM23AlwaysOn(t *testing.T) {
	evalInterval = 200 * time.Millisecond
	t.Cleanup(func() { evalInterval = 0 })
	s := newM23Stack(t, config.Idle{AgentIdle: config.Duration(time.Second), UserIdle: config.Duration(time.Second), StopAfterFrozen: config.Duration(24 * time.Hour)})
	b := s.createBox("always")
	boxOrb := string(b.OrbID)

	id, cwd := s.boxThread("always", "")
	assert.Equal(t, "~", cwd)
	s.waitThread(id, 60*time.Second, func(v threadView) bool { return v.State == "idle" })
	s.rest("DELETE", "/api/v1/threads/"+id, nil, nil, 204)
	since := time.Now()

	plain := s.newThread(map[string]any{"prompt": "hi"})
	s.waitOrbState(plain.OrbID, "frozen", 30*time.Second)
	if d := 5*time.Second - time.Since(since); d > 0 {
		time.Sleep(d)
	}
	assert.Equal(t, apigen.OrbState("ready"), s.orb(boxOrb).State)
	for _, ev := range s.orbEvents(boxOrb) {
		assert.NotContains(t, []string{"freezing", "deleting"}, ev[strings.Index(ev, ">")+1:], "box 的 orb 事件 %s", ev)
	}
	assert.Equal(t, apigen.BoxStateRunning, s.box("always").State)
}

// ---------------------------------------------------------------- 掉线恢复

// box 的 orbd 退出（容器退出、宿主重启）后，对账把 ready 却没有 link 的 orb 送回 waking：起的是新的 orbd，app 服务是新进程。
func TestE2EM23Revive(t *testing.T) {
	prevTick, prevAfter := boxesTick, boxesUnlinkedAfter
	boxesTick, boxesUnlinkedAfter = 200*time.Millisecond, time.Second
	t.Cleanup(func() { boxesTick, boxesUnlinkedAfter = prevTick, prevAfter })
	s := newM23Stack(t, m23Idle)
	b := s.createBox("rv")
	boxOrb := string(b.OrbID)
	src := boxFixture(t, s)
	src.mustDeploy(t, src.repo, "--on", "rv", "--name", "hello")
	code, _, pid1 := s.appGet("hello", "/pid")
	require.Equal(t, http.StatusOK, code, pid1)

	orbd1 := orbdPID(boxOrb)
	require.NotZero(t, orbd1, "找到 box 的 orbd")
	// SIGTERM：orbd 正常退出会停掉它的服务（SIGKILL 会留下 app 的进程组继续监听，后面的 GET 就证明不了什么）
	require.NoError(t, syscall.Kill(orbd1, syscall.SIGTERM))

	var revived bool
	require.Eventually(t, func() bool {
		evs, err := s.srv.Store.Q.ListOrbEvents(context.Background(), boxOrb)
		require.NoError(t, err)
		for _, ev := range evs {
			if ev.FromState == "ready" && ev.ToState == "waking" && strings.Contains(ev.Detail, "link lost") {
				revived = true
			}
		}
		return revived
	}, 30*time.Second, 100*time.Millisecond, "ready → waking（link lost）")
	s.waitOrbState(boxOrb, "ready", 60*time.Second)
	s.waitBox("rv", apigen.BoxStateRunning)
	orbd2 := orbdPID(boxOrb)
	assert.NotZero(t, orbd2)
	assert.NotEqual(t, orbd1, orbd2)

	var pid2 string
	require.Eventually(t, func() bool {
		c, _, body := s.appGet("hello", "/pid")
		pid2 = body
		return c == http.StatusOK
	}, 60*time.Second, 200*time.Millisecond, "app 恢复")
	assert.NotEqual(t, pid1, pid2)
	code, _, body := s.appGet("hello", "/")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "v1", body)
}

// ---------------------------------------------------------------- 带源码的 release 与单仓多 app

func TestE2EM23Source(t *testing.T) {
	s := newM23Stack(t, m23Idle)
	src := boxFixture(t, s) // 只借它的线程与 orb；仓库另建
	mono := filepath.Join(src.home, "mono")
	svc := "services:\n  web:\n    command: ./fakeapp\n"
	writeTree(t, mono, map[string]string{
		"README.md":             "mono\n",
		"crm/.oryn/box.yaml":    "include: [\"../shared\"]\n" + svc,
		"crm/version.txt":       "crm1",
		"status/.oryn/box.yaml": svc,
		"status/version.txt":    "status1",
		"shared/lib.txt":        "shared\n",
	})
	copyFakeapp(t, filepath.Join(mono, "crm"))
	copyFakeapp(t, filepath.Join(mono, "status"))
	gitE2E(t, mono, "init", "-q", "-b", "main")
	commitAll(t, mono, "mono")
	crmDir, statusDir := filepath.Join(mono, "crm"), filepath.Join(mono, "status")

	// 两个 app 各自部署（第一次顺带 --create 建 box）
	src.mustDeploy(t, crmDir, "--on", "mono", "--name", "crm", "--create")
	src.mustDeploy(t, statusDir, "--on", "mono", "--name", "status")
	b := s.waitBox("mono", apigen.BoxStateRunning)
	boxHome := s.drv.HomeDir(string(b.OrbID))
	assert.Equal(t, "crm1", s.ownerGet("crm", "/"))
	assert.Equal(t, "status1", s.ownerGet("status", "/"))
	var targets struct {
		Targets map[string]struct {
			On, Name, Root string
		} `json:"targets"`
	}
	raw, err := os.ReadFile(filepath.Join(mono, ".oryn", "box-deploy.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &targets))
	assert.Len(t, targets.Targets, 2)
	assert.Equal(t, "crm", targets.Targets["crm"].Root)
	assert.Equal(t, "status", targets.Targets["status"].Root)

	// crm 的 release：只检出 crm、根目录文件与 include，工作区干净
	crmCur := filepath.Join(boxHome, "apps", "crm", "current")
	assert.NoDirExists(t, filepath.Join(crmCur, "status"))
	assert.DirExists(t, filepath.Join(crmCur, "shared"))
	assert.FileExists(t, filepath.Join(crmCur, "README.md"))
	assert.Empty(t, gitE2E(t, crmCur, "status", "--porcelain"))
	origRepo := s.boxApp("mono", "crm").Repo
	assert.True(t, strings.HasPrefix(origRepo, "local:"), origRepo)

	// 开在 crm 上的线程落在 app 目录
	_, cwd := s.boxThread("mono", "crm")
	assert.True(t, strings.HasSuffix(cwd, "/apps/crm/current/crm"), cwd)

	// box 上改了被跟踪的文件：再部署被拒，--discard-box-changes 通过
	require.NoError(t, os.WriteFile(filepath.Join(crmCur, "crm", "version.txt"), []byte("edited on box"), 0o644))
	code, out := src.deploy(t, crmDir)
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "box_changes")
	src.mustDeploy(t, crmDir, "--discard-box-changes")
	assert.Equal(t, "crm1", s.ownerGet("crm", "/"))

	// 换仓库：另一个历史不相干的仓库，同名 app
	other := filepath.Join(src.home, "other")
	writeTree(t, other, map[string]string{".oryn/box.yaml": svc, "version.txt": "other1"})
	copyFakeapp(t, other)
	gitE2E(t, other, "init", "-q", "-b", "main")
	commitAll(t, other, "other")

	code, out = src.deploy(t, other, "--on", "mono", "--name", "crm")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "app_repo_mismatch")

	code, out = src.deploy(t, other, "--on", "mono", "--name", "crm", "--move")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "box_changes")
	assert.Contains(t, out, "add --discard-box-changes")

	writeTree(t, other, map[string]string{"refuse": "x"})
	commitAll(t, other, "refuse")
	code, out = src.deploy(t, other, "--on", "mono", "--name", "crm", "--move", "--discard-box-changes")
	assert.Equal(t, 1, code, out)
	assert.Contains(t, out, "not_listening")
	app := s.boxApp("mono", "crm")
	assert.Equal(t, origRepo, app.Repo)
	assert.Equal(t, "crm", app.Root)
	_, cwd = s.boxThread("mono", "crm")
	assert.True(t, strings.HasSuffix(cwd, "/apps/crm/current/crm"), cwd)

	require.NoError(t, os.Remove(filepath.Join(other, "refuse")))
	commitAll(t, other, "unrefuse")
	src.mustDeploy(t, other, "--on", "mono", "--name", "crm", "--move", "--discard-box-changes")
	app = s.boxApp("mono", "crm")
	assert.NotEqual(t, origRepo, app.Repo)
	assert.Empty(t, app.Root)
	assert.Equal(t, "other1", s.ownerGet("crm", "/"))

	// 回滚到换仓库之前：绑定跟着回去，从原仓库部署不需要 --move
	s.rest("POST", "/api/v1/boxes/mono/apps/crm/rollback", map[string]any{}, nil, 200)
	app = s.boxApp("mono", "crm")
	assert.Equal(t, origRepo, app.Repo)
	assert.Equal(t, "crm", app.Root)
	s.waitAppBody("crm", "/", "crm1")
	src.mustDeploy(t, crmDir)
	assert.Equal(t, "crm1", s.ownerGet("crm", "/"))
}
