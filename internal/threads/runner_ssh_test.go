package threads

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
)

// fakeRunnerAgent 是测试用的 RunnerAgent：按线程给出项目主机的 ssh.json，记录公钥登记与扣下文件的备注。
type fakeRunnerAgent struct {
	mu    sync.Mutex
	ssh   map[string][]byte
	known map[string]string
	keys  map[string]string // 线程 → 公钥
	orbOf map[string]string // 线程 → runner orb
	notes []withheldNote
}

type withheldNote struct {
	orbID, threadID string
	withheld        []agentcfg.Withheld
}

func newFakeRunnerAgent() *fakeRunnerAgent {
	return &fakeRunnerAgent{ssh: map[string][]byte{}, known: map[string]string{}, keys: map[string]string{}, orbOf: map[string]string{}}
}

func (r *fakeRunnerAgent) ProjectSSH(_ context.Context, threadID string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ssh[threadID], nil
}

func (r *fakeRunnerAgent) KnownSecrets(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.known, nil
}

func (r *fakeRunnerAgent) NoteWithheld(_ context.Context, orbID, threadID string, w []agentcfg.Withheld) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, withheldNote{orbID, threadID, w})
}

func (r *fakeRunnerAgent) RegisterThreadKey(threadID, orbID, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[threadID], r.orbOf[threadID] = key, orbID
	return nil
}

func (r *fakeRunnerAgent) DropThreadKey(threadID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, threadID)
}

func (r *fakeRunnerAgent) DropOrbKeys(orbID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for th, o := range r.orbOf {
		if o == orbID {
			delete(r.keys, th)
		}
	}
}

func (r *fakeRunnerAgent) key(threadID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keys[threadID]
}

func filePaths(fs []apigen.LinkCtlOmpHomeFile) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

// M21：runner 线程的项目层带上该线程项目主机合成的 ssh.json；交付前过一遍无密钥投递校验——中央配置项目层里形似密钥的
// 串、等于某个已知密钥值的串所在的文件不进 projectConfig，备注写出路径（不含密钥）；干净文件照常交付。orb 的项目层
// （setup / omphome.apply）不经这里。
func TestRunnerProjectConfigScreensSecrets(t *testing.T) {
	layers := projLayers{"prj_a": {
		"mcp.json":   []byte(`{"mcpServers":{"x":{"headers":{"Authorization":"Bearer sk-test0123456789abcdefXYZ"}}}}`),
		"config.yml": []byte("note: vault-value-for-test\n"),
		"AGENTS.md":  []byte("A rules"),
	}}
	f := newFixture(t, Config{Projects: layers})
	f.projectWithID("prj_a")
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	ra := newFakeRunnerAgent()
	ra.known["db/token"] = "vault-value-for-test"
	f.s.SetRunnerAgent(ra)
	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", orbs.FeatureOmpHomeApply, "ssh"}
	th := f.threadIn(runner, "prj_a")
	sshJSON := []byte(`{"hosts":{"pa-db":{"host":"127.0.0.1","port":"${ORYN_SSH_PORT}","username":"pa-db","description":"d"}}}`)
	ra.ssh[th.id] = sshJSON

	p := startParams(t, f, o, th)
	require.NotNil(t, p.ProjectConfig)
	assert.Equal(t, []string{"AGENTS.md", "ssh.json"}, filePaths(p.ProjectConfig.Files))
	for _, fl := range p.ProjectConfig.Files {
		if fl.Path == "ssh.json" {
			assert.Equal(t, base64.StdEncoding.EncodeToString(sshJSON), fl.ContentB64)
			assert.Equal(t, 0o600, fl.Mode)
		}
	}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), base64.StdEncoding.EncodeToString(layers["prj_a"]["mcp.json"]))
	assert.NotContains(t, string(raw), base64.StdEncoding.EncodeToString(layers["prj_a"]["config.yml"]))

	ra.mu.Lock()
	require.Len(t, ra.notes, 1)
	n := ra.notes[0]
	ra.mu.Unlock()
	assert.Equal(t, runner, n.orbID)
	assert.Equal(t, th.id, n.threadID)
	require.Len(t, n.withheld, 2)
	assert.Equal(t, "config.yml", n.withheld[0].Path)
	assert.Equal(t, "包含密钥 db/token 的值", n.withheld[0].Reason)
	assert.Equal(t, "mcp.json", n.withheld[1].Path)
	for _, w := range n.withheld {
		assert.NotContains(t, w.Reason, "vault-value-for-test")
		assert.NotContains(t, w.Reason, "sk-test0123456789abcdefXYZ")
	}
	assert.Len(t, layers["prj_a"], 3, "中央存储的项目层不被改动")

	// 只有 ssh.json：项目层照样交付
	f.projectWithID("prj_b")
	thb := f.threadIn(runner, "prj_b")
	ra.ssh[thb.id] = sshJSON
	p = startParams(t, f, o, thb)
	require.NotNil(t, p.ProjectConfig)
	assert.Equal(t, []string{"ssh.json"}, filePaths(p.ProjectConfig.Files))
}

