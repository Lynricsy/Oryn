package sshgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	maxJumpChain   = 5
	maxTags        = 16
	maxTagLen      = 32
	maxDescription = 500
)

var (
	hostNameRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
	hostAddressRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
)

// HostPatch 是主机的局部更新。JumpHostSet / CompatSet 表示请求里出现了该键（含 null）：
// 出现且为 nil = 清掉，未出现 = 不改。
type HostPatch struct {
	apigen.SSHHostUpdate
	JumpHostSet bool
	CompatSet   bool
}

func passwordAAD(hostID string) string { return "ssh/host/" + hostID + "/password" }

// ListHosts 返回全部主机（全局在前，再按项目、名称）。
func (s *Service) ListHosts(ctx context.Context) (apigen.SSHHostList, error) {
	rows, err := s.st.Q.ListSshHosts(ctx)
	if err != nil {
		return apigen.SSHHostList{}, err
	}
	out := apigen.SSHHostList{Hosts: make([]apigen.SSHHost, 0, len(rows))}
	for _, h := range rows {
		out.Hosts = append(out.Hosts, hostView(h))
	}
	return out, nil
}

// GetHost 返回单台主机。
func (s *Service) GetHost(ctx context.Context, id string) (apigen.SSHHost, error) {
	h, err := s.host(ctx, id)
	if err != nil {
		return apigen.SSHHost{}, err
	}
	return hostView(h), nil
}

func (s *Service) host(ctx context.Context, id string) (db.SshHost, error) {
	h, err := s.st.Q.GetSshHost(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.SshHost{}, notFound("主机不存在")
	}
	return h, err
}

// CreateHost 登记主机。
func (s *Service) CreateHost(ctx context.Context, in apigen.SSHHostCreate) (apigen.SSHHost, error) {
	now := s.now()
	h := db.SshHost{
		ID: ids.SSHHost(), ProjectID: in.ProjectID, Name: strings.TrimSpace(in.Name), Address: in.Address, Port: 22,
		Username: in.Username, AuthType: string(in.AuthType), KeyID: in.KeyID, JumpHostID: in.JumpHostID,
		Enabled: true, Compat: in.Compat, CreatedAt: now, UpdatedAt: now,
	}
	if in.Port != nil {
		h.Port = int64(*in.Port)
	}
	if in.Description != nil {
		h.Description = *in.Description
	}
	if in.Enabled != nil {
		h.Enabled = *in.Enabled
	}
	if in.AllowAutomation != nil {
		h.AllowAutomation = *in.AllowAutomation
	}
	if in.AllowForward != nil {
		h.AllowForward = *in.AllowForward
	}
	if h.ProjectID != nil && *h.ProjectID == "" {
		h.ProjectID = nil
	}
	pw := in.Password
	if pw != nil && *pw == "" {
		pw = nil
	}
	if err := s.check(ctx, &h, nil, pw, in.Tags); err != nil {
		return apigen.SSHHost{}, err
	}
	err := s.st.Q.InsertSshHost(ctx, db.InsertSshHostParams{
		ID: h.ID, ProjectID: h.ProjectID, Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username,
		AuthType: h.AuthType, KeyID: h.KeyID, PasswordEnc: h.PasswordEnc, JumpHostID: h.JumpHostID,
		Description: h.Description, Tags: h.Tags, Enabled: h.Enabled, AllowAutomation: h.AllowAutomation,
		AllowForward: h.AllowForward, Compat: h.Compat, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return apigen.SSHHost{}, conflict("同名主机已存在")
		}
		return apigen.SSHHost{}, err
	}
	s.afterHostChange(h.ID, h.ProjectID)
	return hostView(h), nil
}

