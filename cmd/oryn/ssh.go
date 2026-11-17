package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// `oryn ssh …`（M18）：管理控制面登记的 SSH 主机与钥匙，测试连接、重置指纹、查网关审计。
// 主机按名字定位：--project 给出作用域（缺省全局），只在该作用域里按名（或 id）匹配。

// newSshCmd 构造 `oryn ssh` 命令树；client 在测试时替换为假服务端。
func newSshCmd(client func() (*apiClient, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh",
		Short: "SSH 主机与钥匙：登记、测试、重置指纹、审计",
	}
	key := &cobra.Command{
		Use:   "key",
		Short: "SSH 钥匙：生成、导入、查看公钥、删除",
	}
	key.AddCommand(
		newSshKeyLsCmd(client),
		newSshKeyGenCmd(client),
		newSshKeyImportCmd(client),
		newSshKeyPubCmd(client),
		newSshKeyRenameCmd(client),
		newSshKeyRmCmd(client),
	)
	cmd.AddCommand(
		newSshLsCmd(client),
		newSshAddCmd(client),
		newSshSetCmd(client),
		newSshRmCmd(client),
		newSshTestCmd(client),
		newSshResetHostKeyCmd(client),
		key,
		newSshAuditCmd(client),
	)
	return cmd
}

func init() { root.AddCommand(newSshCmd(newClient)) }

// sshHostsPath 是主机集合的 REST 路径。
const sshHostsPath = "/api/v1/ssh/hosts"

// sshHostPath 返回单台主机（及其子资源 suffix）的路径。
func sshHostPath(id, suffix string) string { return sshHostsPath + "/" + escapePathParam(id) + suffix }

// sshListHosts 取全部主机（全局在前）。
func sshListHosts(ctx context.Context, c *apiClient) ([]apigen.SSHHost, error) {
	var list apigen.SSHHostList
	if err := c.do(ctx, http.MethodGet, sshHostsPath, nil, &list); err != nil {
		return nil, err
	}
	return list.Hosts, nil
}

// sshScope 把 --project 解析成项目 id；空串 = 全局。
func sshScope(ctx context.Context, c *apiClient, project string) (string, error) {
	if project == "" {
		return "", nil
	}
	return resolveProject(ctx, c, project)
}

// sshInScope 判定主机是否属于作用域 projectID（空串 = 全局）。
func sshInScope(h apigen.SSHHost, projectID string) bool {
	if h.ProjectID == nil {
		return projectID == ""
	}
	return *h.ProjectID == projectID
}

// sshFindHost 在作用域 projectID 里按名（或 id）找主机。
func sshFindHost(hosts []apigen.SSHHost, projectID, name string) (apigen.SSHHost, bool) {
	for _, h := range hosts {
		if sshInScope(h, projectID) && (h.Name == name || h.ID == name) {
			return h, true
		}
	}
	return apigen.SSHHost{}, false
}

// sshNoHost 是按名找不到主机时的错误。
func sshNoHost(name, project string) error {
	if project == "" {
		return fmt.Errorf("没有这台主机：%q（全局）", name)
	}
	return fmt.Errorf("没有这台主机：%q（项目 %s）", name, project)
}

// sshLocate 取全部主机并在 --project 作用域里定位 name；返回定位到的主机与全部主机（供跳板解析）。
func sshLocate(ctx context.Context, c *apiClient, project, name string) (apigen.SSHHost, []apigen.SSHHost, error) {
	pid, err := sshScope(ctx, c, project)
	if err != nil {
		return apigen.SSHHost{}, nil, err
	}
	hosts, err := sshListHosts(ctx, c)
	if err != nil {
		return apigen.SSHHost{}, nil, err
	}
	h, ok := sshFindHost(hosts, pid, name)
	if !ok {
		return apigen.SSHHost{}, nil, sshNoHost(name, project)
	}
	return h, hosts, nil
}

// sshResolveJump 按名解析跳板：项目主机先找同项目、再找全局；全局主机只找全局（与服务端的作用域规则一致）。
func sshResolveJump(hosts []apigen.SSHHost, projectID, name string) (string, error) {
	if projectID != "" {
		if h, ok := sshFindHost(hosts, projectID, name); ok {
			return h.ID, nil
		}
	}
	if h, ok := sshFindHost(hosts, "", name); ok {
		return h.ID, nil
	}
	return "", fmt.Errorf("跳板 %q 不存在（只能用全局主机或同项目的主机）", name)
}

