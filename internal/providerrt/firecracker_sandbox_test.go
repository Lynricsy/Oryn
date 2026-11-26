package providerrt

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/config"
	"github.com/Lynricsy/Oryn/internal/fcguest"
)

func sandboxOn(cfg *config.Firecracker) { cfg.Sandbox.Enabled = true }

func fcSandbox(network apigen.SandboxNetwork) func(*apigen.JobSpawnParams) {
	return func(p *apigen.JobSpawnParams) { p.Sandbox = &apigen.JobSpawnSandbox{Network: network} }
}

// none 沙箱：没有 tap、jailer 之后建 link 中继监听、start 带 sandbox 与 linkVia 且不带 DNS；不能开网；delete 关中继。
func TestFCSandboxNone(t *testing.T) {
	c := newFCTest(t, sandboxOn)
	t.Cleanup(c.d.relays.closeAll)
	c.must(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkNone)))
	assert.Equal(t, []string{
		"MakeDisk disk.ext4.tmp 40 2000000000",
		"Launch " + orbA,
		"LinkListen " + orbA,
		"Boot " + orbA,
		"guest start " + orbA,
	}, c.f.take())
	m := c.meta(orbA)
	assert.True(t, m.Sandbox)
	assert.True(t, m.NoNIC)
	assert.Equal(t, "172.31.255.1:7643", m.LinkTarget)
	assert.NotContains(t, fcBootArgs(m), "ip=")
	assert.True(t, c.d.relays.has(orbA, "172.31.255.1:7643"))
	start, ok := c.f.lastReq(fcguest.OpStart)
	require.True(t, ok)
	assert.Equal(t, fcguest.Request{
		Op: fcguest.OpStart, URL: "https://172.31.255.1:7643", OrbID: orbA, Token: "oot_secret_" + orbA,
		NowMs: fcTestNow.UnixMilli(), Sandbox: true, LinkVia: "vsock:5006",
	}, start)

	// 已在运行：只发 start，中继不重建。
	r := c.d.relays.m[orbA]
	c.must(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkNone)))
	assert.Equal(t, []string{"guest start " + orbA}, c.f.take())
	assert.Same(t, r, c.d.relays.m[orbA])

	_, err := c.exec(Job{Op: "set_network", OrbID: orbA, Network: "egress"})
	require.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "network_unavailable:"), err.Error())
	c.must(Job{Op: "set_network", OrbID: orbA, Network: "none"})
	assert.Empty(t, c.f.take(), "没有网卡：none 不做事")

	c.must(Job{Op: "delete", OrbID: orbA})
	assert.False(t, c.d.relays.has(orbA, "172.31.255.1:7643"))
	assert.True(t, r.isClosed())
}

// egress 沙箱有 tap 与 eth0；set_network 把 tap 置 down / up；普通 VM 不能 set_network。
func TestFCSandboxEgress(t *testing.T) {
	c := newFCTest(t, sandboxOn)
	t.Cleanup(c.d.relays.closeAll)
	c.must(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkEgress)))
	assert.Equal(t, []string{
		"MakeDisk disk.ext4.tmp 40 2000000000",
		"TapUp " + tapName(orbA),
		"Launch " + orbA,
		"LinkListen " + orbA,
		"Boot " + orbA,
		"guest start " + orbA,
	}, c.f.take())
	m := c.meta(orbA)
	assert.True(t, m.Sandbox)
	assert.False(t, m.NoNIC)
	assert.Contains(t, fcBootArgs(m), " ip="+m.GuestIP+"::")
	start, _ := c.f.lastReq(fcguest.OpStart)
	assert.Equal(t, []string{"1.1.1.1", "1.0.0.1"}, start.DNS)
	assert.True(t, start.Sandbox)
	assert.Equal(t, "vsock:5006", start.LinkVia)

	c.must(Job{Op: "set_network", OrbID: orbA, Network: "none"})
	assert.Equal(t, []string{"TapLink " + tapName(orbA) + " down"}, c.f.take())
	c.must(Job{Op: "set_network", OrbID: orbA, Network: "egress"})
	assert.Equal(t, []string{"TapLink " + tapName(orbA) + " up"}, c.f.take())

	c.must(fcSpawn(orbB))
	c.f.take()
	_, err := c.exec(Job{Op: "set_network", OrbID: orbB, Network: "none"})
	assert.ErrorContains(t, err, "不是沙箱")
	_, err = c.exec(Job{Op: "set_network", OrbID: "orb-missing", Network: "none"})
	assert.ErrorContains(t, err, "不是沙箱")
	assert.Empty(t, c.f.take())
	assert.False(t, c.meta(orbB).Sandbox)
}

