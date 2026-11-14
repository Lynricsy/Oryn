package api

import (
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// maxRoleBytes 是 role id 的字节上限。
const maxRoleBytes = 128

// role 头像：role 列表来自 omp-home 的 config.yml（cycleOrder），头像存在 role_avatars（有行 = 设了头像）。
// 不在 cycleOrder 里但仍有头像的 role 作为遗留项列在末尾，只能清除、不能重设。

// ListRoleIdentities 列出 cycleOrder 里的 role（按其顺序）与遗留项（按 role 升序）。
// config.yml 解析失败照样 200：roles 按缺省 cycleOrder，带 configError。
func (s *Server) ListRoleIdentities(w http.ResponseWriter, r *http.Request) {
	roles, cfgErr := s.Auth.Home.CycleRoles()
	rows, err := s.Store.Q.ListRoleAvatars(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	avatars := make(map[string]db.ListRoleAvatarsRow, len(rows))
	for _, row := range rows {
		avatars[row.Role] = row
	}
	out := apigen.RoleIdentityList{Roles: make([]apigen.RoleIdentity, 0, len(roles)+len(rows))}
	inCycle := make(map[string]bool, len(roles))
	for i := range roles {
		inCycle[roles[i].Role] = true
		a := avatars[roles[i].Role]
		out.Roles = append(out.Roles, roleIdentity(roles[i].Role, &roles[i], a.AvatarIcon, a.AvatarImage))
	}
	// rows 已按 role 升序（ListRoleAvatars 的 ORDER BY role，SQLite BINARY 排序 = 字节序）
	for _, row := range rows {
		if !inCycle[row.Role] {
			out.Roles = append(out.Roles, roleIdentity(row.Role, nil, row.AvatarIcon, row.AvatarImage))
		}
	}
	if cfgErr != nil {
		msg := cfgErr.Error()
		out.ConfigError = &msg
	}
	WriteJSON(w, http.StatusOK, out)
}

// PutRoleIdentity 设置或清除一个 role 的头像：两个头像字段都空 = 清除（删行，幂等，任何 role 都允许）；
// 设置只允许 cycleOrder 里的 role。返回写入后的该 role（组装方式同列表）。
func (s *Server) PutRoleIdentity(w http.ResponseWriter, r *http.Request) {
	var req apigen.RoleIdentityInput
	if !decodeJSON(w, r, &req) {
		return
	}
	role, err := validRole(req.Role)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	icon, image, err := validAvatar(req.AvatarIcon, req.AvatarImage)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	roles, _ := s.Auth.Home.CycleRoles()
	var cr *agentcfg.CycleRole
	for i := range roles {
		if roles[i].Role == role {
			cr = &roles[i]
			break
		}
	}
	ctx := r.Context()
	if icon == nil && image == nil {
		if err := s.Store.Q.DeleteRoleAvatar(ctx, role); err != nil {
			writeErr(w, r, err)
			return
		}
	} else {
		if cr == nil {
			writeErr(w, r, errInvalid("role 不在 cycleOrder 里"))
			return
		}
		if err := s.Store.Q.UpsertRoleAvatar(ctx, db.UpsertRoleAvatarParams{
			Role: role, AvatarIcon: icon, AvatarImage: image, UpdatedAt: time.Now().UnixMilli(),
		}); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	WriteJSON(w, http.StatusOK, roleIdentity(role, cr, icon, image))
}

// roleIdentity 组装一项：cr 非 nil = 在 cycleOrder 里（带 name / selector），nil = 遗留项。
func roleIdentity(role string, cr *agentcfg.CycleRole, icon, image *string) apigen.RoleIdentity {
	out := apigen.RoleIdentity{Role: role, InCycle: cr != nil, AvatarIcon: nonEmpty(icon), AvatarImage: nonEmpty(image)}
	if cr != nil {
		if cr.Name != "" {
			name := cr.Name
			out.Name = &name
		}
		if cr.Selector != "" {
			sel := cr.Selector
			out.Selector = &sel
		}
	}
	return out
}

// validRole 去首尾空白后须非空、≤128 字节且不含控制字符；大小写原样保留。
func validRole(raw string) (string, error) {
	role := strings.TrimSpace(raw)
	if role == "" || len(role) > maxRoleBytes || strings.IndexFunc(role, unicode.IsControl) >= 0 {
		return "", errInvalid("role 须是 1–128 字节且不含控制字符")
	}
	return role, nil
}