// sshListKeys 取全部钥匙。
func sshListKeys(ctx context.Context, c *apiClient) ([]apigen.SSHKey, error) {
	var list apigen.SSHKeyList
	if err := c.do(ctx, http.MethodGet, "/api/v1/ssh/keys", nil, &list); err != nil {
		return nil, err
	}
	return list.Keys, nil
}

// sshResolveKey 按名（或 id）找钥匙。
func sshResolveKey(ctx context.Context, c *apiClient, name string) (apigen.SSHKey, error) {
	keys, err := sshListKeys(ctx, c)
	if err != nil {
		return apigen.SSHKey{}, err
	}
	for _, k := range keys {
		if k.Name == name || k.ID == name {
			return k, nil
		}
	}
	return apigen.SSHKey{}, fmt.Errorf("没有这把钥匙：%q", name)
}

// readSecret 读出 r 的全部内容（至多 64KiB）并去掉末尾换行；用于 --password-stdin 与 --passphrase-file。
func readSecret(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64<<10))
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// parseCompat 解析 --compat：on / off；allowDefault 时另认 default（= 不写进 ssh.json，返回 nil）。
func parseCompat(s string, allowDefault bool) (*bool, error) {
	switch strings.ToLower(s) {
	case "on":
		return new(true), nil
	case "off":
		return new(false), nil
	case "default":
		if allowDefault {
			return nil, nil
		}
	}
	if allowDefault {
		return nil, usageErr("--compat 只能是 on、off 或 default")
	}
	return nil, usageErr("--compat 只能是 on 或 off")
}

// sshHostFlags 是 add 与 set 共用的主机字段标志。
type sshHostFlags struct {
	project, address, user, key, jump, desc, compat string
	port                                            int
	tags                                            []string
	passwordStdin, allowAutomation, allowForward    bool
}

func (f *sshHostFlags) register(fl *pflag.FlagSet) {
	fl.StringVar(&f.project, "project", "", "主机所属项目（名称或 id）；缺省 = 全局")
	fl.StringVar(&f.address, "host", "", "目标机地址（主机名或 IP）")
	fl.StringVar(&f.user, "user", "", "登录用户")
	fl.IntVar(&f.port, "port", 22, "SSH 端口")
	fl.StringVar(&f.key, "key", "", "用这把钥匙登录（钥匙名或 id）")
	fl.BoolVar(&f.passwordStdin, "password-stdin", false, "用密码登录，密码从标准输入读取")
	fl.StringVar(&f.jump, "jump", "", "经这台主机跳转（主机名；项目主机可用同项目或全局主机）")
	fl.StringVar(&f.desc, "desc", "", "描述")
	fl.StringArrayVar(&f.tags, "tag", nil, "标签（可重复）")
	fl.BoolVar(&f.allowAutomation, "allow-automation", false, "允许全自动线程使用")
	fl.BoolVar(&f.allowForward, "allow-forward", false, "允许经网关做本地端口转发")
}

// authFlags 校验 --key 与 --password-stdin 的组合；required 时两者必须且只能给一个。
func (f *sshHostFlags) authFlags(required bool) error {
	if f.key != "" && f.passwordStdin {
		return usageErr("--key 与 --password-stdin 只能给一个")
	}
	if required && f.key == "" && !f.passwordStdin {
		return usageErr("必须给出 --key <钥匙名> 或 --password-stdin 之一")
	}
	return nil
}

func newSshLsCmd(client func() (*apiClient, error)) *cobra.Command {
	var project string
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "列出主机（缺省全局；--project 列该项目，--all 列全部）",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all && project != "" {
				return usageErr("--all 与 --project 只能给一个")
			}
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			pid, err := sshScope(ctx, c, project)
			if err != nil {
				return err
			}
			hosts, err := sshListHosts(ctx, c)
			if err != nil {
				return err
			}
			shown := make([]apigen.SSHHost, 0, len(hosts))
			for _, h := range hosts {
				if all || sshInScope(h, pid) {
					shown = append(shown, h)
				}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return printJSONTo(out, apigen.SSHHostList{Hosts: shown})
			}
			return printSshHosts(ctx, c, out, shown, hosts, all)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&project, "project", "", "只列该项目的主机（名称或 id）")
	fl.BoolVar(&all, "all", false, "列出全局与全部项目的主机")
	fl.BoolVar(&asJSON, "json", false, "输出 JSON（{hosts}）")
	return cmd
}

