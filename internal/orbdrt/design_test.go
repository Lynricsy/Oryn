package orbdrt

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/artifacts"
	"github.com/Lynricsy/Oryn/internal/artifacts/canvas"
	"github.com/Lynricsy/Oryn/internal/artifacts/design"
	"github.com/Lynricsy/Oryn/internal/designengine"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/webui"
)

// fakeVite 顶替引擎的 vite：`build` 时按 src/frames/*.tsx 写出 dist/<id>.html 与 dist/frames.json（src/FAIL 存在时往
// stderr 写一大段再失败）；否则在 PORT 上起 dev 服务：`/frames/<id>` 回帧源码原文，其余路径回显工作副本与 sandbox 目录。
const fakeVite = `const fs = require("fs"), path = require("path"), http = require("http");
const work = process.env.ORYN_DESIGN_DIR;
if (process.argv.includes("build")) {
  if (fs.existsSync(path.join(work, "src", "FAIL"))) {
    process.stderr.write("x".repeat(6000) + "\nBUILD-BROKE: Unexpected token in src/frames/a.tsx\n");
    process.exit(1);
  }
  const dist = path.join(work, "dist");
  fs.rmSync(dist, { recursive: true, force: true });
  fs.mkdirSync(dist, { recursive: true });
  let ids = [];
  try {
    ids = fs.readdirSync(path.join(work, "src", "frames")).filter((n) => n.endsWith(".tsx") && !n.startsWith("_")).map((n) => n.slice(0, -4)).sort();
  } catch {}
  for (const id of ids) {
    const src = fs.readFileSync(path.join(work, "src", "frames", id + ".tsx"), "utf8");
    fs.writeFileSync(path.join(dist, id + ".html"), "<!doctype html><title>" + id + "</title>" + src);
  }
  fs.writeFileSync(path.join(dist, "frames.json"), JSON.stringify(ids.map((id) => ({ id, name: id.toUpperCase(), width: 800, height: 600, device: "none", links: [] }))));
  process.exit(0);
}
http.createServer((q, s) => {
  const m = /^\/frames\/([a-z0-9-]+)$/.exec(q.url.split("?")[0]);
  if (m) {
    try { return s.end(fs.readFileSync(path.join(work, "src", "frames", m[1] + ".tsx"))); } catch { s.statusCode = 404; return s.end(); }
  }
  s.end("dev " + work + " " + process.env.ORYN_DESIGN_SANDBOX);
}).listen(Number(process.env.PORT), "127.0.0.1");
`

// designEnv 是 orbd + 一个跑真实 artifacts.Service 的假控制面：ctl artifact.call 交给 Service 的 orb 方法，source 经
// library.fetch 从 orbd 取回。
type designEnv struct {
	*svcEnv
	s     *artifacts.Service
	store *store.Store
	sock  string
	// failHead 为真时假控制面对 design_head 回错误（模拟控制面不可达）。
	failHead atomic.Bool
	// exportOverride 非空时拦截 export_design_project 调用（测试 sha256 篡改与恶意 zip）。
	exportOverride atomic.Pointer[func(params json.RawMessage) (any, bool, error)]
}

// cpFetch 经 ctl library.fetch 从 orbd 取回 source（控制面的 SourceFetcher）。
type cpFetch struct {
	cp  *fakeCP
	ctl func() *link.Ctl
	n   atomic.Int64
}

func (f *cpFetch) FetchLimit(ctx context.Context, _, path string, _ int64) (io.ReadCloser, int64, error) {
	var res apigen.LinkCtlLibraryFetchResult
	id := fmt.Sprintf("dsn-%d", f.n.Add(1))
	if err := f.ctl().Call(ctx, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: id, Path: path}, &res); err != nil {
		return nil, 0, err
	}
	b, err := io.ReadAll(<-f.cp.libs)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(b)), res.Size, nil
}

type linksMap map[string]orbs.RequestFunc

func (l linksMap) HandleRequest(method string, fn orbs.RequestFunc) { l[method] = fn }

func newDesignEnv(t *testing.T) *designEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s, err := artifacts.New(artifacts.Config{DataDir: dir, PublicURL: "http://localhost:7600"}, st, nil)
	require.NoError(t, err)
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: "prov_1", Name: "local", TokenHash: "h", CreatedAt: 1}))
	require.NoError(t, st.Q.CreateOrb(ctx, db.CreateOrbParams{ID: "orb-test", ProviderID: "prov_1", Type: "small", State: orbs.StateReady, StateChangedAt: 1, TokenHash: "h-orb", CreatedAt: 1}))
	require.NoError(t, st.Q.CreateThread(ctx, db.CreateThreadParams{Kind: "work", ID: testThread, TitleSource: "auto", State: "idle", SessionDir: "~/.oryn/threads/x"}))
	orbID := "orb-test"
	require.NoError(t, st.Q.SetThreadOrb(ctx, db.SetThreadOrbParams{ID: testThread, OrbID: &orbID}))

	cp := newFakeCP(t)
	fetch := &cpFetch{cp: cp}
	fetch.ctl = func() *link.Ctl {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return cp.ctl
	}
	links := linksMap{}
	s.RegisterOrbMethod(links, fetch)
	call := links[string(apigen.MethodArtifactCall)]
	e := &designEnv{s: s, store: st}
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodArtifactCall) {
			return nil, false, nil
		}
		if e.failHead.Load() && strings.Contains(string(params), `"op":"design_head"`) {
			return nil, true, errors.New("control plane unreachable")
		}
		if fn := e.exportOverride.Load(); fn != nil && strings.Contains(string(params), `"op":"export_design_project"`) {
			if res, handled, err := (*fn)(params); handled {
				return res, true, err
			}
		}
		res, err := call(context.Background(), orbID, params)
		return res, true, err
	}

	e.svcEnv = newSvcEnv(t, cp, "")
	// 镜像预装目录：<root>/<引擎版本>/node_modules/vite/bin/vite.js 是假 vite
	share := t.TempDir()
	vite := filepath.Join(share, designengine.Version, "node_modules", "vite", "bin", "vite.js")
	require.NoError(t, os.MkdirAll(filepath.Dir(vite), 0o755))
	require.NoError(t, os.WriteFile(vite, []byte(fakeVite), 0o644))
	e.rt.design.shareRoot = share
	e.setup()
	e.sock = orbsock.Path(e.home)
	waitSock(t, e.sock)
	return e
}

