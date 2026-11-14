package api_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const roleIdentitiesPath = "/api/v1/settings/role-identities"

type roleIdentityJSON struct {
	Role        string  `json:"role"`
	Name        *string `json:"name"`
	InCycle     bool    `json:"inCycle"`
	Selector    *string `json:"selector"`
	AvatarIcon  *string `json:"avatarIcon"`
	AvatarImage *string `json:"avatarImage"`
}

type roleListJSON struct {
	Roles       []roleIdentityJSON `json:"roles"`
	ConfigError *string            `json:"configError"`
}

func (e *h2Env) roleList(c h2Cred) roleListJSON {
	e.t.Helper()
	r := e.do("GET", roleIdentitiesPath, c, nil)
	require.Equal(e.t, 200, r.status, string(r.body))
	assert.NotContains(e.t, string(r.body), `"models"`)
	var out roleListJSON
	r.json(e.t, &out)
	return out
}

func (e *h2Env) putConfigYML(c h2Cred, content string) {
	e.t.Helper()
	r := e.do("PUT", "/api/v1/agent-config/files/config.yml", c, []byte(content))
	require.Equal(e.t, 200, r.status, string(r.body))
}

// roleKeys 把列表压成 "role:inCycle" 便于整体比较顺序。
func roleKeys(l roleListJSON) []string {
	out := make([]string, len(l.Roles))
	for i, r := range l.Roles {
		if r.InCycle {
			out[i] = r.Role
		} else {
			out[i] = r.Role + "(legacy)"
		}
	}
	return out
}

func roleByID(t *testing.T, l roleListJSON, role string) roleIdentityJSON {
	t.Helper()
	for _, r := range l.Roles {
		if r.Role == role {
			return r
		}
	}
	require.Failf(t, "role 不在列表里", "%s: %v", role, roleKeys(l))
	return roleIdentityJSON{}
}

func TestRoleIdentitiesAuth(t *testing.T) {
	e := newH2Env(t)
	assert.Equal(t, 401, e.do("GET", roleIdentitiesPath, h2Cred{}, nil).status)
	assert.Equal(t, 401, e.do("PUT", roleIdentitiesPath, h2Cred{}, map[string]any{"role": "smol"}).status)
	assert.Equal(t, 403, e.do("PUT", roleIdentitiesPath, h2Cred{cookie: e.webSession()}, map[string]any{"role": "smol", "avatarIcon": "🤖"}).status)
	r := e.do("GET", roleIdentitiesPath, h2Cred{cookie: e.webSession()}, nil)
	assert.Equal(t, 200, r.status, "GET 用 Cookie 不需要 CSRF 头")
}

func TestRoleIdentitiesList(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)

	// 没有 config.yml：缺省 cycleOrder，全部在 cycle 里、没有 name / selector / 头像
	r := e.do("GET", roleIdentitiesPath, c, nil)
	require.Equal(t, 200, r.status)
	assert.JSONEq(t, `{"roles":[{"role":"smol","inCycle":true},{"role":"default","inCycle":true},{"role":"slow","inCycle":true}]}`, string(r.body))

	// 写入 config.yml 后按新 cycleOrder，带 name 与原始 selector
	e.putConfigYML(c, `
cycleOrder: [Mare, Cyrene, Nahida]
modelRoles:
  default: "@Cyrene"
  Cyrene: anthropic/claude-opus-5-5:high
  Mare: [ openai/gpt-6.1:high , "@Cyrene" ]
modelTags:
  Cyrene: { name: 昔涟 }
`)
	l := e.roleList(c)
	assert.Equal(t, []string{"Mare", "Cyrene", "Nahida"}, roleKeys(l))
	assert.Nil(t, l.ConfigError)
	cy := roleByID(t, l, "Cyrene")
	require.NotNil(t, cy.Name)
	assert.Equal(t, "昔涟", *cy.Name)
	require.NotNil(t, cy.Selector)
	assert.Equal(t, "anthropic/claude-opus-5-5:high", *cy.Selector)
	mare := roleByID(t, l, "Mare")
	assert.Nil(t, mare.Name)
	require.NotNil(t, mare.Selector)
	assert.Equal(t, "openai/gpt-6.1:high, @Cyrene", *mare.Selector)
	var raw struct{ Roles []json.RawMessage }
	e.do("GET", roleIdentitiesPath, c, nil).json(t, &raw)
	require.Len(t, raw.Roles, 3)
	assert.JSONEq(t, `{"role":"Nahida","inCycle":true}`, string(raw.Roles[2]), "没配置的 role 省略 name 与 selector")

	// 非法 YAML：照样 200，带 configError，roles 为缺省三项
	e.putConfigYML(c, "cycleOrder: [Mare\n")
	l = e.roleList(c)
	assert.Equal(t, []string{"smol", "default", "slow"}, roleKeys(l))
	require.NotNil(t, l.ConfigError)
	assert.Contains(t, *l.ConfigError, "config.yml")
}