// printSshHosts 打印主机表；withScope 时追加 SCOPE 列（全局或项目名）。
func printSshHosts(ctx context.Context, c *apiClient, out io.Writer, shown, all []apigen.SSHHost, withScope bool) error {
	byID := make(map[string]string, len(all))
	for _, h := range all {
		byID[h.ID] = h.Name
	}
	var keyNames, projectNames map[string]string
	for _, h := range shown {
		if h.KeyID != nil && keyNames == nil {
			keys, err := sshListKeys(ctx, c)
			if err != nil {
				return err
			}
			keyNames = make(map[string]string, len(keys))
			for _, k := range keys {
				keyNames[k.ID] = k.Name
			}
		}
		if withScope && h.ProjectID != nil && projectNames == nil {
			var list apigen.ProjectList
			if err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &list); err != nil {
				return err
			}
			projectNames = make(map[string]string, len(list.Projects))
			for _, p := range list.Projects {
				projectNames[p.ID] = p.Name
			}
		}
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	head := "NAME\tTARGET\tAUTH\tJUMP\tFLAGS\tLAST-TEST"
	if withScope {
		head += "\tSCOPE"
	}
	fmt.Fprintln(tw, head)
	for _, h := range shown {
		auth := string(h.AuthType)
		if h.KeyID != nil {
			name := keyNames[*h.KeyID]
			if name == "" {
				name = *h.KeyID
			}
			auth += ":" + name
		}
		jump := "-"
		if h.JumpHostID != nil {
			if jump = byID[*h.JumpHostID]; jump == "" {
				jump = *h.JumpHostID
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s", termText(h.Name), sshTarget(h), termText(auth), termText(jump), sshHostFlagsText(h), sshLastTest(h.LastTest))
		if withScope {
			scope := "global"
			if h.ProjectID != nil {
				if scope = projectNames[*h.ProjectID]; scope == "" {
					scope = *h.ProjectID
				}
			}
			fmt.Fprintf(tw, "\t%s", termText(scope))
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// sshTarget 返回 `用户@地址:端口`。
func sshTarget(h apigen.SSHHost) string {
	return termText(h.Username + "@" + h.Address + ":" + strconv.Itoa(h.Port))
}

// sshHostFlagsText 把开关与标签拼成 FLAGS 列：disabled、automation、forward、compat=on|off、#标签。
func sshHostFlagsText(h apigen.SSHHost) string {
	var fs []string
	if !h.Enabled {
		fs = append(fs, "disabled")
	}
	if h.AllowAutomation {
		fs = append(fs, "automation")
	}
	if h.AllowForward {
		fs = append(fs, "forward")
	}
	if h.Compat != nil {
		if *h.Compat {
			fs = append(fs, "compat=on")
		} else {
			fs = append(fs, "compat=off")
		}
	}
	for _, t := range h.Tags {
		fs = append(fs, "#"+t)
	}
	if len(fs) == 0 {
		return "-"
	}
	return termText(strings.Join(fs, ","))
}

// sshLastTest 返回 LAST-TEST 列：`ok <时间>` / `fail <时间>` / `-`。
func sshLastTest(t *apigen.SSHHostTest) string {
	if t == nil {
		return "-"
	}
	if t.Ok {
		return "ok " + fmtTime(t.At)
	}
	return "fail " + fmtTime(t.At)
}

func newSshAddCmd(client func() (*apiClient, error)) *cobra.Command {
	var f sshHostFlags
	var compat string
	var disabled bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "登记主机，随后立即测试一次连接",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.authFlags(true); err != nil {
				return err
			}
			if f.address == "" || f.user == "" {
				return usageErr("--host 与 --user 必填")
			}
			req := apigen.SSHHostCreate{Name: args[0], Address: f.address, Username: f.user}
			fl := cmd.Flags()
			if fl.Changed("compat") {
				v, err := parseCompat(compat, false)
				if err != nil {
					return err
				}
				req.Compat = v
			}
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			pid, err := sshScope(ctx, c, f.project)
			if err != nil {
				return err
			}
			if pid != "" {
				req.ProjectID = &pid
			}
			if fl.Changed("port") {
				req.Port = &f.port
			}
			if f.key != "" {
				k, err := sshResolveKey(ctx, c, f.key)
				if err != nil {
					return err
				}
				req.AuthType, req.KeyID = apigen.SSHAuthKey, &k.ID
			} else {
				pw, err := readSecret(cmd.InOrStdin())
				if err != nil {
					return err
				}
				if pw == "" {
					return usageErr("--password-stdin：标准输入里没有密码")
				}
				req.AuthType, req.Password = apigen.SSHAuthPassword, &pw
			}
			if f.jump != "" {
				hosts, err := sshListHosts(ctx, c)
				if err != nil {
					return err
				}
				id, err := sshResolveJump(hosts, pid, f.jump)
				if err != nil {
					return err
				}
				req.JumpHostID = &id
			}
			if fl.Changed("desc") {
				req.Description = &f.desc
			}
			if len(f.tags) > 0 {
				req.Tags = f.tags
			}
			if fl.Changed("allow-automation") {
				req.AllowAutomation = &f.allowAutomation
			}
			if fl.Changed("allow-forward") {
				req.AllowForward = &f.allowForward
			}
			if disabled {
				req.Enabled = new(false)
			}
			var h apigen.SSHHost
			if err := c.do(ctx, http.MethodPost, sshHostsPath, req, &h); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "已添加主机 %s（%s）\n", termText(h.Name), h.ID)
			var res apigen.SSHTestResult
			if err := c.do(ctx, http.MethodPost, sshHostPath(h.ID, "/test"), nil, &res); err != nil {
				return fmt.Errorf("主机已添加，但测试请求失败：%w", err)
			}
			printSshTest(out, h.Name, res)
			return nil
		},
	}
	fl := cmd.Flags()
	f.register(fl)
	fl.StringVar(&compat, "compat", "", "Core 的 compat 开关：on 或 off；缺省不写")
	fl.BoolVar(&disabled, "disabled", false, "登记为停用")
	return cmd
}

func newSshSetCmd(client func() (*apiClient, error)) *cobra.Command {
	var f sshHostFlags
	var name, compat string
	var enable, disable, noJump, noTags bool
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "修改主机（只发送给出的字段）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := f.authFlags(false); err != nil {
				return err
			}
			if enable && disable {
				return usageErr("--enable 与 --disable 只能给一个")
			}
			if noJump && f.jump != "" {
				return usageErr("--jump 与 --no-jump 只能给一个")
			}
			if noTags && len(f.tags) > 0 {
				return usageErr("--tag 与 --no-tags 只能给一个")
			}
			fl := cmd.Flags()
			var upd apigen.SSHHostUpdate
			// null 值（取消跳板、compat 回到缺省）在 SSHHostUpdate 里表达不了，单独拼进请求体
			nulls := map[string]bool{}
			if fl.Changed("compat") {
				v, err := parseCompat(compat, true)
				if err != nil {
					return err
				}
				if v == nil {
					nulls["compat"] = true
				}
				upd.Compat = v
			}
			if fl.Changed("name") {
				upd.Name = &name
			}
			if fl.Changed("host") {
				upd.Address = &f.address
			}
			if fl.Changed("user") {
				upd.Username = &f.user
			}
			if fl.Changed("port") {
				upd.Port = &f.port
			}
			if fl.Changed("desc") {
				upd.Description = &f.desc
			}
			if len(f.tags) > 0 {
				upd.Tags = f.tags
			}
			if fl.Changed("allow-automation") {
				upd.AllowAutomation = &f.allowAutomation
			}
			if fl.Changed("allow-forward") {
				upd.AllowForward = &f.allowForward
			}
			if enable || disable {
				upd.Enabled = new(enable)
			}
			if noJump {
				nulls["jumpHostId"] = true
			}
			changed := 0
			fl.Visit(func(p *pflag.Flag) {
				if p.Name != "project" {
					changed++
				}
			})
			if changed == 0 {
				return usageErr("没有要修改的字段")
			}
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			h, hosts, err := sshLocate(ctx, c, f.project, args[0])
			if err != nil {
				return err
			}
			if f.key != "" {
				k, err := sshResolveKey(ctx, c, f.key)
				if err != nil {
					return err
				}
				upd.AuthType, upd.KeyID = new(apigen.SSHAuthKey), &k.ID
			}
			if f.passwordStdin {
				pw, err := readSecret(cmd.InOrStdin())
				if err != nil {
					return err
				}
				if pw == "" {
					return usageErr("--password-stdin：标准输入里没有密码")
				}
				upd.AuthType, upd.Password = new(apigen.SSHAuthPassword), &pw
			}
			if f.jump != "" {
				pid := ""
				if h.ProjectID != nil {
					pid = *h.ProjectID
				}
				id, err := sshResolveJump(hosts, pid, f.jump)
				if err != nil {
					return err
				}
				upd.JumpHostID = &id
			}
			body, err := sshPatchBody(upd, nulls, noTags)
			if err != nil {
				return err
			}
			var res apigen.SSHHost
			if err := c.do(ctx, http.MethodPatch, sshHostPath(h.ID, ""), body, &res); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已更新主机 %s\n", termText(res.Name))
			return err
		},
	}
	fl := cmd.Flags()
	f.register(fl)
	fl.StringVar(&name, "name", "", "改名")
	fl.StringVar(&compat, "compat", "", "Core 的 compat 开关：on、off 或 default（不写进 ssh.json）")
	fl.BoolVar(&enable, "enable", false, "启用")
	fl.BoolVar(&disable, "disable", false, "停用")
	fl.BoolVar(&noJump, "no-jump", false, "取消跳板")
	fl.BoolVar(&noTags, "no-tags", false, "清空标签")
	return cmd
}

