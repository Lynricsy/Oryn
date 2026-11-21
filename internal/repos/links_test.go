package repos

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// thread 建项目的一个线程（repo_pushes 与 repo_commit_threads 引用它）。
func (e *testEnv) thread(t *testing.T, projectID string) string {
	t.Helper()
	tid := ids.Thread()
	if err := e.st.Q.CreateThread(context.Background(), db.CreateThreadParams{ID: tid, ProjectID: &projectID, Title: "t", TitleSource: "auto",
		State: "idle", SessionDir: "d", CreatedAt: 1, UpdatedAt: 1, Kind: "work"}); err != nil {
		t.Fatal(err)
	}
	return tid
}

// links 返回项目的提交 → 线程关联（sha → thread|source）。
func (e *testEnv) links(t *testing.T, projectID string) map[string]string {
	t.Helper()
	rows, err := e.st.DB.QueryContext(context.Background(),
		`SELECT sha, COALESCE(thread_id, ''), source FROM repo_commit_threads WHERE project_id = ?`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var sha, tid, src string
		if err := rows.Scan(&sha, &tid, &src); err != nil {
			t.Fatal(err)
		}
		out[sha] = tid + "|" + src
	}
	return out
}

// pushes 返回项目的推送记录（ref → expand_note，未展开为 "pending"，已展开无备注为 ""）。
func (e *testEnv) pushes(t *testing.T, projectID string) map[string]string {
	t.Helper()
	rows, err := e.st.DB.QueryContext(context.Background(),
		`SELECT ref, CASE WHEN expanded_at IS NULL THEN 'pending' ELSE COALESCE(expand_note, '') END FROM repo_pushes WHERE project_id = ?`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ref, st string
		if err := rows.Scan(&ref, &st); err != nil {
			t.Fatal(err)
		}
		out[ref] = st
	}
	return out
}

func (e *testEnv) push(projectID, threadID, host, repo string, refs ...egress.RefUpdate) {
	e.svc.GitPushed(context.Background(), orbs.PushedRefs{OrbID: "orb-1", ProjectID: projectID, ThreadID: threadID, Host: host, Repo: repo, Refs: refs})
}

func TestPushLinksNewBranch(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	bare := e.fake.BarePath("acme/widgets")
	prj := e.project(t, "https://github.com/acme/widgets")
	tid := e.thread(t, prj)
	e.settle(t, prj)

	// 线程在 feat 上提交两次，推送 000…0 → c2；c1、c2 不在 main 上
	commitTo(t, bare, "feat", map[string]string{"a.txt": "1"}, "c1")
	c1 := gitCmd(t, bare, "rev-parse", "refs/heads/feat")
	commitTo(t, bare, "feat", map[string]string{"a.txt": "2"}, "c2")
	c2 := gitCmd(t, bare, "rev-parse", "refs/heads/feat")

	// 仓库不符、标签与删除都不记
	e.push(prj, tid, "github.com", "acme/other", egress.RefUpdate{Old: zeroSHA, New: c2, Ref: "refs/heads/feat"})
	e.push(prj, tid, "git.example", "acme/widgets", egress.RefUpdate{Old: zeroSHA, New: c2, Ref: "refs/heads/feat"})
	e.push(prj, tid, "github.com", "acme/widgets",
		egress.RefUpdate{Old: zeroSHA, New: c2, Ref: "refs/tags/v1"},
		egress.RefUpdate{Old: c2, New: zeroSHA, Ref: "refs/heads/gone"})
	if got := e.pushes(t, prj); len(got) != 0 {
		t.Fatalf("不该入库：%v", got)
	}

	e.push(prj, tid, "github.com", "acme/widgets", egress.RefUpdate{Old: zeroSHA, New: c2, Ref: "refs/heads/feat"})
	e.waitIdle(t, prj) // GitPushed 已 Kick 一次拉取
	e.sync(t, prj)
	want := map[string]string{c1: tid + "|push", c2: tid + "|push"}
	if got := e.links(t, prj); len(got) != 2 || got[c1] != want[c1] || got[c2] != want[c2] {
		t.Fatalf("links = %v，want %v", got, want)
	}
	if got := e.pushes(t, prj); got["refs/heads/feat"] != "" {
		t.Fatalf("推送应已展开：%v", got)
	}
}

func TestPushLinksFastForward(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	bare := e.fake.BarePath("acme/widgets")
	prj := e.project(t, "https://github.com/acme/widgets")
	tid := e.thread(t, prj)
	e.settle(t, prj)
	old := gitCmd(t, bare, "rev-parse", "refs/heads/main")
	commitTo(t, bare, "main", map[string]string{"b.txt": "1"}, "m1")
	m1 := gitCmd(t, bare, "rev-parse", "refs/heads/main")

	e.push(prj, tid, "github.com", "acme/widgets", egress.RefUpdate{Old: old, New: m1, Ref: "refs/heads/main"})
	e.waitIdle(t, prj)
	e.sync(t, prj)
	if got := e.links(t, prj); len(got) != 1 || got[m1] != tid+"|push" {
		t.Fatalf("links = %v，只应有 m1", got)
	}
}

// 新提交一直没出现在镜像里：24 h 内保持待展开，之后记 missing。
func TestPushLinksMissing(t *testing.T) {
	e := newTestEnv(t, nil, nil, nil)
	prj := e.project(t, "https://github.com/acme/widgets")
	tid := e.thread(t, prj)
	e.settle(t, prj)
	ghost := strings.Repeat("ab", 20)
	e.push(prj, tid, "github.com", "acme/widgets", egress.RefUpdate{Old: zeroSHA, New: ghost, Ref: "refs/heads/feat"})
	e.waitIdle(t, prj)
	e.sync(t, prj)
	if got := e.pushes(t, prj)["refs/heads/feat"]; got != "pending" {
		t.Fatalf("推送 = %q，want pending", got)
	}
	e.svc.sweepPushes(context.Background())
	if got := e.pushes(t, prj)["refs/heads/feat"]; got != "pending" {
		t.Fatalf("24 h 内推送 = %q，want pending", got)
	}
	e.clock.Advance(25 * time.Hour)
	e.svc.sweepPushes(context.Background())
	if got := e.pushes(t, prj)["refs/heads/feat"]; got != "missing" {
		t.Fatalf("推送 = %q，want missing", got)
	}
	if got := e.links(t, prj); len(got) != 0 {
		t.Fatalf("不该有关联：%v", got)
	}
	e.clock.Advance(31 * 24 * time.Hour)
	e.svc.sweepPushes(context.Background())
	if got := e.pushes(t, prj); len(got) != 0 {
		t.Fatalf("30 天前的推送记录应删除：%v", got)
	}
}
