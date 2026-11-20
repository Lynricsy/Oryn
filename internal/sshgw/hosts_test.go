package sshgw

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

func TestHostValidation(t *testing.T) {
	e := newEnv(t)
	k := e.key(t, "k")
	cases := []struct {
		name string
		mod  func(*apigen.SSHHostCreate)
		code string
		msg  string
	}{
		{"name dot", func(in *apigen.SSHHostCreate) { in.Name = ".x" }, "invalid_request", "主机名"},
		{"name dash", func(in *apigen.SSHHostCreate) { in.Name = "-x" }, "invalid_request", "主机名"},
		{"name slash", func(in *apigen.SSHHostCreate) { in.Name = "a/b" }, "invalid_request", "主机名"},
		{"name long", func(in *apigen.SSHHostCreate) { in.Name = strings.Repeat("a", 65) }, "invalid_request", "主机名"},
		{"addr empty", func(in *apigen.SSHHostCreate) { in.Address = "  " }, "invalid_request", "地址"},
		{"addr space", func(in *apigen.SSHHostCreate) { in.Address = "a b" }, "invalid_request", "地址"},
		{"addr dash", func(in *apigen.SSHHostCreate) { in.Address = "-oProxyCommand" }, "invalid_request", "地址"},
		{"port 0", func(in *apigen.SSHHostCreate) { in.Port = new(0) }, "invalid_request", "端口"},
		{"port big", func(in *apigen.SSHHostCreate) { in.Port = new(65536) }, "invalid_request", "端口"},
		{"user empty", func(in *apigen.SSHHostCreate) { in.Username = "" }, "invalid_request", "登录用户"},
		{"user space", func(in *apigen.SSHHostCreate) { in.Username = "a b" }, "invalid_request", "登录用户"},
		{"user dash", func(in *apigen.SSHHostCreate) { in.Username = "-l" }, "invalid_request", "登录用户"},
		{"no key", func(in *apigen.SSHHostCreate) { in.KeyID = nil }, "invalid_request", "请选择一把钥匙"},
		{"bad key", func(in *apigen.SSHHostCreate) { in.KeyID = new("sshk_nope") }, "invalid_request", "钥匙不存在"},
		{"no password", func(in *apigen.SSHHostCreate) { in.AuthType = apigen.SSHAuthPassword }, "invalid_request", "请填写密码"},
		{"empty password", func(in *apigen.SSHHostCreate) {
			in.AuthType, in.Password = apigen.SSHAuthPassword, new("")
		}, "invalid_request", ""},
		{"too many tags", func(in *apigen.SSHHostCreate) {
			for i := range 17 {
				in.Tags = append(in.Tags, strings.Repeat("t", i+1))
			}
		}, "invalid_request", "标签"},
		{"long tag", func(in *apigen.SSHHostCreate) { in.Tags = []string{strings.Repeat("t", 33)} }, "invalid_request", "标签"},
		{"long desc", func(in *apigen.SSHHostCreate) { in.Description = new(strings.Repeat("描", 501)) }, "invalid_request", "描述"},
		{"bad project", func(in *apigen.SSHHostCreate) { in.ProjectID = new("prj_nope") }, "not_found", "项目不存在"},
		{"bad jump", func(in *apigen.SSHHostCreate) { in.JumpHostID = new("ssh_nope") }, "invalid_request", "跳板"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := hostIn("web", k.ID)
			c.mod(&in)
			_, err := e.svc.CreateHost(context.Background(), in)
			requireCode(t, err, c.code, c.msg)
		})
	}

	// 「empty password」的空串在新建时同样要求填写
	in := hostIn("web", k.ID)
	in.AuthType, in.Password = apigen.SSHAuthPassword, new("")
	_, err := e.svc.CreateHost(context.Background(), in)
	requireCode(t, err, "invalid_request", "请填写密码")
}

