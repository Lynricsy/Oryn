package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/glyph"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// avatarResolver 批量解析通知头像，避免 List() 对全局助手身份与同一线程重复查库。
type avatarResolver struct {
	s           *Service
	assistantOK bool
	assistant   *apigen.NotificationAvatar
	threads     map[string]*db.Thread
}

func (s *Service) newAvatarResolver() *avatarResolver {
	return &avatarResolver{s: s, threads: map[string]*db.Thread{}}
}

// resolve 返回一条通知的发送者头像元数据：
//   - 全局助手主线（动态）与全局助手对话线程（kind == "assistant"），以及 /settings/assistant 系统通知 → 全局助手头像；
//   - 其它普通线程 → 线程标题卡图标 + 线程色；
//   - 无线程系统通知 → system 默认。
func (r *avatarResolver) resolve(ctx context.Context, threadID, link *string, title string) *apigen.NotificationAvatar {
	if threadID != nil && *threadID != "" {
		th := r.thread(ctx, *threadID)
		if th != nil && th.Kind == "assistant" {
			return r.assistantAvatar(ctx)
		}
		rawTitle := title
		if th != nil && th.Title != "" {
			rawTitle = th.Title
		}
		av := &apigen.NotificationAvatar{Kind: apigen.NotificationAvatarKindThread}
		if card, ok := glyph.ParseTitleCard(rawTitle); ok && card.Icon != "" {
			av.Icon = new(card.Icon)
		}
		if th != nil && th.Color != nil && *th.Color != "" {
			av.Color = new(*th.Color)
		}
		return av
	}
	if link != nil && isAssistantPath(*link) {
		return r.assistantAvatar(ctx)
	}
	return &apigen.NotificationAvatar{Kind: apigen.NotificationAvatarKindSystem}
}

func isAssistantPath(link string) bool {
	return link == "/settings/assistant" || strings.HasPrefix(link, "/settings/assistant/") ||
		link == "/assistant" || strings.HasPrefix(link, "/assistant/")
}

func (r *avatarResolver) thread(ctx context.Context, id string) *db.Thread {
	if th, ok := r.threads[id]; ok {
		return th
	}
	row, err := r.s.st.Q.GetThread(ctx, id)
	if err != nil {
		r.threads[id] = nil
		return nil
	}
	r.threads[id] = &row
	return &row
}

// assistantAvatar 读取全局助手自己的头像设置（不跟随默认 Agent 身份，与客户端 AssistantMark 一致）：
// 自定义图片只下发 8 位摘要 hash，由移动端按需拉取并缓存；自定义字形直接随载荷下发并附 hash；
// 均未设置时 icon 与 hash 皆空，客户端显示默认 sparkle 标记。
func (r *avatarResolver) assistantAvatar(ctx context.Context) *apigen.NotificationAvatar {
	if !r.assistantOK {
		r.assistantOK = true
		av := &apigen.NotificationAvatar{Kind: apigen.NotificationAvatarKindAssistant}
		if row, err := r.s.st.Q.GetOwnerAssistantIdentity(ctx); err == nil {
			switch {
			case row.AssistantAvatarImage != nil && *row.AssistantAvatarImage != "":
				av.Hash = new(avatarDigest("image", *row.AssistantAvatarImage))
			case row.AssistantAvatarIcon != nil && *row.AssistantAvatarIcon != "":
				av.Icon = new(*row.AssistantAvatarIcon)
				av.Hash = new(avatarDigest("icon", *row.AssistantAvatarIcon))
			}
		}
		r.assistant = av
	}
	cp := *r.assistant
	return &cp
}

func avatarDigest(kind, val string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(val))
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// resolveAvatar 解析单条通知的头像元数据。
func (s *Service) resolveAvatar(ctx context.Context, threadID, link *string, title string) *apigen.NotificationAvatar {
	return s.newAvatarResolver().resolve(ctx, threadID, link, title)
}
