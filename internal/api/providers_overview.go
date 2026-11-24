package api

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// GetProvidersOverview 实现 GET /api/v1/providers/overview：一次聚合所有 provider 的状态、宿主机指标、
// 下属 orb 计数/配额/实时采样、项目快照存储占用与任务队列健康度，避免客户端 N+1 请求。
func (s *Server) GetProvidersOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := s.Store.Q
	now := time.Now().UnixMilli()
	since24h := now - (24 * time.Hour).Milliseconds()

	provRows, err := q.ListProviders(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	orbRows, err := q.ListOrbs(ctx, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sampleRows, err := q.ListLatestOrbSamples(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	deletedRows, err := q.CountDeletedOrbsByProvider(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	snapRows, err := q.SummarizeProjectSnapshotsByProvider(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	jobRows, err := q.SummarizeJobsByProvider(ctx, &since24h)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	threadRows, err := q.ListThreadsForLiveOrbs(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	projectRows, err := q.ListProjects(ctx, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	projectSnapIDs, err := q.ListProjectSnapshotIDs(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	// 建立内存索引，避免 N+1 查询。
	sampleByOrb := make(map[string]apigen.OrbMetricSample, len(sampleRows))
	for _, sr := range sampleRows {
		sampleByOrb[sr.OrbID] = apigen.OrbMetricSample{
			TS:         sr.Ts,
			CPUPct:     float32(sr.CpuPct),
			MemUsedMB:  float32(sr.MemUsedMb),
			MemLimitMB: float32(sr.MemLimitMb),
			DiskUsedMB: float32(sr.DiskUsedMb),
			NetRxBytes: sr.NetRxBytes,
			NetTxBytes: sr.NetTxBytes,
		}
	}
	deletedByProv := make(map[string]int, len(deletedRows))
	for _, dr := range deletedRows {
		deletedByProv[dr.ProviderID] = int(dr.DeletedCount)
	}
	snapByProv := make(map[string]apigen.ProviderSnapshotStats, len(snapRows))
	for _, sr := range snapRows {
		snapByProv[sr.ProviderID] = apigen.ProviderSnapshotStats{
			ReadyCount:    int(sr.ReadyCount),
			BuildingCount: int(sr.BuildingCount),
			RetiredCount:  int(sr.RetiredCount),
			TotalBytes:    sr.ReadyBytes,
		}
	}
	jobsByProv := make(map[string]apigen.ProviderJobStats, len(jobRows))
	for _, jr := range jobRows {
		jobsByProv[jr.ProviderID] = apigen.ProviderJobStats{
			Queued:       int(jr.QueuedCount),
			Claimed:      int(jr.ClaimedCount),
			Succeeded24H: int(jr.Succeeded24h),
			Failed24H:    int(jr.Failed24h),
		}
	}
	type threadRef struct{ id, title string }
	threadsByOrb := make(map[string][]threadRef, len(threadRows))
	for _, tr := range threadRows {
		if tr.OrbID == nil {
			continue
		}
		threadsByOrb[*tr.OrbID] = append(threadsByOrb[*tr.OrbID], threadRef{id: tr.ID, title: tr.Title})
	}
	projectNameByID := make(map[string]string, len(projectRows))
	for _, pr := range projectRows {
		projectNameByID[pr.ID] = pr.Name
	}
	projectSnapSet := make(map[string]struct{}, len(projectSnapIDs))
	for _, id := range projectSnapIDs {
		projectSnapSet[id] = struct{}{}
	}
	orbsByProv := make(map[string][]db.Orb)
	for _, o := range orbRows {
		orbsByProv[o.ProviderID] = append(orbsByProv[o.ProviderID], o)
	}

	out := apigen.ProvidersOverview{
		GeneratedAt: now,
		Providers:   make([]apigen.ProviderOverviewItem, 0, len(provRows)),
	}
	if s.Orbs != nil && s.Orbs.Service != nil {
		d := s.Orbs.Service.Defaults()
		var od apigen.OrbDefaults
		if d.Provider != "" {
			od.Provider = new(d.Provider)
		}
		if d.Type != "" {
			od.Type = new(d.Type)
		}
		if od.Provider != nil || od.Type != nil {
			out.OrbDefaults = &od
		}
	}

	for _, p := range provRows {
		if p.Driver == orbs.HostDriver {
			// 内置的 @host（协调宿主）不是用户的算力 provider
			continue
		}
		pv := providerJSON(p, now)
		item := apigen.ProviderOverviewItem{
			Provider:      pv,
			AllowMultiple: p.AllowMultiple,
			Snapshots:     snapByProv[p.ID],
			Jobs:          jobsByProv[p.ID],
			OrbItems:      make([]apigen.ProviderOrbSummary, 0, len(orbsByProv[p.ID])),
		}
		if p.Version != "" {
			item.Version = new(p.Version)
		}
		if p.BaseImageID != "" {
			item.BaseImageID = new(p.BaseImageID)
		}
		if p.HostMetrics != "" {
			var hm apigen.ProviderHostMetrics
			if json.Unmarshal([]byte(p.HostMetrics), &hm) == nil && hm.SampledAt > 0 {
				item.HostMetrics = &hm
			}
		}

		// 解析每个类型是否支持 nestedDocker
		nestedDockerTypes := make(map[string]bool, len(pv.OrbTypes))
		for _, ot := range pv.OrbTypes {
			if slices.Contains(ot.Features, orbs.FeatureDocker) {
				nestedDockerTypes[ot.Name] = true
			}
		}

		item.Orbs.Deleted = deletedByProv[p.ID]
		for _, o := range orbsByProv[p.ID] {
			item.Orbs.Total++
			item.Allocated.TotalCpus += float32(o.Cpus)
			item.Allocated.TotalMemoryMB += o.MemoryMb

			switch o.State {
			case orbs.StateReady:
				item.Orbs.Running++
				item.Allocated.RunningCpus += float32(o.Cpus)
				item.Allocated.RunningMemoryMB += o.MemoryMb
			case orbs.StateRequested, orbs.StateSpawning, orbs.StateConnecting,
				orbs.StateSettingUp, orbs.StateResuming, orbs.StateWaking:
				item.Orbs.Starting++
			case orbs.StateFrozen, orbs.StateFreezing:
				item.Orbs.Frozen++
			case orbs.StateStopped, orbs.StateStopping:
				item.Orbs.Stopped++
			case orbs.StateFailed:
				item.Orbs.Failed++
			case orbs.StateDeleting:
				item.Orbs.Deleting++
			}

			var pool *apigen.OrbPoolState
			if o.PoolState != nil {
				switch *o.PoolState {
				case orbs.PoolWarming, orbs.PoolIdle, orbs.PoolClaimed:
					item.Orbs.WarmPool++
					pool = new(apigen.OrbPoolState(*o.PoolState))
				}
			}
			if o.Box == 1 {
				item.Orbs.Boxes++
			}
			if o.Computer == 1 {
				item.Orbs.Computers++
			}

			refs := threadsByOrb[o.ID]
			tIDs := make([]string, 0, len(refs))
			tTitles := make([]string, 0, len(refs))
			for _, ref := range refs {
				tIDs = append(tIDs, ref.id)
				tTitles = append(tTitles, ref.title)
			}

			summary := apigen.ProviderOrbSummary{
				ID:             o.ID,
				Type:           o.Type,
				State:          apigen.OrbState(o.State),
				Cpus:           float32(o.Cpus),
				MemoryMB:       int(o.MemoryMb),
				ProjectID:      o.ProjectID,
				ThreadIds:      tIDs,
				ThreadTitles:   tTitles,
				Pool:           pool,
				StateChangedAt: o.StateChangedAt,
				CreatedAt:      o.CreatedAt,
			}
			if o.ProjectID != nil {
				if name, ok := projectNameByID[*o.ProjectID]; ok {
					summary.ProjectName = &name
				}
			}
			if o.Box == 1 {
				summary.Box = new(true)
			}
			if o.Computer == 1 {
				summary.Computer = new(true)
			}
			if o.Automation {
				summary.Automation = new(true)
			}
			if nestedDockerTypes[o.Type] {
				summary.NestedDocker = new(true)
			}
			if o.SnapshotOf != nil && *o.SnapshotOf != "" {
				if _, ok := projectSnapSet[*o.SnapshotOf]; ok {
					summary.SnapshotKind = new(apigen.OrbSnapshotKindProject)
				} else {
					summary.SnapshotKind = new(apigen.OrbSnapshotKindFork)
				}
			}
			if o.State == orbs.StateFailed {
				if reason, err := orbs.FailureReason(ctx, q, o.ID); err == nil && reason != "" {
					summary.FailureReason = &reason
				}
			}
			if act := maxOptionalTS(o.LastAgentActivityAt, o.LastUserInteractionAt); act != nil {
				summary.LastActivityAt = act
			}
			if smp, ok := sampleByOrb[o.ID]; ok {
				smpCopy := smp
				summary.LatestSample = &smpCopy
				item.Live.NetRxBytes += smp.NetRxBytes
				item.Live.NetTxBytes += smp.NetTxBytes
				if o.State == orbs.StateReady {
					item.Live.SampledOrbs++
					item.Live.CPUPct += smp.CPUPct
					item.Live.MemUsedMB += smp.MemUsedMB
					item.Live.MemLimitMB += smp.MemLimitMB
				}
			}

			item.OrbItems = append(item.OrbItems, summary)
		}

		// orb 明细按运行优先级排序，同组按最近状态变更倒序。
		slices.SortFunc(item.OrbItems, func(a, b apigen.ProviderOrbSummary) int {
			if c := cmp.Compare(orbOverviewRank(a.State), orbOverviewRank(b.State)); c != 0 {
				return c
			}
			if c := cmp.Compare(b.StateChangedAt, a.StateChangedAt); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})

		// 汇总到全局 Summary。
		out.Summary.ProvidersTotal++
		switch {
		case pv.DisabledAt != nil:
			out.Summary.ProvidersDisabled++
		case pv.Online:
			out.Summary.ProvidersOnline++
		default:
			out.Summary.ProvidersOffline++
		}
		if pv.Online && item.HostMetrics != nil {
			out.Summary.Host.ReportedCount++
			out.Summary.Host.CPUCores += item.HostMetrics.CPUCores
			out.Summary.Host.MemTotalMB += item.HostMetrics.MemTotalMB
			out.Summary.Host.MemUsedMB += item.HostMetrics.MemUsedMB
			out.Summary.Host.DiskTotalBytes += item.HostMetrics.DiskTotalBytes
			out.Summary.Host.DiskUsedBytes += item.HostMetrics.DiskUsedBytes
		}
		addOrbCounts(&out.Summary.Orbs, item.Orbs)
		out.Summary.Allocated.RunningCpus += item.Allocated.RunningCpus
		out.Summary.Allocated.RunningMemoryMB += item.Allocated.RunningMemoryMB
		out.Summary.Allocated.TotalCpus += item.Allocated.TotalCpus
		out.Summary.Allocated.TotalMemoryMB += item.Allocated.TotalMemoryMB
		out.Summary.Live.SampledOrbs += item.Live.SampledOrbs
		out.Summary.Live.CPUPct += item.Live.CPUPct
		out.Summary.Live.MemUsedMB += item.Live.MemUsedMB
		out.Summary.Live.MemLimitMB += item.Live.MemLimitMB
		out.Summary.Live.NetRxBytes += item.Live.NetRxBytes
		out.Summary.Live.NetTxBytes += item.Live.NetTxBytes
		out.Summary.Snapshots.ReadyCount += item.Snapshots.ReadyCount
		out.Summary.Snapshots.BuildingCount += item.Snapshots.BuildingCount
		out.Summary.Snapshots.RetiredCount += item.Snapshots.RetiredCount
		out.Summary.Snapshots.TotalBytes += item.Snapshots.TotalBytes
		out.Summary.Jobs.Queued += item.Jobs.Queued
		out.Summary.Jobs.Claimed += item.Jobs.Claimed
		out.Summary.Jobs.Succeeded24H += item.Jobs.Succeeded24H
		out.Summary.Jobs.Failed24H += item.Jobs.Failed24H

		out.Providers = append(out.Providers, item)
	}

	// provider 排序：在线 > 离线 > 已停用，同组按名称排序。
	slices.SortFunc(out.Providers, func(a, b apigen.ProviderOverviewItem) int {
		if c := cmp.Compare(providerOverviewRank(a.Provider), providerOverviewRank(b.Provider)); c != 0 {
			return c
		}
		return cmp.Compare(a.Provider.Name, b.Provider.Name)
	})

	WriteJSON(w, http.StatusOK, out)
}

func providerOverviewRank(p apigen.Provider) int {
	if p.DisabledAt != nil {
		return 2
	}
	if p.Online {
		return 0
	}
	return 1
}

func orbOverviewRank(st apigen.OrbState) int {
	switch string(st) {
	case orbs.StateReady:
		return 0
	case orbs.StateRequested, orbs.StateSpawning, orbs.StateConnecting,
		orbs.StateSettingUp, orbs.StateResuming, orbs.StateWaking:
		return 1
	case orbs.StateFailed:
		return 2
	case orbs.StateFrozen, orbs.StateFreezing:
		return 3
	case orbs.StateStopped, orbs.StateStopping:
		return 4
	default:
		return 5
	}
}

func addOrbCounts(dst *apigen.ProviderOrbCounts, src apigen.ProviderOrbCounts) {
	dst.Total += src.Total
	dst.Running += src.Running
	dst.Starting += src.Starting
	dst.Frozen += src.Frozen
	dst.Stopped += src.Stopped
	dst.Failed += src.Failed
	dst.Deleting += src.Deleting
	dst.Deleted += src.Deleted
	dst.WarmPool += src.WarmPool
	dst.Boxes += src.Boxes
	dst.Computers += src.Computers
}

func maxOptionalTS(a, b *int64) *int64 {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		v := *b
		return &v
	case b == nil:
		v := *a
		return &v
	case *a >= *b:
		v := *a
		return &v
	default:
		v := *b
		return &v
	}
}
