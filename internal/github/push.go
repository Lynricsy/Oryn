package github

import (
	"context"
	"encoding/json"
)

// RepoSync 是项目仓库镜像的拉取入口（*repos.Service 实现，M24 步骤 2.6）。
type RepoSync interface{ Kick(projectID string) }

// SetRepoSync 注入项目仓库镜像（镜像服务在 GitHub 服务之后构造；须在 Run 之前调用）。
func (s *Service) SetRepoSync(r RepoSync) { s.repoSync = r }

func init() {
	on("push", (*Service).onPush)
}

// onPush 让绑定到该仓库的每个未归档项目的镜像后台拉取一次（Kick 单飞，不阻塞投递处理）。
func (s *Service) onPush(ctx context.Context, _ string, body []byte) (string, error) {
	if s.repoSync == nil {
		return "ignored:no_repo_sync", nil
	}
	var p struct {
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	if p.Repository.ID == 0 {
		return "ignored:payload", nil
	}
	prjs, err := s.st.Q.ListProjectsByGithubRepo(ctx, &p.Repository.ID)
	if err != nil {
		return "", err
	}
	if len(prjs) == 0 {
		return "ignored:no_project", nil
	}
	for _, prj := range prjs {
		s.repoSync.Kick(prj.ID)
	}
	return "handled", nil
}