func (e *designEnv) call(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	raw, err := orbsock.Call(ctx, e.sock, method, params, nil)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (e *designEnv) open(p orbsock.DesignOpenParams) orbsock.DesignOpenResult {
	e.t.Helper()
	var res orbsock.DesignOpenResult
	require.NoError(e.t, e.call(orbsock.MethodDesignOpen, p, &res))
	return res
}

func (e *designEnv) seal(turn string) (orbsock.DesignSealResult, error) {
	var res orbsock.DesignSealResult
	err := e.call(orbsock.MethodDesignSeal, orbsock.DesignSealParams{ThreadID: testThread, TurnID: turn}, &res)
	return res, err
}

func (e *designEnv) head(id string) artifacts.Artifact {
	e.t.Helper()
	a, err := e.s.Get(context.Background(), id)
	require.NoError(e.t, err)
	return a
}

func writeSrc(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func readVersion(t *testing.T, s *artifacts.Service, id string, version int, path string) string {
	t.Helper()
	b, _, err := s.ReadFile(context.Background(), id, version, path)
	require.NoError(t, err)
	return string(b)
}

// M29 T4：design.open 新建空设计画布、写出引擎（node_modules 软链到预装目录）与 sandbox、起 dev 服务（dsn-<hash>，
// 注入 ORYN_DESIGN_DIR / ORYN_DESIGN_SANDBOX）；改源码后 design.seal 构建并封存成本回合的版本（帧对齐成画框）；
// 源码没变则 skip；构建失败回 build_failed 带 stderr 尾、不落版本；design.turn 先以上一回合封存遗留的脏副本；
// 控制面的 conflict 原样回来，之后再 open 保留脏副本；干净而落后的副本重新物化；design.status 报 dirty 与 check。
func TestDesignOpenSeal(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()

	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread, Title: "登录流程"})
	id := res.ID
	assert.Equal(t, filepath.Join(e.home, "designs", id), res.Dir)
	assert.Equal(t, 1, res.Version)
	a := e.head(id)
	assert.Equal(t, "登录流程", a.Title)
	assert.Equal(t, artifacts.KindCanvas, a.Kind)
	doc, err := canvas.Parse([]byte(readVersion(t, e.s, id, 1, artifacts.EntryCanvas)))
	require.NoError(t, err)
	require.NotNil(t, doc.Design)
	assert.Empty(t, doc.Pages)

	engine := filepath.Join(e.home, ".oryn", "design", "engine", designengine.Version)
	assert.FileExists(t, filepath.Join(engine, "vite.config.mjs"))
	link, err := os.Readlink(filepath.Join(engine, "node_modules"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(e.rt.design.shareRoot, designengine.Version, "node_modules"), link)
	sandbox := filepath.Join(e.home, ".oryn", "design", "sandbox")
	assert.DirExists(t, sandbox)

	name := designServiceName(id)
	assert.Regexp(t, `^dsn-[0-9a-f]{10}$`, name)
	svc := e.rt.svc.snapshot(name)
	assert.Equal(t, apigen.OrbServiceStatusRunning, svc.Status)
	require.NotNil(t, svc.Port)
	assert.Equal(t, designDevURL(*svc.Port), res.DevURL)
	body := httpGet(t, res.DevURL)
	assert.Equal(t, "dev "+res.Dir+" "+sandbox, body, "dev 服务拿到工作副本与 sandbox 目录")
	meta, err := readDesignMeta(res.Dir)
	require.NoError(t, err)
	assert.Equal(t, designMeta{ArtifactID: id, ThreadID: testThread, BaseVersion: 1, Engine: designengine.Version,
		SealedHash: meta.SealedHash, Service: name, Port: *svc.Port}, meta)

	// 封存：两帧 → v2，记在回合下
	require.NoError(t, e.call(orbsock.MethodDesignTurn, orbsock.DesignTurnParams{ThreadID: testThread, TurnID: "turn-1", Title: "做登录与注册"}, nil))
	writeSrc(t, res.Dir, "src/frames/login.tsx", "export default () => <p>login</p>")
	writeSrc(t, res.Dir, "src/frames/signup.tsx", "export default () => <p>signup</p>")
	writeSrc(t, res.Dir, "src/node_modules/dep/index.js", "不同步")
	var status orbsock.DesignStatusResult
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &status))
	require.Len(t, status.Designs, 1)
	assert.True(t, status.Designs[0].Dirty)
	assert.Nil(t, status.Designs[0].Check)

	sealed, err := e.seal("turn-1")
	require.NoError(t, err)
	assert.Equal(t, []orbsock.DesignSealed{{ID: id, Version: 2}}, sealed.Sealed)
	assert.Empty(t, sealed.Skipped)
	doc, err = canvas.Parse([]byte(readVersion(t, e.s, id, 2, artifacts.EntryCanvas)))
	require.NoError(t, err)
	require.Len(t, doc.Pages, 2)
	login, ok := doc.PageByID("login")
	require.True(t, ok)
	assert.Equal(t, "LOGIN", login.Name)
	assert.Equal(t, "dist/login.html", login.File)
	assert.Contains(t, readVersion(t, e.s, id, 2, "dist/login.html"), "<p>login</p>")
	v, err := e.s.Version(ctx, id, 2)
	require.NoError(t, err)
	assert.Equal(t, "turn-1", v.TurnID)
	assert.Equal(t, "做登录与注册", v.Title)
	files, err := e.s.Files(ctx, id, 2)
	require.NoError(t, err)
	for _, f := range files {
		assert.NotContains(t, f.Path, "node_modules")
	}
	meta, err = readDesignMeta(res.Dir)
	require.NoError(t, err)
	assert.Equal(t, 2, meta.BaseVersion)

	// 没变：skip
	sealed, err = e.seal("turn-1")
	require.NoError(t, err)
	assert.Empty(t, sealed.Sealed)
	assert.Equal(t, []string{id}, sealed.Skipped)

	// 构建失败：build_failed，消息是 stderr 末尾 4 KiB，不落版本
	writeSrc(t, res.Dir, "src/FAIL", "1")
	_, err = e.seal("turn-1")
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeBuildFailed, se.Code)
	assert.Contains(t, se.Message, "BUILD-BROKE: Unexpected token")
	assert.Less(t, len(se.Message), 4<<10+300, "只带 stderr 尾")
	assert.Equal(t, 2, e.head(id).Version)
	require.NoError(t, os.Remove(filepath.Join(res.Dir, "src", "FAIL")))

	// 新回合开始时先以上一回合封存遗留的脏副本：删掉 signup
	require.NoError(t, os.Remove(filepath.Join(res.Dir, "src", "frames", "signup.tsx")))
	require.NoError(t, e.call(orbsock.MethodDesignTurn, orbsock.DesignTurnParams{ThreadID: testThread, TurnID: "turn-2", Title: "下一轮"}, nil))
	assert.Equal(t, 3, e.head(id).Version)
	v, err = e.s.Version(ctx, id, 3)
	require.NoError(t, err)
	assert.Equal(t, "turn-1", v.TurnID, "以旧回合封存")
	doc, err = canvas.Parse([]byte(readVersion(t, e.s, id, 3, artifacts.EntryCanvas)))
	require.NoError(t, err)
	assert.Len(t, doc.Pages, 1)
	_, _, err = e.s.ReadFile(ctx, id, 3, "dist/signup.html")
	assert.Error(t, err, "消失的帧产物被删除")

	// 别处在 v3 之后改了 src/frames/login.tsx（v4）；本地也改 → conflict 原样回来
	_, err = e.s.Write(ctx, artifacts.WriteParams{ID: id, BaseVersion: 3, Files: []artifacts.FileChange{
		{Path: "src/frames/login.tsx", Data: strings.NewReader("export default () => <p>remote</p>")},
	}})
	require.NoError(t, err)
	writeSrc(t, res.Dir, "src/frames/login.tsx", "export default () => <p>mine</p>")
	_, err = e.seal("turn-2")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "conflict", se.Code)
	assert.Contains(t, se.Message, "src/frames/login.tsx")
	assert.Equal(t, 4, e.head(id).Version)

	// 再 open：副本落后且脏，保留本地改动
	res2 := e.open(orbsock.DesignOpenParams{ThreadID: testThread, ID: id})
	assert.Equal(t, 3, res2.Version)
	b, err := os.ReadFile(filepath.Join(res.Dir, "src", "frames", "login.tsx"))
	require.NoError(t, err)
	assert.Equal(t, "export default () => <p>mine</p>", string(b))

	// 退回到封存时的内容（干净）后再 open：重新物化到 v4，src/node_modules 保留
	writeSrc(t, res.Dir, "src/frames/login.tsx", "export default () => <p>login</p>")
	res3 := e.open(orbsock.DesignOpenParams{ThreadID: testThread, ID: id})
	assert.Equal(t, 4, res3.Version)
	b, err = os.ReadFile(filepath.Join(res.Dir, "src", "frames", "login.tsx"))
	require.NoError(t, err)
	assert.Equal(t, "export default () => <p>remote</p>", string(b))
	assert.FileExists(t, filepath.Join(res.Dir, "src", "node_modules", "dep", "index.js"))
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &status))
	require.Len(t, status.Designs, 1)
	assert.False(t, status.Designs[0].Dirty)

	// design.status 的 check：最新版本的检查按当前源码换算（改了源码 → stale）
	_, err = e.store.DB.ExecContext(ctx, `UPDATE artifact_versions SET check_json = ? WHERE artifact_id = ? AND n = 4`,
		`{"status":"clean","issues":[],"fingerprint":"`+meta2Hash(t, res.Dir)+`"}`, id)
	require.NoError(t, err)
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &status))
	require.NotNil(t, status.Designs[0].Check)
	assert.Equal(t, apigen.ArtifactCheckStatus("clean"), status.Designs[0].Check.Status)
	writeSrc(t, res.Dir, "src/frames/login.tsx", "export default () => <p>changed</p>")
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &status))
	assert.True(t, status.Designs[0].Dirty)
	assert.Equal(t, apigen.ArtifactCheckStatus("stale"), status.Designs[0].Check.Status)

	// 别的线程看不到这个副本；不是画布的 artifact 打不开
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: "T-other"}, &status))
	assert.Empty(t, status.Designs)
	page, err := e.s.Create(ctx, artifacts.CreateParams{Title: "页", ThreadID: testThread, Files: []artifacts.FileChange{{Path: "index.html", Data: strings.NewReader("<p>x</p>")}}})
	require.NoError(t, err)
	err = e.call(orbsock.MethodDesignOpen, orbsock.DesignOpenParams{ThreadID: testThread, ID: page.ID}, nil)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "invalid", se.Code)
}

// M29 T4：src/ 里 >3 MiB 的二进制文件经 fetch_design_files 跨页物化后逐字节一致。
func TestDesignMaterializeLarge(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	big := make([]byte, 3_500_000)
	_, err := rand.Read(big)
	require.NoError(t, err)
	_, err = e.s.Write(ctx, artifacts.WriteParams{ID: res.ID, BaseVersion: 1, Files: []artifacts.FileChange{
		{Path: "src/assets/hero.bin", Data: bytes.NewReader(big)},
		{Path: "src/frames/a.tsx", Data: strings.NewReader("export default () => <p>a</p>")},
	}})
	require.NoError(t, err)
	res2 := e.open(orbsock.DesignOpenParams{ThreadID: testThread, ID: res.ID})
	assert.Equal(t, 2, res2.Version)
	got, err := os.ReadFile(filepath.Join(res.Dir, "src", "assets", "hero.bin"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(big, got), "逐字节一致")
	var status orbsock.DesignStatusResult
	require.NoError(t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &status))
	require.Len(t, status.Designs, 1)
	assert.False(t, status.Designs[0].Dirty, "物化后 sealedHash 与磁盘一致")
}

