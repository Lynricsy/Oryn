package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M24 阶段 ③：`oryn project create <名> --push` 把本机仓库建成托管项目：先检查本机仓库，再建一个空的托管仓库
// （emptyRepo，基线 = 当前分支），加远端、登记凭据助手，最后推上去。

// gitIn 在 dir 里执行 git 并返回去掉首尾空白的 stdout；失败时 err 是 *exec.ExitError（stderr 不带出）。
func gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// gitExitCode 取 git 的退出码；不是 ExitError 时为 -1。
func gitExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// projectPush 实现 --push。remote 是远端名（调用方已填缺省 origin），remoteGiven 表示用户显式给了 --remote。
// 本机检查有一项不过就不建项目；项目建好之后的失败保留项目（远端与助手已配好，重推即可）。
func projectPush(ctx context.Context, c *apiClient, credURL, dir string, req apigen.ProjectCreate, remote string, remoteGiven bool, stdout, stderr io.Writer) (apigen.Project, error) {
	var p apigen.Project
	if remote == "" || strings.HasPrefix(remote, "-") {
		return p, usageErr("远端名 %q 无效", remote)
	}
	if out, err := gitIn(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		return p, errors.New("当前目录不是 git 工作区：在本机仓库里运行 --push")
	}
	branch, err := gitIn(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch == "" {
		return p, errors.New("HEAD 处于分离状态：先 git switch 到要作为基线的分支")
	}
	if _, err := gitIn(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD"); err != nil {
		return p, fmt.Errorf("分支 %s 还没有提交：先提交一次", branch)
	}
	// 检查 3a：仓库用了 Git LFS 时本机必须有 git-lfs，否则托管库里只会剩指针
	usesLFS, err := repoUsesLFS(ctx, dir)
	if err != nil {
		return p, fmt.Errorf("git ls-files 失败：%w", err)
	}
	if usesLFS {
		if _, err := gitIn(ctx, dir, "lfs", "version"); err != nil {
			return p, errors.New("仓库里有 Git LFS 文件，但本机没有 git-lfs：先安装 git-lfs 再运行 --push")
		}
	}
	// 读配置原值而不是 get-url：后者会套用 insteadOf
	if u, err := gitIn(ctx, dir, "config", "--get", "remote."+remote+".url"); err == nil {
		if !remoteGiven {
			return p, fmt.Errorf("远端 %s 已指向 %s：用 --remote <名字> 换个远端名（如 --remote oryn）", remote, u)
		}
		return p, fmt.Errorf("远端 %s 已存在（%s）", remote, u)
	}

	req.Hosted, req.EmptyRepo, req.BaseBranch = new(true), new(true), new(branch)
	if err := c.do(ctx, http.MethodPost, "/api/v1/projects", req, &p); err != nil {
		return apigen.Project{}, err
	}
	name := termText(p.Name)
	cloneURL := ""
	if p.CloneURL != nil {
		cloneURL = *p.CloneURL
	}
	// 克隆地址来自服务器：不是 http(s) 的地址绝不交给 git（ext:: 这类地址等于在本机执行任意命令）
	origin, ok := credentialOrigin(cloneURL)
	if !ok || !sameOrigin(cloneURL, credURL) {
		fmt.Fprintf(stderr, "项目 %s（%s）已建好，克隆地址：%s\n", name, p.ID, termText(cloneURL))
		if !ok {
			return p, fmt.Errorf("克隆地址不是 http(s) 地址，没有添加远端")
		}
		return p, fmt.Errorf("克隆地址与登录的服务器 %s 不同，凭据助手不会应答：用 oryn login --url %s 重新登录后手工执行 git remote add %s %s && git push -u %s %s",
			termText(credURL), origin, remote, termText(cloneURL), remote, branch)
	}
	if _, err := gitIn(ctx, dir, "remote", "add", remote, cloneURL); err != nil {
		return p, fmt.Errorf("git remote add 失败；项目 %s（%s）已建好，仓库是空的。手工执行：git remote add %s %s && git push -u %s %s",
			name, p.ID, remote, cloneURL, remote, branch)
	}
	key := "credential." + origin + ".helper"
	if _, err := gitIn(ctx, dir, "config", "--local", "--unset-all", key); err != nil && gitExitCode(err) != 5 {
		return p, fmt.Errorf("git config --unset-all %s 失败：%w", key, err)
	}
	for _, v := range []string{"", orynHelper()} {
		if _, err := gitIn(ctx, dir, "config", "--local", "--add", key, v); err != nil {
			return p, fmt.Errorf("git config --add %s 失败：%w", key, err)
		}
	}
	// 显式上传 LFS 对象：pre-push 钩子只在装过 git-lfs 钩子的仓库里才有（用 core.hooksPath 的仓库会被跳过），
	// 缺了对象托管库就只剩指针。重复上传没有代价：batch 对已有对象不给 actions。
	if usesLFS {
		lfsPush := exec.CommandContext(ctx, "git", "-C", dir, "lfs", "push", "--all", remote, branch)
		lfsPush.Stdout, lfsPush.Stderr = stdout, stderr
		lfsPush.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if err := lfsPush.Run(); err != nil {
			return p, &exitCodeError{code: 1, err: fmt.Errorf("LFS 对象上传失败；项目 %s（%s）已建好，仓库是空的。排查后重跑：git lfs push --all %s %s && git push -u %s %s",
				name, p.ID, remote, branch, remote, branch)}
		}
	}
	push := exec.CommandContext(ctx, "git", "-C", dir, "push", "-u", remote, branch)
	push.Stdout, push.Stderr = stdout, stderr
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if err := push.Run(); err != nil {
		return p, &exitCodeError{code: 1, err: fmt.Errorf("推送失败；项目 %s（%s）已建好，仓库是空的。排查后重跑：git push -u %s %s",
			name, p.ID, remote, branch)}
	}
	return p, nil
}

// repoUsesLFS 报告工作区里是否有 filter=lfs 的已跟踪文件（attr pathspec，git 2.13 起）。
func repoUsesLFS(ctx context.Context, dir string) (bool, error) {
	out, err := gitIn(ctx, dir, "ls-files", "-z", "--", ":(attr:filter=lfs)")
	return out != "", err
}
