package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// runSsh 用假服务端执行 `oryn ssh …`；stdin 为空串时标准输入为空。
func runSsh(f *fakeAPI, stdin string, args ...string) (string, string, error) {
	cmd := newSshCmd(func() (*apiClient, error) { return f.client(), nil })
	// 与 root 一致：出错时不打印用法
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out, errb bytes.Buffer
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

// sshFixtures 预置：项目 demo（prj_A）、钥匙 deploy、全局主机 bastion / web1、项目主机 web1 与 jump2。
func sshFixtures(f *fakeAPI) {
	prj := "prj_A"
	keyID := "sshk_1"
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	f.json("GET /api/v1/projects", 200, apigen.ProjectList{Projects: []apigen.Project{{ID: prj, Name: "demo"}}})
	f.json("GET /api/v1/ssh/keys", 200, apigen.SSHKeyList{Keys: []apigen.SSHKey{
		{ID: keyID, Name: "deploy", Type: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA deploy", Fingerprint: "SHA256:abc", HostCount: 2},
	}})
	f.json("GET /api/v1/ssh/hosts", 200, apigen.SSHHostList{Hosts: []apigen.SSHHost{
		{ID: "ssh_G1", Name: "bastion", Address: "10.0.0.1", Port: 22, Username: "ops", AuthType: apigen.SSHAuthKey, KeyID: &keyID, Enabled: true, Tags: []string{"edge"},
			LastTest: &apigen.SSHHostTest{At: at, Ok: true}},
		{ID: "ssh_G2", Name: "web1", Address: "10.0.0.2", Port: 2222, Username: "root", AuthType: apigen.SSHAuthPassword, HasPassword: true, JumpHostID: new("ssh_G1"), Tags: []string{}, AllowForward: true},
		{ID: "ssh_P1", ProjectID: &prj, Name: "web1", Address: "10.1.0.2", Port: 22, Username: "app", AuthType: apigen.SSHAuthKey, KeyID: &keyID, Enabled: true, Tags: []string{}, AllowAutomation: true},
		{ID: "ssh_P2", ProjectID: &prj, Name: "jump2", Address: "10.1.0.1", Port: 22, Username: "app", AuthType: apigen.SSHAuthKey, KeyID: &keyID, Enabled: true, Tags: []string{}},
	}})
}

func lastReq(t *testing.T, f *fakeAPI) recordedReq {
	t.Helper()
	reqs := f.requests()
	require.NotEmpty(t, reqs)
	return reqs[len(reqs)-1]
}

func TestSshAddRequestBody(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	f.json("POST /api/v1/ssh/hosts", 201, apigen.SSHHost{ID: "ssh_N", Name: "db"})
	f.json("POST /api/v1/ssh/hosts/ssh_N/test", 200, apigen.SSHTestResult{Ok: true, LatencyMs: 42, Os: new("linux"), HostKeyFingerprint: new("SHA256:fp"),
		Snapshot: &apigen.SSHSnapshot{Load1: new(float32(0.5)), CPUPct: new(float32(12.5)), MemUsedKb: new(int64(1 << 20)), MemTotalKb: new(int64(4 << 20)),
			Disks: []apigen.SSHSnapshotDisk{{Mount: "/", UsedKb: 25 << 20, TotalKb: 100 << 20}}}})

	// 项目主机 + 钥匙 + 跳板：跳板先找同项目
	out, _, err := runSsh(f, "", "add", "db", "--project", "demo", "--host", "10.1.0.9", "--user", "pg", "--port", "2200",
		"--key", "deploy", "--jump", "jump2", "--desc", "主库", "--tag", "db", "--tag", "prod", "--allow-automation", "--allow-forward", "--compat", "on", "--disabled")
	require.NoError(t, err)
	var post recordedReq
	for _, r := range f.requests() {
		if r.Method == "POST" && r.Path == "/api/v1/ssh/hosts" {
			post = r
		}
	}
	assert.JSONEq(t, `{"projectId":"prj_A","name":"db","address":"10.1.0.9","port":2200,"username":"pg","authType":"key","keyId":"sshk_1",
		"jumpHostId":"ssh_P2","description":"主库","tags":["db","prod"],"allowAutomation":true,"allowForward":true,"compat":true,"enabled":false}`, string(post.Body))
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_N/test", lastReq(t, f).Path)
	assert.Contains(t, out, "已添加主机 db（ssh_N）")
	assert.Contains(t, out, "成功（42ms），系统 linux")
	assert.Contains(t, out, "指纹 SHA256:fp")
	assert.Contains(t, out, "负载 0.50  CPU 12.5%  内存 1.0 GiB / 4.0 GiB")
	assert.Contains(t, out, "磁盘 /  25.0 GiB / 100.0 GiB（25%）")

	// 全局主机 + 密码（标准输入，去掉末尾换行）+ 全局跳板；缺省字段不发送
	_, _, err = runSsh(f, "s3cret\n", "add", "db", "--host", "h", "--user", "u", "--password-stdin", "--jump", "bastion")
	require.NoError(t, err)
	reqs := f.requests()
	assert.JSONEq(t, `{"name":"db","address":"h","username":"u","authType":"password","password":"s3cret","jumpHostId":"ssh_G1"}`, string(reqs[len(reqs)-2].Body))

	// 全局主机不能拿项目主机当跳板
	_, _, err = runSsh(f, "", "add", "db", "--host", "h", "--user", "u", "--key", "deploy", "--jump", "jump2")
	assert.ErrorContains(t, err, "跳板 \"jump2\" 不存在")

	// 测试失败不影响 add 的退出码
	f.json("POST /api/v1/ssh/hosts/ssh_N/test", 200, apigen.SSHTestResult{Ok: false, LatencyMs: 3, Error: new("连不上 h:22：refused")})
	out, _, err = runSsh(f, "", "add", "db", "--host", "h", "--user", "u", "--key", "deploy")
	require.NoError(t, err)
	assert.Contains(t, out, "失败（3ms）：连不上 h:22：refused")
}

func TestSshAddFlagErrors(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	for _, args := range [][]string{
		{"add", "db", "--host", "h", "--user", "u"},
		{"add", "db", "--host", "h", "--user", "u", "--key", "deploy", "--password-stdin"},
		{"add", "db", "--user", "u", "--key", "deploy"},
		{"add", "db", "--host", "h", "--user", "u", "--key", "deploy", "--compat", "maybe"},
		{"set", "web1"},
		{"set", "web1", "--project", "demo"},
		{"set", "web1", "--key", "deploy", "--password-stdin"},
		{"set", "web1", "--enable", "--disable"},
		{"set", "web1", "--jump", "bastion", "--no-jump"},
		{"ls", "--all", "--project", "demo"},
		{"audit", "--limit", "0"},
	} {
		_, _, err := runSsh(f, "pw", args...)
		requireUsageErr(t, err)
	}
	assert.Empty(t, f.requests(), "调用错误不发请求")

	// 标准输入为空时 --password-stdin 报调用错误，不创建主机
	_, _, err := runSsh(f, "\n", "add", "db", "--host", "h", "--user", "u", "--password-stdin")
	requireUsageErr(t, err)
	for _, r := range f.requests() {
		assert.NotEqual(t, "POST", r.Method)
	}
}

func TestSshResolveByScope(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	f.json("POST /api/v1/ssh/hosts/ssh_G2/reset-host-key", 200, apigen.SSHHost{ID: "ssh_G2", Name: "web1"})
	f.json("POST /api/v1/ssh/hosts/ssh_P1/reset-host-key", 200, apigen.SSHHost{ID: "ssh_P1", Name: "web1"})
	f.json("DELETE /api/v1/ssh/hosts/ssh_P2", 204, nil)

	// 同名：缺省命中全局，--project 命中项目
	_, _, err := runSsh(f, "", "reset-host-key", "web1")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_G2/reset-host-key", lastReq(t, f).Path)
	out, _, err := runSsh(f, "", "reset-host-key", "web1", "--project", "demo")
	require.NoError(t, err)
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_P1/reset-host-key", lastReq(t, f).Path)
	assert.Contains(t, out, "已重置主机 web1 的指纹")

	// 项目主机在全局作用域里找不到
	_, _, err = runSsh(f, "", "rm", "jump2")
	assert.ErrorContains(t, err, "没有这台主机")
	_, _, err = runSsh(f, "", "rm", "jump2", "--project", "demo")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", lastReq(t, f).Method)
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_P2", lastReq(t, f).Path)

	_, _, err = runSsh(f, "", "rm", "web1", "--project", "nope")
	assert.ErrorContains(t, err, "项目 \"nope\" 不存在")
}

func TestSshSetPatchBody(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	f.json("PATCH /api/v1/ssh/hosts/ssh_P1", 200, apigen.SSHHost{ID: "ssh_P1", Name: "web1"})
	f.json("PATCH /api/v1/ssh/hosts/ssh_G2", 200, apigen.SSHHost{ID: "ssh_G2", Name: "web1"})

	// null 与缺省区分：--no-jump、--compat default 发 null；--no-tags 发空数组
	out, _, err := runSsh(f, "", "set", "web1", "--project", "demo", "--no-jump", "--compat", "default", "--no-tags", "--disable", "--allow-automation=false")
	require.NoError(t, err)
	assert.Contains(t, out, "已更新主机 web1")
	r := lastReq(t, f)
	assert.Equal(t, "PATCH", r.Method)
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_P1", r.Path)
	assert.JSONEq(t, `{"jumpHostId":null,"compat":null,"tags":[],"enabled":false,"allowAutomation":false}`, string(r.Body))

	// 只发给出的字段；改用钥匙登录带 authType；跳板按全局作用域解析
	_, _, err = runSsh(f, "", "set", "web1", "--host", "10.0.0.3", "--port", "22", "--key", "deploy", "--jump", "bastion", "--tag", "a", "--name", "web2", "--compat", "off", "--enable")
	require.NoError(t, err)
	r = lastReq(t, f)
	assert.Equal(t, "/api/v1/ssh/hosts/ssh_G2", r.Path)
	assert.JSONEq(t, `{"address":"10.0.0.3","port":22,"authType":"key","keyId":"sshk_1","jumpHostId":"ssh_G1","tags":["a"],"name":"web2","compat":false,"enabled":true}`, string(r.Body))

	// 改为密码登录
	_, _, err = runSsh(f, "pw2\r\n", "set", "web1", "--password-stdin")
	require.NoError(t, err)
	assert.JSONEq(t, `{"authType":"password","password":"pw2"}`, string(lastReq(t, f).Body))
}

func TestSshTestExitCode(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	f.json("POST /api/v1/ssh/hosts/ssh_G1/test", 200, apigen.SSHTestResult{Ok: true, LatencyMs: 7})

	out, _, err := runSsh(f, "", "test", "bastion")
	require.NoError(t, err)
	assert.Contains(t, out, "测试 bastion：成功（7ms）")

	f.json("POST /api/v1/ssh/hosts/ssh_G1/test", 200, apigen.SSHTestResult{Ok: false, LatencyMs: 9, Error: new("登录 ops@10.0.0.1:22 失败：denied")})
	out, _, err = runSsh(f, "", "test", "bastion")
	var ec *exitCodeError
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 1, ec.code)
	assert.Contains(t, out, "失败（9ms）：登录 ops@10.0.0.1:22 失败：denied")

	body := `{"ok":false,"checkedAt":1,"latencyMs":2,  "error":"x"}`
	f.raw("POST /api/v1/ssh/hosts/ssh_G1/test", "application/json", body)
	out, _, err = runSsh(f, "", "test", "bastion", "--json")
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, 1, ec.code)
	assert.Equal(t, body+"\n", out)
}

