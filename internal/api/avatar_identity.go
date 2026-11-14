package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/glyph"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// owner 表上有三组身份列：全局 agent（名字 + 头像）、全局助手（名字 + 头像，原始值，回落由客户端做）、
// 用户（只有头像）。三组的校验与 handler 流程相同，各自只提供读写那组列的查询。

const (
	// maxAvatarIconBytes / maxAvatarImageBytes 是头像两个字段的上限（图片按整个 data URL 计）。
	maxAvatarIconBytes  = 64
	maxAvatarImageBytes = 128 << 10
	// maxIdentityNameRunes 是身份显示名去首尾空白后的字符上限。
	maxIdentityNameRunes = 40
)

var avatarDataURL = regexp.MustCompile(`^data:image/(png|webp|jpeg);base64,`)

// saveAvatar 把规范化后的头像写入某一组列，返回受影响行数（0 = 尚无所有者）。
type saveAvatar func(ctx context.Context, icon, image *string) (int64, error)

// saveIdentity 把规范化后的名字与头像写入某一组列，返回受影响行数（0 = 尚无所有者）。
type saveIdentity func(ctx context.Context, name, icon, image *string) (int64, error)

// GetAgentIdentity 返回全局 agent 身份；尚未初始化所有者时视为默认。
func (s *Server) GetAgentIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerAgentIdentity(r.Context())
	writeIdentity(w, r, row.AgentName, row.AgentAvatarIcon, row.AgentAvatarImage, err)
}

// PutAgentIdentity 整体替换全局 agent 身份：省略或空串的字段即清除，全都空 = 恢复默认。
func (s *Server) PutAgentIdentity(w http.ResponseWriter, r *http.Request) {
	putIdentity(w, r, func(ctx context.Context, name, icon, image *string) (int64, error) {
		return s.Store.Q.SetOwnerAgentIdentity(ctx, db.SetOwnerAgentIdentityParams{
			AgentName: name, AgentAvatarIcon: icon, AgentAvatarImage: image,
		})
	})
}

// GetAssistantIdentity 返回全局助手身份的原始值（省略的字段 = 跟随 agent 身份，由客户端回落）。
func (s *Server) GetAssistantIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerAssistantIdentity(r.Context())
	writeIdentity(w, r, row.AssistantName, row.AssistantAvatarIcon, row.AssistantAvatarImage, err)
}

// PutAssistantIdentity 整体替换全局助手身份，规则同 PutAgentIdentity（清除 = 改为跟随 agent 身份）。
func (s *Server) PutAssistantIdentity(w http.ResponseWriter, r *http.Request) {
	putIdentity(w, r, func(ctx context.Context, name, icon, image *string) (int64, error) {
		return s.Store.Q.SetOwnerAssistantIdentity(ctx, db.SetOwnerAssistantIdentityParams{
			AssistantName: name, AssistantAvatarIcon: icon, AssistantAvatarImage: image,
		})
	})
}

// GetUserIdentity 返回用户头像；尚未初始化所有者时视为默认。
func (s *Server) GetUserIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerUserIdentity(r.Context())
	writeAvatar(w, r, row.UserAvatarIcon, row.UserAvatarImage, err)
}

// PutUserIdentity 整体替换用户头像：省略或空串的字段即清除，两者都空 = 恢复默认。
func (s *Server) PutUserIdentity(w http.ResponseWriter, r *http.Request) {
	putAvatar(w, r, func(ctx context.Context, icon, image *string) (int64, error) {
		return s.Store.Q.SetOwnerUserIdentity(ctx, db.SetOwnerUserIdentityParams{UserAvatarIcon: icon, UserAvatarImage: image})
	})
}

// nonEmpty 把空串视为未设置。
func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

func avatarJSON(icon, image *string) apigen.AvatarIdentity {
	return apigen.AvatarIdentity{AvatarIcon: nonEmpty(icon), AvatarImage: nonEmpty(image)}
}