// sshPatchBody 把 upd 编码成 JSON 对象，再补上 nulls 里的 null 字段；clearTags 时写 `"tags": []`。
func sshPatchBody(upd apigen.SSHHostUpdate, nulls map[string]bool, clearTags bool) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(upd)
	if err != nil {
		return nil, err
	}
	body := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, err
	}
	for k := range nulls {
		body[k] = json.RawMessage("null")
	}
	if clearTags {
		body["tags"] = json.RawMessage("[]")
	}
	return body, nil
}

func newSshRmCmd(client func() (*apiClient, error)) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "删除主机",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			h, _, err := sshLocate(ctx, c, project, args[0])
			if err != nil {
				return err
			}
			if err := c.do(ctx, http.MethodDelete, sshHostPath(h.ID, ""), nil, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已删除主机 %s\n", termText(h.Name))
			return err
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "主机所属项目（名称或 id）；缺省 = 全局")
	return cmd
}

func newSshTestCmd(client func() (*apiClient, error)) *cobra.Command {
	var project string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "test <name>",
		Short: "测试连接并采一次状态快照；失败时退出码 1",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			h, _, err := sshLocate(ctx, c, project, args[0])
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := c.do(ctx, http.MethodPost, sshHostPath(h.ID, "/test"), nil, &raw); err != nil {
				return err
			}
			var res apigen.SSHTestResult
			if err := json.Unmarshal(raw, &res); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				if err := writeRawJSON(out, raw); err != nil {
					return err
				}
			} else {
				printSshTest(out, h.Name, res)
			}
			if !res.Ok {
				return &exitCodeError{code: 1, err: fmt.Errorf("主机 %q 测试未通过", h.Name)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "主机所属项目（名称或 id）；缺省 = 全局")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}

