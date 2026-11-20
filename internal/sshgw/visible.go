package sshgw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbs"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// visible 返回这台 orb 能用的主机（按名排序）与因全自动被挡下的主机。协调 orb 返回 managed=false。
// 取启用的全局主机与本项目主机，同名时项目主机盖过全局主机。runner（M21）规则相同，但只有全局主机（hostScope）。
func (s *Service) visible(ctx context.Context, orb db.Orb) (usable, blocked []db.SshHost, managed bool, err error) {
	if orb.Coordinator {
		return nil, nil, false, nil
	}
	scope := hostScope(orb)
	rows, err := s.st.Q.ListSshHosts(ctx)
	if err != nil {
		return nil, nil, true, err
	}
	byName := map[string]db.SshHost{}
	for _, h := range rows {
		if h.Enabled && h.ProjectID == nil {
			byName[h.Name] = h
		}
	}
	if scope != nil {
		for _, h := range rows {
			if h.Enabled && h.ProjectID != nil && *h.ProjectID == *scope {
				byName[h.Name] = h
			}
		}
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		h := byName[n]
		if orb.Automation && !h.AllowAutomation {
			blocked = append(blocked, h)
		} else {
			usable = append(usable, h)
		}
	}
	return usable, blocked, true, nil
}

// resolve 按 orb 内的主机名找主机；找不到时 deny 为给 orb 看的拒绝原因（与网关一致）。
func (s *Service) resolve(ctx context.Context, orb db.Orb, name string) (h db.SshHost, deny string, err error) {
	usable, blocked, managed, err := s.visible(ctx, orb)
	if err != nil {
		return db.SshHost{}, "", err
	}
	for _, h := range usable {
		if h.Name == name {
			return h, "", nil
		}
	}
	for _, h := range blocked {
		if h.Name == name {
			return h, fmt.Sprintf("主机 %q 不允许全自动线程使用（可在 设置 → SSH 主机 里放开）", name), nil
		}
	}
	if managed {
		scope := hostScope(orb)
		rows, err := s.st.Q.ListSshHosts(ctx)
		if err != nil {
			return db.SshHost{}, "", err
		}
		for _, h := range rows {
			inScope := h.ProjectID == nil || (scope != nil && *h.ProjectID == *scope)
			if inScope && h.Name == name && !h.Enabled {
				return h, fmt.Sprintf("主机 %q 已停用", name), nil
			}
		}
	}
	return db.SshHost{}, fmt.Sprintf("主机 %q 未登记，或对这台 orb 不可见", name), nil
}

// hostScope 返回 orb 可见的项目主机所属项目；nil = 只有全局主机。runner（M21）恒为 nil：网关收到的是 runner 本机
// 127.0.0.1:7822 上的连接，只能归属到 runner 的常驻 orb，认不出发起连接的线程，而同一台 runner 上可以同时跑多个项目的
// 线程——项目级主机对 runner 一律不可见（即便 orb 行意外带了 project_id）。
func hostScope(orb db.Orb) *string {
	if orbs.IsRunner(orb) {
		return nil
	}
	return orb.ProjectID
}

// hostDescription 拼 orb 内 ssh.json 的描述：真实用户与地址，再接描述与标签。
func hostDescription(h db.SshHost) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s@%s:%d", h.Username, h.Address, h.Port)
	if d := strings.TrimSpace(h.Description); d != "" {
		b.WriteString(" — ")
		b.WriteString(d)
	}
	if tags := parseTags(h.Tags); len(tags) > 0 {
		b.WriteString(" [")
		b.WriteString(strings.Join(tags, ", "))
		b.WriteString("]")
	}
	return b.String()
}

