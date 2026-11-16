package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
)

// maxPollPages 是一轮轮询最多翻的投递日志页数。
const maxPollPages = 50

// hookDelivery 是投递日志列表里的一条。
type hookDelivery struct {
	ID         int64  `json:"id"`
	GUID       string `json:"guid"`
	Event      string `json:"event"`
	Redelivery bool   `json:"redelivery"`
}

// pollOnce 拉取 App 的投递日志，补齐 webhook 没送到的投递。首次只建立基线，不回放历史。
func (s *Service) pollOnce(ctx context.Context) error {
	app, err := s.st.Q.GetGithubApp(ctx)
	if err != nil {
		if isNoRows(err) {
			return nil
		}
		return err
	}
	jwt, err := s.appJWT(app)
	if err != nil {
		return err
	}
	auth := "Bearer " + jwt
	cursor := app.HookCursor

	var fresh []hookDelivery
	path := "/app/hook/deliveries?per_page=100"
	for page := 0; page < maxPollPages && path != ""; page++ {
		var items []hookDelivery
		h, err := s.request(ctx, http.MethodGet, path, auth, nil, &items)
		if err != nil {
			return err
		}
		if cursor == 0 {
			// 首次：游标设为当前最新的投递，之前的不回放
			next := int64(1)
			for _, it := range items {
				next = max(next, it.ID)
			}
			return s.st.Q.SetGithubHookCursor(ctx, next)
		}
		older := false
		for _, it := range items {
			if it.ID > cursor {
				fresh = append(fresh, it)
			} else {
				older = true
			}
		}
		if older {
			break
		}
		path = nextLink(h)
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].ID < fresh[j].ID })
	seen := cursor
	for _, it := range fresh {
		if !it.Redelivery {
			exists, err := s.st.Q.GithubDeliveryExists(ctx, it.GUID)
			if err != nil {
				return err
			}
			if !exists {
				if err := s.pollDelivery(ctx, auth, it); err != nil {
					slog.Warn("github: 取投递详情失败，本轮停止", "id", it.ID, "err", err)
					return s.st.Q.SetGithubHookCursor(ctx, max(it.ID-1, cursor))
				}
			}
		}
		seen = max(seen, it.ID)
	}
	return s.st.Q.SetGithubHookCursor(ctx, seen)
}

// pollDelivery 取一条投递的详情并受理。
func (s *Service) pollDelivery(ctx context.Context, auth string, it hookDelivery) error {
	var detail struct {
		Event   string `json:"event"`
		Request struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"request"`
	}
	if err := s.call(ctx, http.MethodGet, fmt.Sprintf("/app/hook/deliveries/%d", it.ID), auth, nil, &detail); err != nil {
		return err
	}
	body, err := json.Marshal(detail.Request.Payload)
	if err != nil {
		return err
	}
	event := it.Event
	if event == "" {
		event = detail.Event
	}
	s.ingest(ctx, it.GUID, event, body, "poll")
	return nil
}
