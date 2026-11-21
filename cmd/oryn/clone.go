package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M24 阶段 ③：`oryn clone <项目>` 克隆 Oryn 托管仓库，并在新仓库里登记 `oryn git-credential` 作为该主机的凭据助手。

var cloneCmd = &cobra.Command{
	Use:   "clone <项目名|id> [目录]",
	Short: "克隆 Oryn 托管的项目仓库（用 oryn login 的设备令牌认证）",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cred, err := loadCredentials()
		if err != nil {
			return err
		}
		c, err := newClient()
		if err != nil {
			return err
		}
		dir := ""
		if len(args) == 2 {
			dir = args[1]
		}
		return orynClone(cmd.Context(), c, cred.URL, args[0], dir, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() { root.AddCommand(cloneCmd) }

// findProject 按名称或 id 查项目，含已归档项目（克隆归档项目照常可用，只是不能推送）。
func findProject(ctx context.Context, c *apiClient, name string) (apigen.Project, error) {
	for _, path := range []string{"/api/v1/projects", "/api/v1/projects?archived=true"} {
		var list apigen.ProjectList
		if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
			return apigen.Project{}, err
		}
		for _, p := range list.Projects {
			if p.Name == name || p.ID == name {
				return p, nil
			}
		}
	}
	return apigen.Project{}, fmt.Errorf("项目 %q 不存在", name)
}

// orynClone 克隆托管项目 name 到 dir（空 = 项目名）。credURL 是 oryn login 的服务器地址：克隆地址必须与它同源，
// 否则登记的凭据助手不会应答（它只把令牌交给登录的那台服务器）。
func orynClone(ctx context.Context, c *apiClient, credURL, name, dir string, stdout, stderr io.Writer) error {
	p, err := findProject(ctx, c, name)
	if err != nil {
		return err
	}
	pname := termText(p.Name)
	if p.Hosted == nil || !*p.Hosted {
		if p.RepoURL != nil && *p.RepoURL != "" {
			return usageErr("项目 %s 使用外部仓库，请直接运行：git clone %s", pname, termText(*p.RepoURL))
		}
		return usageErr("项目 %s 没有仓库", pname)
	}
	if p.CloneURL == nil || *p.CloneURL == "" {
		return fmt.Errorf("服务器没有给出项目 %s 的克隆地址", pname)
	}
	cloneURL := *p.CloneURL
	origin, ok := credentialOrigin(cloneURL)
	if !ok {
		return fmt.Errorf("克隆地址 %s 不是 http(s) 地址", termText(cloneURL))
	}
	if !sameOrigin(cloneURL, credURL) {
		return fmt.Errorf("克隆地址 %s 与登录的服务器 %s 不同，凭据助手不会应答：用 oryn login --url %s 重新登录",
			termText(cloneURL), termText(credURL), termText(origin))
	}
	if dir == "" {
		if !safeDirName(p.Name) {
			return usageErr("项目名 %q 不能直接作目录名：oryn clone %s <目录>", p.Name, p.ID)
		}
		dir = p.Name
	}
	cmd := exec.CommandContext(ctx, "git", cloneArgs(cloneURL, dir, orynHelper())...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return &exitCodeError{code: ee.ExitCode(), err: fmt.Errorf("git clone 失败（退出码 %d）", ee.ExitCode())}
		}
		return err
	}
	return nil
}

// safeDirName 报告项目名能否直接作为当前目录下的子目录名。
func safeDirName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && !strings.HasPrefix(name, "-")
}

// orynHelper 是登记给 git 的凭据助手命令：PATH 里的 oryn 就是本程序时写 `!oryn git-credential`（升级后路径变化也不坏），
// 否则写本程序的绝对路径。
func orynHelper() string {
	exe, err := os.Executable()
	if err != nil {
		return "!oryn git-credential"
	}
	if p, err := exec.LookPath("oryn"); err == nil {
		a, errA := os.Stat(p)
		b, errB := os.Stat(exe)
		if errA == nil && errB == nil && os.SameFile(a, b) {
			return "!oryn git-credential"
		}
	}
	return "!" + shellQuote(exe) + " git-credential"
}

// shellQuote 把 s 包成 POSIX shell 单引号字面量（git 用 sh 执行 `!` 开头的助手）。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// credentialOrigin 返回 http(s) 地址的 scheme://host[:port]（小写）；其它地址 ok=false。
func credentialOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Host), true
}

// sameOrigin 报告两个 http(s) 地址的 scheme 与 host[:port] 是否相同（不区分大小写）。
func sameOrigin(a, b string) bool {
	oa, okA := credentialOrigin(a)
	ob, okB := credentialOrigin(b)
	return okA && okB && oa == ob
}

// cloneArgs 是 `git clone` 的参数：先把该主机的助手列表清空（系统钥匙串之类的助手不会把令牌存走），
// 再登记 oryn 的助手；`-c` 写进新仓库的配置并在拉取前生效。
func cloneArgs(cloneURL, dir, helper string) []string {
	origin, _ := credentialOrigin(cloneURL)
	key := "credential." + origin + ".helper"
	return []string{"clone", "-c", key + "=", "-c", key + "=" + helper, "--", cloneURL, dir}
}
