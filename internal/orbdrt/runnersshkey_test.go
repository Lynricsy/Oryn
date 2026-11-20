package orbdrt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M21：runner 上每个线程的 SSH 密钥跟监督器生灭——创建监督器时生成，私钥在 `threads/<线程>/ssh/<公钥指纹前 12 位>/
// id_ed25519`（0600、两级目录 0700），每代监督器路径不同；agent 环境的 ORYN_SSH_KEY / ORYN_SSH_PORT 不可被下发的 env
// 覆盖；幂等 agent.start 与崩溃自重启沿用同一把（同一路径）；停止后该子目录删除，再启动换一把新的、换一个路径。
func TestRunnerThreadSSHKey(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	port := f.rt.sshPort()
	require.NotZero(t, port)
	dump := filepath.Join(f.home, "dump")
	sshDir := filepath.Join(f.home, ".oryn", "threads", thread1, "ssh")
	keyPathOf := func(pub string) string {
		t.Helper()
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
		require.NoError(t, err)
		sum := sha256.Sum256(k.Marshal())
		return filepath.Join(sshDir, hex.EncodeToString(sum[:])[:12], "id_ed25519")
	}
	start := func() apigen.LinkCtlAgentStartResult {
		t.Helper()
		var res apigen.LinkCtlAgentStartResult
		require.NoError(t, f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
			ThreadID: thread1, Cwd: f.work, SessionDir: "~/.oryn/threads/" + thread1,
			Env:        map[string]string{"FAKEOMP_DUMP_DIR": dump, "ORYN_SSH_KEY": "/evil/key", "ORYN_SSH_PORT": "1"},
			InitFrames: initFrames(thread1),
		}, &res))
		require.NotNil(t, res.SSHPublicKey)
		return res
	}
	dumpedKey := func(want string) {
		t.Helper()
		var rec agentDump
		require.Eventually(t, func() bool {
			b, err := os.ReadFile(filepath.Join(dump, thread1+".json"))
			return err == nil && json.Unmarshal(b, &rec) == nil && rec.Env["ORYN_SSH_KEY"] == want
		}, 10*time.Second, 20*time.Millisecond, "ORYN_SSH_KEY 应为 %s（下发的 env 覆盖不了）", want)
		assert.Equal(t, strconv.Itoa(port), rec.Env["ORYN_SSH_PORT"])
	}

	r1 := start()
	keyPath := keyPathOf(*r1.SSHPublicKey)
	i, _ := f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	fi, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	for _, d := range []string{filepath.Dir(keyPath), sshDir} {
		di, err := os.Stat(d)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), di.Mode().Perm(), d)
	}
	priv := readFile(t, keyPath)
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	require.NoError(t, err)
	assert.Equal(t, ssh.KeyAlgoED25519, signer.PublicKey().Type())
	assert.Equal(t, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), *r1.SSHPublicKey)
	dumpedKey(keyPath)

	// 幂等：同一把公钥，私钥文件不变
	r2 := start()
	assert.Equal(t, r1.Pid, r2.Pid)
	assert.Equal(t, *r1.SSHPublicKey, *r2.SSHPublicKey)
	assert.Equal(t, priv, readFile(t, keyPath))

	// 崩溃自重启沿用同一把、同一路径
	require.NoError(t, os.Remove(filepath.Join(dump, thread1+".json")))
	require.NoError(t, syscall.Kill(r1.Pid, syscall.SIGKILL))
	j, _ := f.cp.waitNotif(t, i, "agent.status", statusIs(thread1, "exited"))
	f.cp.waitNotif(t, j, "agent.status", statusIs(thread1, "ready"))
	assert.Equal(t, priv, readFile(t, keyPath))
	dumpedKey(keyPath)
	r3 := start()
	assert.NotEqual(t, r1.Pid, r3.Pid)
	assert.Equal(t, *r1.SSHPublicKey, *r3.SSHPublicKey)

	// 停止：监督器结束，该代的子目录删除；再启动换新密钥、新路径
	require.NoError(t, f.cp.call(t, "agent.stop", apigen.LinkCtlAgentStopParams{ThreadID: thread1, GraceSeconds: 5}, nil))
	assert.NoDirExists(t, filepath.Dir(keyPath))
	r4 := start()
	assert.NotEqual(t, *r1.SSHPublicKey, *r4.SSHPublicKey)
	key4 := keyPathOf(*r4.SSHPublicKey)
	assert.NotEqual(t, keyPath, key4)
	assert.FileExists(t, key4)
	dumpedKey(key4)
}

