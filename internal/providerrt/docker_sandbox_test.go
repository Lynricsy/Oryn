package providerrt

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/unixsock"
)

func newSandboxDocker(t *testing.T) (*Docker, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sbx")
	d := NewDocker(config.Docker{
		Image: "oryn-orb:dev", DefaultType: "small", PidsLimit: 4096,
		// extra_run_args 是给 orb 的，沙箱不追加（这里的 --network host 与自带 seccomp 都不该出现）。
		ExtraRunArgs: []string{"--network", "host", "--security-opt", "seccomp=unconfined"},
		Types: map[string]config.DockerType{
			"small": {CPUs: 2, Memory: "4g"},
			"dind":  {CPUs: 4, Memory: "8g", Runtime: config.SysboxRuntime, NestedDocker: true},
		},
		Sandbox: config.DockerSandbox{Enabled: true, Network: "oryn-sbx", DNS: []string{"1.1.1.1", "9.9.9.9"}, SocketDir: dir},
	})
	t.Cleanup(d.relays.closeAll)
	return d, dir
}

func sandboxSpawn(orbID, typ, orynURL string, network apigen.SandboxNetwork) Job {
	img := "ignored:img"
	return Job{Op: "spawn", OrbID: orbID, Spawn: &apigen.JobSpawnParams{
		Type: typ, Image: img,
		Env:     apigen.JobSpawnEnv{OrynURL: orynURL, OrynOrbID: orbID, OrynOrbToken: "oot_tok"},
		Sandbox: &apigen.JobSpawnSandbox{Network: network},
	}}
}

// 新建沙箱容器的 argv 逐字固定（计划 2.6）；中继目录 0755、socket 0666，经它连得到 ORYN_URL 的 host:port。
func TestDockerSandboxSpawnArgs(t *testing.T) {
	fd := newFakeDocker(t)
	d, dir := newSandboxDocker(t)
	ctx := context.Background()
	target := echoServer(t)

	fd.set(t, "missing", false)
	require.NoError(t, execErr(d, ctx, sandboxSpawn("orb-x", "small", "http://"+target, apigen.SandboxNetworkNone), io.Discard))
	log := fd.log(t)
	assert.Equal(t, "create --init --name orb-x --hostname orb-x --label oryn-orb=1"+
		" --label oryn-sandbox=none --label oryn-sandbox-link="+target+
		" --cpus 2 --memory 4g --pids-limit 4096 --shm-size 1g --cap-drop NET_RAW --cap-drop NET_ADMIN"+
		" --network none -v "+dir+"/orb-x:/run/oryn-link:ro"+
		" -e ORYN_URL -e ORYN_ORB_ID -e ORYN_ORB_TOKEN -e ORYN_SANDBOX -e ORYN_LINK_VIA"+
		" --security-opt seccomp=PROFILE oryn-orb:dev orbd run", log[1])
	assert.Equal(t, "ENV http://"+target+" orb-x oot_tok sandbox=1 via=unix:/run/oryn-link/link.sock", log[2])
	assert.Equal(t, "start orb-x", log[len(log)-1])

	st, err := os.Stat(filepath.Join(dir, "orb-x"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), st.Mode().Perm())
	sock := filepath.Join(dir, "orb-x", "link.sock")
	st, err = os.Stat(sock)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSocket, st.Mode().Type())
	assert.Equal(t, os.FileMode(0o666), st.Mode().Perm())
	c, err := unixsock.Dial(ctx, sock)
	require.NoError(t, err)
	roundTrip(t, c, "relayed")
	_ = c.Close()

	// egress + 嵌套 Docker 类型：接入沙箱网络并带 --dns；runtime 照旧；不加 --cap-drop。
	fd.set(t, "missing", false)
	require.NoError(t, execErr(d, ctx, sandboxSpawn("orb-y", "dind", "https://172.18.0.1:7643", apigen.SandboxNetworkEgress), io.Discard))
	log = fd.log(t)
	assert.Equal(t, "create --init --name orb-y --hostname orb-y --label oryn-orb=1"+
		" --label oryn-sandbox=egress --label oryn-sandbox-link=172.18.0.1:7643"+
		" --cpus 4 --memory 8g --pids-limit 4096 --shm-size 1g"+
		" --network oryn-sbx --dns 1.1.1.1 --dns 9.9.9.9 -v "+dir+"/orb-y:/run/oryn-link:ro"+
		" -e ORYN_URL -e ORYN_ORB_ID -e ORYN_ORB_TOKEN -e ORYN_NESTED_DOCKER -e ORYN_SANDBOX -e ORYN_LINK_VIA"+
		" --runtime sysbox-runc --security-opt seccomp=PROFILE oryn-orb:dev orbd run", log[1])
	assert.Equal(t, "ENV https://172.18.0.1:7643 orb-y oot_tok nested=1 sandbox=1 via=unix:/run/oryn-link/link.sock", log[2])

	// 已有容器：分支不变（不 create），中继沿用（同一目标不重建）。
	before := d.relays.m["orb-x"]
	fd.set(t, "running", false)
	require.NoError(t, execErr(d, ctx, sandboxSpawn("orb-x", "small", "http://"+target, apigen.SandboxNetworkNone), io.Discard))
	assert.NotContains(t, strings.Join(fd.log(t), "\n"), "create ")
	assert.Same(t, before, d.relays.m["orb-x"])
}