// UpdateHost 改主机（只改给出的字段；作用域不可改）。
func (s *Service) UpdateHost(ctx context.Context, id string, p HostPatch) (apigen.SSHHost, error) {
	old, err := s.host(ctx, id)
	if err != nil {
		return apigen.SSHHost{}, err
	}
	h := old
	u := p.SSHHostUpdate
	if u.Name != nil {
		h.Name = strings.TrimSpace(*u.Name)
	}
	if u.Address != nil {
		h.Address = *u.Address
	}
	if u.Port != nil {
		h.Port = int64(*u.Port)
	}
	if u.Username != nil {
		h.Username = *u.Username
	}
	if u.AuthType != nil {
		h.AuthType = string(*u.AuthType)
	}
	if u.KeyID != nil {
		h.KeyID = u.KeyID
	}
	if p.JumpHostSet {
		h.JumpHostID = u.JumpHostID
		if h.JumpHostID != nil && *h.JumpHostID == "" {
			h.JumpHostID = nil
		}
	}
	if u.Description != nil {
		h.Description = *u.Description
	}
	if u.Enabled != nil {
		h.Enabled = *u.Enabled
	}
	if u.AllowAutomation != nil {
		h.AllowAutomation = *u.AllowAutomation
	}
	if u.AllowForward != nil {
		h.AllowForward = *u.AllowForward
	}
	if p.CompatSet {
		h.Compat = u.Compat
	}
	tags := u.Tags
	if tags == nil {
		if err := json.Unmarshal([]byte(old.Tags), &tags); err != nil {
			tags = nil
		}
	}
	pw := u.Password
	if pw != nil && *pw == "" {
		pw = nil // 留空 = 沿用原密码
	}
	if err := s.check(ctx, &h, &old, pw, tags); err != nil {
		return apigen.SSHHost{}, err
	}
	h.UpdatedAt = s.now()
	_, err = s.st.Q.UpdateSshHost(ctx, db.UpdateSshHostParams{
		Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username, AuthType: h.AuthType, KeyID: h.KeyID,
		PasswordEnc: h.PasswordEnc, JumpHostID: h.JumpHostID, Description: h.Description, Tags: h.Tags,
		Enabled: h.Enabled, AllowAutomation: h.AllowAutomation, AllowForward: h.AllowForward, Compat: h.Compat,
		UpdatedAt: h.UpdatedAt, ID: h.ID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return apigen.SSHHost{}, conflict("同名主机已存在")
		}
		return apigen.SSHHost{}, err
	}
	if h.Address != old.Address || h.Port != old.Port {
		// 换了机器，旧指纹不再适用
		if _, err := s.st.Q.ClearSshHostKey(ctx, db.ClearSshHostKeyParams{At: h.UpdatedAt, ID: h.ID}); err != nil {
			return apigen.SSHHost{}, err
		}
		h.HostKey, h.HostKeyAt = nil, nil
	}
	s.afterHostChange(h.ID, h.ProjectID)
	return hostView(h), nil
}

// DeleteHost 删除主机；被其他主机用作跳板时拒绝。
func (s *Service) DeleteHost(ctx context.Context, id string) error {
	h, err := s.host(ctx, id)
	if err != nil {
		return err
	}
	n, err := s.st.Q.CountSshHostsByJump(ctx, &id)
	if err != nil {
		return err
	}
	if n > 0 {
		return conflict("主机被其他主机用作跳板")
	}
	if _, err := s.st.Q.DeleteSshHost(ctx, id); err != nil {
		return err
	}
	s.afterHostChange(id, h.ProjectID)
	return nil
}

// ResetHostKey 清掉已固定的主机指纹（下次成功连接重新固定）。
func (s *Service) ResetHostKey(ctx context.Context, id string) (apigen.SSHHost, error) {
	if _, err := s.host(ctx, id); err != nil {
		return apigen.SSHHost{}, err
	}
	if _, err := s.st.Q.ClearSshHostKey(ctx, db.ClearSshHostKeyParams{At: s.now(), ID: id}); err != nil {
		return apigen.SSHHost{}, err
	}
	s.invalidate(id)
	s.cfg.Changed()
	return s.GetHost(ctx, id)
}

func (s *Service) afterHostChange(id string, projectID *string) {
	s.invalidate(id)
	pid := ""
	if projectID != nil {
		pid = *projectID
	}
	s.cfg.Apply(pid)
	s.cfg.Changed()
}

