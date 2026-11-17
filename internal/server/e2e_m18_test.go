package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/orbsock"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// M18 验收（docs/plan-m18.md「Verification」第 3 条）：真实 orynd（newStack：process 驱动的 orbd + fakeomp）、
// 进程内假目标机（sshgwtest），orb 里用真实 OpenSSH 客户端经 orbd 的 7822 回环端口 → link 的 ssh 流 → 控制面网关连到目标机。
// 子用例按顺序共享一套环境，前一步失败时停止。

type m18Env struct {
	*stack
	ctx    context.Context
	prj    string
	key    apigen.SSHKey
	target *sshgwtest.FakeSSHD
	web1   apigen.SSHHost
	o      orbCtx
	port   int
	root   *testing.T // 整套用例的 t：假目标机要活过单个子用例
}

func TestE2EM18(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("没有 ssh 可执行文件")
	}
	s := newStack(t, config.Idle{AgentIdle: config.Duration(time.Hour), UserIdle: config.Duration(time.Hour), StopAfterFrozen: config.Duration(24 * time.Hour)})
	e := &m18Env{stack: s, ctx: context.Background(), root: t}
	steps := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"K1钥匙主机与测试", e.stepKeyHostTest},
		{"K2orb内SSH配置", e.stepOrbConfig},
		{"K3经网关执行", e.stepExec},
		{"K4全自动线程", e.stepAutomation},
		{"K5主机变化下发", e.stepRedeliver},
		{"K6跳板", e.stepJump},
		{"K7指纹变化与重置", e.stepHostKeyChange},
		{"K8集中管理拦截", e.stepManaged},
		{"K9ssh_exec_many", e.stepExecMany},
		{"K10SFTP", e.stepSFTP},
		{"K11网页终端", e.stepTerminal},
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

// ---------------------------------------------------------------- 步骤

// K1：生成钥匙、授权给假目标机、登记项目主机 web1 并测试：成功，指纹即目标机的，审计有 test 行。
func (e *m18Env) stepKeyHostTest(t *testing.T) {
	var prj struct {
		ID string `json:"id"`
	}
	e.rest("POST", "/api/v1/projects", map[string]any{"name": "ssh-e2e"}, &prj, 201)
	e.prj = prj.ID
	e.rest("POST", "/api/v1/ssh/keys", map[string]any{"name": "main", "mode": "generate"}, &e.key, 201)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(e.key.PublicKey))
	require.NoError(t, err)
	e.target = sshgwtest.NewFakeSSHD(e.root, sshgwtest.Options{AuthorizedKeys: []ssh.PublicKey{pub}})
	addr, port := e.target.Addr()
	e.rest("POST", "/api/v1/ssh/hosts", map[string]any{
		"name": "web1", "address": addr, "port": port, "username": "deploy", "authType": "key", "keyId": e.key.ID,
		"projectId": e.prj,
	}, &e.web1, 201)

	var res apigen.SSHTestResult
	e.rest("POST", "/api/v1/ssh/hosts/"+e.web1.ID+"/test", nil, &res, 200)
	require.True(t, res.Ok, "测试失败：%v", res.Error)
	require.NotNil(t, res.HostKeyFingerprint)
	assert.Equal(t, e.target.HostKeyFingerprint(), *res.HostKeyFingerprint)
	assert.True(t, e.hasAudit(t, func(a apigen.SSHAuditEntry) bool { return a.Kind == "test" && a.Ok }), "审计有 test 行")
}