// 未开启沙箱：spawn 报错、Info 不声明；开启后声明 capabilities.sandbox。
func TestDockerSandboxDisabled(t *testing.T) {
	fd := newFakeDocker(t)
	fd.set(t, "missing", false)
	d := NewDocker(config.Docker{Image: "oryn-orb:dev", DefaultType: "small"})
	err := execErr(d, context.Background(), sandboxSpawn("orb-x", "small", "http://u", apigen.SandboxNetworkNone), io.Discard)
	assert.EqualError(t, err, "docker: sandbox is not enabled on this provider")
	assert.NoFileExists(t, filepath.Join(fd.dir, "log"), "报错先于任何 docker 命令")
	assert.Nil(t, d.Info().Capabilities.Sandbox)

	on, _ := newSandboxDocker(t)
	assert.Equal(t, new(true), on.Info().Capabilities.Sandbox)
}

// set_network：非沙箱报错；none 沙箱开网 → network_unavailable；已处于目标不做事；否则 connect / disconnect。
func TestDockerSandboxSetNetwork(t *testing.T) {
	fd := newFakeDocker(t)
	d, _ := newSandboxDocker(t)
	run := func(inspect, network string) ([]string, error) {
		fd.set(t, "running", false)
		fd.write(t, "sandbox-inspect", inspect+"\n")
		err := execErr(d, context.Background(), Job{Op: "set_network", OrbID: "orb-x", Network: network}, io.Discard)
		var cmds []string
		for _, l := range fd.log(t) {
			if strings.HasPrefix(l, "network ") {
				cmds = append(cmds, l)
			}
		}
		return cmds, err
	}

	cmds, err := run(`|{"bridge":{}}`, "none")
	assert.ErrorContains(t, err, "不是沙箱")
	assert.Empty(t, cmds)
	assert.Equal(t, `container inspect --format {{index .Config.Labels "oryn-sandbox"}}|{{json .NetworkSettings.Networks}} orb-x`, fd.log(t)[0])

	cmds, err = run(`none|{"none":{}}`, "egress")
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "network_unavailable:"), err.Error())
	assert.Empty(t, cmds)
	cmds, err = run(`none|{"none":{}}`, "none")
	require.NoError(t, err)
	assert.Empty(t, cmds)

	cmds, err = run(`egress|{"oryn-sbx":{"IPAddress":"172.29.0.2"}}`, "egress")
	require.NoError(t, err)
	assert.Empty(t, cmds, "已接入：不做事")
	cmds, err = run(`egress|{"oryn-sbx":{"IPAddress":"172.29.0.2"}}`, "none")
	require.NoError(t, err)
	assert.Equal(t, []string{"network disconnect -f oryn-sbx orb-x"}, cmds)

	cmds, err = run(`egress|{}`, "egress")
	require.NoError(t, err)
	assert.Equal(t, []string{"network connect oryn-sbx orb-x"}, cmds)
	cmds, err = run(`egress|{}`, "none")
	require.NoError(t, err)
	assert.Empty(t, cmds, "已断开：不做事")

	// 容器不存在：报错。
	fd.set(t, "missing", false)
	assert.Error(t, execErr(d, context.Background(), Job{Op: "set_network", OrbID: "orb-x", Network: "none"}, io.Discard))
}

