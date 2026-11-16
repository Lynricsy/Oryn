package github

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// maxRepoResults 是仓库列表一次返回的上限。
const maxRepoResults = 50

type repoListEntry struct {
	repos []apigen.GitHubRepo
	exp   time.Time
}

type repoPayload struct {
	ID            int64   `json:"id"`
	FullName      string  `json:"full_name"`
	Private       bool    `json:"private"`
	DefaultBranch string  `json:"default_branch"`
	Description   *string `json:"description"`
	PushedAt      *string `json:"pushed_at"`
}

// ListRepos 列出「我的账号」经 App 可访问的仓库：按 full_name 子串（忽略大小写）过滤，按最近推送倒序，至多 50 个。
func (s *Service) ListRepos(ctx context.Context, q string) ([]apigen.GitHubRepo, error) {
	tok, err := s.tokenFor(ctx, slotUser)
	if err != nil {
		return nil, err
	}
	all, err := s.userRepos(ctx, tok)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]apigen.GitHubRepo, 0, min(len(all), maxRepoResults))
	for _, r := range all {
		if q == "" || strings.Contains(strings.ToLower(r.FullName), q) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b apigen.GitHubRepo) int {
		var x, y int64
		if a.PushedAt != nil {
			x = *a.PushedAt
		}
		if b.PushedAt != nil {
			y = *b.PushedAt
		}
		return cmp.Compare(y, x)
	})
	if len(out) > maxRepoResults {
		out = out[:maxRepoResults]
	}
	return out, nil
}

// userRepos 取用户令牌经各安装可访问的全部仓库（去重），按令牌的 sha256 缓存 repoListTTL。
func (s *Service) userRepos(ctx context.Context, tok string) ([]apigen.GitHubRepo, error) {
	key := sha256.Sum256([]byte(tok))
	now := s.now()
	s.mu.Lock()
	if e, ok := s.repoLists[key]; ok && now.Before(e.exp) {
		s.mu.Unlock()
		return e.repos, nil
	}
	s.mu.Unlock()

	auth := "Bearer " + tok
	var instIDs []int64
	err := s.paged(ctx, "/user/installations?per_page=100", auth, 10, func(raw json.RawMessage) error {
		var page struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return err
		}
		for _, in := range page.Installations {
			instIDs = append(instIDs, in.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var repos []apigen.GitHubRepo
	for _, id := range instIDs {
		path := fmt.Sprintf("/user/installations/%d/repositories?per_page=100", id)
		err := s.paged(ctx, path, auth, 10, func(raw json.RawMessage) error {
			var page struct {
				Repositories []repoPayload `json:"repositories"`
			}
			if err := json.Unmarshal(raw, &page); err != nil {
				return err
			}
			for _, r := range page.Repositories {
				if seen[r.ID] {
					continue
				}
				seen[r.ID] = true
				repos = append(repos, repoAPI(r))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	if s.repoLists == nil {
		s.repoLists = map[[32]byte]repoListEntry{}
	}
	for k, e := range s.repoLists {
		if !now.Before(e.exp) {
			delete(s.repoLists, k)
		}
	}
	s.repoLists[key] = repoListEntry{repos: repos, exp: now.Add(repoListTTL)}
	s.mu.Unlock()
	return repos, nil
}

func repoAPI(r repoPayload) apigen.GitHubRepo {
	out := apigen.GitHubRepo{ID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: r.DefaultBranch}
	if r.Description != nil && *r.Description != "" {
		out.Description = r.Description
	}
	if r.PushedAt != nil {
		if t, err := time.Parse(time.RFC3339, *r.PushedAt); err == nil {
			ms := t.UnixMilli()
			out.PushedAt = &ms
		}
	}
	return out
}