func meta2Hash(t *testing.T, dir string) string {
	t.Helper()
	h, err := designSourceHash(dir)
	require.NoError(t, err)
	return h
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

// fakeChromium 顶替 orb 里的 Chromium：参数逐行记进 <自身路径>.log；--screenshot= 时把「PNG <路径> <窗口>」写进
// 截图文件；否则取页面（静态产物页另要求同源 ./probe.js 可取），把页面里 `probe: [...]` 标记的 issues 写成探针结果
// 节点打到 stdout（页面含 NOPROBE 时不写结果节点）。
const fakeChromium = `#!/usr/bin/env node
const fs = require("fs");
const args = process.argv.slice(2);
fs.appendFileSync(__filename + ".log", JSON.stringify(args) + "\n");
const url = args[args.length - 1];
const size = (args.find((a) => a.startsWith("--window-size=")) || "").slice(14);
(async () => {
  const body = await (await fetch(url)).text();
  const shot = args.find((a) => a.startsWith("--screenshot="));
  if (shot) {
    fs.writeFileSync(shot.slice(13), "PNG " + new URL(url).pathname + " " + size);
    return;
  }
  if (new URL(url).pathname.endsWith(".html") && !(await fetch(new URL("./probe.js", url))).ok) {
    process.stdout.write("<html><body>no probe.js</body></html>");
    return;
  }
  if (body.includes("NOPROBE")) {
    process.stdout.write("<html><body>nothing</body></html>");
    return;
  }
  const m = /probe: (\[.*\])/.exec(body);
  process.stdout.write('<html><body><div id="root"></div><script id="oryn-probe" type="application/json">{"v":1,"issues":' + (m ? m[1] : "[]") + "}</script></body></html>");
})().catch((e) => { process.stderr.write(String(e)); process.exit(1); });
`

// fakeNpm 顶替 PATH 上的 npm：`install --package-lock-only`（WriteProjectLock）按当前目录的 package.json 写出根条目
// 一致的锁（依赖里有 boom 时输出一大段后失败）；其余 install（src/ 下装设计依赖）建 node_modules 并（重）写
// package-lock.json。
const fakeNpm = `#!/usr/bin/env node
const fs = require("fs");
const args = process.argv.slice(2);
const pkg = JSON.parse(fs.readFileSync("package.json", "utf8"));
const deps = { ...(pkg.devDependencies || {}), ...(pkg.dependencies || {}) };
if (args.includes("--package-lock-only")) {
  if (deps.boom) {
    process.stdout.write("x".repeat(6000) + "\nnpm ERR! 404 boom is not in this registry\n");
    process.exit(1);
  }
  fs.writeFileSync("package-lock.json", JSON.stringify({ name: pkg.name, lockfileVersion: 3, packages: { "": { name: pkg.name, dependencies: pkg.dependencies } } }));
  process.exit(0);
}
fs.mkdirSync("node_modules", { recursive: true });
fs.writeFileSync("package-lock.json", JSON.stringify({ name: "design", lockfileVersion: 3, deps }) + "\n");
`

// writeExe 在 dir 下写一个可执行脚本，返回它的路径。
func writeExe(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	return p
}

// chromiumCalls 读假 Chromium 记下的每次调用参数。
func chromiumCalls(t *testing.T, bin string) [][]string {
	t.Helper()
	b, err := os.ReadFile(bin + ".log")
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out [][]string
	for line := range strings.Lines(string(b)) {
		var args []string
		require.NoError(t, json.Unmarshal([]byte(line), &args))
		out = append(out, args)
	}
	return out
}

func (e *designEnv) check(p orbsock.DesignCheckParams) (orbsock.DesignCheckResult, error) {
	var res orbsock.DesignCheckResult
	p.ThreadID = testThread
	err := e.call(orbsock.MethodDesignCheck, p, &res)
	return res, err
}

func (e *designEnv) status(id string) orbsock.DesignStatus {
	e.t.Helper()
	var st orbsock.DesignStatusResult
	require.NoError(e.t, e.call(orbsock.MethodDesignStatus, orbsock.DesignStatusParams{ThreadID: testThread}, &st))
	for _, d := range st.Designs {
		if d.ID == id {
			return d
		}
	}
	e.t.Fatalf("design.status has no %s", id)
	return orbsock.DesignStatus{}
}

const clippedIssue = `{"rule":"text-clipped","loc":"src/frames/home.tsx:3:5","text":"立即开始","detail":64}`

// M29 Q1：design.check 用 orb 里的 Chromium 对 dev 服务上的帧跑探针：汇总各帧 issues（带帧 id）、窗口取帧声明的尺寸
// （缺省 1280×800）、参数里没有 --no-sandbox 且每次用自己的 --user-data-dir；frames 过滤、未知帧 invalid_params、
// screenshot 回每帧 PNG；全帧检查的结果缓存后由 design.status 反映、封存时写进新版本的 check_json，局部检查不进缓存；
// 改源码后缓存失效（封存不带 check）；探针没跑成的帧让结果为 unverified。
func TestDesignCheck(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	bin := writeExe(t, t.TempDir(), "chromium", fakeChromium)
	e.rt.design.chromium = bin

	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread, Title: "检查"})
	id := res.ID
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export const frame = { name: \"首页\", width: 390, height: 844 };\n// probe: ["+clippedIssue+"]\nexport default () => <button>立即开始</button>")
	writeSrc(t, res.Dir, "src/frames/about.tsx", "export default () => <p>about</p>")
	writeSrc(t, res.Dir, "src/frames/_layout.tsx", "export default ({children}) => children")

	got, err := e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Equal(t, "issues", got.Status)
	require.Len(t, got.Issues, 1)
	assert.Equal(t, orbsock.DesignCheckIssue{Rule: "text-clipped", Frame: "home", Loc: "src/frames/home.tsx:3:5", Text: "立即开始", Detail: json.RawMessage(`64`)}, got.Issues[0])
	assert.Empty(t, got.Images)
	assert.Empty(t, got.Message)
	calls := chromiumCalls(t, bin)
	require.Len(t, calls, 2, "_layout 不是帧")
	sizes := map[string]string{}
	for _, args := range calls {
		assert.NotContains(t, args, "--no-sandbox")
		assert.Contains(t, args, "--headless=new")
		assert.Contains(t, args, "--dump-dom")
		assert.Contains(t, args, "--virtual-time-budget=5000")
		var profile, size string
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok {
				profile = v
			}
			if v, ok := strings.CutPrefix(a, "--window-size="); ok {
				size = v
			}
		}
		require.NotEmpty(t, profile)
		assert.NoDirExists(t, profile, "临时 profile 跑完删掉")
		url := args[len(args)-1]
		assert.Regexp(t, `^http://127\.0\.0\.1:\d+/frames/(home|about)\?__oryn_probe=1$`, url)
		sizes[url[strings.LastIndex(url, "/")+1:strings.Index(url, "?")]] = size
	}
	assert.Equal(t, map[string]string{"home": "390,844", "about": "1280,800"}, sizes)

	// design.status 反映这次全帧检查
	st := e.status(id)
	require.NotNil(t, st.Check)
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), st.Check.Status)
	require.Len(t, st.Check.Issues, 1)
	assert.Equal(t, "home", *st.Check.Issues[0].Frame)

	// frames 过滤：只跑 about；局部检查不进缓存
	got, err = e.check(orbsock.DesignCheckParams{ID: id, Frames: []string{"about"}})
	require.NoError(t, err)
	assert.Equal(t, "clean", got.Status)
	assert.Empty(t, got.Issues)
	assert.Len(t, chromiumCalls(t, bin), 3)
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), e.status(id).Check.Status)

	// 未知帧
	_, err = e.check(orbsock.DesignCheckParams{ID: id, Frames: []string{"home", "nope"}})
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeInvalidParams, se.Code)
	assert.Contains(t, se.Message, "nope")

	// screenshot：每帧一张同尺寸的 PNG
	got, err = e.check(orbsock.DesignCheckParams{ID: id, Screenshot: true})
	require.NoError(t, err)
	require.Len(t, got.Images, 2)
	imgs := map[string]string{}
	for _, im := range got.Images {
		b, err := base64.StdEncoding.DecodeString(im.Data)
		require.NoError(t, err)
		imgs[im.Frame] = string(b)
	}
	assert.Equal(t, map[string]string{"about": "PNG /frames/about 1280,800", "home": "PNG /frames/home 390,844"}, imgs)
	for _, args := range chromiumCalls(t, bin)[3:] {
		assert.NotContains(t, args, "--no-sandbox")
	}

	// 封存带上与当前源码一致的缓存检查：新版本的 check_json 被写入，fingerprint = 新版本 src/ 的指纹
	sealed, err := e.seal("turn-1")
	require.NoError(t, err)
	require.Len(t, sealed.Sealed, 1)
	v, err := e.s.Version(ctx, id, sealed.Sealed[0].Version)
	require.NoError(t, err)
	require.NotEmpty(t, v.Check, "check_json 被写入")
	var chk design.Check
	require.NoError(t, json.Unmarshal(v.Check, &chk))
	assert.Equal(t, design.CheckIssues, chk.Status)
	require.Len(t, chk.Issues, 1)
	assert.Equal(t, "home", chk.Issues[0].Frame)
	assert.Equal(t, meta2Hash(t, res.Dir), chk.Fingerprint)

	// 检查之后又改了源码：缓存失效，封存不带 check
	writeSrc(t, res.Dir, "src/frames/about.tsx", "export default () => <p>about 2</p>")
	assert.Equal(t, apigen.ArtifactCheckStatus("stale"), e.status(id).Check.Status)
	sealed, err = e.seal("turn-1")
	require.NoError(t, err)
	v, err = e.s.Version(ctx, id, sealed.Sealed[0].Version)
	require.NoError(t, err)
	assert.Empty(t, v.Check)

	// 页面里没有探针结果：该帧没跑成，整体 unverified 并说明
	writeSrc(t, res.Dir, "src/frames/about.tsx", "NOPROBE")
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>ok</p>")
	got, err = e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Equal(t, "unverified", got.Status)
	assert.Contains(t, got.Message, "未检查 about：页面里没有探针结果")
}