// check 规范化并校验合并后的主机；old 为 nil 表示新建。pw 非 nil 时换成新密码。
func (s *Service) check(ctx context.Context, h *db.SshHost, old *db.SshHost, pw *string, tags []string) error {
	if !hostNameRe.MatchString(h.Name) {
		return invalid("主机名只能含字母、数字与 _ . -，不能以 . 或 - 开头，最长 64")
	}
	h.Address = strings.TrimSpace(h.Address)
	if h.Address == "" || len(h.Address) > 255 || !hostAddressRe.MatchString(h.Address) || strings.HasPrefix(h.Address, "-") {
		return invalid("地址不能为空，且只能是主机名或 IP")
	}
	if h.Port < 1 || h.Port > 65535 {
		return invalid("端口必须在 1–65535")
	}
	h.Username = strings.TrimSpace(h.Username)
	if h.Username == "" || len(h.Username) > 64 || strings.HasPrefix(h.Username, "-") ||
		strings.IndexFunc(h.Username, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return invalid("登录用户不能为空，且不能含空白")
	}
	if utf8.RuneCountInString(h.Description) > maxDescription {
		return invalid("描述最长 500 字符")
	}
	norm, err := normalizeTags(tags)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(norm)
	h.Tags = string(b)
	if h.ProjectID != nil && old == nil {
		if _, err := s.st.Q.GetProject(ctx, *h.ProjectID); errors.Is(err, sql.ErrNoRows) {
			return notFound("项目不存在")
		} else if err != nil {
			return err
		}
	}
	switch h.AuthType {
	case string(apigen.SSHAuthKey):
		h.PasswordEnc = nil
		if h.KeyID == nil || *h.KeyID == "" {
			return invalid("请选择一把钥匙")
		}
		if _, err := s.st.Q.GetSshKey(ctx, *h.KeyID); errors.Is(err, sql.ErrNoRows) {
			return invalid("钥匙不存在")
		} else if err != nil {
			return err
		}
	case string(apigen.SSHAuthPassword):
		h.KeyID = nil
		if pw != nil {
			enc, err := s.cfg.Box.Seal(passwordAAD(h.ID), []byte(*pw))
			if err != nil {
				return err
			}
			h.PasswordEnc = enc
		} else if old == nil || len(old.PasswordEnc) == 0 {
			return invalid("请填写密码")
		} else {
			h.PasswordEnc = old.PasswordEnc
		}
	default:
		return invalid("认证方式只能是 key 或 password")
	}
	if old != nil && old.Enabled && !h.Enabled {
		n, err := s.st.Q.CountEnabledSshHostsByJump(ctx, &h.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			return conflict("还有启用中的主机拿它当跳板")
		}
	}
	if h.JumpHostID != nil {
		if err := s.checkJump(ctx, h, old); err != nil {
			return err
		}
	}
	return nil
}

// checkJump 校验跳板：存在、作用域、启用状态、环与深度。
func (s *Service) checkJump(ctx context.Context, h *db.SshHost, old *db.SshHost) error {
	jump, err := s.st.Q.GetSshHost(ctx, *h.JumpHostID)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("跳板主机不存在")
	}
	if err != nil {
		return err
	}
	switch {
	case h.ProjectID == nil && jump.ProjectID != nil:
		return invalid("全局主机只能用全局主机作跳板")
	case h.ProjectID != nil && jump.ProjectID != nil && *jump.ProjectID != *h.ProjectID:
		return invalid("跳板只能是全局主机或同一项目的主机")
	}
	if h.Enabled && !jump.Enabled {
		if old != nil && !old.Enabled {
			return conflict("它的跳板已停用")
		}
		return invalid("跳板 %q 已停用", jump.Name)
	}
	cur := jump
	for depth := 1; ; depth++ {
		if cur.ID == h.ID {
			return invalid("跳板链存在循环")
		}
		if depth > maxJumpChain {
			return invalid("跳板链超过 %d 级", maxJumpChain)
		}
		if cur.JumpHostID == nil {
			return nil
		}
		if cur, err = s.st.Q.GetSshHost(ctx, *cur.JumpHostID); err != nil {
			return err
		}
	}
}

// normalizeTags 去首尾空白、丢弃空串、大小写不敏感去重（保留首次出现），并限制长度与数量。
func normalizeTags(raw []string) ([]string, error) {
	tags := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t == "" || seen[k] {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return nil, invalid("标签最多 16 个，每个最长 32 字符")
		}
		seen[k] = true
		tags = append(tags, t)
	}
	if len(tags) > maxTags {
		return nil, invalid("标签最多 16 个，每个最长 32 字符")
	}
	return tags, nil
}

func parseTags(s string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil || tags == nil {
		return []string{}
	}
	return tags
}

// hostView 是对外的主机视图（不含密文）。
func hostView(h db.SshHost) apigen.SSHHost {
	v := apigen.SSHHost{
		ID: h.ID, ProjectID: h.ProjectID, Name: h.Name, Address: h.Address, Port: int(h.Port), Username: h.Username,
		AuthType: apigen.SSHAuthType(h.AuthType), KeyID: h.KeyID, HasPassword: len(h.PasswordEnc) > 0,
		JumpHostID: h.JumpHostID, Description: h.Description, Tags: parseTags(h.Tags), Enabled: h.Enabled,
		AllowAutomation: h.AllowAutomation, AllowForward: h.AllowForward, Compat: h.Compat,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
	if h.HostKey != nil {
		if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(*h.HostKey)); err == nil {
			hk := &apigen.SSHHostKey{Type: pub.Type(), Fingerprint: ssh.FingerprintSHA256(pub)}
			if h.HostKeyAt != nil {
				hk.PinnedAt = *h.HostKeyAt
			}
			v.HostKey = hk
		}
	}
	if h.LastTestAt != nil {
		t := &apigen.SSHHostTest{At: *h.LastTestAt, Error: h.LastTestError, Os: h.LastTestOs}
		if h.LastTestOk != nil {
			t.Ok = *h.LastTestOk
		}
		v.LastTest = t
	}
	return v
}
