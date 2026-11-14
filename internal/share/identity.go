package share

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/transcript"
)

// 分享页的身份（M9.6）：全局 agent 的名字与头像，加上这段对话里用到且设了头像的 role。
// 公开条目里的来源记录只带每个分享各不相同的不透明键（roleKey），role id、名字、头像只经 PublicIdentity 出网，
// 且只含用到的 role；没用到的 role 在匿名面上至多以不透明键出现。本文件是身份在匿名面上的唯一出口。

// roleKey 是 role 在某个分享里的不透明键：hex(sha256(shareID + "\x00" + role)) 的前 16 位。
// shareID（shr_…）不出现在任何公开响应里，因此键既不能反推 role，也不能跨分享关联。
func roleKey(shareID, role string) string {
	sum := sha256.Sum256([]byte(shareID + "\x00" + role))
	return hex.EncodeToString(sum[:])[:16]
}

// publicModelSourceData 是公开面上来源记录的 data：只留白名单字段，role / alias 换成不透明键。
type publicModelSourceData struct {
	V      int    `json:"v"`
	Source string `json:"source"`
	Model  string `json:"model"`
	Role   string `json:"role,omitempty"`
	Alias  string `json:"alias,omitempty"`
}

// publicEntry 返回条目在某个分享公开面上的视图：主会话文件里的来源记录换成清洗形状
// `{"type":"custom","customType":"oryn-model-source","data":{v, source, model, role?, alias?}}`（role / alias 为不透明键，
// 其余字段丢弃）；其余条目交给 transcript.Public（其它 custom 条目仍被清空）。
func publicEntry(shareID string, e transcript.Entry) (transcript.Entry, bool) {
	if !transcript.IsMainFile(e.File) {
		return transcript.Entry{}, false
	}
	ms, ok := transcript.ParseModelSource(e)
	if !ok {
		return transcript.Public(e)
	}
	d := publicModelSourceData{V: 1, Source: ms.Source, Model: ms.Model}
	if ms.Role != "" {
		d.Role = roleKey(shareID, ms.Role)
	}
	if ms.Alias != "" {
		d.Alias = roleKey(shareID, ms.Alias)
	}
	b, err := json.Marshal(struct {
		Type       string                `json:"type"`
		CustomType string                `json:"customType"`
		Data       publicModelSourceData `json:"data"`
	}{"custom", transcript.ModelSourceType, d})
	if err != nil {
		return transcript.Public(e)
	}
	e.Payload = b
	return e, true
}

// nonEmpty 把空串 / nil 统一成 nil。
func nonEmpty(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

// buildIdentity 按范围现算身份：全局 agent 身份（名字与头像都没设则省略），以及范围内活动分支上「生效期间出现过
// message 条目」的 role / startup 来源记录各自取候选 [role, alias] 里第一个设了头像的 role（按 role 去重、按首次用到排序）。
func (s *Service) buildIdentity(ctx context.Context, sc *scope) (apigen.PublicShareIdentity, error) {
	out := apigen.PublicShareIdentity{Roles: []apigen.PublicRoleIdentity{}}
	ag, err := s.st.Q.GetOwnerAgentIdentity(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if a := (apigen.AgentIdentity{Name: nonEmpty(ag.AgentName), AvatarIcon: nonEmpty(ag.AgentAvatarIcon), AvatarImage: nonEmpty(ag.AgentAvatarImage)}); a.Name != nil || a.AvatarIcon != nil || a.AvatarImage != nil {
		out.Agent = &a
	}

	links, err := s.st.Q.ListMainEntryLinks(ctx, db.ListMainEntryLinksParams{ThreadID: sc.thread.ID, UntilSeq: sc.until})
	if err != nil {
		return out, err
	}
	types := make(map[int64]string, len(links))
	for _, l := range links {
		if sc.onPath[l.Seq] {
			types[l.Seq] = l.Type
		}
	}
	srcs, err := s.st.Q.ListModelSourceEntries(ctx, db.ListModelSourceEntriesParams{ThreadID: sc.thread.ID, UntilSeq: sc.until})
	if err != nil {
		return out, err
	}
	payloads := make(map[int64][]byte, len(srcs))
	for _, r := range srcs {
		payloads[r.Seq] = r.Payload
	}
	branch := make([]transcript.Entry, 0, len(sc.branch))
	for _, seq := range sc.branch {
		if typ, ok := types[seq]; ok {
			branch = append(branch, transcript.Entry{Seq: seq, Type: typ, Payload: payloads[seq]})
		}
	}
	used := transcript.UsedRoles(branch)
	if len(used) == 0 {
		return out, nil
	}

	var names map[string]string
	if s.cfg.RoleNames != nil {
		names = s.cfg.RoleNames()
	}
	avatars := map[string]*db.GetRoleAvatarRow{} // role → 头像行（nil = 没设）
	avatar := func(role string) (*db.GetRoleAvatarRow, error) {
		if a, ok := avatars[role]; ok {
			return a, nil
		}
		row, err := s.st.Q.GetRoleAvatar(ctx, role)
		var a *db.GetRoleAvatarRow
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return nil, err
		case nonEmpty(row.AvatarIcon) != nil || nonEmpty(row.AvatarImage) != nil:
			a = &row
		}
		avatars[role] = a
		return a, nil
	}
	added := map[string]bool{}
	for _, cands := range used {
		for _, role := range cands {
			a, err := avatar(role)
			if err != nil {
				return out, err
			}
			if a == nil {
				continue
			}
			if !added[role] {
				added[role] = true
				ri := apigen.PublicRoleIdentity{Key: roleKey(sc.share.ID, role), Role: role, AvatarIcon: nonEmpty(a.AvatarIcon), AvatarImage: nonEmpty(a.AvatarImage)}
				if n := names[role]; n != "" {
					ri.Name = &n
				}
				out.Roles = append(out.Roles, ri)
			}
			break
		}
	}
	return out, nil
}

// PublicIdentity 对应 `GET public/shares/{slug}/identity`：快照分享返回创建时固化的身份（升级前创建、没有身份行的
// 返回 `{roles: []}`）；实时分享按当前设置与当前范围现算。不存在、已撤销或线程已删除 → ErrNotFound。
func (s *Service) PublicIdentity(ctx context.Context, slug string) (apigen.PublicShareIdentity, error) {
	sh, err := s.activeBySlug(ctx, slug)
	if err != nil {
		return apigen.PublicShareIdentity{}, err
	}
	if sh.Mode != string(apigen.ShareModeSnapshot) {
		sc, err := s.scopeOf(ctx, sh)
		if err != nil {
			return apigen.PublicShareIdentity{}, err
		}
		return s.buildIdentity(ctx, sc)
	}
	if _, err := s.getThread(ctx, sh.ThreadID); err != nil {
		return apigen.PublicShareIdentity{}, err
	}
	out := apigen.PublicShareIdentity{Roles: []apigen.PublicRoleIdentity{}}
	payload, err := s.st.Q.GetThreadShareIdentity(ctx, sh.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return apigen.PublicShareIdentity{}, err
	}
	if out.Roles == nil {
		out.Roles = []apigen.PublicRoleIdentity{}
	}
	return out, nil
}
