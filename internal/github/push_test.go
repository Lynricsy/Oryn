package github

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// kickRecorder 记下被 Kick 的项目。
type kickRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (k *kickRecorder) Kick(id string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ids = append(k.ids, id)
}

func (k *kickRecorder) got() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.ids...)
}

func pushEvent(repoID int64) map[string]any {
	return map[string]any{"ref": "refs/heads/main", "repository": map[string]any{"id": repoID}}
}

func TestPushKicksRepoSync(t *testing.T) {
	assert.Contains(t, hookEvents, "push")

	e := newEnv(t)
	// 没注入镜像：忽略
	g := e.deliver("push", pushEvent(42))
	assert.Equal(t, "ignored:no_repo_sync", e.delivery(g).Outcome)

	k := &kickRecorder{}
	e.s.SetRepoSync(k)
	// 仓库没有绑定项目
	g = e.deliver("push", pushEvent(42))
	assert.Equal(t, "ignored:no_project", e.delivery(g).Outcome)
	assert.Empty(t, k.got())

	p := e.boundProject()
	g = e.deliver("push", pushEvent(42))
	assert.Equal(t, "handled", e.delivery(g).Outcome)
	assert.Equal(t, []string{p}, k.got())

	// 载荷缺仓库 id
	g = e.deliver("push", map[string]any{"ref": "refs/heads/main"})
	assert.Equal(t, "ignored:payload", e.delivery(g).Outcome)
	assert.Len(t, k.got(), 1)
}

func TestPushSkipsArchivedProject(t *testing.T) {
	e := newEnv(t)
	k := &kickRecorder{}
	e.s.SetRepoSync(k)
	p := e.boundProject()
	prj := e.getProject(p)
	_, err := e.st.Q.UpdateProject(e.ctx, db.UpdateProjectParams{
		Name: prj.Name, RepoUrl: prj.RepoUrl, ArchivedAt: new(int64(3)), UpdatedAt: 3, ID: p,
	})
	require.NoError(t, err)
	g := e.deliver("push", pushEvent(42))
	assert.Equal(t, "ignored:no_project", e.delivery(g).Outcome)
	assert.Empty(t, k.got())
}
