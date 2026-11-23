package github

import (
	"context"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// commitAvatarsQuery 取一个提交的作者与共同作者（GitHub 按 git 作者与 `Co-authored-by` trailer 列出），以及能对上的账号。
const commitAvatarsQuery = `query($owner:String!,$name:String!,$oid:GitObjectID!){repository(owner:$owner,name:$name){object(oid:$oid){... on Commit{authors(first:20){nodes{email user{login avatarUrl(size:80)}}}}}}}`

// CommitAvatars 以用户身份查项目仓库（须在 github.com）里提交 sha 的作者在 GitHub 上的账号与头像，按邮箱去重（不分大小写），
// 只返回对上账号的人。项目不存在或仓库不在 github.com 时返回 (nil, nil)；身份未连接、仓库不可读等返回错误。
func (s *Service) CommitAvatars(ctx context.Context, projectID, sha string) ([]apigen.ProjectRepoAvatar, error) {
	prj, err := s.st.Q.GetProject(ctx, projectID)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	full, ok := githubRepoPath(prj.RepoUrl)
	if !ok {
		return nil, nil
	}
	owner, name, _ := strings.Cut(full, "/")
	var out struct {
		Repository *struct {
			Object *struct {
				Authors struct {
					Nodes []struct {
						Email string `json:"email"`
						User  *struct {
							Login     string `json:"login"`
							AvatarURL string `json:"avatarUrl"`
						} `json:"user"`
					} `json:"nodes"`
				} `json:"authors"`
			} `json:"object"`
		} `json:"repository"`
	}
	if err := s.graphql(ctx, slotUser, commitAvatarsQuery, map[string]any{"owner": owner, "name": name, "oid": sha}, &out); err != nil {
		return nil, err
	}
	if out.Repository == nil || out.Repository.Object == nil {
		return nil, nil
	}
	var avatars []apigen.ProjectRepoAvatar
	seen := map[string]bool{}
	for _, n := range out.Repository.Object.Authors.Nodes {
		key := strings.ToLower(n.Email)
		if n.User == nil || n.User.AvatarURL == "" || n.Email == "" || seen[key] {
			continue
		}
		// 只接受 https 头像地址：前端直接当 <img src> 用
		if !strings.HasPrefix(n.User.AvatarURL, "https://") {
			continue
		}
		seen[key] = true
		avatars = append(avatars, apigen.ProjectRepoAvatar{Email: n.Email, Login: n.User.Login, AvatarURL: n.User.AvatarURL})
	}
	return avatars, nil
}