// K2：项目线程的 orb 就绪后，orb 里有 ssh.json、known_hosts 与 ~/.oryn/ssh/config。
func (e *m18Env) stepOrbConfig(t *testing.T) {
	var created struct {
		Thread threadView `json:"thread"`
	}
	e.rest("POST", "/api/v1/threads", map[string]any{
		"prompt": "hello", "projectId": e.prj,
		"executor": map[string]any{"kind": "new_orb", "provider": "local", "type": "small"},
	}, &created, 201)
	th := e.waitThread(created.Thread.ID, 90*time.Second, func(v threadView) bool {
		return (v.State == "idle" || v.State == "awaiting_input") && v.OrbID != ""
	})
	e.o = orbCtx{thread: th.ID, orb: th.OrbID, home: e.drv.HomeDir(th.OrbID)}

	var cfg struct {
		Hosts map[string]struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
		} `json:"hosts"`
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		b, err := os.ReadFile(filepath.Join(e.o.home, ".omp", "agent", "ssh.json"))
		require.NoError(c, err)
		require.NoError(c, json.Unmarshal(b, &cfg))
		require.Contains(c, cfg.Hosts, "web1")
	}, 10*time.Second, 50*time.Millisecond)
	h := cfg.Hosts["web1"]
	assert.Equal(t, "127.0.0.1", h.Host)
	assert.Equal(t, "web1", h.Username)
	require.NotZero(t, h.Port)
	e.port = h.Port

	kh, err := os.ReadFile(filepath.Join(e.o.home, ".ssh", "known_hosts"))
	require.NoError(t, err)
	var found bool
	for l := range strings.SplitSeq(string(kh), "\n") {
		if strings.HasPrefix(l, "[127.0.0.1]:"+strconv.Itoa(e.port)+" ") && strings.HasSuffix(l, " oryn-ssh-gateway") {
			found = true
		}
	}
	assert.True(t, found, "known_hosts 有网关条目：%s", kh)
	oc, err := os.ReadFile(filepath.Join(e.o.home, ".oryn", "ssh", "config"))
	require.NoError(t, err)
	assert.Contains(t, string(oc), "Host web1")
}

// K3：orb 的 known_hosts + 真实 ssh 客户端执行命令：输出与退出码原样，审计记 orb 的 exec。
func (e *m18Env) stepExec(t *testing.T) {
	out, stderr, code := e.ssh(t, "echo hi; exit 3")
	assert.Equal(t, "hi\n", out, stderr)
	assert.Equal(t, 3, code, stderr)
	assert.True(t, e.hasAudit(t, func(a apigen.SSHAuditEntry) bool {
		return a.Source == "orb" && a.Kind == "exec" && a.Detail == "echo hi; exit 3" && a.ExitCode != nil && *a.ExitCode == 3 &&
			a.OrbID != nil && *a.OrbID == e.o.orb && a.ThreadID != nil && *a.ThreadID == e.o.thread
	}), "审计有带 orb 与线程的 exec 行")
}

// K4：orb 标为全自动后被拒（stderr 带原因、审计 deny）；主机放开 allowAutomation 后成功。
func (e *m18Env) stepAutomation(t *testing.T) {
	require.NoError(t, e.srv.Store.Q.SetOrbAutomation(e.ctx, db.SetOrbAutomationParams{ID: e.o.orb, Automation: true}))
	_, stderr, code := e.ssh(t, "echo hi; exit 3")
	assert.NotEqual(t, 3, code)
	assert.Contains(t, stderr, "不允许全自动线程使用")
	assert.True(t, e.hasAudit(t, func(a apigen.SSHAuditEntry) bool { return a.Kind == "deny" }))

	e.rest("PATCH", "/api/v1/ssh/hosts/"+e.web1.ID, map[string]any{"allowAutomation": true}, nil, 200)
	out, _, code := e.ssh(t, "echo hi; exit 3")
	assert.Equal(t, "hi\n", out)
	assert.Equal(t, 3, code)
}

// K5：改描述后 5 秒内 orb 里的 ssh.json 跟着变。
func (e *m18Env) stepRedeliver(t *testing.T) {
	e.rest("PATCH", "/api/v1/ssh/hosts/"+e.web1.ID, map[string]any{"description": "front door"}, nil, 200)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		b, err := os.ReadFile(filepath.Join(e.o.home, ".omp", "agent", "ssh.json"))
		require.NoError(c, err)
		assert.Contains(c, string(b), "front door")
	}, 5*time.Second, 50*time.Millisecond)
}