// M21：agent.start 带来的项目层 `.omp/ssh.json` 里的主机名并入 runner 的 SSH 配置（bash 用的 ~/.oryn/ssh/config 与
// Core 的 -F 同一份），全局主机照旧；本机 OpenSSH 在 ORYN_SSH_KEY 设置时把 IdentityFile 展开成该线程的私钥。
func TestRunnerProjectSSHConfig(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{
		OmpHome: []apigen.LinkCtlOmpHomeFile{}, SSH: sshCfg(apigen.LinkCtlSSHHost{Name: "web", Description: "g"}),
	}, nil))
	port := f.rt.sshPort()
	pid := "prj_a"
	ompHosts := `{"hosts":{"pa-db":{"host":"127.0.0.1","port":"${ORYN_SSH_PORT}","username":"pa-db","description":"d"}}}`
	var res apigen.LinkCtlAgentStartResult
	require.NoError(t, f.cp.call(t, "agent.start", apigen.LinkCtlAgentStartParams{
		ThreadID: thread1, Cwd: f.work, SessionDir: "~/.oryn/threads/" + thread1, InitFrames: initFrames(thread1),
		ProjectID: &pid, ProjectConfig: &apigen.LinkCtlProjectConfig{Files: []apigen.LinkCtlOmpHomeFile{ompFile("ssh.json", ompHosts)}},
	}, &res))
	f.cp.waitNotif(t, 0, "agent.status", statusIs(thread1, "ready"))
	cfgPath := filepath.Join(f.home, ".oryn", "ssh", "config")
	cfg := readFile(t, cfgPath)
	assert.Contains(t, cfg, "\nHost pa-db\n  HostName 127.0.0.1\n  Port "+strconv.Itoa(port)+"\n  User pa-db\n  IdentityFile ${ORYN_SSH_KEY}\n")
	assert.Contains(t, cfg, "\nHost web\n")
	assert.Equal(t, cfg, readFile(t, filepath.Join(f.home, ".oryn", "omp", "ssh", "config")))

	// 后来的全局下发不丢项目主机
	require.NoError(t, f.cp.call(t, string(apigen.MethodOmpHomeApply), apigen.LinkCtlOmpHomeApplyParams{
		OmpHome: []apigen.LinkCtlOmpHomeFile{}, SSH: sshCfg(apigen.LinkCtlSSHHost{Name: "web2", Description: "g"}),
	}, nil))
	cfg = readFile(t, cfgPath)
	assert.Contains(t, cfg, "\nHost pa-db\n")
	assert.Contains(t, cfg, "\nHost web2\n")
	assert.NotContains(t, cfg, "\nHost web\n")

	// 本机 OpenSSH 解析：`-G` 原样列出 `${ORYN_SSH_KEY}`（展开发生在载入身份时），用 -v 看载入的身份文件；
	// 端口改成 1，连接被拒，不会连到网关。
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("没有 ssh")
	}
	key := filepath.Join(f.home, ".oryn", "threads", thread1, "ssh", "id_ed25519")
	for _, target := range [][]string{{"pa-db"}, {"pa-db@127.0.0.1"}} {
		args := append([]string{"-F", cfgPath, "-v", "-p", "1", "-o", "BatchMode=yes"}, target...)
		cmd := exec.Command("ssh", append(args, "true")...)
		cmd.Env = append(os.Environ(), "ORYN_SSH_KEY="+key)
		out, _ := cmd.CombinedOutput()
		assert.Contains(t, string(out), "identity file "+key+" type ", "%v: %s", target, out)
	}
}
