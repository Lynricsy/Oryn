package boxes

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// boxState 把 orb 状态映射成 box 状态。
func boxState(orbState string) apigen.BoxState {
	switch orbState {
	case orbs.StateReady:
		return apigen.BoxStateRunning
	case orbs.StateFreezing, orbs.StateStopping:
		return apigen.BoxStateStopping
	case orbs.StateFrozen, orbs.StateStopped:
		return apigen.BoxStateStopped
	case orbs.StateFailed:
		return apigen.BoxStateFailed
	}
	// requested / spawning / connecting / setting_up / resuming / waking
	return apigen.BoxStateStarting
}

// view 把 box 行与它的 orb 转成 API 形态（含 app 列表）。
func (s *Service) view(ctx context.Context, b db.Box, o db.Orb) (apigen.Box, error) {
	q := s.st.Q
	v := apigen.Box{
		ID: b.ID, Name: b.Name, OrbID: b.OrbID, State: boxState(o.State), Desired: apigen.BoxDesired(b.Desired),
		Type: o.Type, Cpus: float32(o.Cpus), MemoryMB: int(o.MemoryMb), CreatedAt: b.CreatedAt, Apps: []apigen.BoxApp{},
	}
	p, err := q.GetProvider(ctx, o.ProviderID)
	switch {
	case err == nil:
		v.Provider = p.Name
	case !errors.Is(err, sql.ErrNoRows):
		return apigen.Box{}, err
	}
	if o.State == orbs.StateFailed {
		reason, err := orbs.FailureReason(ctx, q, o.ID)
		if err != nil {
			return apigen.Box{}, err
		}
		if reason != "" {
			v.FailureReason = &reason
		}
	}
	if b.Desired == desiredStopped {
		v.StoppedBy = new(apigen.BoxStoppedBy(b.DesiredBy))
	}
	if b.SchedOn != nil && b.SchedOff != nil && b.SchedTz != nil {
		v.Schedule = &apigen.BoxSchedule{On: *b.SchedOn, Off: *b.SchedOff, Tz: *b.SchedTz}
	}
	apps, err := q.ListBoxApps(ctx, b.ID)
	if err != nil {
		return apigen.Box{}, err
	}
	if len(apps) == 0 {
		return v, nil
	}
	open, err := q.ListOpenBoxDeploysByBox(ctx, b.ID)
	if err != nil {
		return apigen.Box{}, err
	}
	deploying := map[string]bool{}
	for _, d := range open {
		deploying[d.App] = true
	}
	svcs, _, err := orbs.ServicesView(ctx, q, o.ID, s.now().UnixMilli())
	if err != nil {
		return apigen.Box{}, err
	}
	for _, a := range apps {
		av, err := s.appView(ctx, q, a, deploying[a.Name], svcs)
		if err != nil {
			return apigen.Box{}, err
		}
		if av.Live != nil && (v.LastDeployAt == nil || av.Live.ActivatedAt > *v.LastDeployAt) {
			v.LastDeployAt = new(av.Live.ActivatedAt)
		}
		v.Apps = append(v.Apps, av)
	}
	return v, nil
}

// appView 把 app 行转成 API 形态：services 取 orb 服务镜像里属于它的项。
func (s *Service) appView(ctx context.Context, q *db.Queries, a db.BoxApp, deploying bool, svcs []apigen.OrbService) (apigen.BoxApp, error) {
	v := apigen.BoxApp{
		Name: a.Name, Public: a.Public, Repo: a.Repo, Root: a.Root, Source: a.Source, Port: int(a.Port),
		Deploying: deploying, Preview: a.Preview, Services: []apigen.OrbService{},
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
	}
	if u := strings.TrimSuffix(s.cfg.Base.AppURL(a.Name), "/"); u != "" {
		v.URL = &u
	}
	if a.LiveReleaseID != nil {
		r, err := q.GetBoxRelease(ctx, *a.LiveReleaseID)
		switch {
		case err == nil:
			v.Live = new(releaseView(r))
		case !errors.Is(err, sql.ErrNoRows):
			return apigen.BoxApp{}, err
		}
	}
	for _, sv := range svcs {
		if sv.Source == apigen.OrbServiceSourceApp && sv.App != nil && *sv.App == a.Name {
			v.Services = append(v.Services, sv)
		}
	}
	return v, nil
}

// releaseView 把 release 行转成 API 形态。
func releaseView(r db.BoxRelease) apigen.BoxRelease {
	return apigen.BoxRelease{
		ID: r.ID, Commit: r.CommitSha, Branch: r.Branch, Repo: r.Repo, Root: r.Root, Source: r.Source,
		SizeBytes: r.SizeBytes, State: apigen.BoxReleaseState(r.State), ThreadID: r.ThreadID,
		CreatedAt: r.CreatedAt, ActivatedAt: r.ActivatedAt,
	}
}