func TestHostCreateNormalizesAndScopes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	p := e.project(t, "prj_a")

	in := hostIn("web", k.ID)
	in.Address = " 10.0.0.2 "
	in.Tags = []string{" prod ", "PROD", "", "db"}
	h := e.mustHost(t, in)
	require.Equal(t, 22, h.Port)
	require.Equal(t, "10.0.0.2", h.Address)
	require.Equal(t, []string{"prod", "db"}, h.Tags)
	require.True(t, h.Enabled)
	require.False(t, h.AllowAutomation)
	require.Nil(t, h.ProjectID)

	_, err := e.svc.CreateHost(ctx, hostIn("web", k.ID))
	requireCode(t, err, "conflict", "同名主机已存在")
	// 项目里同名不冲突
	pin := hostIn("web", k.ID)
	pin.ProjectID = &p
	ph := e.mustHost(t, pin)
	require.Equal(t, p, *ph.ProjectID)
	_, err = e.svc.CreateHost(ctx, pin)
	requireCode(t, err, "conflict", "同名主机已存在")
	require.Equal(t, []string{"", p}, e.appliedLayers())

	list, err := e.svc.ListHosts(ctx)
	require.NoError(t, err)
	require.Len(t, list.Hosts, 2)
	require.Nil(t, list.Hosts[0].ProjectID, "全局在前")
}

func TestHostPasswordAndAuthSwitch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	in := apigen.SSHHostCreate{Name: "pw", Address: "h", Username: "u", AuthType: apigen.SSHAuthPassword, Password: new("hunter2")}
	h := e.mustHost(t, in)
	require.True(t, h.HasPassword)
	sec, err := e.svc.Secrets(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"ssh/host/pw/password": "hunter2"}, sec)

	// 改描述不带密码：沿用
	h, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("d"), Password: new("")}})
	require.NoError(t, err)
	require.True(t, h.HasPassword)

	// 切到钥匙：密文被清掉
	h, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AuthType: new(apigen.SSHAuthKey), KeyID: &k.ID}})
	require.NoError(t, err)
	require.False(t, h.HasPassword)
	row, err := e.st.Q.GetSshHost(ctx, h.ID)
	require.NoError(t, err)
	require.Nil(t, row.PasswordEnc)

	// 切回密码但不给密码：拒绝
	_, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AuthType: new(apigen.SSHAuthPassword)}})
	requireCode(t, err, "invalid_request", "请填写密码")
	h, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AuthType: new(apigen.SSHAuthPassword), Password: new("x")}})
	require.NoError(t, err)
	require.Nil(t, h.KeyID)
	require.True(t, h.HasPassword)
}

func TestJumpRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	p := e.project(t, "prj_a")
	q := e.project(t, "prj_b")

	// 链：h0 ← h1 ← … ← h5（h5 经 5 级跳板）
	prev := ""
	var chain []apigen.SSHHost
	for i := range 6 {
		in := hostIn("h"+string(rune('0'+i)), k.ID)
		if prev != "" {
			in.JumpHostID = &prev
		}
		h := e.mustHost(t, in)
		chain = append(chain, h)
		prev = h.ID
	}
	in := hostIn("h6", k.ID)
	in.JumpHostID = &prev
	_, err := e.svc.CreateHost(ctx, in)
	requireCode(t, err, "invalid_request", "跳板链超过 5 级")

	// 环：h0 指向 h2
	_, err = e.svc.UpdateHost(ctx, chain[0].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{JumpHostID: &chain[2].ID}, JumpHostSet: true})
	requireCode(t, err, "invalid_request", "跳板链存在循环")
	_, err = e.svc.UpdateHost(ctx, chain[0].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{JumpHostID: &chain[0].ID}, JumpHostSet: true})
	requireCode(t, err, "invalid_request", "跳板链存在循环")

	// 作用域：项目主机可用全局与同项目，全局主机不能用项目主机，项目主机不能用别的项目的主机
	pj := hostIn("pj", k.ID)
	pj.ProjectID = &p
	pjh := e.mustHost(t, pj)
	viaGlobal := hostIn("pj2", k.ID)
	viaGlobal.ProjectID, viaGlobal.JumpHostID = &p, &chain[0].ID
	e.mustHost(t, viaGlobal)
	viaSame := hostIn("pj3", k.ID)
	viaSame.ProjectID, viaSame.JumpHostID = &p, &pjh.ID
	e.mustHost(t, viaSame)
	g := hostIn("g", k.ID)
	g.JumpHostID = &pjh.ID
	_, err = e.svc.CreateHost(ctx, g)
	requireCode(t, err, "invalid_request", "全局主机只能用全局主机作跳板")
	other := hostIn("q1", k.ID)
	other.ProjectID, other.JumpHostID = &q, &pjh.ID
	_, err = e.svc.CreateHost(ctx, other)
	requireCode(t, err, "invalid_request", "同一项目")

	// 跳板被引用：拒删、拒停用
	requireCode(t, e.svc.DeleteHost(ctx, chain[0].ID), "conflict", "主机被其他主机用作跳板")
	_, err = e.svc.UpdateHost(ctx, chain[4].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(false)}})
	requireCode(t, err, "conflict", "还有启用中的主机拿它当跳板")
	// 先停 h5，再停 h4；之后启用 h5 → 跳板已停用
	_, err = e.svc.UpdateHost(ctx, chain[5].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(false)}})
	require.NoError(t, err)
	_, err = e.svc.UpdateHost(ctx, chain[4].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(false)}})
	require.NoError(t, err)
	_, err = e.svc.UpdateHost(ctx, chain[5].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Enabled: new(true)}})
	requireCode(t, err, "conflict", "它的跳板已停用")
	// 停用的主机仍可改别的字段
	_, err = e.svc.UpdateHost(ctx, chain[5].ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("x")}})
	require.NoError(t, err)
	// 新主机不能选停用的跳板
	in = hostIn("h7", k.ID)
	in.JumpHostID = &chain[4].ID
	_, err = e.svc.CreateHost(ctx, in)
	requireCode(t, err, "invalid_request", "已停用")
	// null 取消跳板
	h, err := e.svc.UpdateHost(ctx, chain[5].ID, HostPatch{JumpHostSet: true})
	require.NoError(t, err)
	require.Nil(t, h.JumpHostID)
	// 删除被引用的跳板之外的叶子
	require.NoError(t, e.svc.DeleteHost(ctx, chain[5].ID))
	requireCode(t, e.svc.DeleteHost(ctx, chain[5].ID), "not_found", "")
}

func TestAddressChangeClearsHostKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	h := e.mustHost(t, hostIn("web", k.ID))
	pin := func() {
		_, err := e.st.Q.PinSshHostKey(ctx, db.PinSshHostKeyParams{HostKey: new(e.svc.gatewayPublicKey()), At: new(int64(5)), ID: h.ID})
		require.NoError(t, err)
	}
	pin()
	got, err := e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("x")}})
	require.NoError(t, err)
	require.NotNil(t, got.HostKey, "改描述不清指纹")
	require.Equal(t, "ssh-ed25519", got.HostKey.Type)
	got, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Port: new(2222)}})
	require.NoError(t, err)
	require.Nil(t, got.HostKey)
	pin()
	got, err = e.svc.ResetHostKey(ctx, h.ID)
	require.NoError(t, err)
	require.Nil(t, got.HostKey)

	// compat：null 清掉，缺省不改
	got, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Compat: new(true)}, CompatSet: true})
	require.NoError(t, err)
	require.True(t, *got.Compat)
	got, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("y")}})
	require.NoError(t, err)
	require.NotNil(t, got.Compat)
	got, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{CompatSet: true})
	require.NoError(t, err)
	require.Nil(t, got.Compat)
}

func TestVisibleShadowingAndAutomation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	p := e.project(t, "prj_a")

	g := hostIn("web", k.ID)
	g.Description, g.Tags = new("global"), []string{"a", "b"}
	e.mustHost(t, g)
	auto := hostIn("auto", k.ID)
	auto.AllowAutomation = new(true)
	e.mustHost(t, auto)
	off := hostIn("off", k.ID)
	off.Enabled = new(false)
	e.mustHost(t, off)
	pw := hostIn("web", k.ID)
	pw.ProjectID, pw.Address, pw.Port = &p, "10.9.9.9", new(2200)
	e.mustHost(t, pw)

	orb := db.Orb{ID: "orb-1", ProjectID: &p, Kind: "orb"}
	cfg, err := e.svc.OrbConfig(ctx, orb)
	require.NoError(t, err)
	require.Equal(t, e.svc.gatewayPublicKey(), cfg.HostKey)
	require.Equal(t, []apigen.LinkCtlSSHHost{
		{Name: "auto", Description: "root@10.0.0.1:22"},
		{Name: "web", Description: "root@10.9.9.9:2200"},
	}, cfg.Hosts)

	// 无项目的 orb 看到全局 web（带描述与标签）
	cfg, err = e.svc.OrbConfig(ctx, db.Orb{ID: "orb-2", Kind: "orb"})
	require.NoError(t, err)
	require.Equal(t, "root@10.0.0.1:22 — global [a, b]", cfg.Hosts[1].Description)

	// 全自动 orb 只看到放开的主机
	orb.Automation = true
	usable, blocked, managed, err := e.svc.visible(ctx, orb)
	require.NoError(t, err)
	require.True(t, managed)
	require.Len(t, usable, 1)
	require.Equal(t, "auto", usable[0].Name)
	require.Len(t, blocked, 1)
	_, deny, err := e.svc.resolve(ctx, orb, "web")
	require.NoError(t, err)
	require.Contains(t, deny, "不允许全自动线程使用")
	_, deny, err = e.svc.resolve(ctx, orb, "off")
	require.NoError(t, err)
	require.Contains(t, deny, "已停用")
	_, deny, err = e.svc.resolve(ctx, orb, "nope")
	require.NoError(t, err)
	require.Contains(t, deny, "未登记")

	// runner（M21）只有全局主机：网关只能把连接归属到 runner 的常驻 orb，认不出线程的项目——即便 orb 行带了 project_id，
	// 项目里同名的 web 也不盖过全局的、只属于项目的主机一律不可见；全自动同样只用放开的主机
	ponly := hostIn("ponly", k.ID)
	ponly.ProjectID = &p
	e.mustHost(t, ponly)
	runner := db.Orb{ID: "r", Kind: "runner", ProjectID: &p}
	rcfg, err := e.svc.OrbConfig(ctx, runner)
	require.NoError(t, err)
	require.NotNil(t, rcfg)
	require.Equal(t, e.svc.gatewayPublicKey(), rcfg.HostKey)
	require.Equal(t, []apigen.LinkCtlSSHHost{
		{Name: "auto", Description: "root@10.0.0.1:22"},
		{Name: "web", Description: "root@10.0.0.1:22 — global [a, b]"},
	}, rcfg.Hosts)
	_, deny, err = e.svc.resolve(ctx, runner, "ponly")
	require.NoError(t, err)
	require.Contains(t, deny, "未登记，或对这台 orb 不可见", "runner 连项目级主机一律拒绝")
	pcfg, err := e.svc.OrbConfig(ctx, db.Orb{ID: "orb-1", ProjectID: &p, Kind: "orb"})
	require.NoError(t, err)
	require.Len(t, pcfg.Hosts, 3, "对照：同项目的 orb 看得到 ponly")
	runner.Automation = true
	usable, blocked, managed, err = e.svc.visible(ctx, runner)
	require.NoError(t, err)
	require.True(t, managed)
	require.Len(t, usable, 1)
	require.Equal(t, "auto", usable[0].Name)
	require.Len(t, blocked, 1)

	// 协调宿主不受管
	cfg, err = e.svc.OrbConfig(ctx, db.Orb{ID: "c", Kind: "orb", Coordinator: true})
	require.NoError(t, err)
	require.Nil(t, cfg)
}