// lastURL 是假 Chromium 最近一次调用打开的页面地址。
func lastURL(t *testing.T, bin string) string {
	t.Helper()
	calls := chromiumCalls(t, bin)
	require.NotEmpty(t, calls)
	args := calls[len(calls)-1]
	return args[len(args)-1]
}

// M29 Q1：design.check 把控制面 design_head 的审美配置拼进页面地址（dev 帧与静态 dist 两条路径都带）；缓存连同配置
// 记下，方向或 surface 变了之后 design.status 不再用它、封存也不带它；取不到 head 时按缺省（只跑版面规则）照常检查、
// 在 message 说明、不缓存。
func TestDesignCheckProbeConfig(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	bin := writeExe(t, t.TempDir(), "chromium", fakeChromium)
	e.rt.design.chromium = bin
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	id := res.ID
	writeSrc(t, res.Dir, "src/frames/home.tsx", "// probe: ["+clippedIssue+"]\nexport default () => <p>x</p>")
	setDirection := func(surface, direction string) {
		require.NoError(t, e.store.Q.SetArtifactDesignDirection(ctx, db.SetArtifactDesignDirectionParams{ArtifactID: id, Surface: &surface, Direction: &direction}))
	}

	// 没有方向：只带 __oryn_probe=1
	_, err := e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Regexp(t, `/frames/home\?__oryn_probe=1$`, lastURL(t, bin))
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), e.status(id).Check.Status)

	// 选定方向（landing）：带上审美配置
	setDirection("landing", `{"id":"calm"}`)
	assert.Nil(t, e.status(id).Check, "配置变了：缓存不再用，head 上也还没有检查")
	_, err = e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Regexp(t, `/frames/home\?__oryn_probe=1&__oryn_aesthetic=1&__oryn_surface=landing$`, lastURL(t, bin))
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), e.status(id).Check.Status)

	// 检查之后改成 app：封存不带这次缓存的检查
	setDirection("app", `{"id":"calm"}`)
	sealed, err := e.seal("turn-1")
	require.NoError(t, err)
	require.Len(t, sealed.Sealed, 1)
	v, err := e.s.Version(ctx, id, sealed.Sealed[0].Version)
	require.NoError(t, err)
	assert.Empty(t, v.Check, "方向变了，缓存的检查不进封存")

	// 配置一致时照常带上（对照）
	writeSrc(t, res.Dir, "src/frames/home.tsx", "// probe: ["+clippedIssue+"]\nexport default () => <p>y</p>")
	_, err = e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Regexp(t, `\?__oryn_probe=1&__oryn_aesthetic=1&__oryn_surface=app$`, lastURL(t, bin))
	sealed, err = e.seal("turn-1")
	require.NoError(t, err)
	v, err = e.s.Version(ctx, id, sealed.Sealed[0].Version)
	require.NoError(t, err)
	assert.NotEmpty(t, v.Check)

	// 静态 dist 路径同样带配置
	_, err = e.rt.svc.Stop(designServiceName(id), false)
	require.NoError(t, err)
	_, err = e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+/home\.html\?__oryn_probe=1&__oryn_aesthetic=1&__oryn_surface=app$`, lastURL(t, bin))
	_, err = e.rt.svc.Restart(designServiceName(id))
	require.NoError(t, err)
	e.rt.settle(ctx, e.rt.svc.snapshot(designServiceName(id)))

	// 取不到 head：按缺省检查，说明原因，不缓存
	writeSrc(t, res.Dir, "src/frames/home.tsx", "// probe: ["+clippedIssue+"]\nexport default () => <p>z</p>")
	e.failHead.Store(true)
	got, err := e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Equal(t, "issues", got.Status)
	assert.Contains(t, got.Message, "取不到控制面的设计配置")
	assert.Regexp(t, `/frames/home\?__oryn_probe=1$`, lastURL(t, bin))
	e.failHead.Store(false)
	assert.Equal(t, apigen.ArtifactCheckStatus("stale"), e.status(id).Check.Status, "没进缓存：仍是 head 的检查（源码已变）")
}

// M34 C4：design_head 的落选帧不参与全帧 design.check（message 说明，点名照查）；问题只在落选帧上时结果与
// design.status 都是 clean；落选帧集合变了，缓存的检查失效。
func TestDesignCheckArchived(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	bin := writeExe(t, t.TempDir(), "chromium", fakeChromium)
	e.rt.design.chromium = bin
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	id := res.ID
	writeSrc(t, res.Dir, "src/frames/home.tsx", "// probe: ["+clippedIssue+"]\nexport default () => <p>x</p>")
	writeSrc(t, res.Dir, "src/frames/about.tsx", "export default () => <p>about</p>")
	_, err := e.seal("turn-1")
	require.NoError(t, err)
	// pick 在最新版本的 canvas.json 上写一个方案组 hero（a = home，b = about），picked 为空表示未选定
	pick := func(picked string) {
		t.Helper()
		v := e.head(id).Version
		doc, err := canvas.Parse([]byte(readVersion(t, e.s, id, v, artifacts.EntryCanvas)))
		require.NoError(t, err)
		page := map[string]string{}
		for _, p := range doc.Pages {
			page[strings.TrimSuffix(strings.TrimPrefix(p.Source, "src/frames/"), ".tsx")] = p.ID
		}
		g := canvas.Group{ID: "hero", Canvas: doc.Canvases[0].ID, Name: "首屏", Variants: []canvas.Variant{
			{ID: "a", Name: "A", Pages: []string{page["home"]}}, {ID: "b", Name: "B", Pages: []string{page["about"]}},
		}}
		if picked != "" {
			g.Picked = &picked
		}
		doc.Groups = []canvas.Group{g}
		_, err = e.s.Write(ctx, artifacts.WriteParams{ID: id, BaseVersion: v, Files: []artifacts.FileChange{
			{Path: artifacts.EntryCanvas, Data: bytes.NewReader(doc.JSON())},
		}})
		require.NoError(t, err)
	}

	// 未选定：两帧都查，home 有问题
	pick("")
	got, err := e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Equal(t, "issues", got.Status)
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), e.status(id).Check.Status)

	// 选定 b：home 落选，缓存失效（head 上也没有检查）
	pick("b")
	assert.Nil(t, e.status(id).Check, "落选帧变了：缓存不再用")
	n := len(chromiumCalls(t, bin))
	got, err = e.check(orbsock.DesignCheckParams{ID: id})
	require.NoError(t, err)
	assert.Equal(t, "clean", got.Status, "问题只在落选帧上")
	assert.Empty(t, got.Issues)
	assert.Contains(t, got.Message, "跳过落选方案的帧：home")
	require.Len(t, chromiumCalls(t, bin), n+1)
	assert.Regexp(t, `/frames/about\?`, lastURL(t, bin))
	assert.Equal(t, apigen.ArtifactCheckStatus("clean"), e.status(id).Check.Status)

	// 点名落选帧照查，不进缓存
	got, err = e.check(orbsock.DesignCheckParams{ID: id, Frames: []string{"home"}})
	require.NoError(t, err)
	assert.Equal(t, "issues", got.Status)
	assert.Equal(t, apigen.ArtifactCheckStatus("clean"), e.status(id).Check.Status)

	// 改选 a：落选帧变成 about，缓存失效
	pick("a")
	assert.Nil(t, e.status(id).Check, "落选帧变了：缓存不再用")
}

// M34 C4：版本上的检查结果去掉落选帧的问题；去掉后一处不剩时 issues 记作 clean。
func TestDesignCheckAPIArchived(t *testing.T) {
	raw := json.RawMessage(`{"status":"issues","issues":[{"rule":"text-clipped","frame":"home"}],"fingerprint":"h"}`)
	got := designAPICheck(raw, "h", []string{"home"})
	require.NotNil(t, got)
	assert.Equal(t, apigen.ArtifactCheckStatus("clean"), got.Status)
	assert.Empty(t, got.Issues)
	got = designAPICheck(raw, "h", []string{"about"})
	assert.Equal(t, apigen.ArtifactCheckStatus("issues"), got.Status)
	assert.Len(t, got.Issues, 1)
	assert.Equal(t, apigen.ArtifactCheckStatus("stale"), designAPICheck(raw, "h2", []string{"home"}).Status)
}

// M29 Q1：没有 Chromium 时 design.check 回 unverified 并说明（不报错）；没有工作副本（别的线程、没 open 过）回 no_design。
func TestDesignCheckUnavailable(t *testing.T) {
	e := newDesignEnv(t)
	e.rt.design.chromium = filepath.Join(t.TempDir(), "chromium") // 不存在
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>x</p>")

	got, err := e.check(orbsock.DesignCheckParams{ID: res.ID, Screenshot: true})
	require.NoError(t, err)
	assert.Equal(t, "unverified", got.Status)
	assert.Empty(t, got.Issues)
	assert.Empty(t, got.Images)
	assert.Contains(t, got.Message, "没有 Chromium")
	assert.Equal(t, apigen.ArtifactCheckStatus("unverified"), e.status(res.ID).Check.Status)

	var se *orbsock.Error
	err = e.call(orbsock.MethodDesignCheck, orbsock.DesignCheckParams{ThreadID: "T-other", ID: res.ID}, nil)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeNoDesign, se.Code)
	_, err = e.check(orbsock.DesignCheckParams{ID: "art_missing"})
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeNoDesign, se.Code)
}

// M29 Q1：dev 服务没在跑时 design.check 在 127.0.0.1 临时起静态服务提供 dist/ 与 sandbox 的 probe.js；结果照常汇总，
// 说明检查的是构建产物，不进缓存。没有 probe.js 时探针结果缺失（unverified）。
func TestDesignCheckStatic(t *testing.T) {
	e := newDesignEnv(t)
	bin := writeExe(t, t.TempDir(), "chromium", fakeChromium)
	e.rt.design.chromium = bin
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	writeSrc(t, res.Dir, "src/frames/home.tsx", "// probe: ["+clippedIssue+"]\nexport default () => <p>x</p>")
	_, err := e.seal("turn-1")
	require.NoError(t, err)
	_, err = e.rt.svc.Stop(designServiceName(res.ID), false)
	require.NoError(t, err)
	sandbox := filepath.Join(e.home, ".oryn", "design", "sandbox")
	require.NoError(t, os.MkdirAll(sandbox, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sandbox, "probe.js"), []byte("/* probe */"), 0o644))

	got, err := e.check(orbsock.DesignCheckParams{ID: res.ID})
	require.NoError(t, err)
	assert.Equal(t, "issues", got.Status)
	require.Len(t, got.Issues, 1)
	assert.Equal(t, "home", got.Issues[0].Frame)
	assert.Contains(t, got.Message, "dev 服务没在跑")
	calls := chromiumCalls(t, bin)
	require.Len(t, calls, 1)
	assert.Regexp(t, `^http://127\.0\.0\.1:\d+/home\.html\?__oryn_probe=1$`, calls[0][len(calls[0])-1])
	st := e.status(res.ID)
	assert.Nil(t, st.Check, "静态产物上的检查不进缓存")

	require.NoError(t, os.Remove(filepath.Join(sandbox, "probe.js")))
	got, err = e.check(orbsock.DesignCheckParams{ID: res.ID})
	require.NoError(t, err)
	assert.Equal(t, "unverified", got.Status)
}