func TestSshLs(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)

	out, _, err := runSsh(f, "", "ls")
	require.NoError(t, err)
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "LAST-TEST")
	assert.Regexp(t, `bastion\s+ops@10\.0\.0\.1:22\s+key:deploy\s+-\s+#edge\s+ok \S+ \S+`, out)
	assert.Regexp(t, `web1\s+root@10\.0\.0\.2:2222\s+password\s+bastion\s+disabled,forward\s+-`, out)
	assert.NotContains(t, out, "jump2")
	assert.NotContains(t, out, "SCOPE")

	out, _, err = runSsh(f, "", "ls", "--project", "demo")
	require.NoError(t, err)
	assert.Regexp(t, `web1\s+app@10\.1\.0\.2:22\s+key:deploy\s+-\s+automation\s+-`, out)
	assert.NotContains(t, out, "bastion")

	out, _, err = runSsh(f, "", "ls", "--all")
	require.NoError(t, err)
	assert.Contains(t, out, "SCOPE")
	assert.Regexp(t, `jump2\s.*\sdemo\n`, out)
	assert.Regexp(t, `bastion\s.*\sglobal\n`, out)

	out, _, err = runSsh(f, "", "ls", "--project", "demo", "--json")
	require.NoError(t, err)
	assert.Contains(t, out, `"ssh_P1"`)
	assert.NotContains(t, out, `"ssh_G1"`)
}

