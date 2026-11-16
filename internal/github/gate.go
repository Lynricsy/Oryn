package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sender 是事件的发送者（载荷里的 sender）。
type sender struct{ Login, Type string }

func senderOf(p senderPayload) sender { return sender{Login: p.Login, Type: p.Type} }

// 驱动者档位（R13）。M15 步骤 1.4 只用 writers，4.1 开放其余档位。
const (
	driversWriters          = "writers"
	driversWritersAndAuthor = "writers_and_author"
)

// writerAssociations 是直接视为写权限者的 author_association。
var writerAssociations = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

type permKey struct {
	repo  string // owner/name，小写
	login string // 小写；空串表示「我的账号对该仓库有没有 push 权限」
}

type permEntry struct {
	ok  bool
	exp time.Time
}

// canDrive 报告发送者 who 能否在仓库 repo（owner/name）上驱动 agent（R13）。author 是 issue / PR 的作者，assoc 是发送者
// 这条内容的 author_association（取不到时为空），drivers 是档位。判定顺序：Bot 一律拒绝；我的账号放行；
// writers_and_author 档放行作者；assoc 为 OWNER / MEMBER / COLLABORATOR 放行；其余经 isWriter 查权限接口。
// reason 非空表示拒绝；err 非 nil 表示上游故障（可重试）。
func (s *Service) canDrive(ctx context.Context, repo string, who sender, author, assoc, drivers string) (ok bool, reason string, err error) {
	if who.Type == "Bot" {
		return false, "bot", nil
	}
	if who.Login == "" {
		return false, "no_write_access", nil
	}
	if user, err := s.st.Q.GetGithubIdentity(ctx, slotUser); err == nil && strings.EqualFold(user.Login, who.Login) {
		return true, "", nil
	} else if err != nil && !isNoRows(err) {
		return false, "", err
	}
	if drivers == driversWritersAndAuthor && author != "" && strings.EqualFold(author, who.Login) {
		return true, "", nil
	}
	if writerAssociations[strings.ToUpper(assoc)] {
		return true, "", nil
	}
	if repo == "" {
		return false, "no_write_access", nil
	}
	allowed, err := s.isWriter(ctx, repo, who.Login)
	if err != nil {
		return false, "", err
	}
	if !allowed {
		return false, "no_write_access", nil
	}
	return true, "", nil
}

// isWriter 经 `collaborators/{login}/permission` 判定 login 对仓库是否有 write / admin 权限。该接口要求调用者有 push 权限
// （否则回 403，附录 B-9），所以先看我的账号的 permissions.push：为假时直接判否、不调接口；接口回 403 / 404 也判否。
// 两级结果都缓存 permTTL。我的账号未连接或读不到仓库时判否。
func (s *Service) isWriter(ctx context.Context, repo, login string) (bool, error) {
	repoKey := strings.ToLower(repo)
	push, err := s.cachedPerm(permKey{repo: repoKey}, func() (bool, error) {
		var r struct {
			Permissions struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		err := s.callAs(ctx, slotUser, http.MethodGet, "/repos/"+repo, nil, &r)
		var ge *Error
		switch kind := errKind(err); {
		case err == nil:
			return r.Permissions.Push, nil
		case errors.As(err, &ge) && ge.Code == "github_identity_required",
			kind == kindNotFound, kind == kindOrgRestricted, kind == kindSSORequired, isStatus(err, http.StatusForbidden):
			return false, nil
		}
		return false, err
	})
	if err != nil || !push {
		return false, err
	}
	return s.cachedPerm(permKey{repo: repoKey, login: strings.ToLower(login)}, func() (bool, error) {
		var out struct {
			Permission string `json:"permission"`
		}
		err := s.callAs(ctx, slotUser, http.MethodGet, "/repos/"+repo+"/collaborators/"+url.PathEscape(login)+"/permission", nil, &out)
		switch {
		case err == nil:
			return out.Permission == "admin" || out.Permission == "write", nil
		case isStatus(err, http.StatusForbidden), isStatus(err, http.StatusNotFound):
			return false, nil
		}
		return false, err
	})
}

// cachedPerm 取 perms 缓存里未过期的结果；没有时调用 fetch，成功的结果缓存 permTTL（上游故障不缓存）。
func (s *Service) cachedPerm(key permKey, fetch func() (bool, error)) (bool, error) {
	s.mu.Lock()
	c, hit := s.perms[key]
	s.mu.Unlock()
	if hit && s.now().Before(c.exp) {
		return c.ok, nil
	}
	ok, err := fetch()
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	if s.perms == nil {
		s.perms = map[permKey]permEntry{}
	}
	s.perms[key] = permEntry{ok: ok, exp: s.now().Add(permTTL)}
	s.mu.Unlock()
	return ok, nil
}