// M29 Q1 / D2：有 src/package.json 时封存装依赖后重读 src/（npm 新建的 src/package-lock.json 进本次封存，下一次封存
// 不会因为它多出一版），vite build 之后写出 dist/_project/ 的工程锁并一并封存；工程锁生成失败回 build_failed 带 npm
// 输出尾、不落版本。
func TestDesignSealDepsAndProjectLock(t *testing.T) {
	npmDir := t.TempDir()
	writeExe(t, npmDir, "npm", fakeNpm)
	t.Setenv("PATH", npmDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	e := newDesignEnv(t)
	ctx := context.Background()
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	id := res.ID
	writeSrc(t, res.Dir, "src/package.json", `{"dependencies":{"left-pad":"1.3.0"}}`)
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>x</p>")

	sealed, err := e.seal("turn-1")
	require.NoError(t, err)
	require.Equal(t, []orbsock.DesignSealed{{ID: id, Version: 2}}, sealed.Sealed)
	files, err := e.s.Files(ctx, id, 2)
	require.NoError(t, err)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	assert.Contains(t, paths, "src/package-lock.json", "npm 新建的锁进了这次封存")
	assert.Contains(t, paths, "dist/_project/package.json")
	assert.Contains(t, paths, "dist/_project/package-lock.json")
	assert.NotContains(t, strings.Join(paths, "\n"), "node_modules")
	lock := readVersion(t, e.s, id, 2, "dist/_project/package-lock.json")
	pkg := readVersion(t, e.s, id, 2, "dist/_project/package.json")
	require.NoError(t, designengine.VerifyProjectLock([]byte(`{"dependencies":{"left-pad":"1.3.0"}}`), []byte(lock)))
	assert.Contains(t, pkg, `"left-pad": "1.3.0"`)

	sealed, err = e.seal("turn-1")
	require.NoError(t, err)
	assert.Empty(t, sealed.Sealed, "锁已在上一版里，不再多出一版")
	assert.Equal(t, []string{id}, sealed.Skipped)
	assert.Equal(t, 2, e.head(id).Version)

	// 工程锁生成失败：build_failed，带 npm 输出尾，不落版本
	writeSrc(t, res.Dir, "src/package.json", `{"dependencies":{"boom":"1.0.0"}}`)
	_, err = e.seal("turn-1")
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeBuildFailed, se.Code)
	assert.Contains(t, se.Message, "npm ERR! 404 boom")
	assert.Less(t, len(se.Message), 4<<10+300)
	assert.Equal(t, 2, e.head(id).Version)
}

// M29 I2 / K5：design.turn 开新回合时把副本与 head 对账，不必再调 design.open——查看器里恢复出的新版本（干净且落后
// 的副本）重新物化进工作副本并重启 dev 服务；脏副本保留本地改动。
func TestDesignTurnReconcile(t *testing.T) {
	e := newDesignEnv(t)
	ctx := context.Background()
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	id := res.ID
	require.NoError(t, e.call(orbsock.MethodDesignTurn, orbsock.DesignTurnParams{ThreadID: testThread, TurnID: "turn-1"}, nil))
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>one</p>")
	_, err := e.seal("turn-1")
	require.NoError(t, err)
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>two</p>")
	_, err = e.seal("turn-1")
	require.NoError(t, err)
	require.Equal(t, 3, e.head(id).Version)

	svc := designServiceName(id)
	pid := e.rt.svc.snapshot(svc).Pid
	require.NotNil(t, pid)
	_, err = e.s.RestoreVersion(ctx, id, 2, 3, artifacts.Author{Label: "user"})
	require.NoError(t, err)
	require.Equal(t, 4, e.head(id).Version)

	require.NoError(t, e.call(orbsock.MethodDesignTurn, orbsock.DesignTurnParams{ThreadID: testThread, TurnID: "turn-2"}, nil))
	b, err := os.ReadFile(filepath.Join(res.Dir, "src", "frames", "home.tsx"))
	require.NoError(t, err)
	assert.Equal(t, "export default () => <p>one</p>", string(b), "工作副本是恢复后的源码")
	meta, err := readDesignMeta(res.Dir)
	require.NoError(t, err)
	assert.Equal(t, 4, meta.BaseVersion)
	assert.False(t, e.status(id).Dirty)
	require.Eventually(t, func() bool {
		o := e.rt.svc.snapshot(svc)
		return o.Status == apigen.OrbServiceStatusRunning && o.Pid != nil && *o.Pid != *pid
	}, 20*time.Second, 20*time.Millisecond, "dev 服务重启")
	assert.Equal(t, "export default () => <p>one</p>", httpGet(t, designDevURL(meta.Port)+"frames/home"))

	// 脏副本：上一回合的封存失败（构建坏了）后，design.turn 的对账保留本地改动
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export default () => <p>mine</p>")
	writeSrc(t, res.Dir, "src/FAIL", "1")
	_, err = e.s.RestoreVersion(ctx, id, 3, 4, artifacts.Author{Label: "user"})
	require.NoError(t, err)
	require.Equal(t, 5, e.head(id).Version)
	require.NoError(t, e.call(orbsock.MethodDesignTurn, orbsock.DesignTurnParams{ThreadID: testThread, TurnID: "turn-3"}, nil))
	b, err = os.ReadFile(filepath.Join(res.Dir, "src", "frames", "home.tsx"))
	require.NoError(t, err)
	assert.Equal(t, "export default () => <p>mine</p>", string(b), "脏改动保留")
	assert.FileExists(t, filepath.Join(res.Dir, "src", "FAIL"))
	meta, err = readDesignMeta(res.Dir)
	require.NoError(t, err)
	assert.Equal(t, 4, meta.BaseVersion)
	assert.Equal(t, 5, e.head(id).Version)
}