// printSshTest 打印一次测试结果：首行成败与延迟，其后指纹与快照。
func printSshTest(w io.Writer, name string, r apigen.SSHTestResult) {
	name = termText(name)
	if !r.Ok {
		msg := "未知错误"
		if r.Error != nil && *r.Error != "" {
			msg = termText(*r.Error)
		}
		fmt.Fprintf(w, "测试 %s：失败（%dms）：%s\n", name, r.LatencyMs, msg)
		return
	}
	line := fmt.Sprintf("测试 %s：成功（%dms）", name, r.LatencyMs)
	if r.Os != nil && *r.Os != "" {
		line += "，系统 " + termText(*r.Os)
	}
	fmt.Fprintln(w, line)
	if r.HostKeyFingerprint != nil {
		fmt.Fprintf(w, "  指纹 %s\n", termText(*r.HostKeyFingerprint))
	}
	s := r.Snapshot
	if s == nil {
		return
	}
	var parts []string
	if s.Load1 != nil {
		parts = append(parts, fmt.Sprintf("负载 %.2f", *s.Load1))
	}
	if s.CPUPct != nil {
		parts = append(parts, fmt.Sprintf("CPU %.1f%%", *s.CPUPct))
	}
	if s.MemUsedKb != nil && s.MemTotalKb != nil {
		parts = append(parts, "内存 "+fmtKB(*s.MemUsedKb)+" / "+fmtKB(*s.MemTotalKb))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "  %s\n", strings.Join(parts, "  "))
	}
	for _, d := range s.Disks {
		pct := ""
		if d.TotalKb > 0 {
			pct = fmt.Sprintf("（%d%%）", d.UsedKb*100/d.TotalKb)
		}
		fmt.Fprintf(w, "  磁盘 %s  %s / %s%s\n", termText(d.Mount), fmtKB(d.UsedKb), fmtKB(d.TotalKb), pct)
	}
}

