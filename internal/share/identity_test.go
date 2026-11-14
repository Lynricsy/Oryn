package share_test

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/share"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

var hex16 = regexp.MustCompile(`^[0-9a-f]{16}$`)

// 各 role 头像 data URL 的可辨认前缀（负向检查用）。
func avatarOf(role string) string {
	return "data:image/png;base64,AVATAR-" + strings.ToUpper(role) + "-"
}

func (e *env) agentIdentity(name string) {
	e.t.Helper()
	_, err := e.st.DB.Exec(`INSERT OR IGNORE INTO owner (id, display_name, user_handle, created_at) VALUES (1, 'me', x'01', 1)`)
	require.NoError(e.t, err)
	img := "data:image/png;base64,AGENT-" + name
	_, err = e.st.Q.SetOwnerAgentIdentity(e.ctx, db.SetOwnerAgentIdentityParams{AgentName: &name, AgentAvatarImage: &img})
	require.NoError(e.t, err)
}

func (e *env) roleAvatar(role, suffix string) {
	e.t.Helper()
	img := avatarOf(role) + suffix
	require.NoError(e.t, e.st.Q.UpsertRoleAvatar(e.ctx, db.UpsertRoleAvatarParams{Role: role, AvatarImage: &img, UpdatedAt: 1}))
}

func source(src, role, alias string) map[string]any {
	d := map[string]any{"v": 1, "source": src, "model": "openai/gpt-6.1-sol"}
	if role != "" {
		d["role"] = role
	}
	if alias != "" {
		d["alias"] = alias
	}
	return map[string]any{"customType": "oryn-model-source", "data": d}
}

// roleThread：先点 Cyrene 随即换成 Mare（Cyrene 生效期间没有消息），Mare 下有一个回合；Nahida 从未选过。
// Mare、Cyrene、Nahida 都设了头像，agent 有名字与头像。
func (e *env) roleThread() {
	e.thread(tid, "Roles", nil)
	e.add(tid, "s1.jsonl", "mc0", "", "model_change", map[string]any{"model": "openai/gpt-6.1-sol"})
	e.add(tid, "s1.jsonl", "rc", "mc0", "custom", source("role", "Cyrene", ""))
	e.add(tid, "s1.jsonl", "rm", "rc", "custom", source("role", "Mare", ""))
	e.add(tid, "s1.jsonl", "u1", "rm", "message", msg("user", "hello"))
	e.add(tid, "s1.jsonl", "a1", "u1", "message", msg("assistant", "hi there"))
	e.leaf(tid, "a1")
	for _, r := range []string{"Mare", "Cyrene", "Nahida"} {
		e.roleAvatar(r, "v1")
	}
	e.agentIdentity("Ayla")
	e.roleNames = map[string]string{"Mare": "Mare the Swift", "Cyrene": "Cyrene Name", "Nahida": "Nahida Name"}
}

func identityJSON(t *testing.T, id apigen.PublicShareIdentity) string {
	t.Helper()
	b, err := json.Marshal(id)
	require.NoError(t, err)
	return string(b)
}