// 真 Chromium（ORYN_TEST_CHROMIUM=1，且嵌入的 Web 产物里有 sandbox/probe.js）：dev 服务停掉后对静态 dist/ 起临时服务，
// 页面里一个 overflow:hidden 截断的按钮报出 text-clipped 与它的 loc，screenshot 回一张 PNG。宿主机以 root 跑时经包装
// 脚本降到 nobody（orb 里 uid 1000 直接跑，不加 --no-sandbox）。
func TestDesignCheckChromium(t *testing.T) {
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 to run Chromium integration tests")
	}
	probe, ok := webui.Asset("sandbox/probe.js")
	if !ok {
		t.Fatal("嵌入的 Web 产物里没有 sandbox/probe.js：先 make web")
	}
	e := newDesignEnv(t)
	if os.Geteuid() == 0 {
		e.rt.design.chromium = rootChromium(t)
	}
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	writeSrc(t, res.Dir, "src/frames/home.tsx", "export const frame = { width: 390, height: 844 };\nexport default () => null")
	_, err := e.rt.svc.Stop(designServiceName(res.ID), false)
	require.NoError(t, err)
	sandbox := filepath.Join(e.home, ".oryn", "design", "sandbox")
	require.NoError(t, os.MkdirAll(sandbox, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sandbox, "probe.js"), probe, 0o644))
	const button = `<button data-oryn-loc="src/frames/home.tsx:7:9" style="width:80px;height:32px;overflow:hidden;white-space:nowrap;` +
		`background:#0f766e;color:#ffffff;border:0">立即开始免费试用</button>`
	// 模拟引擎入口：页面脚本渲染出按钮后加载同源 ./probe.js
	writeSrc(t, res.Dir, "dist/home.html", `<!doctype html><html><head><meta charset="utf-8"></head><body><div id="root"></div><script>`+
		`document.getElementById("root").innerHTML = `+designJSString(button)+`;`+
		`if (new URLSearchParams(location.search).get("__oryn_probe") === "1") {`+
		` const s = document.createElement("script"); s.src = new URL("./probe.js", location.href).href; document.head.append(s); }`+
		`</script></body></html>`)

	got, err := e.check(orbsock.DesignCheckParams{ID: res.ID, Screenshot: true})
	require.NoError(t, err)
	t.Logf("design.check: status=%s message=%q issues=%+v images=%d", got.Status, got.Message, got.Issues, len(got.Images))
	assert.Equal(t, "issues", got.Status)
	var clipped *orbsock.DesignCheckIssue
	for i := range got.Issues {
		if got.Issues[i].Rule == "text-clipped" {
			clipped = &got.Issues[i]
		}
	}
	require.NotNil(t, clipped)
	assert.Equal(t, "home", clipped.Frame)
	assert.Equal(t, "src/frames/home.tsx:7:9", clipped.Loc)
	require.Len(t, got.Images, 1)
	png, err := base64.StdEncoding.DecodeString(got.Images[0].Data)
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")), "PNG")
}

// designJSString 把 s 写成 JS 字符串字面量（JSON 字符串即合法的 JS 字符串）。
func designJSString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// 真引擎 + 真 dev server + 真 Chromium（ORYN_TEST_CHROMIUM=1，ORYN_TEST_DESIGN_ENGINE_MODULES=<npm ci 过的引擎
// node_modules>，嵌入的 Web 产物里要有 sandbox/probe.js）：一帧里同时有 3 个 eyebrow 标签（超过 ceil(3/3)=1）、3 个连续
// 左右图文分栏 section 和一处内联颜色字面量。(a) 没选方向：三条审美规则都不报；(b) 选方向、surface=landing：都报；
// (c) surface=app：只报 raw-color。
func TestDesignCheckChromiumDev(t *testing.T) {
	modules := os.Getenv("ORYN_TEST_DESIGN_ENGINE_MODULES")
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" || modules == "" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 and ORYN_TEST_DESIGN_ENGINE_MODULES=<engine node_modules> to run")
	}
	if _, ok := webui.Asset("sandbox/probe.js"); !ok {
		t.Fatal("嵌入的 Web 产物里没有 sandbox/probe.js：先 make web")
	}
	ctx := context.Background()
	e := newDesignEnv(t)
	share := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(share, designengine.Version), 0o755))
	require.NoError(t, os.Symlink(modules, filepath.Join(share, designengine.Version, "node_modules")))
	e.rt.design.shareRoot = share
	if os.Geteuid() == 0 {
		e.rt.design.chromium = rootChromium(t)
	}
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	id := res.ID
	writeSrc(t, res.Dir, "src/frames/home.tsx", `const eyebrow = { textTransform: "uppercase", letterSpacing: "0.2em", fontSize: 12 } as const;

function Split({ flip, n }: { flip?: boolean; n: number }) {
	const media = (
		<svg width="500" height="300" viewBox="0 0 10 6">
			<rect width="10" height="6" />
		</svg>
	);
	const text = (
		<div style={{ width: 500 }}>
			<span style={eyebrow}>Feature {n}</span>
			<p>这里是一段足够长的说明文字，介绍这个功能的细节。</p>
		</div>
	);
	return (
		<section style={{ display: "flex", gap: 40, width: 1100, padding: 20 }}>
			{flip ? text : media}
			{flip ? media : text}
		</section>
	);
}

export default function Home() {
	return (
		<main>
			<Split n={1} />
			<Split flip n={2} />
			<Split n={3} />
			<p style={{ color: "#ff0000" }}>红色字面量</p>
		</main>
	);
}
`)
	rulesOf := func(label string) map[string]bool {
		t.Helper()
		got, err := e.check(orbsock.DesignCheckParams{ID: id})
		require.NoError(t, err)
		t.Logf("%s: status=%s message=%q", label, got.Status, got.Message)
		out := map[string]bool{}
		for _, is := range got.Issues {
			t.Logf("  %s %s %s %q %s", is.Rule, is.Frame, is.Loc, is.Text, is.Detail)
			out[is.Rule] = true
		}
		require.NotEqual(t, "unverified", got.Status, got.Message)
		return out
	}
	setDirection := func(surface string) {
		direction := `{"id":"calm"}`
		require.NoError(t, e.store.Q.SetArtifactDesignDirection(ctx, db.SetArtifactDesignDirectionParams{ArtifactID: id, Surface: &surface, Direction: &direction}))
	}

	got := rulesOf("(a) no direction")
	for _, r := range []string{"eyebrow-excess", "split-repeat", "raw-color"} {
		assert.False(t, got[r], "(a) %s", r)
	}
	setDirection("landing")
	got = rulesOf("(b) landing")
	for _, r := range []string{"eyebrow-excess", "split-repeat", "raw-color"} {
		assert.True(t, got[r], "(b) %s", r)
	}
	setDirection("app")
	got = rulesOf("(c) app")
	assert.True(t, got["raw-color"], "(c) raw-color")
	assert.False(t, got["eyebrow-excess"], "(c) eyebrow-excess")
	assert.False(t, got["split-repeat"], "(c) split-repeat")
}