func identityJSON(name, icon, image *string) apigen.AgentIdentity {
	return apigen.AgentIdentity{Name: nonEmpty(name), AvatarIcon: nonEmpty(icon), AvatarImage: nonEmpty(image)}
}

// writeAvatar 输出读取结果；没有所有者行（sql.ErrNoRows）按默认处理。
func writeAvatar(w http.ResponseWriter, r *http.Request, icon, image *string, err error) {
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, avatarJSON(icon, image))
}

// writeIdentity 同 writeAvatar，多一个名字。
func writeIdentity(w http.ResponseWriter, r *http.Request, name, icon, image *string, err error) {
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, identityJSON(name, icon, image))
}

func putAvatar(w http.ResponseWriter, r *http.Request, save saveAvatar) {
	var req apigen.AvatarIdentity
	if !decodeJSON(w, r, &req) {
		return
	}
	icon, image, err := validAvatar(req.AvatarIcon, req.AvatarImage)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := save(r.Context(), icon, image)
	if !savedOwner(w, r, n, err) {
		return
	}
	WriteJSON(w, http.StatusOK, avatarJSON(icon, image))
}

func putIdentity(w http.ResponseWriter, r *http.Request, save saveIdentity) {
	var req apigen.AgentIdentity
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := validIdentityName(req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	icon, image, err := validAvatar(req.AvatarIcon, req.AvatarImage)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := save(r.Context(), name, icon, image)
	if !savedOwner(w, r, n, err) {
		return
	}
	WriteJSON(w, http.StatusOK, identityJSON(name, icon, image))
}

// savedOwner 处理写 owner 行的结果：出错或没有所有者行（409）时已写出响应并返回 false。
func savedOwner(w http.ResponseWriter, r *http.Request, n int64, err error) bool {
	if err != nil {
		writeErr(w, r, err)
		return false
	}
	if n == 0 {
		WriteError(w, http.StatusConflict, "conflict", "尚未完成初始化（没有所有者）")
		return false
	}
	return true
}

// validIdentityName 去首尾空白；空 → nil；含控制字符或超过 40 个字符 → invalid_request。
func validIdentityName(p *string) (*string, error) {
	if p == nil {
		return nil, nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(v) > maxIdentityNameRunes || strings.IndexFunc(v, unicode.IsControl) >= 0 {
		return nil, errInvalid("name 须是 1–40 个字符且不含控制字符")
	}
	return &v, nil
}

// validAvatar 校验并规范化头像两个字段：空串视为省略；两者互斥；图标须是单个字形；
// 图片须是 png / webp / jpeg 的 base64 data URL 且整串不超过 128KiB。
func validAvatar(iconIn, imageIn *string) (icon, image *string, err error) {
	if iconIn != nil {
		if v := strings.TrimSpace(*iconIn); v != "" {
			icon = &v
		}
	}
	image = nonEmpty(imageIn)
	switch {
	case icon != nil && image != nil:
		return nil, nil, errInvalid("avatarIcon 与 avatarImage 至多给一个")
	case icon != nil && !glyph.Single(*icon, maxAvatarIconBytes):
		return nil, nil, errInvalid("avatarIcon 须是单个字形（≤64 字节）")
	case image != nil && len(*image) > maxAvatarImageBytes:
		return nil, nil, &statusError{http.StatusRequestEntityTooLarge, "too_large", "avatarImage 超过 128KiB"}
	case image != nil:
		prefix := avatarDataURL.FindString(*image)
		if prefix == "" {
			return nil, nil, errInvalid("avatarImage 须是 data:image/(png|webp|jpeg);base64, 开头的 data URL")
		}
		if _, err := base64.StdEncoding.DecodeString((*image)[len(prefix):]); err != nil {
			return nil, nil, errInvalid("avatarImage 的 base64 无效")
		}
	}
	return icon, image, nil
}