// 快照在创建时固化身份，之后改 agent 名字 / 换 role 头像 / 删 role_avatars 行都不变；实时分享随之变化；
// 只有 Mare 用到时匿名响应里不含 Cyrene、Nahida 及它们的头像；撤销后 404 且身份行被删除。
func TestShareIdentitySnapshotAndLive(t *testing.T) {
	e := newEnv(t)
	e.roleThread()
	snap, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	live, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)

	sid, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	require.NotNil(t, sid.Agent)
	assert.Equal(t, "Ayla", *sid.Agent.Name)
	require.Len(t, sid.Roles, 1, "只有 Mare 用到")
	mare := sid.Roles[0]
	assert.Equal(t, "Mare", mare.Role)
	assert.Equal(t, "Mare the Swift", *mare.Name)
	assert.Equal(t, avatarOf("Mare")+"v1", *mare.AvatarImage)
	assert.Regexp(t, hex16, mare.Key)

	lid, err := e.svc.PublicIdentity(e.ctx, live.Slug)
	require.NoError(t, err)
	require.Len(t, lid.Roles, 1)
	assert.NotEqual(t, mare.Key, lid.Roles[0].Key, "不同分享的键不同")

	// 匿名条目里的来源记录：role 是本分享的键，与身份接口一致；全部匿名文本不含未用到的 role
	for _, sh := range []apigen.ThreadShare{snap, live} {
		id, err := e.svc.PublicIdentity(e.ctx, sh.Slug)
		require.NoError(t, err)
		entries := e.allEntries(sh.Slug, 50)
		var keys []string
		for _, en := range entries {
			p := payloadOf(en)
			if en["type"] == "custom" {
				assert.Equal(t, "oryn-model-source", p["customType"])
				keys = append(keys, p["data"].(map[string]any)["role"].(string))
			}
		}
		require.Len(t, keys, 2, "Cyrene 与 Mare 两条记录都在（以不透明键出现）")
		assert.Equal(t, id.Roles[0].Key, keys[1], "Mare 的记录键 = 身份里的键")
		raw, _ := json.Marshal(entries)
		text := string(raw) + identityJSON(t, id)
		for _, leak := range []string{"Cyrene", "Nahida", avatarOf("Cyrene"), avatarOf("Nahida"), "Mare", "shr_"} {
			if leak == "Mare" {
				assert.NotContains(t, string(raw), leak, "条目里没有 role id")
				continue
			}
			assert.NotContains(t, text, leak)
		}
	}

	// 改设置：快照不变，实时跟随
	before := identityJSON(t, sid)
	e.agentIdentity("Bryn")
	e.roleAvatar("Mare", "v2")
	e.roleNames = map[string]string{"Mare": "Renamed"}
	sid2, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	assert.Equal(t, before, identityJSON(t, sid2), "快照身份创建时固化")
	lid2, err := e.svc.PublicIdentity(e.ctx, live.Slug)
	require.NoError(t, err)
	assert.Equal(t, "Bryn", *lid2.Agent.Name)
	require.Len(t, lid2.Roles, 1)
	assert.Equal(t, avatarOf("Mare")+"v2", *lid2.Roles[0].AvatarImage)
	assert.Equal(t, "Renamed", *lid2.Roles[0].Name)

	// 删掉 Mare 的头像：快照仍在，实时不再列出
	require.NoError(t, e.st.Q.DeleteRoleAvatar(e.ctx, "Mare"))
	sid3, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	assert.Equal(t, before, identityJSON(t, sid3))
	lid3, err := e.svc.PublicIdentity(e.ctx, live.Slug)
	require.NoError(t, err)
	assert.Empty(t, lid3.Roles)

	// 实时分享里新用到的 role 出现；快照没有
	e.add(tid, "s1.jsonl", "rh", "a1", "custom", source("role", "Hyacinthia", ""))
	e.add(tid, "s1.jsonl", "u2", "rh", "message", msg("user", "again"))
	e.roleAvatar("Hyacinthia", "v1")
	e.leaf(tid, "u2")
	lid4, err := e.svc.PublicIdentity(e.ctx, live.Slug)
	require.NoError(t, err)
	require.Len(t, lid4.Roles, 1)
	assert.Equal(t, "Hyacinthia", lid4.Roles[0].Role)
	sid4, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	assert.Equal(t, before, identityJSON(t, sid4))

	// 撤销：身份 404，快照的身份行被删除
	require.NoError(t, e.svc.Revoke(e.ctx, snap.ID))
	require.NoError(t, e.svc.Revoke(e.ctx, live.ID))
	for _, sh := range []apigen.ThreadShare{snap, live} {
		_, err := e.svc.PublicIdentity(e.ctx, sh.Slug)
		assert.ErrorIs(t, err, share.ErrNotFound)
	}
	var n int
	require.NoError(t, e.st.DB.QueryRow(`SELECT count(*) FROM thread_share_identities WHERE share_id IN (?, ?)`, snap.ID, live.ID).Scan(&n))
	assert.Zero(t, n)
}

// role 用到但没头像 → 不在 roles 里；startup 记录的 default 没头像而别名目标有 → 取别名目标；agent 未设置 → 没有 agent；
// manual 记录下的回合不产生任何 role。
func TestShareIdentityCandidates(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Cands", nil)
	e.add(tid, "s1.jsonl", "rs", "", "custom", source("startup", "default", "Cyrene"))
	e.add(tid, "s1.jsonl", "u1", "rs", "message", msg("user", "one"))
	e.add(tid, "s1.jsonl", "rp", "u1", "custom", source("role", "Plain", ""))
	e.add(tid, "s1.jsonl", "u2", "rp", "message", msg("user", "two"))
	e.add(tid, "s1.jsonl", "rn", "u2", "custom", source("manual", "", ""))
	e.add(tid, "s1.jsonl", "u3", "rn", "message", msg("user", "three"))
	e.leaf(tid, "u3")
	e.roleAvatar("Cyrene", "v1")
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)
	id, err := e.svc.PublicIdentity(e.ctx, sh.Slug)
	require.NoError(t, err)
	assert.Nil(t, id.Agent, "agent 身份未设置：省略")
	require.Len(t, id.Roles, 1)
	assert.Equal(t, "Cyrene", id.Roles[0].Role)
	assert.Nil(t, id.Roles[0].Name, "没有名字表：省略 name")
	text := identityJSON(t, id)
	assert.NotContains(t, text, "Plain")
	assert.NotContains(t, text, `"default"`)

	// 条目里 startup 记录的 alias 键 = 身份里 Cyrene 的键
	for _, en := range e.allEntries(sh.Slug, 50) {
		if en["entryId"] == "rs" {
			d := payloadOf(en)["data"].(map[string]any)
			assert.Equal(t, id.Roles[0].Key, d["alias"])
			assert.NotEqual(t, id.Roles[0].Key, d["role"])
		}
	}
}

