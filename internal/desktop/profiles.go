package desktop

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// cleanName 校验 Profile 名字：trim 后 1–40 个字符，不含控制字符。
func cleanName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" || utf8.RuneCountInString(n) > maxNameRunes || !utf8.ValidString(n) || strings.IndexFunc(n, unicode.IsControl) >= 0 {
		return "", errf(http.StatusBadRequest, "invalid_request", "Profile 名字须为 1–40 个字符且不含控制字符")
	}
	return n, nil
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// getProfile 读取 Profile；不存在 → errNotFound。
func getProfile(ctx context.Context, q *db.Queries, id string) (db.BrowserProfile, error) {
	p, err := q.GetBrowserProfile(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound
	}
	return p, err
}

// view 把 Profile 行转换成 API 形态（holder 与 readers 带代表线程）。
func (s *Service) view(ctx context.Context, q *db.Queries, p db.BrowserProfile, readers []db.ListBrowserProfileReadersRow) (apigen.BrowserProfile, error) {
	v := apigen.BrowserProfile{
		ID: p.ID, Name: p.Name, Revision: p.Revision, SizeBytes: p.SizeBytes, SavedAt: p.SavedAt,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.SaveError != nil {
		e := apigen.BrowserProfileSaveError(*p.SaveError)
		v.SaveError = &e
	}
	if p.HolderOrbID != nil {
		h, err := orbs.HolderView(ctx, q, *p.HolderOrbID, deref64(p.HeldAt))
		if err != nil {
			return v, err
		}
		v.Holder = &h
	}
	for _, r := range readers {
		if r.ProfileID == nil || *r.ProfileID != p.ID {
			continue
		}
		h, err := orbs.HolderView(ctx, q, r.OrbID, deref64(r.ReadonlyAt))
		if err != nil {
			return v, err
		}
		v.Readers = append(v.Readers, h)
	}
	return v, nil
}

// get 返回 Profile 的 API 形态。
func (s *Service) get(ctx context.Context, id string) (apigen.BrowserProfile, error) {
	p, err := getProfile(ctx, s.st.Q, id)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	readers, err := s.st.Q.ListBrowserProfileReaders(ctx, &id)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	return s.view(ctx, s.st.Q, p, readers)
}

// List 按名字列出全部 Profile。
func (s *Service) List(ctx context.Context) ([]apigen.BrowserProfile, error) {
	rows, err := s.st.Q.ListBrowserProfiles(ctx)
	if err != nil {
		return nil, err
	}
	readers, err := s.st.Q.ListBrowserProfileReaders(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.BrowserProfile, 0, len(rows))
	for _, p := range rows {
		v, err := s.view(ctx, s.st.Q, p, readers)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Create 新建空 Profile（修订 0）。
func (s *Service) Create(ctx context.Context, name string) (apigen.BrowserProfile, error) {
	n, err := cleanName(name)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	id := ids.BrowserProfile()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		cnt, err := q.CountBrowserProfiles(ctx)
		if err != nil {
			return err
		}
		if cnt >= maxProfiles {
			return errf(http.StatusConflict, "profile_limit", fmt.Sprintf("Profile 最多 %d 个", maxProfiles))
		}
		err = q.CreateBrowserProfile(ctx, db.CreateBrowserProfileParams{ID: id, Name: n, Now: s.nowMs()})
		if isUnique(err) {
			return errf(http.StatusConflict, "profile_name_taken", "已有同名 Profile")
		}
		return err
	})
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	return s.get(ctx, id)
}

// Rename 改名。
func (s *Service) Rename(ctx context.Context, id, name string) (apigen.BrowserProfile, error) {
	n, err := cleanName(name)
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	rows, err := s.st.Q.RenameBrowserProfile(ctx, db.RenameBrowserProfileParams{Name: n, Now: s.nowMs(), ID: id})
	if isUnique(err) {
		return apigen.BrowserProfile{}, errf(http.StatusConflict, "profile_name_taken", "已有同名 Profile")
	}
	if err != nil {
		return apigen.BrowserProfile{}, err
	}
	if rows == 0 {
		return apigen.BrowserProfile{}, errNotFound
	}
	return s.get(ctx, id)
}

// Delete 删除 Profile：被独占持有 → profile_in_use；成功后清空引用它的项目默认值并删除归档目录。
// 只读使用者不阻止删除（其绑定经外键置空）。
func (s *Service) Delete(ctx context.Context, id string) error {
	unlock, err := s.profLocks.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	readers, err := s.st.Q.ListBrowserProfileReaders(ctx, &id)
	if err != nil {
		return err
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteBrowserProfile(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			p, err := getProfile(ctx, q, id)
			if err != nil {
				return err
			}
			return s.inUse(ctx, q, p)
		}
		return q.ClearProjectsBrowserProfile(ctx, id)
	})
	if err != nil {
		return err
	}
	if err := os.RemoveAll(s.profileDir(id)); err != nil {
		slog.Warn("删除 Profile 归档目录失败", "profile", id, "err", err)
	}
	slog.Info("浏览器 Profile 已删除", "profile", id)
	for _, r := range readers {
		s.o.RefreshDesktop(ctx, r.OrbID)
	}
	return nil
}