func TestSshKeys(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	f.json("POST /api/v1/ssh/keys", 201, apigen.SSHKey{ID: "sshk_2", Name: "ci", Type: "ssh-ed25519", PublicKey: "ssh-ed25519 BBBB ci", Fingerprint: "SHA256:ci"})
	f.json("PATCH /api/v1/ssh/keys/sshk_1", 200, apigen.SSHKey{ID: "sshk_1", Name: "deploy2"})
	f.json("DELETE /api/v1/ssh/keys/sshk_1", 204, nil)

	out, _, err := runSsh(f, "", "key", "ls")
	require.NoError(t, err)
	assert.Regexp(t, `NAME\s+TYPE\s+FINGERPRINT\s+HOSTS`, out)
	assert.Regexp(t, `deploy\s+ssh-ed25519\s+SHA256:abc\s+2`, out)

	out, errOut, err := runSsh(f, "", "key", "gen", "ci")
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519 BBBB ci\n", out)
	assert.Contains(t, errOut, "authorized_keys")
	assert.JSONEq(t, `{"name":"ci","mode":"generate"}`, string(lastReq(t, f).Body))

	dir := t.TempDir()
	pass := filepath.Join(dir, "pass")
	require.NoError(t, os.WriteFile(pass, []byte("hunter2\n"), 0o600))
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nxx\n-----END OPENSSH PRIVATE KEY-----\n"
	out, _, err = runSsh(f, pem, "key", "import", "ci", "-", "--passphrase-file", pass)
	require.NoError(t, err)
	assert.Contains(t, out, "已导入钥匙 ci")
	assert.JSONEq(t, `{"name":"ci","mode":"import","privateKey":`+jsonString(pem)+`,"passphrase":"hunter2"}`, string(lastReq(t, f).Body))

	keyFile := filepath.Join(dir, "id")
	require.NoError(t, os.WriteFile(keyFile, []byte(pem), 0o600))
	_, _, err = runSsh(f, "", "key", "import", "ci", keyFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"ci","mode":"import","privateKey":`+jsonString(pem)+`}`, string(lastReq(t, f).Body))

	_, _, err = runSsh(f, "", "key", "import", "ci", "-")
	requireUsageErr(t, err)

	out, _, err = runSsh(f, "", "key", "pub", "deploy")
	require.NoError(t, err)
	assert.Equal(t, "ssh-ed25519 AAAA deploy\n", out)

	_, _, err = runSsh(f, "", "key", "rename", "deploy", "deploy2")
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"deploy2"}`, string(lastReq(t, f).Body))

	_, _, err = runSsh(f, "", "key", "rm", "deploy")
	require.NoError(t, err)
	assert.Equal(t, "DELETE", lastReq(t, f).Method)

	_, _, err = runSsh(f, "", "key", "pub", "nope")
	assert.ErrorContains(t, err, "没有这把钥匙")
}