// M34 S3：真引擎 + 真 dev server + 真 Chromium（条件同上）。两帧写同一段 text-accent 文字：home-a 不属于带主题的方案，
// 用 theme.css 的深色强调色；home-b 属于主题 pale 的方案，src/themes/pale.css 把强调色覆盖成浅黄。只有 home-b 报
// contrast-low，说明方案主题按帧生效、互不影响。
func TestDesignVariantThemeChromium(t *testing.T) {
	modules := os.Getenv("ORYN_TEST_DESIGN_ENGINE_MODULES")
	if os.Getenv("ORYN_TEST_CHROMIUM") != "1" || modules == "" {
		t.Skip("set ORYN_TEST_CHROMIUM=1 and ORYN_TEST_DESIGN_ENGINE_MODULES=<engine node_modules> to run")
	}
	if _, ok := webui.Asset("sandbox/probe.js"); !ok {
		t.Fatal("嵌入的 Web 产物里没有 sandbox/probe.js：先 make web")
	}
	e := newDesignEnv(t)
	share := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(share, designengine.Version), 0o755))
	require.NoError(t, os.Symlink(modules, filepath.Join(share, designengine.Version, "node_modules")))
	e.rt.design.shareRoot = share
	if os.Geteuid() == 0 {
		e.rt.design.chromium = rootChromium(t)
	}
	res := e.open(orbsock.DesignOpenParams{ThreadID: testThread})
	const frame = `export default function F() {
	return <main className="bg-bg p-8"><p className="text-accent text-base">这是一段用强调色写的正文，用来检查对比度。</p></main>;
}
`
	writeSrc(t, res.Dir, "src/theme.css", "@theme {\n  --color-bg: #ffffff;\n  --color-text: #111111;\n  --color-accent: #0f766e;\n}\n")
	writeSrc(t, res.Dir, "src/themes/pale.css", ":root[data-oryn-theme=\"pale\"] {\n  --color-accent: #fde68a;\n}\n")
	writeSrc(t, res.Dir, "src/frames/home-a.tsx", frame)
	writeSrc(t, res.Dir, "src/frames/home-b.tsx", frame)
	writeSrc(t, res.Dir, "src/variants.json", `{"groups":[{"id":"home","name":"首页","variants":[`+
		`{"id":"a","name":"深色","frames":["home-a"]},{"id":"b","name":"浅黄","theme":"pale","frames":["home-b"]}]}]}`)

	got, err := e.check(orbsock.DesignCheckParams{ID: res.ID})
	require.NoError(t, err)
	t.Logf("design.check: status=%s message=%q", got.Status, got.Message)
	require.NotEqual(t, "unverified", got.Status, got.Message)
	low := map[string]bool{}
	for _, is := range got.Issues {
		t.Logf("  %s %s %s %q %s", is.Rule, is.Frame, is.Loc, is.Text, is.Detail)
		if is.Rule == "contrast-low" {
			low[is.Frame] = true
		}
	}
	assert.True(t, low["home-b"], "home-b 用浅黄强调色，应报 contrast-low")
	assert.False(t, low["home-a"], "home-a 用 theme.css 的强调色，不应报 contrast-low")
}

// rootChromium 在宿主机以 root 跑测试时包一层：把本次的 profile 与临时目录交给 nobody 后以 nobody 执行原参数（root 下
// Chromium 不加 --no-sandbox 起不来；orb 里 uid 1000 直接跑）。
func rootChromium(t *testing.T) string {
	t.Helper()
	var real string
	for _, p := range designChromiumPaths {
		if _, err := os.Stat(p); err == nil {
			real = p
			break
		}
	}
	require.NotEmpty(t, real, "no chromium")
	return writeExe(t, t.TempDir(), "chromium", `#!/bin/sh
for a in "$@"; do case "$a" in --user-data-dir=*) d="${a#--user-data-dir=}"; chown -R nobody "$d" "$(dirname "$d")";; esac; done
exec setpriv --reuid=nobody --regid=65534 --clear-groups `+real+` "$@"
`)
}

// M29 D3：design.eject 经真 artifacts.Service + ExportProject 分片传输并安全解压到目标目录（含目标尚不存在与
// 已存在空目录两种场景），校验 package.json、package-lock.json、.oryn/box.yaml、src/frames/** 及跨页大文件完整；
// 拒绝非空目标目录、相对路径与坏参数；拒绝 sha256 篡改；拒绝 zip-slip / 绝对路径 / 符号链接恶意条目；runner.sock 不认。
func TestDesignEject(t *testing.T) {
	e := newDesignEnv(t)
	opened := e.open(orbsock.DesignOpenParams{ThreadID: testThread, Title: "官网设计"})
	id := opened.ID

	writeSrc(t, opened.Dir, "src/frames/home.tsx", `export const frame = { name: "首页", width: 1280, height: 800 };
export default function Home() { return <main>Hello Box</main>; }`)
	writeSrc(t, opened.Dir, "src/theme.css", `@theme { --color-bg: #fff; --color-text: #111; --color-accent: #0f766e; }`)
	// 放一块不可压缩随机数据，确保导出 zip 跨多页分片传输
	blob := make([]byte, 2_600_000)
	_, err := rand.Read(blob)
	require.NoError(t, err)
	blobPath := filepath.Join(opened.Dir, "src", "assets", "hero.bin")
	require.NoError(t, os.MkdirAll(filepath.Dir(blobPath), 0o755))
	require.NoError(t, os.WriteFile(blobPath, blob, 0o644))

	sealed, err := e.seal("turn-eject")
	require.NoError(t, err)
	require.Len(t, sealed.Sealed, 1)
	ver := sealed.Sealed[0].Version

	// 1. 目标目录尚不存在：完整分片拉取并解压
	outDir := filepath.Join(t.TempDir(), "ejected-app")
	var res orbsock.DesignEjectResult
	require.NoError(t, e.call(orbsock.MethodDesignEject, orbsock.DesignEjectParams{
		ThreadID: testThread, ID: id, To: outDir,
	}, &res))
	assert.Equal(t, id, res.ID)
	assert.Equal(t, ver, res.Version)
	assert.Equal(t, outDir, res.Dir)
	assert.Greater(t, res.Files, 8)

	for _, rel := range []string{
		"package.json",
		"package-lock.json",
		".oryn/box.yaml",
		"src/frames/home.tsx",
		"src/theme.css",
		"oryn-frames.json",
	} {
		assert.FileExists(t, filepath.Join(outDir, filepath.FromSlash(rel)), rel)
	}
	gotHome, err := os.ReadFile(filepath.Join(outDir, "src", "frames", "home.tsx"))
	require.NoError(t, err)
	assert.Contains(t, string(gotHome), "Hello Box")
	gotBox, err := os.ReadFile(filepath.Join(outDir, ".oryn", "box.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(gotBox), "npm ci && npm run build")
	gotBlob, err := os.ReadFile(filepath.Join(outDir, "src", "assets", "hero.bin"))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(blob, gotBlob), "跨页二进制文件应逐字节一致")

	// 2. 目标目录已存在且为空目录：允许落位并指定明确版本号
	emptyDir := filepath.Join(t.TempDir(), "empty-dir")
	require.NoError(t, os.MkdirAll(emptyDir, 0o755))
	var res2 orbsock.DesignEjectResult
	require.NoError(t, e.call(orbsock.MethodDesignEject, orbsock.DesignEjectParams{
		ThreadID: testThread, ID: id, Version: ver, To: emptyDir,
	}, &res2))
	assert.Equal(t, res.Files, res2.Files)
	assert.FileExists(t, filepath.Join(emptyDir, "package.json"))

	// 3. 覆盖非空目标目录、普通文件、相对路径、非法 id、缺 threadId 均被拒（invalid_params）
	nonEmpty := filepath.Join(t.TempDir(), "has-file")
	require.NoError(t, os.MkdirAll(nonEmpty, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "keep.txt"), []byte("keep"), 0o644))
	plainFile := filepath.Join(t.TempDir(), "plain.txt")
	require.NoError(t, os.WriteFile(plainFile, []byte("x"), 0o644))

	for _, tc := range []struct {
		name string
		p    orbsock.DesignEjectParams
	}{
		{"非空目录", orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: nonEmpty}},
		{"目标是文件", orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: plainFile}},
		{"相对路径", orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: "rel/dir"}},
		{"根目录", orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: "/"}},
		{"非法 id", orbsock.DesignEjectParams{ThreadID: testThread, ID: "../bad", To: filepath.Join(t.TempDir(), "x")}},
		{"缺 threadId", orbsock.DesignEjectParams{ID: id, To: filepath.Join(t.TempDir(), "x")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := e.call(orbsock.MethodDesignEject, tc.p, nil)
			var se *orbsock.Error
			require.ErrorAs(t, err, &se)
			assert.Equal(t, orbsock.CodeInvalidParams, se.Code)
		})
	}
	// 确认非空目录里的原文件未被破坏
	keep, err := os.ReadFile(filepath.Join(nonEmpty, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep", string(keep))

	// 4. sha256 篡改被拒且不创建目标目录
	badShaDir := filepath.Join(t.TempDir(), "bad-sha")
	tamper := func(_ json.RawMessage) (any, bool, error) {
		payload := []byte("not even matching sha")
		page := orbsock.DesignProjectPage{
			Version: ver,
			Size:    int64(len(payload)),
			Sha256:  strings.Repeat("0", 64),
			Offset:  0,
			Data:    base64.StdEncoding.EncodeToString(payload),
		}
		b, _ := json.Marshal(page)
		return apigen.LinkCtlArtifactCallResult{Text: string(b)}, true, nil
	}
	e.exportOverride.Store(&tamper)
	err = e.call(orbsock.MethodDesignEject, orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: badShaDir}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different digest")
	assert.NoDirExists(t, badShaDir)

	// 5. zip-slip / 绝对路径 / 符号链接恶意条目被拒且不落盘
	for _, tc := range []struct {
		name string
		zip  []byte
	}{
		{"父目录穿越", buildTestZip(t, testZipEntry{name: "../escaped.txt", data: "pwn"})},
		{"嵌套穿越", buildTestZip(t, testZipEntry{name: "a/../../escaped.txt", data: "pwn"})},
		{"绝对路径", buildTestZip(t, testZipEntry{name: "/tmp/escaped.txt", data: "pwn"})},
		{"符号链接", buildTestZip(t, testZipEntry{name: "link", data: "/etc/passwd", mode: os.ModeSymlink | 0o777})},
	} {
		t.Run("恶意zip/"+tc.name, func(t *testing.T) {
			rawZip := tc.zip
			sum := sha256.Sum256(rawZip)
			serveEvil := func(_ json.RawMessage) (any, bool, error) {
				page := orbsock.DesignProjectPage{
					Version: ver,
					Size:    int64(len(rawZip)),
					Sha256:  hex.EncodeToString(sum[:]),
					Offset:  0,
					Data:    base64.StdEncoding.EncodeToString(rawZip),
				}
				b, _ := json.Marshal(page)
				return apigen.LinkCtlArtifactCallResult{Text: string(b)}, true, nil
			}
			e.exportOverride.Store(&serveEvil)
			base := t.TempDir()
			dst := filepath.Join(base, "sub", "target")
			err := e.call(orbsock.MethodDesignEject, orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: dst}, nil)
			require.Error(t, err)
			assert.NoDirExists(t, dst)
			assert.NoFileExists(t, filepath.Join(base, "sub", "escaped.txt"))
			assert.NoFileExists(t, filepath.Join(base, "escaped.txt"))
		})
	}
	e.exportOverride.Store(nil)
}