// K6：web1 经跳板 bastion 连接：成功，bastion 收到一条 direct-tcpip。
func (e *m18Env) stepJump(t *testing.T) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(e.key.PublicKey))
	require.NoError(t, err)
	bastion := sshgwtest.NewFakeSSHD(e.root, sshgwtest.Options{AuthorizedKeys: []ssh.PublicKey{pub}, AllowDirectTCPIP: true})
	addr, port := bastion.Addr()
	var b apigen.SSHHost
	e.rest("POST", "/api/v1/ssh/hosts", map[string]any{
		"name": "bastion", "address": addr, "port": port, "username": "jump", "authType": "key", "keyId": e.key.ID,
	}, &b, 201)
	e.rest("PATCH", "/api/v1/ssh/hosts/"+e.web1.ID, map[string]any{"jumpHostId": b.ID}, nil, 200)
	out, _, code := e.ssh(t, "echo hi; exit 3")
	assert.Equal(t, "hi\n", out)
	assert.Equal(t, 3, code)
	assert.Equal(t, 1, bastion.DirectTCPIPCount())
}

// K7：目标机换主机密钥后被拒（stderr 带「指纹变了」）；重置指纹后成功并固定新指纹。
func (e *m18Env) stepHostKeyChange(t *testing.T) {
	e.target.SetHostKey(sshgwtest.RandomSigner(t))
	_, stderr, code := e.ssh(t, "echo hi; exit 3")
	assert.NotEqual(t, 3, code)
	assert.Contains(t, stderr, "指纹变了")

	e.rest("POST", "/api/v1/ssh/hosts/"+e.web1.ID+"/reset-host-key", nil, nil, 200)
	out, _, code := e.ssh(t, "echo hi; exit 3")
	assert.Equal(t, "hi\n", out)
	assert.Equal(t, 3, code)
	var h apigen.SSHHost
	e.rest("GET", "/api/v1/ssh/hosts/"+e.web1.ID, nil, &h, 200)
	require.NotNil(t, h.HostKey)
	assert.Equal(t, e.target.HostKeyFingerprint(), h.HostKey.Fingerprint)
}

// K8：orb 线程里的 ssh_manage 与 `/ssh add` 都回 managed_by_oryn。
func (e *m18Env) stepManaged(t *testing.T) {
	w := e.dial()
	for i, frame := range []map[string]any{
		{"type": "ssh_manage", "action": "create", "name": "x", "host": "y"},
		{"type": "prompt", "message": "/ssh add x --host y"},
	} {
		id := "m" + strconv.Itoa(i)
		w.send(map[string]any{"type": "cmd", "threadId": e.o.thread, "id": id, "frame": frame})
		w.until(10*time.Second, func(m wsMsg) bool {
			if m.Type == "error" && m.ID == id {
				assert.Equal(t, "managed_by_oryn", m.Code)
				return true
			}
			return false
		})
	}
}

// K9：orb 内经 orbd.sock 调 ssh_exec_many：web1 成功、未登记的主机失败，审计记 tool 来源。
func (e *m18Env) stepExecMany(t *testing.T) {
	ctx, cancel := context.WithTimeout(e.ctx, time.Minute)
	defer cancel()
	raw, err := orbsock.Call(ctx, orbsock.Path(e.o.home), orbsock.MethodSshCall, map[string]any{
		"threadId": e.o.thread, "tool": "ssh_exec_many", "params": map[string]any{"hosts": []string{"web1", "nope"}, "command": "echo ok"},
	}, nil)
	require.NoError(t, err)
	var res apigen.LinkCtlSSHCallResult
	require.NoError(t, json.Unmarshal(raw, &res))
	assert.Contains(t, res.Text, "## web1 — exit 0")
	assert.Contains(t, res.Text, "## nope — 失败：")
	assert.True(t, e.hasAudit(t, func(a apigen.SSHAuditEntry) bool { return a.Source == "tool" }))
}