func TestRoleIdentitiesPut(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	png := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	e.putConfigYML(c, "cycleOrder: [Mare, mare, Cyrene]\nmodelRoles: { Mare: openai/gpt-6.1 }\nmodelTags: { Mare: { name: 玛蕾 } }\n")

	// 设图标：返回同列表组装的该项，列表带回
	r := e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "Mare", "avatarIcon": " 🐴 "})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"role":"Mare","name":"玛蕾","inCycle":true,"selector":"openai/gpt-6.1","avatarIcon":"🐴"}`, string(r.body))
	// 大小写原样：mare 是另一行
	r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "mare", "avatarImage": png})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"role":"mare","inCycle":true,"avatarImage":"`+png+`"}`, string(r.body))
	l := e.roleList(c)
	require.NotNil(t, roleByID(t, l, "Mare").AvatarIcon)
	assert.Equal(t, "🐴", *roleByID(t, l, "Mare").AvatarIcon)
	assert.Nil(t, roleByID(t, l, "Mare").AvatarImage)
	require.NotNil(t, roleByID(t, l, "mare").AvatarImage)
	assert.Equal(t, png, *roleByID(t, l, "mare").AvatarImage)
	assert.Nil(t, roleByID(t, l, "Cyrene").AvatarIcon)
	rows, err := e.st.Q.ListRoleAvatars(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)

	// 整体替换：换成图片后图标被清除
	r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "Mare", "avatarImage": png, "avatarIcon": ""})
	require.Equal(t, 200, r.status, string(r.body))
	assert.Nil(t, roleByID(t, e.roleList(c), "Mare").AvatarIcon)

	// 错误
	big := "data:image/webp;base64," + strings.Repeat("A", 128<<10)
	r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "Cyrene", "avatarImage": big})
	assert.Equal(t, 413, r.status)
	assert.Equal(t, "too_large", r.code(t))
	for _, bad := range []map[string]any{
		{"role": "smol", "avatarIcon": "🤖"}, // 不在 cycleOrder 里
		{"role": "MARE", "avatarIcon": "🤖"}, // 大小写不同即不同 role
		{"role": "Cyrene", "avatarIcon": "🤖", "avatarImage": png},
		{"role": "Cyrene", "avatarIcon": "robot"},
		{"role": "Cyrene", "avatarImage": "https://example.com/a.png"},
		{"role": "", "avatarIcon": "🤖"},
		{"role": "   "},
		{"role": strings.Repeat("r", 129)},
		{"role": "a\nb"},
		{"role": "a\u0007b", "avatarIcon": "🤖"},
	} {
		r := e.do("PUT", roleIdentitiesPath, c, bad)
		assert.Equal(t, 400, r.status, "%v: %s", bad, r.body)
		assert.Equal(t, "invalid_request", r.code(t))
	}
	rows, err = e.st.Q.ListRoleAvatars(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2, "被拒的请求不写库")

	// 128 字节的 role 合法（不在 cycle 里只能清除；清除幂等）
	r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": strings.Repeat("r", 128)})
	assert.Equal(t, 200, r.status, string(r.body))

	// 清除：行消失，再清一次仍 200；仍在 cycle 里的 role 返回 inCycle 为真
	for range 2 {
		r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "mare"})
		require.Equal(t, 200, r.status, string(r.body))
		assert.JSONEq(t, `{"role":"mare","inCycle":true}`, string(r.body))
	}
	_, err = e.st.Q.GetRoleAvatar(context.Background(), "mare")
	require.Error(t, err)
	_, err = e.st.Q.GetRoleAvatar(context.Background(), "Mare")
	require.NoError(t, err, "清 mare 不影响 Mare")
}

// 从 cycleOrder 删掉一个已设头像的 role：它成为遗留项排在末尾，可清除、不可重设。
func TestRoleIdentitiesLegacy(t *testing.T) {
	e := newH2Env(t)
	c := h2DeviceToken(t, e)
	e.putConfigYML(c, "cycleOrder: [Zed, Mare, Alpha, Cyrene]\nmodelRoles: { Mare: openai/gpt-6.1 }\nmodelTags: { Mare: { name: 玛蕾 } }\n")
	for _, role := range []string{"Zed", "Mare", "Alpha", "Cyrene"} {
		require.Equal(t, 200, e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": role, "avatarIcon": "🤖"}).status)
	}

	e.putConfigYML(c, "cycleOrder: [Cyrene]\nmodelRoles: { Mare: openai/gpt-6.1 }\nmodelTags: { Mare: { name: 玛蕾 } }\n")
	l := e.roleList(c)
	assert.Equal(t, []string{"Cyrene", "Alpha(legacy)", "Mare(legacy)", "Zed(legacy)"}, roleKeys(l), "遗留项按 role 升序排在末尾")
	mare := roleByID(t, l, "Mare")
	assert.False(t, mare.InCycle)
	assert.Nil(t, mare.Name, "遗留项不带 name")
	assert.Nil(t, mare.Selector, "遗留项不带 selector")
	require.NotNil(t, mare.AvatarIcon)

	r := e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "Mare", "avatarIcon": "🐴"})
	assert.Equal(t, 400, r.status, "遗留项不可重设")
	assert.Equal(t, "invalid_request", r.code(t))
	assert.Equal(t, "🤖", *roleByID(t, e.roleList(c), "Mare").AvatarIcon)

	r = e.do("PUT", roleIdentitiesPath, c, map[string]any{"role": "Mare"})
	require.Equal(t, 200, r.status, string(r.body))
	assert.JSONEq(t, `{"role":"Mare","inCycle":false}`, string(r.body))
	assert.Equal(t, []string{"Cyrene", "Alpha(legacy)", "Zed(legacy)"}, roleKeys(e.roleList(c)))

	// config.yml 解析失败时缺省三项 + 全部头像行作为遗留项
	e.putConfigYML(c, "cycleOrder: [\n")
	l = e.roleList(c)
	assert.Equal(t, []string{"smol", "default", "slow", "Alpha(legacy)", "Cyrene(legacy)", "Zed(legacy)"}, roleKeys(l))
	require.NotNil(t, l.ConfigError)
}