// inUse 返回 profile_in_use 错误，响应体为 ProfileInUseConflict（带持有者代表线程的标题与开始持有的时间）。
func (s *Service) inUse(ctx context.Context, q *db.Queries, p db.BrowserProfile) error {
	if p.HolderOrbID == nil {
		return errf(http.StatusConflict, "profile_in_use", "Profile 正在使用")
	}
	h, err := orbs.HolderView(ctx, q, *p.HolderOrbID, deref64(p.HeldAt))
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Profile「%s」正在 orb %s 上使用", p.Name, h.OrbID)
	if h.ThreadTitle != nil {
		msg = fmt.Sprintf("Profile「%s」正在线程「%s」里使用", p.Name, strOr(h.ThreadTitle, *h.ThreadID))
	}
	return &Error{
		Code: "profile_in_use", Status: http.StatusConflict, Message: msg,
		Conflict: &apigen.ProfileInUseConflict{
			Code: apigen.ProfileInUseConflictCodeProfileInUse, Message: msg, ProfileID: p.ID, ProfileName: p.Name, Holder: h,
		},
	}
}

// normMode 规范化选用方式：空 = exclusive；非法 → invalid_request。
func normMode(mode apigen.BrowserProfileMode) (apigen.BrowserProfileMode, error) {
	if mode == "" {
		return apigen.BrowserProfileModeExclusive, nil
	}
	if !mode.Valid() {
		return "", errf(http.StatusBadRequest, "invalid_request", "profileMode 须为 exclusive / readonly / takeover")
	}
	return mode, nil
}

// ResolveForNewOrb 决定新 orb 选用的 Profile：explicit == nil → 项目的默认 Profile（没有项目或没设为空）；
// *explicit == "" → 不用；否则须存在（profile_not_found）。以 exclusive 选用而 Profile 正被某台 orb 独占持有 → profile_in_use。
func (s *Service) ResolveForNewOrb(ctx context.Context, explicit *string, projectID string, mode apigen.BrowserProfileMode) (string, error) {
	mode, err := normMode(mode)
	if err != nil {
		return "", err
	}
	var id string
	switch {
	case explicit == nil:
		if projectID == "" {
			return "", nil
		}
		prj, err := s.st.Q.GetProject(ctx, projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if prj.DefaultBrowserProfileID == nil || *prj.DefaultBrowserProfileID == "" {
			return "", nil
		}
		id = *prj.DefaultBrowserProfileID
	case *explicit == "":
		return "", nil
	default:
		id = *explicit
	}
	p, err := getProfile(ctx, s.st.Q, id)
	if err != nil {
		if errors.Is(err, errNotFound) && explicit == nil {
			slog.Warn("项目的默认浏览器 Profile 已不存在，按不用处理", "project", projectID, "profile", id)
			return "", nil
		}
		return "", err
	}
	if mode == apigen.BrowserProfileModeExclusive && p.HolderOrbID != nil {
		return "", s.inUse(ctx, s.st.Q, p)
	}
	return id, nil
}

// CheckExists 校验 Profile 存在（项目默认值写入前）；不存在 → profile_not_found。
func (s *Service) CheckExists(ctx context.Context, id string) error {
	_, err := getProfile(ctx, s.st.Q, id)
	return err
}
