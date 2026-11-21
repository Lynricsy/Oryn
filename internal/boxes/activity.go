package boxes

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	// activityThreadDays 是活动流里列出线程的回看天数。
	activityThreadDays = 30
	// usageDays 是用量图的天数（UTC 自然日，含今天）。
	usageDays = 21
)

// Activity 返回活动流（ref 为空 = 全部 box）：box_activity 行加上 box orb 上近 30 天建的工作线程，按时间倒序截到 limit
// （limit 落在 1–100，越界取边界）。
func (s *Service) Activity(ctx context.Context, ref string, limit int) ([]apigen.BoxActivity, error) {
	limit = min(max(limit, 1), 100)
	q := s.st.Q
	var boxes []db.Box
	var rows []db.BoxActivity
	if ref == "" {
		all, err := q.ListBoxes(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range all {
			if o, err := s.orbs.Get(ctx, b.OrbID); err == nil && !gone(o) {
				boxes = append(boxes, b)
			}
		}
		if rows, err = q.ListAllBoxActivity(ctx, int64(limit)); err != nil {
			return nil, err
		}
	} else {
		b, _, err := s.live(ctx, ref)
		if err != nil {
			return nil, err
		}
		boxes = []db.Box{b}
		if rows, err = q.ListBoxActivity(ctx, db.ListBoxActivityParams{BoxID: b.ID, Lim: int64(limit)}); err != nil {
			return nil, err
		}
	}
	names := make(map[string]string, len(boxes))
	for _, b := range boxes {
		names[b.ID] = b.Name
	}
	out := make([]apigen.BoxActivity, 0, len(rows))
	for _, r := range rows {
		name, ok := names[r.BoxID]
		if !ok {
			continue
		}
		out = append(out, activityView(r, name))
	}
	since := s.now().Add(-activityThreadDays * 24 * time.Hour).UnixMilli()
	for _, b := range boxes {
		ths, err := q.ListThreadsByOrbSince(ctx, db.ListThreadsByOrbSinceParams{OrbID: &b.OrbID, Since: since})
		if err != nil {
			return nil, err
		}
		for _, t := range ths {
			if t.Kind != "work" {
				continue
			}
			v := apigen.BoxActivity{
				ID: "t:" + t.ID, BoxID: b.ID, BoxName: b.Name, TS: t.CreatedAt, Kind: apigen.BoxActivityThread,
				Actor: apigen.BoxActorOwner, ThreadID: new(t.ID), Title: new(t.Title),
			}
			if app := appOfCwd(t.Cwd); app != "" {
				v.App = &app
			}
			out = append(out, v)
		}
	}
	slices.SortStableFunc(out, func(a, b apigen.BoxActivity) int { return cmp.Compare(b.TS, a.TS) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// appOfCwd 从线程 cwd（`~/apps/<app>/…`）取 app 名；不是 app 目录时为空串。
func appOfCwd(cwd *string) string {
	if cwd == nil {
		return ""
	}
	rest, ok := strings.CutPrefix(*cwd, "~/apps/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}

// activityView 把活动行转成 API 形态。
func activityView(r db.BoxActivity, boxName string) apigen.BoxActivity {
	v := apigen.BoxActivity{
		ID: "a:" + strconv.FormatInt(r.ID, 10), BoxID: r.BoxID, BoxName: boxName, TS: r.Ts,
		Kind: apigen.BoxActivityKind(r.Kind), Actor: apigen.BoxActor(r.Actor), App: r.App,
	}
	var d activity
	if json.Unmarshal([]byte(r.Detail), &d) == nil {
		v.Commit, v.ReleaseID, v.ThreadID = nonEmpty(d.Commit), nonEmpty(d.ReleaseID), nonEmpty(d.ThreadID)
		v.Error, v.From, v.To, v.Public = nonEmpty(d.Error), nonEmpty(d.From), nonEmpty(d.To), d.Public
	}
	return v
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Usage 返回最近 21 个 UTC 自然日（旧到新，含今天）每天在这台 box 上建的工作线程数与 orb 处于 ready 的时长占比。
// 今天只算到现在；box 创建当天从创建时刻算起，之前的日子两项都是 0。
func (s *Service) Usage(ctx context.Context, ref string) (apigen.BoxUsage, error) {
	b, _, err := s.live(ctx, ref)
	if err != nil {
		return apigen.BoxUsage{}, err
	}
	q := s.st.Q
	nowT := s.now().UTC()
	now := nowT.UnixMilli()
	today := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, time.UTC)
	first := today.AddDate(0, 0, -(usageDays - 1))
	start := first.UnixMilli()

	days := make([]apigen.BoxUsageDay, usageDays)
	bounds := make([]int64, usageDays+1)
	for i := range usageDays {
		d := first.AddDate(0, 0, i)
		days[i].Date = d.Format(time.DateOnly)
		bounds[i] = d.UnixMilli()
	}
	bounds[usageDays] = today.AddDate(0, 0, 1).UnixMilli()
	dayOf := func(ts int64) int {
		i, _ := slices.BinarySearch(bounds, ts+1)
		return i - 1
	}

	ths, err := q.ListThreadsByOrbSince(ctx, db.ListThreadsByOrbSinceParams{OrbID: &b.OrbID, Since: start})
	if err != nil {
		return apigen.BoxUsage{}, err
	}
	for _, t := range ths {
		if i := dayOf(t.CreatedAt); t.Kind == "work" && i >= 0 && i < usageDays {
			days[i].Threads++
		}
	}

	// ready 区间：窗口起点之前的最后一条事件定初始状态，之后逐条累加
	up := make([]int64, usageDays)
	state := ""
	if ev, err := q.GetOrbEventBefore(ctx, db.GetOrbEventBeforeParams{OrbID: b.OrbID, Before: start}); err == nil {
		state = ev.ToState
	} else if !errors.Is(err, sql.ErrNoRows) {
		return apigen.BoxUsage{}, err
	}
	evs, err := q.ListOrbEventsSince(ctx, db.ListOrbEventsSinceParams{OrbID: b.OrbID, Since: start})
	if err != nil {
		return apigen.BoxUsage{}, err
	}
	at := start
	addUp := func(from, to int64) {
		for i := range usageDays {
			lo, hi := max(from, bounds[i]), min(to, bounds[i+1])
			if hi > lo {
				up[i] += hi - lo
			}
		}
	}
	for _, ev := range evs {
		ts := min(max(ev.Ts, at), now)
		if state == orbs.StateReady {
			addUp(at, ts)
		}
		at, state = ts, ev.ToState
	}
	if state == orbs.StateReady {
		addUp(at, now)
	}

	for i := range usageDays {
		lo, hi := max(bounds[i], b.CreatedAt), min(bounds[i+1], now)
		if hi <= lo {
			days[i].Threads = 0
			continue
		}
		days[i].UpRatio = float32(min(float64(up[i])/float64(hi-lo), 1))
	}
	return apigen.BoxUsage{Days: days}, nil
}
