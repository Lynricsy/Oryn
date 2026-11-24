package orbdrt

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	"github.com/Lynricsy/Oryn/internal/designengine"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// fakeVite 顶替引擎的 vite：`build` 时按 src/frames/*.tsx 写出 dist/<id>.html 与 dist/frames.json（src/FAIL 存在时往
// stderr 写一大段再失败）；否则在 PORT 上起一个回显工作副本与 sandbox 目录的 dev 服务。
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
http.createServer((q, s) => s.end("dev " + work + " " + process.env.ORYN_DESIGN_SANDBOX)).listen(Number(process.env.PORT), "127.0.0.1");
`

// designEnv 是 orbd + 一个跑真实 artifacts.Service 的假控制面：ctl artifact.call 交给 Service 的 orb 方法，source 经
// library.fetch 从 orbd 取回。
type designEnv struct {
	*svcEnv
	s     *artifacts.Service
	store *store.Store
	sock  string
}

// cpFetch 经 ctl library.fetch 从 orbd 取回 source（控制面的 SourceFetcher）。
type cpFetch struct {
	cp  *fakeCP
	ctl func() *link.Ctl
	n   atomic.Int64
}

func (f *cpFetch) Fetch(ctx context.Context, _, path string) (io.ReadCloser, int64, error) {
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
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodArtifactCall) {
			return nil, false, nil
		}
		res, err := call(context.Background(), orbID, params)
		return res, true, err
	}

	e := &designEnv{svcEnv: newSvcEnv(t, cp, ""), s: s, store: st}
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
