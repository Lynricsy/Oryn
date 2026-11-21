package repos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/ids"
)

const widgetsURL = "https://github.com/acme/widgets"

func TestSyncCloneFetchPrune(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)

	if st := e.status(t, id); st.State != StateCloning || st.Kind != KindMirror {
		t.Fatalf("首次 Status = %+v，想要 cloning", st)
	}
	e.waitIdle(t, id)
	st := e.status(t, id)
	if st.State != StateReady || deref(st.DefaultBranch) != "main" || deref(st.BaseBranch) != "main" || deref(st.SizeBytes) <= 0 || st.Empty || st.SyncedAt == nil {
		t.Fatalf("克隆后 Status = %+v", st)
	}
	if deref(st.CloneURL) != widgetsURL || st.LastError != nil || st.Syncing {
		t.Fatalf("克隆后 Status = %+v", st)
	}
	dir := e.svc.dir(id)
	if head := gitCmd(t, dir, "symbolic-ref", "HEAD"); head != "refs/heads/main" {
		t.Fatalf("HEAD = %q", head)
	}

	bare := e.fake.BarePath("acme/widgets")
	commitTo(t, bare, "b", map[string]string{"b.txt": "b\n"}, "branch b")
	e.sync(t, id)
	if !e.svc.hasBranch(context.Background(), dir, "b") {
		t.Fatal("同步后镜像里没有 refs/heads/b")
	}

	gitCmd(t, bare, "update-ref", "-d", "refs/heads/b")
	e.sync(t, id)
	if e.svc.hasBranch(context.Background(), dir, "b") {
		t.Fatal("上游删除 b 后镜像仍有 refs/heads/b")
	}
}

func TestStatusStaleAfter(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)
	e.settle(t, id)

	before := e.infoRefs()
	for range 3 {
		if st := e.status(t, id); st.Syncing {
			t.Fatalf("StaleAfter 内的 Status 触发了拉取：%+v", st)
		}
	}
	if got := e.infoRefs(); got != before {
		t.Fatalf("StaleAfter 内 info/refs 调用 %d → %d", before, got)
	}

	e.clock.Advance(2 * time.Minute)
	if st := e.status(t, id); !st.Syncing || st.State != StateReady {
		t.Fatalf("过期后 Status = %+v，想要 ready+syncing", st)
	}
	e.waitIdle(t, id)
	if got := e.infoRefs(); got <= before {
		t.Fatalf("过期后没有拉取：info/refs 调用 %d → %d", before, got)
	}
}

func TestArchivedNoAutoClone(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)
	e.exec(t, `UPDATE projects SET archived_at = 1 WHERE id = ?`, id)

	if st := e.status(t, id); st.State != StateAbsent {
		t.Fatalf("归档项目 Status = %+v，想要 absent", st)
	}
	e.waitIdle(t, id)
	if n := e.infoRefs(); n != 0 {
		t.Fatalf("归档项目自动拉取了 %d 次", n)
	}
	if st := e.sync(t, id); st.State != StateReady {
		t.Fatalf("归档项目手动同步后 Status = %+v", st)
	}
}

func TestNoRepoAndSyncErrors(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, "")
	if st := e.status(t, id); st.Kind != KindNone || st.State != StateAbsent || !st.Available {
		t.Fatalf("无仓库 Status = %+v", st)
	}
	if _, err := e.svc.Sync(context.Background(), id); err != errNoRepo {
		t.Fatalf("无仓库 Sync err = %v", err)
	}
	if _, err := e.svc.Status(context.Background(), "prj_nope"); err != errNotFound {
		t.Fatalf("不存在的项目 err = %v", err)
	}
}

func TestAddressChange(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)
	e.exec(t, `UPDATE projects SET github_repo_id = 42 WHERE id = ?`, id)
	e.settle(t, id)
	dir := e.svc.dir(id)

	// GitHub 改名：绑定的仓库 id 不变 → 只更新 remote_url
	e.exec(t, `UPDATE projects SET repo_url = 'https://github.com/acme/gadgets' WHERE id = ?`, id)
	e.svc.ProjectChanged(context.Background(), id)
	if !exists(dir) {
		t.Fatal("改名跟随时镜像被删了")
	}
	if r := e.row(t, id); r == nil || deref(r.RemoteUrl) != "https://github.com/acme/gadgets" {
		t.Fatalf("改名后 row = %+v", r)
	}

	// 换成另一个仓库 → 删除镜像
	e.exec(t, `UPDATE projects SET repo_url = 'https://github.com/acme/other', github_repo_id = 7 WHERE id = ?`, id)
	e.svc.ProjectChanged(context.Background(), id)
	if exists(dir) || e.row(t, id) != nil {
		t.Fatal("改成另一个仓库后镜像还在")
	}

	// 清空仓库地址同样删除
	id2 := e.project(t, widgetsURL)
	e.settle(t, id2)
	e.exec(t, `UPDATE projects SET repo_url = NULL WHERE id = ?`, id2)
	if st := e.status(t, id2); st.Kind != KindNone {
		t.Fatalf("清空地址后 Status = %+v", st)
	}
	if exists(e.svc.dir(id2)) || e.row(t, id2) != nil {
		t.Fatal("清空仓库地址后镜像还在")
	}
}