// M21：runner 线程的 SSH 公钥跟 orbd 的监督器生灭——agent.start 结果带的公钥登记（幂等的重复 agent.start 同值刷新）；
// 会自重启的崩溃保留，重启次数用尽、有意停止、线程删除时注销；旧监督器迟到的结束（晚于新 agent.start 的应答、早于新
// 监督器的 starting / ready）不动新公钥；runner 离线（→ stopped）时清掉整台的登记，link 断开本身不清。
func TestRunnerThreadKeyLifecycle(t *testing.T) {
	f := newFixture(t, Config{})
	f.projectWithID("prj_a")
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	ra := newFakeRunnerAgent()
	f.s.SetRunnerAgent(ra)
	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", orbs.FeatureOmpHomeApply, "ssh"}
	o.startPub = "ssh-ed25519 AAAAkey1"
	th := f.threadIn(runner, "prj_a")

	start := func() {
		t.Helper()
		startParams(t, f, o, th)
		require.Eventually(t, func() bool { return ra.key(th.id) == o.startPub }, 5*time.Second, 10*time.Millisecond)
	}
	status := func(st string, left *int) {
		t.Helper()
		b, err := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: apigen.LinkCtlAgentStatusParamsStatus(st), RestartsLeft: left})
		require.NoError(t, err)
		f.s.onAgentStatus(f.ctx, runner, b)
	}
	start()
	ra.mu.Lock()
	assert.Equal(t, runner, ra.orbOf[th.id])
	ra.mu.Unlock()

	// 会自重启的崩溃：保留
	status("starting", nil)
	status("exited", new(2))
	assert.Equal(t, "ssh-ed25519 AAAAkey1", ra.key(th.id))
	// 幂等 agent.start（例如 link 宽限期内重连）回同一把：同值刷新
	start()
	// 重启次数用尽：注销
	status("starting", nil)
	status("exited", new(0))
	assert.Empty(t, ra.key(th.id))

	// 新监督器、新公钥；有意停止（不带 restartsLeft）：注销
	o.startPub = "ssh-ed25519 AAAAkey2"
	start()
	status("starting", nil)
	status("exited", nil)
	assert.Empty(t, ra.key(th.id))

	// 竞争：新 agent.start 已应答并登记新公钥之后，旧监督器的结束（restartsLeft=0 或不带）才到达——新公钥仍有效；
	// 新监督器报过 starting 之后的结束照常注销
	o.startPub = "ssh-ed25519 AAAAkey4"
	start()
	status("exited", new(0))
	assert.Equal(t, "ssh-ed25519 AAAAkey4", ra.key(th.id), "旧监督器迟到的结束不误删新公钥")
	status("exited", nil)
	assert.Equal(t, "ssh-ed25519 AAAAkey4", ra.key(th.id))
	status("starting", nil)
	status("exited", new(0))
	assert.Empty(t, ra.key(th.id))
	o.startPub = "ssh-ed25519 AAAAkey2"

	// link 断开不清；runner 离线定时器到期（→ stopped）清整台
	start()
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: runner, Kind: OrbEventLinkDown})
	assert.Equal(t, "ssh-ed25519 AAAAkey2", ra.key(th.id))
	f.s.handleOrbEvent(f.ctx, OrbEvent{OrbID: runner, Kind: OrbEventState, From: "ready", To: "stopped", Seq: 1 << 40})
	assert.Empty(t, ra.key(th.id))
	// 归档：注销（网关随之关闭经它认证的连接）；agent 仍在运行时取消归档，幂等 agent.start 取回同一把公钥重新登记
	yes, no := true, false
	start()
	status("starting", nil)
	status("ready", nil)
	_, err := f.s.Patch(f.ctx, th.id, apigen.ThreadPatch{Archived: &yes})
	require.NoError(t, err)
	assert.Empty(t, ra.key(th.id), "归档即注销")
	_, err = f.s.Patch(f.ctx, th.id, apigen.ThreadPatch{Archived: &no})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return ra.key(th.id) == o.startPub }, 5*time.Second, 10*time.Millisecond, "取消归档后重新登记")
	// 幂等 agent.start 时 orbd 只唤醒现有监督器、不重报 starting / ready：它之后的结束仍归这一代，照常注销
	require.Eventually(t, func() bool { th.mu.Lock(); defer th.mu.Unlock(); return !th.starting }, 5*time.Second, 10*time.Millisecond)
	status("exited", nil)
	assert.Empty(t, ra.key(th.id), "取消归档重新登记后，同一代监督器的结束仍注销公钥")
	// 标记已交付顺带归档：同样注销
	o.startPub = "ssh-ed25519 AAAAkey5"
	start()
	status("starting", nil)
	status("ready", nil)
	_, err = f.s.Patch(f.ctx, th.id, apigen.ThreadPatch{Shipped: &yes})
	require.NoError(t, err)
	assert.Empty(t, ra.key(th.id), "已交付顺带归档即注销")

	// 线程删除：注销
	require.NoError(t, ra.RegisterThreadKey(th.id, runner, "ssh-ed25519 AAAAkey3"))
	require.NoError(t, f.s.Delete(f.ctx, th.id))
	assert.Empty(t, ra.key(th.id))
}