// fmtKB 把 KiB 数格式化为 MiB / GiB（一位小数）。
func fmtKB(kb int64) string {
	if kb >= 1<<20 {
		return fmt.Sprintf("%.1f GiB", float64(kb)/(1<<20))
	}
	return fmt.Sprintf("%.1f MiB", float64(kb)/(1<<10))
}

func newSshResetHostKeyCmd(client func() (*apiClient, error)) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "reset-host-key <name>",
		Short: "清除已固定的主机指纹；下次成功连接时重新固定",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			h, _, err := sshLocate(ctx, c, project, args[0])
			if err != nil {
				return err
			}
			if err := c.do(ctx, http.MethodPost, sshHostPath(h.ID, "/reset-host-key"), nil, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已重置主机 %s 的指纹；下次连接时重新固定\n", termText(h.Name))
			return err
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "主机所属项目（名称或 id）；缺省 = 全局")
	return cmd
}

func newSshKeyLsCmd(client func() (*apiClient, error)) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "列出钥匙",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			var raw json.RawMessage
			if err := c.do(cmd.Context(), http.MethodGet, "/api/v1/ssh/keys", nil, &raw); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeRawJSON(out, raw)
			}
			var list apigen.SSHKeyList
			if err := json.Unmarshal(raw, &list); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tFINGERPRINT\tHOSTS")
			for _, k := range list.Keys {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", termText(k.Name), termText(k.Type), termText(k.Fingerprint), k.HostCount)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}

func newSshKeyGenCmd(client func() (*apiClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "gen <name>",
		Short: "生成 ed25519 钥匙，把公钥打印到标准输出",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			var k apigen.SSHKey
			req := apigen.SSHKeyCreate{Name: args[0], Mode: apigen.SSHKeyCreateGenerate}
			if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/ssh/keys", req, &k); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), k.PublicKey); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "把这一行加到目标机的 ~/.ssh/authorized_keys")
			return nil
		},
	}
}

func newSshKeyImportCmd(client func() (*apiClient, error)) *cobra.Command {
	var passFile string
	cmd := &cobra.Command{
		Use:   "import <name> <file|->",
		Short: "导入私钥（带口令的私钥解开后以无口令形式加密保存，口令不落库）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var pem []byte
			var err error
			if args[1] == "-" {
				pem, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 1<<20))
			} else {
				pem, err = os.ReadFile(args[1])
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(pem)) == "" {
				return usageErr("私钥内容为空")
			}
			req := apigen.SSHKeyCreate{Name: args[0], Mode: apigen.SSHKeyCreateImport, PrivateKey: new(string(pem))}
			if passFile != "" {
				fh, err := os.Open(passFile)
				if err != nil {
					return err
				}
				pass, err := readSecret(fh)
				fh.Close()
				if err != nil {
					return err
				}
				req.Passphrase = &pass
			}
			c, err := client()
			if err != nil {
				return err
			}
			var k apigen.SSHKey
			if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/ssh/keys", req, &k); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已导入钥匙 %s（%s %s）\n", termText(k.Name), termText(k.Type), termText(k.Fingerprint))
			return err
		},
	}
	cmd.Flags().StringVar(&passFile, "passphrase-file", "", "从该文件读取私钥口令")
	return cmd
}

func newSshKeyPubCmd(client func() (*apiClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "pub <name>",
		Short: "打印钥匙的公钥（authorized_keys 一行）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			k, err := sshResolveKey(cmd.Context(), c, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), k.PublicKey)
			return err
		},
	}
}

func newSshKeyRenameCmd(client func() (*apiClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name> <new-name>",
		Short: "重命名钥匙（公钥注释随之改变）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			k, err := sshResolveKey(ctx, c, args[0])
			if err != nil {
				return err
			}
			var res apigen.SSHKey
			if err := c.do(ctx, http.MethodPatch, "/api/v1/ssh/keys/"+escapePathParam(k.ID), apigen.SSHKeyRename{Name: args[1]}, &res); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已重命名为 %s\n", termText(res.Name))
			return err
		},
	}
}