// 未开启沙箱：spawn 报错且不建盘、不占槽位；Info 按开关声明 capabilities.sandbox。
func TestFCSandboxDisabled(t *testing.T) {
	c := newFCTest(t)
	_, err := c.exec(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkNone)))
	assert.EqualError(t, err, "firecracker: sandbox is not enabled on this provider")
	assert.Empty(t, c.f.take())
	assert.NoDirExists(t, c.d.paths.vmDir(orbA))
	assert.Nil(t, c.d.Info().Capabilities.Sandbox)
	assert.Equal(t, new(true), newFCTest(t, sandboxOn).d.Info().Capabilities.Sandbox)
}

// 启动失败时连同中继一起清理。
func TestFCSandboxBootFailure(t *testing.T) {
	c := newFCTest(t, sandboxOn)
	t.Cleanup(c.d.relays.closeAll)
	c.f.startRemote = true
	_, err := c.exec(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkNone)))
	require.Error(t, err)
	assert.False(t, c.d.relays.has(orbA, "172.31.255.1:7643"))
}

// provider 重启后 Housekeep 为在运行的沙箱 VM 重建中继；停机的跳过。
func TestFCSandboxRestoreRelays(t *testing.T) {
	c := newFCTest(t, sandboxOn)
	t.Cleanup(c.d.relays.closeAll)
	const orbC = "orb-01jcccccccccccccccccccccccc"
	c.must(fcSpawn(orbA, fcSandbox(apigen.SandboxNetworkEgress)))
	c.must(fcSpawn(orbB, fcSandbox(apigen.SandboxNetworkNone)))
	c.must(fcSpawn(orbC))
	c.must(Job{Op: "pause", OrbID: orbB, PauseMode: "stop"})
	c.f.take()

	// 新的驱动实例（同一状态目录与宿主）：内存里没有中继。
	d2 := NewFirecracker(c.d.Cfg, c.d.OrbURL)
	d2.host, d2.api, d2.guest = c.f, c.f, c.f
	t.Cleanup(d2.relays.closeAll)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d2.Housekeep(ctx)
	assert.Equal(t, []string{"LinkListen " + orbA}, c.f.take())
	assert.False(t, d2.relays.has(orbA, "172.31.255.1:7643"), "Housekeep 返回时关闭全部中继")

	d2.restoreRelays()
	assert.True(t, d2.relays.has(orbA, "172.31.255.1:7643"))
	assert.False(t, d2.relays.has(orbB, "172.31.255.1:7643"))
}

// 没有网卡的 VM：Boot 不配 eth0，boot_args 不带 ip=。
func TestFCAPIBootNoNIC(t *testing.T) {
	root, err := os.MkdirTemp("", "fcapi")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	p := fcPaths{root: root}
	const orb = "orb-1"
	got := serveFCAPI(t, p.apiSock(orb), nil, "Running")
	vm := newVMMeta(config.DefaultFirecracker(), orb, 3)
	vm.CPUs, vm.MemMB, vm.Sandbox, vm.NoNIC = 2, 1024, true, true
	require.NoError(t, unixAPI{sock: p.apiSock}.Boot(context.Background(), vm))
	var paths []string
	for _, r := range *got {
		paths = append(paths, r.Path)
	}
	assert.Equal(t, []string{"/machine-config", "/boot-source", "/drives/rootfs", "/drives/data", "/vsock", "/serial", "/entropy", "/actions"}, paths)
	assert.Equal(t, `{"boot_args":"console=ttyS0 reboot=k panic=1 pci=off nomodule i8042.noaux i8042.nomux i8042.dumbkbd quiet root=/dev/vda ro init=/usr/local/bin/orbd","kernel_image_path":"/vmlinux"}`, (*got)[1].Body)
}
