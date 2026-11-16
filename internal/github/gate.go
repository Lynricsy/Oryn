package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// sender 是事件的发送者（载荷里的 sender）。
type sender struct{ Login, Type string }

func senderOf(p senderPayload) sender { return sender{Login: p.Login, Type: p.Type} }

// 驱动者档位（R13）。
const (
	driversWriters          = "writers"
	driversWritersAndAuthor = "writers_and_author"
	driversAnyone           = "anyone"
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

// driveRule 是驱动判定用到的规则选项（R13）：档位、规则维护者、评审机器人（登录名都不区分大小写）。
type driveRule struct {
	drivers      string
	maintainers  []string
	reviewerBots []string
}

// defaultDriveRule 用于没有来源规则的线程（手动建的线程关联了 PR 等）：只认写权限者。
var defaultDriveRule = driveRule{drivers: driversWriters}

// ruleOf 取规则的驱动选项；列表列是 JSON 登录名数组，解析失败按空。
func ruleOf(t db.GithubTrigger) driveRule {
	r := driveRule{drivers: t.Drivers}
	_ = json.Unmarshal([]byte(t.Maintainers), &r.maintainers)
	_ = json.Unmarshal([]byte(t.ReviewerBots), &r.reviewerBots)
	if r.drivers == "" {
		r.drivers = driversWritersAndAuthor
	}
	return r
}

// botLogin 规范 bot 登录名：小写并去掉结尾的 `[bot]`（规则里写 `coderabbitai` 或 `coderabbitai[bot]` 都认）。
func botLogin(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), "[bot]")
}

func (r driveRule) isMaintainer(login string) bool {
	for _, m := range r.maintainers {
		if login != "" && strings.EqualFold(strings.TrimSpace(m), login) {
			return true
		}
	}
	return false
}

// isReviewerBot 报告 who 是不是规则的评审机器人：登录名与列表项完全相同；或 who 是 Bot 且去掉 `[bot]` 后相同
// （人类账号不能靠与 bot 同名的登录名冒充）。
func (r driveRule) isReviewerBot(who sender) bool {
	if who.Login == "" {
		return false
	}
	for _, b := range r.reviewerBots {
		if strings.EqualFold(strings.TrimSpace(b), who.Login) || who.Type == "Bot" && botLogin(b) == botLogin(who.Login) {
			return true
		}
	}
	return false
}

// pragmaAllowed 报告 login（这段文字的作者，assoc 是它的 author_association）的 `/model`、`/thinking` 指令是否生效（R14）：
// 只认 OWNER / MEMBER / COLLABORATOR 与规则维护者。
func (r driveRule) pragmaAllowed(login, assoc string) bool {
	return writerAssociations[strings.ToUpper(assoc)] || r.isMaintainer(login)
}

// drive 是驱动判定的结果：ok 为假时 reason 是拒绝原因；limit 是「任何人」档该发送者每小时的运行上限（0 = 不限）。
type drive struct {
	ok     bool
	reason string
	limit  int
}

// canDrive 报告发送者 who 能否在仓库 repo（owner/name）上驱动 agent（R13）。author 是 issue / PR 的作者，assoc 是发送者
// 这条内容的 author_association（取不到时为空），r 是来源规则的选项。判定顺序：
//   - 规则的评审机器人放行（视为维护者指令）；其余 Bot 一律拒绝；
//   - 我的账号、规则维护者、assoc 为 OWNER / MEMBER / COLLABORATOR 放行；
//   - anyone 档放行其余所有人，但按人限流：CONTRIBUTOR 每小时 contributorMaxPerHour 次，其余 anyoneMaxPerHour 次；
//   - writers_and_author 档放行作者；
//   - 其余经 isWriter 查权限接口。
//
// err 非 nil 表示上游故障（可重试）。
func (s *Service) canDrive(ctx context.Context, repo string, who sender, author, assoc string, r driveRule) (drive, error) {
	if r.isReviewerBot(who) {
		return drive{ok: true}, nil
	}
	if who.Type == "Bot" {
		return drive{reason: "bot"}, nil
	}
	if who.Login == "" {
		return drive{reason: "no_write_access"}, nil
	}
	if mine, err := s.isUserLogin(ctx, who.Login); err != nil || mine {
		return drive{ok: mine}, err
	}
	if r.isMaintainer(who.Login) || writerAssociations[strings.ToUpper(assoc)] {
		return drive{ok: true}, nil
	}
	if r.drivers == driversAnyone {
		if strings.EqualFold(assoc, "CONTRIBUTOR") {
			return drive{ok: true, limit: contributorMaxPerHour}, nil
		}
		return drive{ok: true, limit: anyoneMaxPerHour}, nil
	}
	if r.drivers == driversWritersAndAuthor && author != "" && strings.EqualFold(author, who.Login) {
		return drive{ok: true}, nil
	}
	return s.writerDrive(ctx, repo, who.Login)
}

// canLabel 是 issues.labeled 事件的发送者判定（R13，与档位无关：打标签本身需要仓库的分诊权限）：Bot 拒绝；我的账号、
// 仓库 owner、规则维护者放行；其余经 isWriter 查权限接口。
func (s *Service) canLabel(ctx context.Context, repo string, who sender, r driveRule) (drive, error) {
	if who.Type == "Bot" {
		return drive{reason: "bot"}, nil
	}
	if who.Login == "" {
		return drive{reason: "no_write_access"}, nil
	}
	if mine, err := s.isUserLogin(ctx, who.Login); err != nil || mine {
		return drive{ok: mine}, err
	}
	if owner, _, _ := strings.Cut(repo, "/"); strings.EqualFold(owner, who.Login) || r.isMaintainer(who.Login) {
		return drive{ok: true}, nil
	}
	return s.writerDrive(ctx, repo, who.Login)
}

// isUserLogin 报告 login 是不是「我的账号」；我的账号未连接时为否。
func (s *Service) isUserLogin(ctx context.Context, login string) (bool, error) {
	user, err := s.st.Q.GetGithubIdentity(ctx, slotUser)
	switch {
	case err == nil:
		return strings.EqualFold(user.Login, login), nil
	case isNoRows(err):
		return false, nil
	}
	return false, err
}

// writerDrive 经 isWriter 判定；没有写权限时拒绝为 no_write_access。
func (s *Service) writerDrive(ctx context.Context, repo, login string) (drive, error) {
	if repo == "" {
		return drive{reason: "no_write_access"}, nil
	}
	allowed, err := s.isWriter(ctx, repo, login)
	if err != nil {
		return drive{}, err
	}
	if !allowed {
		return drive{reason: "no_write_access"}, nil
	}
	return drive{ok: true}, nil
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
