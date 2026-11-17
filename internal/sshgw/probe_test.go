package sshgw

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

const linuxProbe = `Linux
__ORYN__
cpu  100 0 100 700 100 0 0 0 0 0
cpu  150 0 150 750 150 0 0 0 0 0
__ORYN__
MemTotal:        8000000 kB
MemFree:         1000000 kB
MemAvailable:    6000000 kB
Buffers:          100000 kB
__ORYN__
0.52 0.40 0.30 1/234 5678
__ORYN__
Filesystem     1024-blocks      Used Available Capacity Mounted on
/dev/sda1         10000000   4000000   6000000      40% /
tmpfs              1000000         0   1000000       0% /run
/dev/sdb1         20000000   5000000  15000000      25% /data disk
`

const macProbe = `Darwin
__ORYN__

__ORYN__

__ORYN__

__ORYN__
Filesystem   1024-blocks      Used Available Capacity  Mounted on
/dev/disk3s1s1  482797652  10000000 300000000     4%    /
devfs                 200       200         0   100%    /dev
`

func TestParseSnapshotLinux(t *testing.T) {
	osName, snap := parseSnapshot(linuxProbe)
	require.Equal(t, "linux", osName)
	require.NotNil(t, snap)
	// Δ总 = 200，Δ(idle+iowait) = 100 → 50%
	require.NotNil(t, snap.CPUPct)
	require.InDelta(t, 50.0, *snap.CPUPct, 0.01)
	require.Equal(t, int64(8000000), *snap.MemTotalKb)
	require.Equal(t, int64(2000000), *snap.MemUsedKb)
	require.InDelta(t, 0.52, *snap.Load1, 0.001)
	require.Len(t, snap.Disks, 2)
	require.Equal(t, "/", snap.Disks[0].Mount)
	require.Equal(t, int64(4000000), snap.Disks[0].UsedKb)
	require.Equal(t, int64(10000000), snap.Disks[0].TotalKb)
	require.Equal(t, "/data disk", snap.Disks[1].Mount)
}

func TestParseSnapshotMacOS(t *testing.T) {
	osName, snap := parseSnapshot(macProbe)
	require.Equal(t, "macos", osName)
	require.NotNil(t, snap)
	require.Nil(t, snap.CPUPct)
	require.Nil(t, snap.MemTotalKb)
	require.Nil(t, snap.Load1)
	require.Len(t, snap.Disks, 1)
	require.Equal(t, "/", snap.Disks[0].Mount)
}

func TestParseSnapshotEmpty(t *testing.T) {
	osName, snap := parseSnapshot("")
	require.Empty(t, osName)
	require.Nil(t, snap)
	osName, snap = parseSnapshot("FreeBSD\n__ORYN__\n__ORYN__\n__ORYN__\n__ORYN__\n")
	require.Equal(t, "freebsd", osName)
	require.Nil(t, snap)
}

func TestHostTest(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	ctx := context.Background()

	res, err := e.svc.Test(ctx, e.host.ID)
	require.NoError(t, err)
	require.True(t, res.Ok, "%v", res.Error)
	require.NotNil(t, res.HostKeyFingerprint)
	require.Equal(t, e.target.HostKeyFingerprint(), *res.HostKeyFingerprint)
	require.NotNil(t, res.Os)
	require.NotNil(t, res.Snapshot, "假目标机跑在本机，/proc 与 df 可用")

	h, err := e.svc.GetHost(ctx, e.host.ID)
	require.NoError(t, err)
	require.NotNil(t, h.LastTest)
	require.True(t, h.LastTest.Ok)
	require.Equal(t, res.Os, h.LastTest.Os)
	rows := e.auditRows(t)
	require.Equal(t, "test", string(rows[0].Kind))
	require.Equal(t, "owner", string(rows[0].Source))
	require.True(t, rows[0].Ok)

	// 连不上：照样返回结果，ok=false 带错误，并写回主机
	e.target.Close()
	res, err = e.svc.Test(ctx, e.host.ID)
	require.NoError(t, err)
	require.False(t, res.Ok)
	require.NotNil(t, res.Error)
	require.Contains(t, *res.Error, "连不上")
	h, err = e.svc.GetHost(ctx, e.host.ID)
	require.NoError(t, err)
	require.False(t, h.LastTest.Ok)
	require.Equal(t, res.Error, h.LastTest.Error)
	rows = e.auditRows(t)
	require.Equal(t, "test", string(rows[0].Kind))
	require.False(t, rows[0].Ok)

	_, err = e.svc.Test(ctx, "ssh_nope")
	requireCode(t, err, "not_found", "")
}