// M21：orbd 在应答 agent.start 之前就发出的终止结束（与应答处理竞争）——新一代报过 starting 后就结束、同一代（幂等
// agent.start，不重报状态）结束时，应答都不能把已结束的一代登记回去；旧一代迟到的结束不妨碍新一代登记。
func TestRunnerThreadKeyExitBeforeStartReply(t *testing.T) {
	f := newFixture(t, Config{})
	f.projectWithID("prj_a")
	o := newCtlOrbs(t, f.orbs)
	mo := &memOrbs{Orbs: o, kinds: map[string]string{}, feats: map[string][]string{}}
	f.s.orbs = mo
	ra := newFakeRunnerAgent()
	f.s.SetRunnerAgent(ra)
	runner := f.orb("ready")
	mo.kinds[runner], mo.feats[runner] = orbs.KindRunner, []string{"fs", orbs.FeatureOmpHomeApply, "ssh"}
	th := f.threadIn(runner, "prj_a")
	status := func(st string, left *int) {
		b, err := json.Marshal(apigen.LinkCtlAgentStatusParams{ThreadID: th.id, Status: apigen.LinkCtlAgentStatusParamsStatus(st), RestartsLeft: left})
		require.NoError(t, err)
		f.s.onAgentStatus(f.ctx, runner, b)
	}
	startWith := func(pub string, hook func()) {
		t.Helper()
		o.mu.Lock()
		o.startPub, o.startHook = pub, hook
		o.mu.Unlock()
		startParams(t, f, o, th)
	}

	// 新一代报过 starting 就结束，应答随后才处理：不登记
	startWith("ssh-ed25519 AAAAgen1", func() { status("starting", nil); status("exited", nil) })
	assert.Empty(t, ra.key(th.id), "已结束的新一代不登记")

	// 同一代：正常登记并就绪后，幂等 agent.start 期间它结束（orbd 不重报 starting / ready）：不登记回去
	startWith("ssh-ed25519 AAAAgen2", func() { status("starting", nil); status("ready", nil) })
	assert.Equal(t, "ssh-ed25519 AAAAgen2", ra.key(th.id))
	startWith("ssh-ed25519 AAAAgen2", func() { status("exited", nil) })
	assert.Empty(t, ra.key(th.id), "已结束的同一代不登记回去")

	// 旧一代迟到的结束（新一代报 starting 之前）：新一代照常登记
	startWith("ssh-ed25519 AAAAgen3", func() { status("starting", nil); status("ready", nil) })
	startWith("ssh-ed25519 AAAAgen4", func() { status("exited", new(0)); status("starting", nil) })
	assert.Equal(t, "ssh-ed25519 AAAAgen4", ra.key(th.id), "旧一代迟到的结束不妨碍新一代登记")
}
