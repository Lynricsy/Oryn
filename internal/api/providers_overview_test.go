package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestProvidersOverviewAPI(t *testing.T) {
	e := newOrbsEnv(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	// 1) 在线 provider（prov_1）带宿主机指标 hello
	hm := &apigen.ProviderHostMetrics{
		SampledAt:          now,
		Hostname:           new("gpu-node-01"),
		Os:                 new("linux"),
		Arch:               new("amd64"),
		UptimeSeconds:      new(int64(86400 * 5)),
		CPUCores:           32,
		CPUPct:             new(float32(42.5)),
		LoadAvg1:           new(float32(2.4)),
		LoadAvg5:           new(float32(1.9)),
		LoadAvg15:          new(float32(1.5)),
		MemTotalMB:         65536,
		MemUsedMB:          24576,
		MemAvailableMB:     new(int64(40960)),
		DiskTotalBytes:     500 << 30,
		DiskUsedBytes:      180 << 30,
		DiskAvailableBytes: new(int64(320 << 30)),
		DiskPath:           new("/var/lib/docker"),
	}
	hello := apigen.ProviderHelloRequest{
		InstanceID:   "inst-1",
		Driver:       apigen.Docker,
		Version:      "v1.2.3",
		Capabilities: apigen.ProviderCapabilities{Freeze: true, Snapshot: true, OfflineFs: new(true)},
		Types: []apigen.OrbType{
			{Name: "small", Cpus: 2, MemoryMB: 4096},
			{Name: "large", Cpus: 8, MemoryMB: 16384, Features: []string{"docker"}},
		},
		DefaultType: "small",
		HostMetrics: hm,
	}
	var hr apigen.ProviderHelloResponse
	require.Equal(t, 200, e.do("POST", "/provider/v1/hello", e.provT, hello, &hr))

	// 再次 hello 省略 hostMetrics：不能把已有 host_metrics 清空（兼容老版本或局部续租）
	helloNoMetrics := hello
	helloNoMetrics.HostMetrics = nil
	require.Equal(t, 200, e.do("POST", "/provider/v1/hello", e.provT, helloNoMetrics, &hr))

	// 2) 离线 provider（prov_2）与已停用 provider（prov_3），外加内置 @host（应被排除）
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{
		ID: "prov_2", Name: "fc-east", TokenHash: ids.Hash(ids.Token("opv")), CreatedAt: now - 10_000,
	}))
	require.NoError(t, e.st.Q.CreateProvider(ctx, db.CreateProviderParams{
		ID: "prov_3", Name: "retired-node", TokenHash: ids.Hash(ids.Token("opv")), CreatedAt: now - 20_000,
	}))
	_, err := e.st.Q.DisableProvider(ctx, db.DisableProviderParams{DisabledAt: new(now - 5_000), ID: "prov_3"})
	require.NoError(t, err)
	require.NoError(t, e.st.Q.UpsertHostProvider(ctx, db.UpsertHostProviderParams{
		ID: "prov_host", TokenHash: ids.Hash("host"), Version: "dev", Capabilities: "{}", OrbTypes: "[]", CreatedAt: now,
	}))

	// 3) 造项目、线程、不同状态的 orb（running / frozen / failed / deleted / warmPool / box）
	require.NoError(t, e.st.Q.CreateProject(ctx, db.CreateProjectParams{
		ID: "prj_1", Name: "oryn-core", CreatedAt: now, UpdatedAt: now,
	}))
	createOrb := func(id, provID, typ, state string, cpus, mem int64, pool *string, box int64, projID *string) {
		require.NoError(t, e.st.Q.CreateOrb(ctx, db.CreateOrbParams{
			ID: id, ProviderID: provID, Type: typ, Cpus: cpus, MemoryMb: mem, Image: "oryn-orb:dev",
			ProjectID: projID, State: state, StateChangedAt: now, TokenHash: ids.Hash("tok_" + id),
			CreatedAt: now - 60_000, PoolState: pool, Box: &box,
		}))
	}
	createOrb("orb-run1", "prov_1", "large", orbs.StateReady, 8, 16384, nil, 0, new("prj_1"))
	createOrb("orb-run2", "prov_1", "small", orbs.StateReady, 2, 4096, nil, 1, nil)
	createOrb("orb-frz1", "prov_1", "small", orbs.StateFrozen, 2, 4096, new(orbs.PoolIdle), 0, new("prj_1"))
	createOrb("orb-fail", "prov_1", "small", orbs.StateFailed, 2, 4096, nil, 0, nil)
	createOrb("orb-del1", "prov_1", "small", orbs.StateStopped, 2, 4096, nil, 0, nil)
	require.NoError(t, e.st.Q.MarkOrbDeleted(ctx, db.MarkOrbDeletedParams{DeletedAt: now, ID: "orb-del1"}))

	require.NoError(t, e.st.Q.InsertOrbEvent(ctx, db.InsertOrbEventParams{
		OrbID: "orb-fail", Ts: now, FromState: orbs.StateSpawning, ToState: orbs.StateFailed,
		Detail: `{"reason":"oom_killed"}`,
	}))

	// 线程挂到 orb-run1
	require.NoError(t, e.st.Q.CreateThread(ctx, db.CreateThreadParams{
		ID: "thr_1", ProjectID: new("prj_1"), OrbID: new("orb-run1"), Title: "排查构建缓存",
		TitleSource: "user", State: "working", StateChangedAt: now, SessionDir: "/tmp/s1",
		Kind: "work", CreatedAt: now, UpdatedAt: now,
	}))

	// orb_samples：orb-run1 与 orb-run2 共享同一底层宿主机文件系统（均上报相同的 statfs 磁盘 184320 MB = 180 GiB）；
	// orb-run1 有两条采样（应取较新那条），orb-frz1 有历史采样（只计入网络，不计入实时 CPU/内存，且任何 orb 的 statfs 磁盘不得跨 orb 求和）。
	const sharedDiskMB int64 = 180 * 1024
	require.NoError(t, e.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
		OrbID: "orb-run1", Ts: now - 30_000, CpuPct: 10, MemUsedMb: 1000, MemLimitMb: 16384, DiskUsedMb: sharedDiskMB - 1024, NetRxBytes: 100, NetTxBytes: 50,
	}))
	require.NoError(t, e.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
		OrbID: "orb-run1", Ts: now - 5_000, CpuPct: 85.5, MemUsedMb: 6144, MemLimitMb: 16384, DiskUsedMb: sharedDiskMB, NetRxBytes: 5000, NetTxBytes: 2500,
	}))
	require.NoError(t, e.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
		OrbID: "orb-run2", Ts: now - 5_000, CpuPct: 14.5, MemUsedMb: 2048, MemLimitMb: 4096, DiskUsedMb: sharedDiskMB, NetRxBytes: 2000, NetTxBytes: 1000,
	}))
	require.NoError(t, e.st.Q.InsertOrbSample(ctx, db.InsertOrbSampleParams{
		OrbID: "orb-frz1", Ts: now - 60_000, CpuPct: 50, MemUsedMb: 2048, MemLimitMb: 4096, DiskUsedMb: sharedDiskMB, NetRxBytes: 1000, NetTxBytes: 500,
	}))
	// 项目快照：1 ready（2GiB）+ 1 building
	_, err = e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{
		ID: "snap_1", ProjectID: "prj_1", ProviderID: "prov_1", Type: "small", ConfigHash: "h1", Depth: 1, LineageAt: now, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = e.st.Q.PublishProjectSnapshot(ctx, db.PublishProjectSnapshotParams{
		ReadyAt: now, SizeBytes: new(int64(2 << 30)), BaseCommit: "abc", ID: "snap_1",
	})
	require.NoError(t, err)
	_, err = e.st.Q.InsertBuildingProjectSnapshot(ctx, db.InsertBuildingProjectSnapshotParams{
		ID: "snap_2", ProjectID: "prj_1", ProviderID: "prov_1", Type: "large", ConfigHash: "h2", Depth: 1, LineageAt: now, CreatedAt: now,
	})
	require.NoError(t, err)

	// 任务队列：1 queued
	require.NoError(t, e.st.Q.CreateJob(ctx, db.CreateJobParams{
		ID: "job_1", ProviderID: "prov_1", OrbID: new("orb-run1"), Op: "spawn", Params: "{}", CreatedAt: now,
	}))

	// 调用总览接口
	var ov apigen.ProvidersOverview
	require.Equal(t, 200, e.do("GET", "/api/v1/providers/overview", e.dev, nil, &ov))

	// 全局汇总校验
	assert.Equal(t, 3, ov.Summary.ProvidersTotal)
	assert.Equal(t, 1, ov.Summary.ProvidersOnline)
	assert.Equal(t, 1, ov.Summary.ProvidersOffline)
	assert.Equal(t, 1, ov.Summary.ProvidersDisabled)
	assert.Equal(t, 1, ov.Summary.Host.ReportedCount)
	assert.Equal(t, 32, ov.Summary.Host.CPUCores)
	assert.Equal(t, int64(65536), ov.Summary.Host.MemTotalMB)
	assert.Equal(t, int64(24576), ov.Summary.Host.MemUsedMB)
	assert.Equal(t, int64(500<<30), ov.Summary.Host.DiskTotalBytes)
	assert.Equal(t, int64(180<<30), ov.Summary.Host.DiskUsedBytes)

	assert.Equal(t, 4, ov.Summary.Orbs.Total)
	assert.Equal(t, 2, ov.Summary.Orbs.Running)
	assert.Equal(t, 1, ov.Summary.Orbs.Frozen)
	assert.Equal(t, 1, ov.Summary.Orbs.Failed)
	assert.Equal(t, 1, ov.Summary.Orbs.Deleted)
	assert.Equal(t, 1, ov.Summary.Orbs.WarmPool)
	assert.Equal(t, 1, ov.Summary.Orbs.Boxes)

	assert.InDelta(t, 10.0, float64(ov.Summary.Allocated.RunningCpus), 0.01)
	assert.Equal(t, int64(20480), ov.Summary.Allocated.RunningMemoryMB)
	assert.InDelta(t, 14.0, float64(ov.Summary.Allocated.TotalCpus), 0.01)
	assert.Equal(t, int64(28672), ov.Summary.Allocated.TotalMemoryMB)

	// 两个运行中 orb 的 CPU/内存正常汇总，但共享底盘 statfs 不做跨 orb 累加（宿主机磁盘只记一次 180 GiB）
	assert.Equal(t, 2, ov.Summary.Live.SampledOrbs)
	assert.InDelta(t, 100.0, float64(ov.Summary.Live.CPUPct), 0.01)
	assert.InDelta(t, 8192.0, float64(ov.Summary.Live.MemUsedMB), 0.01)
	assert.Equal(t, int64(8000), ov.Summary.Live.NetRxBytes)
	assert.Equal(t, int64(4000), ov.Summary.Live.NetTxBytes)
	assert.Equal(t, int64(180<<30), ov.Summary.Host.DiskUsedBytes)
	assert.Equal(t, 1, ov.Summary.Snapshots.ReadyCount)
	assert.Equal(t, 1, ov.Summary.Snapshots.BuildingCount)
	assert.Equal(t, int64(2<<30), ov.Summary.Snapshots.TotalBytes)
	assert.Equal(t, 1, ov.Summary.Jobs.Queued)

	// 排序校验：在线(local) -> 离线(fc-east) -> 已停用(retired-node)
	require.Len(t, ov.Providers, 3)
	assert.Equal(t, "local", ov.Providers[0].Provider.Name)
	assert.Equal(t, "fc-east", ov.Providers[1].Provider.Name)
	assert.Equal(t, "retired-node", ov.Providers[2].Provider.Name)

	p0 := ov.Providers[0]
	require.NotNil(t, p0.Version)
	assert.Equal(t, "v1.2.3", *p0.Version)
	require.NotNil(t, p0.HostMetrics)
	assert.Equal(t, "gpu-node-01", *p0.HostMetrics.Hostname)
	require.Len(t, p0.OrbItems, 4)

	// 逐台 orb 明细仍保留各自的 statfs 文件系统样本（与同底盘一致，不会被汇总成 3x）
	var run1, run2, failedOrb *apigen.ProviderOrbSummary
	for i := range p0.OrbItems {
		switch p0.OrbItems[i].ID {
		case "orb-run1":
			run1 = &p0.OrbItems[i]
		case "orb-run2":
			run2 = &p0.OrbItems[i]
		case "orb-fail":
			failedOrb = &p0.OrbItems[i]
		}
	}
	require.NotNil(t, run1)
	require.NotNil(t, run1.ProjectName)
	assert.Equal(t, "oryn-core", *run1.ProjectName)
	assert.Equal(t, []string{"thr_1"}, run1.ThreadIds)
	assert.Equal(t, []string{"排查构建缓存"}, run1.ThreadTitles)
	require.NotNil(t, run1.NestedDocker)
	assert.True(t, *run1.NestedDocker)
	require.NotNil(t, run1.LatestSample)
	assert.InDelta(t, 85.5, float64(run1.LatestSample.CPUPct), 0.01)
	assert.InDelta(t, float64(sharedDiskMB), float64(run1.LatestSample.DiskUsedMB), 0.1)

	require.NotNil(t, run2)
	require.NotNil(t, run2.LatestSample)
	assert.InDelta(t, float64(sharedDiskMB), float64(run2.LatestSample.DiskUsedMB), 0.1)

	require.NotNil(t, failedOrb)
	require.NotNil(t, failedOrb.FailureReason)
	assert.Equal(t, "oom_killed", *failedOrb.FailureReason)
}