func TestLayerStateTracksVisibleContent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	k := e.key(t, "k")
	p := e.project(t, "prj_a")
	base, err := e.svc.LayerState("")
	require.NoError(t, err)
	baseP, err := e.svc.LayerState(p)
	require.NoError(t, err)

	h := e.mustHost(t, hostIn("web", k.ID))
	g1, err := e.svc.LayerState("")
	require.NoError(t, err)
	require.NotEqual(t, base, g1)
	pNow, err := e.svc.LayerState(p)
	require.NoError(t, err)
	require.Equal(t, baseP, pNow, "全局主机不影响项目层摘要")

	_, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{AllowForward: new(true)}})
	require.NoError(t, err)
	g2, err := e.svc.LayerState("")
	require.NoError(t, err)
	require.Equal(t, g1, g2, "端口转发开关在 orb 里不可见")
	_, err = e.svc.UpdateHost(ctx, h.ID, HostPatch{SSHHostUpdate: apigen.SSHHostUpdate{Description: new("x")}})
	require.NoError(t, err)
	g3, err := e.svc.LayerState("")
	require.NoError(t, err)
	require.NotEqual(t, g2, g3)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(g3, &parsed))
}

func TestThreadHosts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.svc.ThreadHosts(ctx, "T-nope")
	requireCode(t, err, "not_found", "")
}
