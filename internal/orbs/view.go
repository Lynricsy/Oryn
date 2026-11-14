package orbs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// View 把 orb 行转换成 API 形态：附 provider 名、挂在其上的线程、失败原因、最近一次 setup / resume 运行，
// 以及服务镜像与 portal（orb 已删除时省略）。
// REST（GET orbs…）与实时推送（WS `orb`、thread.snapshot）共用这一个转换。
func View(ctx context.Context, q *db.Queries, o db.Orb) (apigen.Orb, error) {
	threadIDs, err := q.ListThreadIDsByOrb(ctx, &o.ID)
	if err != nil {
		return apigen.Orb{}, err
	}
	if threadIDs == nil {
		threadIDs = []string{}
	}
	v := apigen.Orb{
		ID: o.ID, ProviderID: o.ProviderID, Type: o.Type, Cpus: float32(o.Cpus), MemoryMB: int(o.MemoryMb),
		Image: o.Image, ProjectID: o.ProjectID, State: apigen.OrbState(o.State), StateChangedAt: o.StateChangedAt,
		SnapshotOf: o.SnapshotOf, LastAgentActivityAt: o.LastAgentActivityAt, LastUserInteractionAt: o.LastUserInteractionAt,
		OrbdVersion: o.OrbdVersion, AgentVersion: o.AgentVersion, CreatedAt: o.CreatedAt, DeletedAt: o.DeletedAt,
		ThreadIds: threadIDs,
	}
	if o.Coordinator {
		yes := true
		v.Coordinator = &yes
	}
	p, err := q.GetProvider(ctx, o.ProviderID)
	switch {
	case err == nil:
		v.ProviderName = &p.Name
	case !errors.Is(err, sql.ErrNoRows):
		return apigen.Orb{}, err
	}
	if o.State == StateFailed {
		reason, err := FailureReason(ctx, q, o.ID)
		if err != nil {
			return apigen.Orb{}, err
		}
		if reason != "" {
			v.FailureReason = &reason
		}
	}
	for _, ph := range []apigen.SetupPhase{apigen.SetupPhaseSetup, apigen.SetupPhaseResume} {
		row, err := q.LatestOrbSetupRun(ctx, db.LatestOrbSetupRunParams{OrbID: o.ID, Phase: string(ph)})
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return apigen.Orb{}, err
		}
		run := SetupRunView(db.ListOrbSetupRunsRow(row))
		if ph == apigen.SetupPhaseSetup {
			v.LastSetup = &run
		} else {
			v.LastResume = &run
		}
	}
	if o.State != StateDeleted {
		services, portals, err := ServicesView(ctx, q, o.ID, time.Now().UnixMilli())
		if err != nil {
			return apigen.Orb{}, err
		}
		v.Services, v.Portals = services, portals
		if v.Desktop, err = DesktopView(ctx, q, o.ID); err != nil {
			return apigen.Orb{}, err
		}
	}
	return v, nil
}

// SetupRunView 把 orb_setup_runs 行（不含日志）转换成 API 形态。
func SetupRunView(r db.ListOrbSetupRunsRow) apigen.OrbSetupRun {
	return apigen.OrbSetupRun{
		ID: r.ID, OrbID: r.OrbID, Phase: apigen.SetupPhase(r.Phase), Trigger: apigen.SetupTrigger(r.Trigger),
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Ok: r.Ok != 0, SetupRan: r.SetupRan != 0,
		Error: r.Error, DurationMs: r.DurationMs, HasRepoScript: r.HasRepoScript != 0, HasProjectScript: r.HasProjectScript != 0,
	}
}

// FailureReason 返回导致 orb 进入 failed 的原因：最近一条 to_state=failed 的 orb_events.detail.reason，
// 没有则取 detail.error（任务失败），再没有则为空。
func FailureReason(ctx context.Context, q *db.Queries, orbID string) (string, error) {
	evs, err := q.ListOrbEvents(ctx, orbID)
	if err != nil {
		return "", err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].ToState != StateFailed {
			continue
		}
		var d struct {
			Reason string `json:"reason"`
			Error  string `json:"error"`
		}
		_ = json.Unmarshal([]byte(evs[i].Detail), &d)
		if d.Reason != "" {
			return d.Reason, nil
		}
		return d.Error, nil
	}
	return "", nil
}