// OrbConfig 返回下发给 orb（含 runner）的 SSH 配置；协调 orb 返回 nil（不动 orb 内的 SSH 配置）。
func (s *Service) OrbConfig(ctx context.Context, orb db.Orb) (*apigen.LinkCtlSSHConfig, error) {
	usable, _, managed, err := s.visible(ctx, orb)
	if err != nil || !managed {
		return nil, err
	}
	cfg := &apigen.LinkCtlSSHConfig{HostKey: s.gatewayPublicKey(), Hosts: make([]apigen.LinkCtlSSHHost, 0, len(usable))}
	for _, h := range usable {
		cfg.Hosts = append(cfg.Hosts, apigen.LinkCtlSSHHost{Name: h.Name, Description: hostDescription(h), Compat: h.Compat})
	}
	return cfg, nil
}

// LayerState 返回一层（"" = 全局，否则项目 id）主机的规范化摘要，只用作下发基线的比较输入：
// 这一层里 orb 可见的内容一变，摘要就变。
func (s *Service) LayerState(projectID string) ([]byte, error) {
	rows, err := s.st.Q.ListSshHosts(context.Background())
	if err != nil {
		return nil, err
	}
	type item struct {
		Name            string `json:"name"`
		Enabled         bool   `json:"enabled"`
		AllowAutomation bool   `json:"allowAutomation"`
		Compat          *bool  `json:"compat,omitempty"`
		Description     string `json:"description"`
	}
	state := struct {
		HostKey string `json:"hostKey"`
		Hosts   []item `json:"hosts"`
	}{HostKey: s.gatewayPublicKey(), Hosts: []item{}}
	for _, h := range rows {
		if (projectID == "") != (h.ProjectID == nil) || (h.ProjectID != nil && *h.ProjectID != projectID) {
			continue
		}
		state.Hosts = append(state.Hosts, item{
			Name: h.Name, Enabled: h.Enabled, AllowAutomation: h.AllowAutomation, Compat: h.Compat, Description: hostDescription(h),
		})
	}
	slices.SortFunc(state.Hosts, func(a, b item) int { return strings.Compare(a.Name, b.Name) })
	return json.Marshal(state)
}

// ThreadHosts 返回线程所在 orb 可见的主机。线程不存在 → not_found；没有 orb 或协调线程 → managed=false。
func (s *Service) ThreadHosts(ctx context.Context, threadID string) (apigen.ThreadSSHHosts, error) {
	out := apigen.ThreadSSHHosts{Hosts: []apigen.ThreadSSHHost{}}
	t, err := s.st.Q.GetThread(ctx, threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, notFound("线程不存在")
	}
	if err != nil {
		return out, err
	}
	if t.OrbID == nil {
		return out, nil
	}
	orb, err := s.orbs.Get(ctx, *t.OrbID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	usable, blocked, managed, err := s.visible(ctx, orb)
	if err != nil {
		return out, err
	}
	out.Managed, out.Automation = managed, orb.Automation
	for _, h := range usable {
		out.Hosts = append(out.Hosts, apigen.ThreadSSHHost{Host: hostView(h), Usable: true})
	}
	reason := apigen.ThreadSSHHostBlockedAutomation
	for _, h := range blocked {
		out.Hosts = append(out.Hosts, apigen.ThreadSSHHost{Host: hostView(h), Usable: false, Blocked: &reason})
	}
	slices.SortFunc(out.Hosts, func(a, b apigen.ThreadSSHHost) int { return strings.Compare(a.Host.Name, b.Host.Name) })
	return out, nil
}

// Secrets 返回每台密码登录主机的明文密码（分享前的密钥扫描用），键为 "ssh/host/<名>/password"。
func (s *Service) Secrets(ctx context.Context) (map[string]string, error) {
	rows, err := s.st.Q.ListSshHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range rows {
		if len(h.PasswordEnc) == 0 {
			continue
		}
		plain, err := s.cfg.Box.Open(passwordAAD(h.ID), h.PasswordEnc)
		if err != nil {
			return nil, err
		}
		k := "ssh/host/" + h.Name + "/password"
		if _, dup := out[k]; dup {
			k += "#" + h.ID // 不同项目的同名主机：都要扫描，不能互相覆盖
		}
		out[k] = string(plain)
		clear(plain)
	}
	return out, nil
}