func newSshKeyRmCmd(client func() (*apiClient, error)) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <name>",
		Short: "删除钥匙（仍有主机使用时被拒绝）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			k, err := sshResolveKey(ctx, c, args[0])
			if err != nil {
				return err
			}
			if err := c.do(ctx, http.MethodDelete, "/api/v1/ssh/keys/"+escapePathParam(k.ID), nil, nil); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "已删除钥匙 %s\n", termText(k.Name))
			return err
		},
	}
}

// sshAuditHost 把 --host 解析成主机 id：给了 --project 时只在该作用域找；否则先找全局，再找唯一同名的项目主机；
// 都找不到而形如 `ssh_…` 时按 id 原样使用（已删除的主机仍有审计）。
func sshAuditHost(ctx context.Context, c *apiClient, project, name string) (string, error) {
	pid, err := sshScope(ctx, c, project)
	if err != nil {
		return "", err
	}
	hosts, err := sshListHosts(ctx, c)
	if err != nil {
		return "", err
	}
	if h, ok := sshFindHost(hosts, pid, name); ok {
		return h.ID, nil
	}
	if project == "" {
		var ids []string
		for _, h := range hosts {
			if h.Name == name {
				ids = append(ids, h.ID)
			}
		}
		switch {
		case len(ids) == 1:
			return ids[0], nil
		case len(ids) > 1:
			return "", usageErr("有多台名为 %q 的项目主机，请用 --project 指定", name)
		}
	}
	if strings.HasPrefix(name, "ssh_") {
		return name, nil
	}
	return "", sshNoHost(name, project)
}

// sshDetailWidth 是审计表 DETAIL 列的最大字符数。
const sshDetailWidth = 80

func newSshAuditCmd(client func() (*apiClient, error)) *cobra.Command {
	var host, project, thread string
	var limit int
	var before int64
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "查询 SSH 审计（网关连接与命令、测试、终端、文件操作；按时间倒序，保留 30 天）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if project != "" && host == "" {
				return usageErr("--project 只用于配合 --host 定位主机")
			}
			q := url.Values{}
			if thread != "" {
				q.Set("thread", thread)
			}
			if cmd.Flags().Changed("limit") {
				if limit < 1 || limit > 200 {
					return usageErr("--limit 须在 1–200 之间")
				}
				q.Set("limit", strconv.Itoa(limit))
			}
			if cmd.Flags().Changed("before") {
				q.Set("before", strconv.FormatInt(before, 10))
			}
			ctx := cmd.Context()
			c, err := client()
			if err != nil {
				return err
			}
			if host != "" {
				id, err := sshAuditHost(ctx, c, project, host)
				if err != nil {
					return err
				}
				q.Set("host", id)
			}
			path := "/api/v1/ssh/audit"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var raw json.RawMessage
			if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeRawJSON(out, raw)
			}
			var list apigen.SSHAuditList
			if err := json.Unmarshal(raw, &list); err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TIME\tSOURCE\tHOST\tKIND\tEXIT\tDETAIL")
			for _, e := range list.Entries {
				exit := "-"
				switch {
				case e.ExitCode != nil:
					exit = strconv.Itoa(*e.ExitCode)
				case !e.Ok:
					exit = "fail"
				}
				detail := termText(e.Detail)
				if r := []rune(detail); len(r) > sshDetailWidth {
					detail = string(r[:sshDetailWidth-1]) + "…"
				}
				if detail == "" {
					detail = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", fmtTime(e.At), e.Source, termText(e.HostName), e.Kind, exit, detail)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if list.NextBefore != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "还有更早的记录：--before %d\n", *list.NextBefore)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&host, "host", "", "只看该主机（主机名或 id）")
	fl.StringVar(&project, "project", "", "--host 所在项目（名称或 id）；缺省先找全局主机")
	fl.StringVar(&thread, "thread", "", "只看该线程（T-…）")
	fl.IntVar(&limit, "limit", 50, "最多返回条数（1–200）")
	fl.Int64Var(&before, "before", 0, "只返回 id 小于该值的记录（分页游标）")
	fl.BoolVar(&asJSON, "json", false, "输出原始 JSON")
	return cmd
}
