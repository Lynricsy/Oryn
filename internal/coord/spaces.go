package coord

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

const (
	maxSpaceName = 100
	maxSpaceGoal = 2000
)

// SpaceToAPI 把 spaces 行转成 API 形态。
func SpaceToAPI(r db.Space) apigen.Space {
	return apigen.Space{
		ID: r.ID, Name: r.Name, Goal: r.Goal, ProjectID: r.ProjectID, ThreadID: r.ThreadID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt,
	}
}

// checkProject 确认项目存在；不存在 → not_found。
func (s *Service) checkProject(ctx context.Context, id string) error {
	if _, err := s.st.Q.GetProject(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errf(CodeNotFound, "project not found")
		}
		return err
	}
	return nil
}

// checkSpaceName 校验名称长度与唯一性（self 为改名的 Space 自身）。
func (s *Service) checkSpaceName(ctx context.Context, name, self string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxSpaceName {
		return "", errf(CodeInvalid, "space name must be 1-%d characters", maxSpaceName)
	}
	other, err := s.st.Q.GetSpaceByName(ctx, name)
	switch {
	case err == nil && other.ID != self:
		return "", errf(CodeSpaceNameTaken, "a Space named %q already exists", name)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	return name, nil
}

// CreateSpace 建 Space：确保协调 orb → 建聊天线程（kind = space）→ 写 spaces 行（失败则删掉刚建的线程）。
func (s *Service) CreateSpace(ctx context.Context, in apigen.SpaceCreate) (apigen.Space, error) {
	name, err := s.checkSpaceName(ctx, in.Name, "")
	if err != nil {
		return apigen.Space{}, err
	}
	goal := ""
	if in.Goal != nil {
		goal = strings.TrimSpace(*in.Goal)
	}
	if utf8.RuneCountInString(goal) > maxSpaceGoal {
		return apigen.Space{}, errf(CodeInvalid, "goal must be at most %d characters", maxSpaceGoal)
	}
	var projectID *string
	if in.ProjectID != nil && *in.ProjectID != "" {
		if err := s.checkProject(ctx, *in.ProjectID); err != nil {
			return apigen.Space{}, err
		}
		projectID = in.ProjectID
	}
	if err := s.checkHost(ctx); err != nil {
		return apigen.Space{}, err
	}
	orbID, err := s.EnsureOrb(ctx)
	if err != nil {
		return apigen.Space{}, err
	}
	id := ids.New("spc")
	now := s.nowMs()
	_, err = s.th.CreateCoordinator(ctx, threads.CoordinatorParams{
		Kind: threads.KindSpace, OrbID: orbID, Title: name, SpaceID: id, Model: s.cfg.Model,
		// spaces 行与聊天线程同一事务写入：agent 一启动就要按 Space 生成系统提示词
		Extra: func(q *db.Queries, threadID string) error {
			return q.CreateSpace(ctx, db.CreateSpaceParams{
				ID: id, Name: name, Goal: goal, ProjectID: projectID, ThreadID: threadID, CreatedAt: now, UpdatedAt: now,
			})
		},
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return apigen.Space{}, errf(CodeSpaceNameTaken, "a Space named %q already exists", name)
		}
		return apigen.Space{}, err
	}
	return s.GetSpace(ctx, id)
}

// GetSpace 返回 Space；不存在 → space_not_found。
func (s *Service) GetSpace(ctx context.Context, id string) (apigen.Space, error) {
	r, err := s.st.Q.GetSpace(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Space{}, errf(CodeSpaceNotFound, "space not found")
	}
	if err != nil {
		return apigen.Space{}, err
	}
	return SpaceToAPI(r), nil
}

// ListSpaces 列出 Space（按名称）；archived 为真时含已归档的。
func (s *Service) ListSpaces(ctx context.Context, archived bool) ([]apigen.Space, error) {
	rows, err := s.st.Q.ListSpaces(ctx, archived)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Space, 0, len(rows))
	for _, r := range rows {
		out = append(out, SpaceToAPI(r))
	}
	return out, nil
}