// jsonString 返回 s 的 JSON 字符串字面量。
func jsonString(s string) string {
	var b bytes.Buffer
	_ = printJSONTo(&b, s)
	return strings.TrimSpace(b.String())
}

func TestSshAudit(t *testing.T) {
	f := newFakeAPI(t)
	sshFixtures(f)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	f.json("GET /api/v1/ssh/audit", 200, apigen.SSHAuditList{
		Entries: []apigen.SSHAuditEntry{
			{ID: 9, At: at, Source: apigen.SSHAuditSourceOrb, HostID: "ssh_P1", HostName: "web1", Kind: apigen.SSHAuditExec, Detail: "echo hi;\nexit 3", ExitCode: new(3), Ok: false},
			{ID: 8, At: at, Source: apigen.SSHAuditSourceOrb, HostID: "ssh_G1", HostName: "bastion", Kind: apigen.SSHAuditDeny, Detail: strings.Repeat("长", 100), Ok: false},
			{ID: 7, At: at, Source: apigen.SSHAuditSourceOwner, HostID: "ssh_G1", HostName: "bastion", Kind: apigen.SSHAuditTest, Ok: true},
		},
		NextBefore: new(int64(7)),
	})

	out, errOut, err := runSsh(f, "", "audit", "--host", "web1", "--project", "demo", "--thread", "T-1", "--limit", "20", "--before", "100")
	require.NoError(t, err)
	r := lastReq(t, f)
	assert.Equal(t, "/api/v1/ssh/audit", r.Path)
	q, err := url.ParseQuery(r.RawQuery)
	require.NoError(t, err)
	assert.Equal(t, url.Values{"host": {"ssh_P1"}, "thread": {"T-1"}, "limit": {"20"}, "before": {"100"}}, q)
	assert.Regexp(t, `TIME\s+SOURCE\s+HOST\s+KIND\s+EXIT\s+DETAIL`, out)
	assert.Regexp(t, `orb\s+web1\s+exec\s+3\s+echo hi; exit 3\n`, out)
	assert.Regexp(t, `orb\s+bastion\s+deny\s+fail\s+长{79}…\n`, out)
	assert.Regexp(t, `owner\s+bastion\s+test\s+-\s+-\n`, out)
	assert.Contains(t, errOut, "--before 7")

	// 无 --project：全局优先；全局没有时取唯一同名项目主机；形如 ssh_ 的未知值按 id 使用
	_, _, err = runSsh(f, "", "audit", "--host", "web1")
	require.NoError(t, err)
	assert.Equal(t, "host=ssh_G2", lastReq(t, f).RawQuery)
	_, _, err = runSsh(f, "", "audit", "--host", "jump2")
	require.NoError(t, err)
	assert.Equal(t, "host=ssh_P2", lastReq(t, f).RawQuery)
	_, _, err = runSsh(f, "", "audit", "--host", "ssh_GONE")
	require.NoError(t, err)
	assert.Equal(t, "host=ssh_GONE", lastReq(t, f).RawQuery)
	_, _, err = runSsh(f, "", "audit", "--host", "nope")
	assert.ErrorContains(t, err, "没有这台主机")

	_, _, err = runSsh(f, "", "audit")
	require.NoError(t, err)
	assert.Empty(t, lastReq(t, f).RawQuery)

	body := `{"entries":[],   "nextBefore":3}`
	f.raw("GET /api/v1/ssh/audit", "application/json", body)
	out, _, err = runSsh(f, "", "audit", "--json")
	require.NoError(t, err)
	assert.Equal(t, body+"\n", out)
}
