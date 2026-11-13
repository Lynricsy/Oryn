package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/glyph"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// agent 头像与用户头像是 owner 表上的两组列，校验、序列化与 handler 流程完全相同，
// 各自只提供读写那组列的查询。

const (
	// maxAvatarIconBytes / maxAvatarImageBytes 是头像两个字段的上限（图片按整个 data URL 计）。
	maxAvatarIconBytes  = 64
	maxAvatarImageBytes = 128 << 10
)

var avatarDataURL = regexp.MustCompile(`^data:image/(png|webp|jpeg);base64,`)

// saveAvatar 把规范化后的头像写入某一组列，返回受影响行数（0 = 尚无所有者）。
type saveAvatar func(ctx context.Context, icon, image *string) (int64, error)

// GetAgentIdentity 返回 agent 头像；尚未初始化所有者时视为默认。
func (s *Server) GetAgentIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerAgentIdentity(r.Context())
	writeAvatar(w, r, row.AgentAvatarIcon, row.AgentAvatarImage, err)
}

// PutAgentIdentity 整体替换 agent 头像：省略或空串的字段即清除，两者都空 = 恢复默认。
func (s *Server) PutAgentIdentity(w http.ResponseWriter, r *http.Request) {
	putAvatar(w, r, func(ctx context.Context, icon, image *string) (int64, error) {
		return s.Store.Q.SetOwnerAgentIdentity(ctx, db.SetOwnerAgentIdentityParams{AgentAvatarIcon: icon, AgentAvatarImage: image})
	})
}

// GetUserIdentity 返回用户头像；尚未初始化所有者时视为默认。
func (s *Server) GetUserIdentity(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Q.GetOwnerUserIdentity(r.Context())
	writeAvatar(w, r, row.UserAvatarIcon, row.UserAvatarImage, err)
}

// PutUserIdentity 整体替换用户头像，规则同 PutAgentIdentity。
func (s *Server) PutUserIdentity(w http.ResponseWriter, r *http.Request) {
	putAvatar(w, r, func(ctx context.Context, icon, image *string) (int64, error) {
		return s.Store.Q.SetOwnerUserIdentity(ctx, db.SetOwnerUserIdentityParams{UserAvatarIcon: icon, UserAvatarImage: image})
	})
}

func avatarJSON(icon, image *string) apigen.AvatarIdentity {
	var out apigen.AvatarIdentity
	if icon != nil && *icon != "" {
		out.AvatarIcon = icon
	}
	if image != nil && *image != "" {
		out.AvatarImage = image
	}
	return out
}

// writeAvatar 输出读取结果；没有所有者行（sql.ErrNoRows）按默认处理。
func writeAvatar(w http.ResponseWriter, r *http.Request, icon, image *string, err error) {
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, r, err)
		return
	}
	WriteJSON(w, http.StatusOK, avatarJSON(icon, image))
}

func putAvatar(w http.ResponseWriter, r *http.Request, save saveAvatar) {
	var req apigen.AvatarIdentity
	if !decodeJSON(w, r, &req) {
		return
	}
	icon, image, err := validAvatar(req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := save(r.Context(), icon, image)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n == 0 {
		WriteError(w, http.StatusConflict, "conflict", "尚未完成初始化（没有所有者）")
		return
	}
	WriteJSON(w, http.StatusOK, avatarJSON(icon, image))
}

// validAvatar 校验并规范化请求：空串视为省略；两者互斥；图标须是单个字形；
// 图片须是 png / webp / jpeg 的 base64 data URL 且整串不超过 128KiB。
func validAvatar(req apigen.AvatarIdentity) (icon, image *string, err error) {
	if req.AvatarIcon != nil {
		if v := strings.TrimSpace(*req.AvatarIcon); v != "" {
			icon = &v
		}
	}
	if req.AvatarImage != nil && *req.AvatarImage != "" {
		image = req.AvatarImage
	}
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