func TestRemove(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)
	e.settle(t, id)
	e.svc.Remove(id)
	if exists(e.svc.dir(id)) || e.row(t, id) != nil {
		t.Fatal("Remove 后目录或行还在")
	}
}

func TestTooLarge(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MaxBytes = 1 }, nil, nil)
	id := e.project(t, widgetsURL)
	st := e.settle(t, id)
	if st.State != StateError || st.LastError == nil || st.LastError.Code != "too_large" {
		t.Fatalf("超限 Status = %+v", st)
	}
	if exists(e.svc.dir(id)) {
		t.Fatal("超限的镜像没有删除")
	}
	// 有 error 时不自动重试
	before := e.infoRefs()
	e.status(t, id)
	e.waitIdle(t, id)
	if e.infoRefs() != before {
		t.Fatal("首次克隆失败后自动重试了")
	}
}

func TestNoSpace(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.MinFreeBytes = 1 << 62 }, nil, nil)
	id := e.project(t, widgetsURL)
	st := e.sync(t, id)
	if st.LastError == nil || st.LastError.Code != "no_space" || st.State != StateError {
		t.Fatalf("磁盘不足 Status = %+v", st)
	}
	if n := e.infoRefs(); n != 0 {
		t.Fatalf("磁盘不足时仍发起了 %d 次拉取", n)
	}
}

func TestHostedMarkerProtected(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	root := e.svc.cfg.Dir
	hostedID, mirrorID := ids.New("prj"), ids.New("prj")
	hosted := filepath.Join(root, hostedID+".git")
	mirror := filepath.Join(root, mirrorID+".git")
	for _, d := range []string{hosted, mirror} {
		if err := os.MkdirAll(filepath.Join(d, "refs", "heads"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(hosted, hostedMarker), []byte(hostedID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeRepoDir(hosted, false); err == nil || !exists(hosted) {
		t.Fatalf("removeRepoDir 删了托管正本：err=%v", err)
	}
	tmp := filepath.Join(root, ids.New("prj")+".git.tmp-abc")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}

	e.svc.sweep(context.Background()) // Run 的启动清扫

	if exists(hosted) || exists(mirror) || exists(tmp) {
		t.Fatal("清扫后无主目录仍在原处")
	}
	moved, _ := filepath.Glob(filepath.Join(root, "orphaned", hostedID+".git-*"))
	if len(moved) != 1 || !exists(filepath.Join(moved[0], hostedMarker)) {
		t.Fatalf("托管正本没有移到 orphaned/：%v", moved)
	}
}

func TestStaleLocks(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	id := e.project(t, widgetsURL)
	dir := e.svc.dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "refs", "heads"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	paths := map[string]bool{ // 路径 → 应当被删
		filepath.Join(dir, "refs", "heads", "x.lock"): true,
		filepath.Join(dir, "HEAD.lock"):               true,
		filepath.Join(dir, "refs", "heads", "y.lock"): false,
	}
	for p, stale := range paths {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if stale {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.svc.sweep(context.Background()) // Run 的启动清扫
	for p, stale := range paths {
		if exists(p) == stale {
			t.Errorf("%s：存在=%v，想要被删=%v", p, exists(p), stale)
		}
	}
	if !exists(dir) {
		t.Fatal("有项目的仓库被清扫删除了")
	}
}

func TestGitMissing(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.Git = "/nonexistent/git" }, nil, nil)
	if ok, reason := e.svc.Available(); ok || reason != "git_missing" {
		t.Fatalf("Available() = %v, %q", ok, reason)
	}
	id := e.project(t, widgetsURL)
	if st := e.status(t, id); st.Available || deref(st.UnavailableReason) != "git_missing" || st.State != StateAbsent {
		t.Fatalf("git 缺失时 Status = %+v", st)
	}
	if _, err := e.svc.Sync(context.Background(), id); err != errUnavailable {
		t.Fatalf("git 缺失时 Sync err = %v", err)
	}
}

func TestValidRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"main": true, "feature/x": true, "v1.0": true,
		"-x": false, "a..b": false, "a//b": false, "a/": false, "x.lock": false, "": false, strings.Repeat("a", 202): false,
	} {
		if got := ValidRef(ref); got != want {
			t.Errorf("ValidRef(%q) = %v，想要 %v", ref, got, want)
		}
	}
}