// 选了 role 之后发生回退 / prewalk 的临时切换（model_change 的 role 为 fallback / prewalk），切回后才有消息：
// 匿名身份仍含该 role，与站内判定一致——这要求分享层把 model_change 的载荷（其中的 role）也交给 UsedRoles。
// 对照：普通的 model_change（未记录来源的切换）之后的消息不算该 role。
func TestShareIdentityTemporarySwitchKeepsRole(t *testing.T) {
	e := newEnv(t)
	e.thread(tid, "Temp", nil)
	e.add(tid, "s1.jsonl", "rm", "", "custom", source("role", "Mare", ""))
	e.add(tid, "s1.jsonl", "fb", "rm", "model_change", map[string]any{"model": "openai/gpt-5", "role": "fallback", "resolvedModelIsFallback": true})
	e.add(tid, "s1.jsonl", "pw", "fb", "model_change", map[string]any{"model": "openai/gpt-6.1-sol", "role": "prewalk"})
	e.add(tid, "s1.jsonl", "u1", "pw", "message", msg("user", "after the temporary switches"))
	e.add(tid, "s1.jsonl", "rc", "u1", "custom", source("role", "Cyrene", ""))
	e.add(tid, "s1.jsonl", "mc", "rc", "model_change", map[string]any{"model": "openai/gpt-5", "role": "default"})
	e.add(tid, "s1.jsonl", "u2", "mc", "message", msg("user", "after an unrecorded switch"))
	e.leaf(tid, "u2")
	e.roleAvatar("Mare", "v1")
	e.roleAvatar("Cyrene", "v1")
	sh, err := e.svc.Create(e.ctx, tid, apigen.ShareModeLive, false)
	require.NoError(t, err)
	id, err := e.svc.PublicIdentity(e.ctx, sh.Slug)
	require.NoError(t, err)
	roles := make([]string, 0, len(id.Roles))
	for _, r := range id.Roles {
		roles = append(roles, r.Role)
	}
	assert.Equal(t, []string{"Mare"}, roles, "临时切换不截断 Mare；未记录来源的切换让 Cyrene 失效")
}

// 升级前创建的快照没有身份行 → {roles: []}、没有 agent；错误 slug 404；`.md` 导出不带身份。
func TestShareIdentityLegacyAndMarkdown(t *testing.T) {
	e := newEnv(t)
	e.roleThread()
	snap, err := e.svc.Create(e.ctx, tid, apigen.ShareModeSnapshot, false)
	require.NoError(t, err)
	md := do(t, e.svc.PageHandler(spa()), http.MethodGet, "/s/"+snap.Slug+".md")
	require.Equal(t, http.StatusOK, md.Code)
	for _, leak := range []string{"Ayla", "Mare", "Cyrene", "Nahida", "AVATAR", "AGENT-"} {
		assert.NotContains(t, md.Body.String(), leak)
	}
	assert.Contains(t, md.Body.String(), "## Assistant")

	require.NoError(t, e.st.Q.DeleteThreadShareIdentity(e.ctx, snap.ID))
	id, err := e.svc.PublicIdentity(e.ctx, snap.Slug)
	require.NoError(t, err)
	assert.Nil(t, id.Agent)
	assert.NotNil(t, id.Roles)
	assert.Empty(t, id.Roles)
	assert.Equal(t, `{"roles":[]}`, identityJSON(t, id))

	_, err = e.svc.PublicIdentity(e.ctx, strings.Repeat("0", 32))
	assert.ErrorIs(t, err, share.ErrNotFound)
	_, err = e.svc.PublicIdentity(e.ctx, "nope")
	assert.ErrorIs(t, err, share.ErrNotFound)
}