// delete：rm -f 之后关中继、删 <socket_dir>/<id>；幂等。
func TestDockerSandboxDelete(t *testing.T) {
	fd := newFakeDocker(t)
	d, dir := newSandboxDocker(t)
	ctx := context.Background()
	fd.set(t, "missing", false)
	require.NoError(t, execErr(d, ctx, sandboxSpawn("orb-x", "small", "http://127.0.0.1:9", apigen.SandboxNetworkNone), io.Discard))
	require.DirExists(t, filepath.Join(dir, "orb-x"))
	r := d.relays.m["orb-x"]
	require.NotNil(t, r)

	fd.set(t, "running", false)
	require.NoError(t, execErr(d, ctx, Job{Op: "delete", OrbID: "orb-x"}, io.Discard))
	assert.Equal(t, []string{"rm -f orb-x"}, fd.log(t))
	assert.NoDirExists(t, filepath.Join(dir, "orb-x"))
	assert.True(t, r.isClosed())
	assert.DirExists(t, dir)
	require.NoError(t, execErr(d, ctx, Job{Op: "delete", OrbID: "orb-x"}, io.Discard))
}

// Preflight：沙箱网络不存在时报错并指向建网脚本；通过时建好 socket_dir（0700）。
func TestDockerSandboxPreflight(t *testing.T) {
	fd := newFakeDocker(t)
	d, dir := newSandboxDocker(t)
	fd.set(t, "running", false)
	fd.write(t, "fail.network", "")
	err := d.Preflight(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deploy/docker/oryn-sbx-net.sh up")
	assert.NoDirExists(t, dir)

	fd.set(t, "running", false)
	fd.write(t, "runtimes", `{"runc":{},"sysbox-runc":{"runtimeArgs":[]}}`)
	require.NoError(t, d.Preflight(context.Background()))
	assert.Contains(t, fd.log(t), "network inspect oryn-sbx")
	st, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
}

// Housekeep：按 docker ps 的标签为在运行的沙箱重建中继（provider 重启后恢复），ctx 结束时全部关闭。
func TestDockerSandboxHousekeep(t *testing.T) {
	fd := newFakeDocker(t)
	d, dir := newSandboxDocker(t)
	target := echoServer(t)
	fd.set(t, "running", false)
	fd.write(t, "ps", "orb-a|"+target+"\norb-b|\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Housekeep(ctx); close(done) }()
	sock := filepath.Join(dir, "orb-a", "link.sock")
	require.Eventually(t, func() bool { return d.relays.has("orb-a", target) }, 5*time.Second, 10*time.Millisecond)
	c, err := unixsock.Dial(context.Background(), sock)
	require.NoError(t, err)
	roundTrip(t, c, "restored")
	_ = c.Close()
	assert.Contains(t, fd.log(t), `ps --filter label=oryn-sandbox --format {{.Names}}|{{.Label "oryn-sandbox-link"}}`)
	assert.NoDirExists(t, filepath.Join(dir, "orb-b"), "没有中继目标的容器跳过")

	r := d.relays.m["orb-a"]
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Housekeep 没有随 ctx 结束返回")
	}
	assert.True(t, r.isClosed())
}
