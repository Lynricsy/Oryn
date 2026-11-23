package repos

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

const (
	avatarTTL      = 24 * time.Hour   // 查到的账号与头像缓存这么久（提交不变，头像地址带用户 id，稳定）
	avatarFailTTL  = 10 * time.Minute // 查询失败（身份未连接、限流等）后这段时间内不再查
	avatarCacheMax = 2000             // 缓存条数上限，满了整体清空（足够简单，命中率在一页提交的范围内够用）
)

var fullShaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CommitAvatars 查一个提交的作者与共同作者在 GitHub 上的账号与头像。项目不存在 → 404；sha 不是完整 40 位 → 400；
// 其余情况（没有 GitHub、仓库不在 github.com、查询失败）返回空列表，不报错。结果按项目 + 提交缓存。
func (s *Service) CommitAvatars(ctx context.Context, projectID, sha string) (apigen.ProjectRepoCommitAvatars, error) {
	out := apigen.ProjectRepoCommitAvatars{Avatars: []apigen.ProjectRepoAvatar{}}
	if !fullShaRe.MatchString(sha) {
		return out, invalid("sha 须为完整的 40 位：%q", sha)
	}
	if _, err := s.project(ctx, projectID); err != nil {
		return out, err
	}
	if s.gh == nil {
		return out, nil
	}
	key := projectID + "\x00" + sha
	now := s.cfg.Now()
	s.avatarMu.Lock()
	if e, ok := s.avatarCache[key]; ok && now.Before(e.until) {
		s.avatarMu.Unlock()
		out.Avatars = append(out.Avatars, e.avatars...)
		return out, nil
	}
	s.avatarMu.Unlock()

	avatars, err := s.gh.CommitAvatars(ctx, projectID, sha)
	ttl := avatarTTL
	if err != nil {
		if ctx.Err() != nil {
			return out, nil // 请求被取消：不记失败
		}
		slog.Warn("查提交作者的 GitHub 头像", "project", projectID, "sha", sha, "err", err)
		avatars, ttl = nil, avatarFailTTL
	}
	s.avatarMu.Lock()
	if s.avatarCache == nil || len(s.avatarCache) >= avatarCacheMax {
		s.avatarCache = map[string]avatarEntry{}
	}
	s.avatarCache[key] = avatarEntry{avatars: avatars, until: now.Add(ttl)}
	s.avatarMu.Unlock()
	out.Avatars = append(out.Avatars, avatars...)
	return out, nil
}

type avatarEntry struct {
	avatars []apigen.ProjectRepoAvatar
	until   time.Time
}
