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

// viewer 是可见性判定的主体：一台 orb，或 runner 上经公钥认出的一个线程（M21）。
type viewer struct {
	orb db.Orb
	// threadID 非空 = runner 线程（拒绝原因里说「这个线程」）。
	threadID string
	// projectID 是可见的项目主机所属项目；nil = 只有全局主机。
	projectID  *string
	automation bool
}

// orbViewer 是 orb 本身：项目与全自动标记取 orb 行。runner 的常驻 orb 不属于任何项目，只看得到全局主机（runner 全局的
// ssh.json 就按它生成）；runner 线程的项目主机经 threadViewer。
func orbViewer(orb db.Orb) viewer {
	return viewer{orb: orb, projectID: orb.ProjectID, automation: orb.Automation}
}

// threadViewer 是 runner 上的线程：项目取线程行。全自动标记沿用 runner orb 的——runner 线程没有全自动入口（GitHub
// 触发、项目定时、发布哨兵都不建 runner 线程），线程行也没有自己的标记；规则与 orb 相同，日后有了入口只改这里。
func threadViewer(orb db.Orb, t db.Thread) viewer {
	return viewer{orb: orb, threadID: t.ID, projectID: t.ProjectID, automation: orb.Automation}
}

// visible 返回 v 能用的主机（按名排序）与因全自动被挡下的主机。协调 orb 返回 managed=false。
// 取启用的全局主机与本项目主机，同名时项目主机盖过全局主机。
func (s *Service) visible(ctx context.Context, v viewer) (usable, blocked []db.SshHost, managed bool, err error) {
	if v.orb.Coordinator {
		return nil, nil, false, nil
	}
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
	if v.projectID != nil {
		for _, h := range rows {
			if h.Enabled && h.ProjectID != nil && *h.ProjectID == *v.projectID {
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
		if v.automation && !h.AllowAutomation {
			blocked = append(blocked, h)
		} else {
			usable = append(usable, h)
		}
	}
	return usable, blocked, true, nil
}

// resolve 按主机名找 v 能用的主机；找不到时 deny 为给 orb 看的拒绝原因（与网关一致）。
func (s *Service) resolve(ctx context.Context, v viewer, name string) (h db.SshHost, deny string, err error) {
	usable, blocked, managed, err := s.visible(ctx, v)
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
		rows, err := s.st.Q.ListSshHosts(ctx)
		if err != nil {
			return db.SshHost{}, "", err
		}
		for _, h := range rows {
			inScope := h.ProjectID == nil || (v.projectID != nil && *h.ProjectID == *v.projectID)
			if inScope && h.Name == name && !h.Enabled {
				return h, fmt.Sprintf("主机 %q 已停用", name), nil
			}
		}
	}
	if v.threadID != "" {
		return db.SshHost{}, fmt.Sprintf("主机 %q 未登记，或对这个线程不可见", name), nil
	}
	return db.SshHost{}, fmt.Sprintf("主机 %q 未登记，或对这台 orb 不可见", name), nil
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

// OrbConfig 返回下发给 orb（含 runner）的 SSH 配置；协调 orb 返回 nil（不动 orb 内的 SSH 配置）。runner 的常驻 orb 不属于
// 项目，这里只有全局主机；其线程的项目主机随 agent.start 的项目层交付（RunnerProjectSSH）。
func (s *Service) OrbConfig(ctx context.Context, orb db.Orb) (*apigen.LinkCtlSSHConfig, error) {
	usable, _, managed, err := s.visible(ctx, orbViewer(orb))
	if err != nil || !managed {
		return nil, err
	}
	cfg := &apigen.LinkCtlSSHConfig{HostKey: s.gatewayPublicKey(), Hosts: make([]apigen.LinkCtlSSHHost, 0, len(usable))}
	for _, h := range usable {
		cfg.Hosts = append(cfg.Hosts, apigen.LinkCtlSSHHost{Name: h.Name, Description: hostDescription(h), Compat: h.Compat})
	}
	return cfg, nil
}

// RunnerSSHPortRef 是 runner 项目层 ssh.json 里的端口：Core 读 ssh.json 时展开环境变量，orbd 给 runner 上的 agent 设
// ORYN_SSH_PORT（本机网关实际端口）。项目层按内容寻址，不随端口变。
const RunnerSSHPortRef = "${ORYN_SSH_PORT}"

// runnerSSHHost 是 runner 项目层 `.omp/ssh.json` 里的一台主机（字段同 orbd 写的全局 ssh.json，端口引用环境变量）。
type runnerSSHHost struct {
	Host        string `json:"host"`
	Port        string `json:"port"`
	Username    string `json:"username"`
	Description string `json:"description"`
	Compat      *bool  `json:"compat,omitempty"`
}

// RunnerProjectSSH 返回 runner 线程项目层里的 ssh.json（M21）：该线程可用的、属于其项目的主机（全局主机已在 runner 全局
// 的 ssh.json 里；同名时 Core 的项目层优先，与网关「项目主机盖过全局主机」一致）。不是 runner 线程、线程不属于项目或
// 没有这样的主机时返回 nil。
func (s *Service) RunnerProjectSSH(ctx context.Context, threadID string) ([]byte, error) {
	t, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if t.OrbID == nil || t.ProjectID == nil {
		return nil, nil
	}
	orb, err := s.orbs.Get(ctx, *t.OrbID)
	if err != nil {
		return nil, err
	}
	if !orbs.IsRunner(orb) {
		return nil, nil
	}
	usable, _, _, err := s.visible(ctx, threadViewer(orb, t))
	if err != nil {
		return nil, err
	}
	hosts := map[string]runnerSSHHost{}
	for _, h := range usable {
		if h.ProjectID == nil {
			continue
		}
		hosts[h.Name] = runnerSSHHost{
			Host: "127.0.0.1", Port: RunnerSSHPortRef, Username: h.Name, Description: strings.Join(strings.Fields(hostDescription(h)), " "),
			Compat: h.Compat,
		}
	}
	if len(hosts) == 0 {
		return nil, nil
	}
	b, err := json.MarshalIndent(map[string]any{"hosts": hosts}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
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

// ThreadHosts 返回线程可见的主机：orb 线程按其 orb，runner 线程按线程的项目（M21）。线程不存在 → not_found；没有 orb 或
// 协调线程 → managed=false。
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
	usable, blocked, managed, err := s.visible(ctx, viewerOf(orb, t))
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

// viewerOf 返回线程 t（在 orb 上）的可见性主体：runner 按线程，其余按 orb。
func viewerOf(orb db.Orb, t db.Thread) viewer {
	if orbs.IsRunner(orb) {
		return threadViewer(orb, t)
	}
	return orbViewer(orb)
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
