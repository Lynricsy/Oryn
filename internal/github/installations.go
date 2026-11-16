package github

import (
	"context"
	"encoding/json"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

type installationPayload struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	RepositorySelection string  `json:"repository_selection"`
	SuspendedAt         *string `json:"suspended_at"`
}

// SyncInstallations 从 GitHub 拉取 App 的全部安装并替换本地列表，随后重新绑定项目。
func (s *Service) SyncInstallations(ctx context.Context) error {
	auth, err := s.appAuth(ctx)
	if err != nil {
		return err
	}
	var all []installationPayload
	err = s.paged(ctx, "/app/installations?per_page=100", auth, 10, func(raw json.RawMessage) error {
		var page []installationPayload
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		all = append(all, page...)
		return nil
	})
	if err != nil {
		return err
	}
	now := s.nowMs()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		keep := make([]int64, 0, len(all))
		for _, in := range all {
			if err := q.UpsertGithubInstallation(ctx, db.UpsertGithubInstallationParams{
				ID: in.ID, AccountLogin: in.Account.Login, AccountType: in.Account.Type,
				RepositorySelection: in.RepositorySelection, Suspended: in.SuspendedAt != nil, UpdatedAt: now,
			}); err != nil {
				return err
			}
			keep = append(keep, in.ID)
		}
		if len(keep) == 0 {
			return q.DeleteGithubInstallations(ctx)
		}
		return q.DeleteGithubInstallationsExcept(ctx, keep)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	clear(s.instTokens)
	s.mu.Unlock()
	if err := s.BindProjects(ctx); err != nil {
		return err
	}
	s.changed()
	return nil
}

// BindProjects 按仓库地址重新绑定全部候选项目（2.1 换成真实现）。
func (s *Service) BindProjects(ctx context.Context) error { return nil }