// M34 S8：用户选定方案后 design.eject 出的工程只含保留下来的帧——落选帧的 src/frames/<id>.tsx 与只被落选方案用到的
// 主题不导出，已选定的组从 src/variants.json 去掉（未选定的组保留，清单仍能通过 ParseVariants），oryn-frames.json
// 的帧带 theme，styles.css 只引入打包进来的主题。
// 设了 ORYN_TEST_EJECT_BUILD=1（需要 npm 与 registry）时再在 eject 目录 npm ci && npm run build，产物里没有落选帧。
func TestDesignEjectVariants(t *testing.T) {
	e := newDesignEnv(t)
	opened := e.open(orbsock.DesignOpenParams{ThreadID: testThread, Title: "方案"})
	id := opened.ID
	frame := func(text string) string {
		return "export default function F() {\n\treturn <main className=\"min-h-screen bg-accent p-8\">" + text + "</main>;\n}\n"
	}
	writeSrc(t, opened.Dir, "src/theme.css", "@theme {\n  --color-accent: #0f766e;\n}\n")
	writeSrc(t, opened.Dir, "src/themes/calm.css", ":root[data-oryn-theme=\"calm\"] {\n  --color-accent: #0369a1;\n}\n")
	writeSrc(t, opened.Dir, "src/themes/warm.css", ":root[data-oryn-theme=\"warm\"] {\n  --color-accent: #c2410c;\n}\n")
	writeSrc(t, opened.Dir, "src/frames/about.tsx", frame("关于"))
	writeSrc(t, opened.Dir, "src/frames/home-a.tsx", frame("LOSER-HOME-A"))
	writeSrc(t, opened.Dir, "src/frames/home-b.tsx", frame("WINNER-HOME-B"))
	writeSrc(t, opened.Dir, "src/frames/cta-a.tsx", frame("CTA-A"))
	writeSrc(t, opened.Dir, "src/frames/cta-b.tsx", frame("CTA-B"))
	writeSrc(t, opened.Dir, "src/variants.json", `{"groups":[{"id":"home","name":"首页","variants":[`+
		`{"id":"a","name":"冷静","theme":"calm","frames":["home-a"]},{"id":"b","name":"暖调","theme":"warm","frames":["home-b"]}]},`+
		`{"id":"cta","name":"行动区","variants":[{"id":"a","name":"按钮","frames":["cta-a"]},{"id":"b","name":"表单","frames":["cta-b"]}]}]}`)
	_, err := e.seal("turn-variants")
	require.NoError(t, err)

	// 用户在画布上选定 b：落成一个用户版本
	ctx := context.Background()
	_, err = e.s.ApplyCanvas(ctx, id, json.RawMessage(`[{"op":"set","id":"home","key":"picked","value":"b"}]`), "tab-1", 1)
	require.NoError(t, err)
	e.s.FlushCanvases()
	st, err := e.s.CanvasState(ctx, id)
	require.NoError(t, err)
	require.Len(t, st.Doc.Archived(), 1, "home-a 的画框落选")

	outDir := filepath.Join(t.TempDir(), "ejected")
	var res orbsock.DesignEjectResult
	require.NoError(t, e.call(orbsock.MethodDesignEject, orbsock.DesignEjectParams{ThreadID: testThread, ID: id, To: outDir}, &res))
	assert.Equal(t, st.Version, res.Version)
	for _, rel := range []string{"src/frames/about.tsx", "src/frames/home-b.tsx", "src/themes/warm.css", "src/variants.json"} {
		assert.FileExists(t, filepath.Join(outDir, filepath.FromSlash(rel)), rel)
	}
	for _, rel := range []string{"src/frames/home-a.tsx", "src/themes/calm.css"} {
		assert.NoFileExists(t, filepath.Join(outDir, filepath.FromSlash(rel)), rel)
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		return string(b)
	}
	var frames, themeNames []string
	for dir, out := range map[string]*[]string{"src/frames": &frames, "src/themes": &themeNames} {
		ents, err := os.ReadDir(filepath.Join(outDir, filepath.FromSlash(dir)))
		require.NoError(t, err)
		for _, ent := range ents {
			*out = append(*out, strings.TrimSuffix(strings.TrimSuffix(ent.Name(), ".tsx"), ".css"))
		}
	}
	vf, err := design.ParseVariants([]byte(read("src/variants.json")), frames, themeNames)
	require.NoError(t, err, "导出的 variants.json 用打包进来的帧与主题能解析")
	require.Len(t, vf.Groups, 1, "已选定的 home 组整组去掉")
	assert.Equal(t, "cta", vf.Groups[0].ID)
	assert.Len(t, vf.Groups[0].Variants, 2)
	var meta struct {
		Frames []designengine.ProjectFrame `json:"frames"`
	}
	require.NoError(t, json.Unmarshal([]byte(read(designengine.FramesFile)), &meta))
	themes := map[string]string{}
	for _, f := range meta.Frames {
		themes[f.ID] = f.Theme
	}
	assert.Equal(t, map[string]string{"about": "", "home-b": "warm", "cta-a": "", "cta-b": ""}, themes, "oryn-frames.json 不列落选帧 home-a")
	styles := read("styles.css")
	assert.Contains(t, styles, "@import \"./src/theme.css\";\n@import \"./src/themes/warm.css\";\n")
	assert.NotContains(t, styles, "calm")

	if os.Getenv("ORYN_TEST_EJECT_BUILD") != "1" {
		t.Log("set ORYN_TEST_EJECT_BUILD=1 to npm ci && npm run build the ejected project")
		return
	}
	cmd := exec.Command("sh", "-c", "npm ci --no-audit --no-fund && npm run build")
	cmd.Dir = outDir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	var bundle strings.Builder
	require.NoError(t, filepath.WalkDir(filepath.Join(outDir, "dist"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		bundle.Write(b)
		return err
	}))
	assert.Contains(t, bundle.String(), "WINNER-HOME-B")
	assert.NotContains(t, bundle.String(), "LOSER-HOME-A", "构建产物里不该有落选帧")
	assert.Contains(t, bundle.String(), "#c2410c", "保留方案的主题进了样式")
	assert.NotContains(t, bundle.String(), "#0369a1", "落选方案的主题不进样式")
}

// M29 D3：runner.sock 不提供 design.eject。
func TestDesignEjectRunnerRejected(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	sock := orbsock.RunnerPath(f.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := orbsock.Call(ctx, sock, orbsock.MethodDesignEject, orbsock.DesignEjectParams{
		ThreadID: "T-1", ID: "art_1", To: filepath.Join(t.TempDir(), "out"),
	}, nil)
	var se *orbsock.Error
	require.ErrorAs(t, err, &se)
	assert.Equal(t, orbsock.CodeMethodNotFound, se.Code)
}

type testZipEntry struct {
	name string
	data string
	mode os.FileMode
}

func buildTestZip(t *testing.T, entries ...testZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.data))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