// UpdateSpace 改 Space：改名同步聊天线程标题；名称 / 目标 / 项目变化后让协调 agent 在静止后以新的系统提示词重启；
// archived 同步归档聊天线程。
func (s *Service) UpdateSpace(ctx context.Context, id string, p apigen.SpacePatch) (apigen.Space, error) {
	cur, err := s.st.Q.GetSpace(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return apigen.Space{}, errf(CodeSpaceNotFound, "space not found")
	}
	if err != nil {
		return apigen.Space{}, err
	}
	next := db.UpdateSpaceParams{ID: id, Name: cur.Name, Goal: cur.Goal, ProjectID: cur.ProjectID, ArchivedAt: cur.ArchivedAt, UpdatedAt: s.nowMs()}
	if p.Name != nil {
		if next.Name, err = s.checkSpaceName(ctx, *p.Name, id); err != nil {
			return apigen.Space{}, err
		}
	}
	if p.Goal != nil {
		next.Goal = strings.TrimSpace(*p.Goal)
		if utf8.RuneCountInString(next.Goal) > maxSpaceGoal {
			return apigen.Space{}, errf(CodeInvalid, "goal must be at most %d characters", maxSpaceGoal)
		}
	}
	if p.ProjectID != nil {
		next.ProjectID = nil
		if *p.ProjectID != "" {
			if err := s.checkProject(ctx, *p.ProjectID); err != nil {
				return apigen.Space{}, err
			}
			next.ProjectID = p.ProjectID
		}
	}
	archiveChange := 0 // 1 = 归档，-1 = 取消归档
	if p.Archived != nil {
		switch {
		case *p.Archived && cur.ArchivedAt == nil:
			now := next.UpdatedAt
			next.ArchivedAt, archiveChange = &now, 1
		case !*p.Archived && cur.ArchivedAt != nil:
			next.ArchivedAt, archiveChange = nil, -1
		}
	}
	if _, err := s.st.Q.UpdateSpace(ctx, next); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return apigen.Space{}, errf(CodeSpaceNameTaken, "a Space named %q already exists", next.Name)
		}
		return apigen.Space{}, err
	}
	if next.Name != cur.Name {
		if err := s.th.SetCoordinatorTitle(ctx, cur.ThreadID, next.Name); err != nil {
			return apigen.Space{}, err
		}
	}
	switch archiveChange {
	case 1:
		if err := s.th.Archive(ctx, cur.ThreadID); err != nil {
			return apigen.Space{}, err
		}
	case -1:
		no := false
		if _, err := s.th.Patch(ctx, cur.ThreadID, apigen.ThreadPatch{Archived: &no}); err != nil {
			return apigen.Space{}, err
		}
	}
	if next.Name != cur.Name || next.Goal != cur.Goal || !eqStr(next.ProjectID, cur.ProjectID) {
		s.th.RefreshAgent(ctx, cur.ThreadID)
	}
	return s.GetSpace(ctx, id)
}

// DeleteSpace 删 Space：先把挂在它上面的「新建线程」定时与工作线程移出，再删聊天线程（spaces 行随之级联删除）。
func (s *Service) DeleteSpace(ctx context.Context, id string) error {
	cur, err := s.st.Q.GetSpace(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return errf(CodeSpaceNotFound, "space not found")
	}
	if err != nil {
		return err
	}
	if err := s.st.Q.ClearSpaceSchedules(ctx, &id); err != nil {
		return err
	}
	// 移出的线程要推送新的 index（spaceId 消失）
	moved, err := s.st.Q.ListThreads(ctx, db.ListThreadsParams{SpaceID: &id})
	if err != nil {
		return err
	}
	if err := s.st.Q.ClearSpaceThreads(ctx, id); err != nil {
		return err
	}
	for _, t := range moved {
		if t.Kind == threads.KindWork {
			s.th.Publish(ctx, t.ID)
		}
	}
	if err := s.th.DeleteSpaceThread(ctx, cur.ThreadID); err != nil && !errors.Is(err, threads.ErrNotFound) {
		return err
	}
	if _, err := s.st.Q.DeleteSpace(ctx, id); err != nil {
		return err
	}
	return nil
}

// CreateConversation 新建一个全局助手对话（无标题，由 omp 自动起标题）。
func (s *Service) CreateConversation(ctx context.Context) (apigen.Thread, error) {
	if err := s.checkHost(ctx); err != nil {
		return apigen.Thread{}, err
	}
	orbID, err := s.EnsureOrb(ctx)
	if err != nil {
		return apigen.Thread{}, err
	}
	return s.th.CreateCoordinator(ctx, threads.CoordinatorParams{Kind: threads.KindAssistant, OrbID: orbID, Model: s.cfg.Model})
}

// Progress 返回 Space 近 days 天（含今天，按 loc 的自然日）的进度：shipped = 当日标记为已交付；
// archived = 当日归档且未标记已交付；open = 当日结束时已创建且未归档。
func (s *Service) Progress(ctx context.Context, id string, days int, loc *time.Location) (apigen.SpaceProgress, error) {
	if _, err := s.GetSpace(ctx, id); err != nil {
		return apigen.SpaceProgress{}, err
	}
	if days <= 0 {
		days = 30
	}
	days = min(days, 90)
	if loc == nil {
		loc = time.UTC
	}
	rows, err := s.st.Q.ListSpaceThreadTimes(ctx, id)
	if err != nil {
		return apigen.SpaceProgress{}, err
	}
	return progress(rows, s.cfg.Now().In(loc), days), nil
}

// progress 是 Progress 的纯计算部分。
func progress(rows []db.ListSpaceThreadTimesRow, now time.Time, days int) apigen.SpaceProgress {
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	out := apigen.SpaceProgress{Days: make([]apigen.SpaceProgressDay, 0, days)}
	for i := days - 1; i >= 0; i-- {
		start := today.AddDate(0, 0, -i)
		end := start.AddDate(0, 0, 1)
		s0, e0 := start.UnixMilli(), end.UnixMilli()
		d := apigen.SpaceProgressDay{Date: start.Format("2006-01-02")}
		for _, r := range rows {
			in := func(ts *int64) bool { return ts != nil && *ts >= s0 && *ts < e0 }
			switch {
			case in(r.ShippedAt):
				d.Shipped++
			case in(r.ArchivedAt) && r.ShippedAt == nil:
				d.Archived++
			}
			if r.CreatedAt < e0 && (r.ArchivedAt == nil || *r.ArchivedAt >= e0) {
				d.Open++
			}
		}
		out.Days = append(out.Days, d)
	}
	return out
}

func eqStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