// K10：SFTP 写、读回、If-Match 不符时 412。
func (e *m18Env) stepSFTP(t *testing.T) {
	var roots struct {
		Home string `json:"home"`
	}
	e.rest("GET", "/api/v1/ssh/hosts/"+e.web1.ID+"/fs/roots", nil, &roots, 200)
	require.NotEmpty(t, roots.Home)
	dir := t.TempDir() // 假目标机与测试同机：写到测试自己的临时目录
	p := filepath.Join(dir, "a.txt")
	base := e.http.URL + "/api/v1/ssh/hosts/" + e.web1.ID + "/fs/file?path=" + p
	resp := e.rawDo(t, "PUT", base, []byte("x"), nil)
	require.Less(t, resp.StatusCode, 300, resp.Status)
	resp = e.rawDo(t, "GET", base, nil, nil)
	require.Equal(t, 200, resp.StatusCode)
	b, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "x", string(b))
	resp = e.rawDo(t, "PUT", base+"&overwrite=true", []byte("y"), map[string]string{"If-Match": `"1-1"`})
	assert.Equal(t, http.StatusPreconditionFailed, resp.StatusCode)
	var er struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&er)
	assert.Equal(t, "modified", er.Code)
}

// K11：网页终端：发 `echo oryn-term`，5 秒内收到含它的输出。
func (e *m18Env) stepTerminal(t *testing.T) {
	var tk struct {
		Ticket string `json:"ticket"`
	}
	e.rest("POST", "/api/v1/ws-ticket", nil, &tk, 200)
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	u := strings.Replace(e.http.URL, "http", "ws", 1) + "/api/v1/ssh/hosts/" + e.web1.ID + "/terminal?ticket=" + tk.Ticket
	c, _, err := websocket.Dial(ctx, u, nil)
	require.NoError(t, err)
	defer func() { _ = c.CloseNow() }()
	require.NoError(t, c.Write(ctx, websocket.MessageBinary, []byte("echo oryn-term\n")))
	var got bytes.Buffer
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(got.String(), "oryn-term\n") && !strings.Contains(got.String(), "oryn-term\r\n") {
		require.True(t, time.Now().Before(deadline), "没收到输出：%q", got.String())
		rctx, rcancel := context.WithDeadline(ctx, deadline)
		typ, b, err := c.Read(rctx)
		rcancel()
		require.NoError(t, err, "已收到：%q", got.String())
		assert.Equal(t, websocket.MessageBinary, typ)
		got.Write(b)
	}
}

// ---------------------------------------------------------------- 辅助

// ssh 在 orb 的 HOME 下用真实 OpenSSH 客户端（Core 的用法：BatchMode、accept-new）经网关登录 web1 执行 cmd。
func (e *m18Env) ssh(t *testing.T, cmd string) (stdout, stderr string, code int) {
	t.Helper()
	c := exec.Command("ssh", "-F", "/dev/null",
		"-o", "UserKnownHostsFile="+filepath.Join(e.o.home, ".ssh", "known_hosts"),
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=20",
		"-p", strconv.Itoa(e.port), "web1@127.0.0.1", cmd)
	// UTF-8 locale：否则 OpenSSH 把横幅里的中文转义成 \ooo
	c.Env = []string{"HOME=" + e.o.home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C.UTF-8"}
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		require.NoError(t, err)
	}
	return out.String(), errOut.String(), code
}

func (e *m18Env) hasAudit(t *testing.T, fn func(apigen.SSHAuditEntry) bool) bool {
	t.Helper()
	var l apigen.SSHAuditList
	e.rest("GET", "/api/v1/ssh/audit?limit=200", nil, &l, 200)
	for _, a := range l.Entries {
		if fn(a) {
			return true
		}
	}
	return false
}

func (e *m18Env) rawDo(t *testing.T, method, url string, body []byte, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
