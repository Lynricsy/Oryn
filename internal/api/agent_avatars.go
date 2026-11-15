package api

import (
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 子 agent 头像：agent 列表 = omp 内置 agent（agentcfg.BundledAgents）+ omp-home `agents/*.md`（frontmatter 的 name），
// 头像存在 agent_avatars（有行 = 设了头像）。与内置 agent 同名的用户文件在 omp 里覆盖内置定义，这里只列一次（source=user）。
// 不在列表里但仍有头像的名字作为遗留项列在末尾，只能清除、不能重设。

// maxAgentNameBytes 是 agent 名的字节上限（与 role 一致）。
const maxAgentNameBytes = 128

// agentWarningKind 是解析失败而跳过的 agent 文件在 warnings 里的 kind。
const agentWarningKind = "invalid_agent"

// ListAgentAvatars 依次列出内置 agent（BundledAgents 的顺序）、用户 agent（按名字）与遗留项（按名字）。
func (s *Server) ListAgentAvatars(w http.ResponseWriter, r *http.Request) {
	defs, warns, err := s.Auth.Home.Agents()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rows, err := s.Store.Q.ListAgentAvatars(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	avatars := make(map[string]db.ListAgentAvatarsRow, len(rows))
	for _, row := range rows {
		avatars[row.Name] = row
	}
	user := make(map[string]bool, len(defs))
	for _, d := range defs {
		user[d.Name] = true
	}
	out := apigen.AgentAvatarList{Agents: make([]apigen.AgentAvatar, 0, len(agentcfg.BundledAgents)+len(defs)+len(rows))}
	listed := make(map[string]bool, cap(out.Agents))
	for _, name := range agentcfg.BundledAgents {
		if user[name] {
			continue // 被同名用户文件覆盖：在用户 agent 里列出
		}
		listed[name] = true
		a := avatars[name]
		out.Agents = append(out.Agents, agentAvatar(name, apigen.AgentAvatarSourceBundled, "", a.AvatarIcon, a.AvatarImage))
	}
	// defs 已按名字排序
	for _, d := range defs {
		listed[d.Name] = true
		a := avatars[d.Name]
		out.Agents = append(out.Agents, agentAvatar(d.Name, apigen.AgentAvatarSourceUser, d.Description, a.AvatarIcon, a.AvatarImage))
	}
	// rows 已按名字升序（ListAgentAvatars 的 ORDER BY name）
	for _, row := range rows {
		if !listed[row.Name] {
			out.Agents = append(out.Agents, agentAvatar(row.Name, apigen.AgentAvatarSourceOrphan, "", row.AvatarIcon, row.AvatarImage))
		}
	}
	for _, wn := range warns {
		out.Warnings = append(out.Warnings, apigen.AgentConfigWarning{Path: wn.Path, Kind: agentWarningKind, Message: wn.Message})
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutAgentAvatar 设置或清除一个 agent 的头像：两个头像字段都空 = 清除（删行，幂等，任何名字都允许）；
// 设置只允许内置 agent 或当前 `agents/*.md` 里的 agent。返回写入后的该 agent（组装方式同列表）。
func (s *Server) PutAgentAvatar(w http.ResponseWriter, r *http.Request) {
	var req apigen.AgentAvatarInput
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := validAgentName(req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	icon, image, err := validAvatar(req.AvatarIcon, req.AvatarImage)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defs, _, err := s.Auth.Home.Agents()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	source, desc := apigen.AgentAvatarSourceOrphan, ""
	if i := slices.IndexFunc(defs, func(d agentcfg.AgentDef) bool { return d.Name == name }); i >= 0 {
		source, desc = apigen.AgentAvatarSourceUser, defs[i].Description
	} else if slices.Contains(agentcfg.BundledAgents, name) {
		source = apigen.AgentAvatarSourceBundled
	}
	ctx := r.Context()
	if icon == nil && image == nil {
		if err := s.Store.Q.DeleteAgentAvatar(ctx, name); err != nil {
			writeErr(w, r, err)
			return
		}
	} else {
		if source == apigen.AgentAvatarSourceOrphan {
			writeErr(w, r, errInvalid("name 既不是内置 agent，也不在 agents/*.md 里"))
			return
		}
		if err := s.Store.Q.UpsertAgentAvatar(ctx, db.UpsertAgentAvatarParams{
			Name: name, AvatarIcon: icon, AvatarImage: image, UpdatedAt: time.Now().UnixMilli(),
		}); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	WriteJSON(w, http.StatusOK, agentAvatar(name, source, desc, icon, image))
}

// agentAvatar 组装一项；description 为空时省略。
func agentAvatar(name string, source apigen.AgentAvatarSource, description string, icon, image *string) apigen.AgentAvatar {
	out := apigen.AgentAvatar{Name: name, Source: source, AvatarIcon: nonEmpty(icon), AvatarImage: nonEmpty(image)}
	if description != "" {
		out.Description = &description
	}
	return out
}

// validAgentName 去首尾空白后须非空、≤128 字节且不含控制字符；大小写原样保留。
func validAgentName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > maxAgentNameBytes || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", errInvalid("name 须是 1–128 字节且不含控制字符")
	}
	return name, nil
}
