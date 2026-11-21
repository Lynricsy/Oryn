package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/boxcfg"
)

// `oryn boxes deploy`（M23）：在线程的 orb 里把仓库的 HEAD 提交构建、打包成 release，经控制面送上 box。
// 外部命令用镜像自带的 git、rsync、tar。

type deployOpts struct {
	Target                 string
	On, Name               string
	Create                 bool
	Provider, Type         string
	Private, Public        bool
	Move, DiscardBoxChange bool
}

func newBoxDeployCmd(api func() (boxAPI, error)) *cobra.Command {
	var o deployOpts
	c := &cobra.Command{
		Use:   "deploy [target] [--on <box>] [--name <app>]",
		Short: "把当前仓库（HEAD 提交）部署到 box（只在线程的 orb 里可用）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.Target = args[0]
			}
			if o.Private && o.Public {
				return usageErr("--private and --public are mutually exclusive")
			}
			a, err := api()
			if err != nil {
				return err
			}
			if !a.inOrb() {
				return usageErr("boxes deploy runs inside a thread's environment; ask the agent in a thread to deploy")
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDeploy(ctx, a, o, cwd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	f := c.Flags()
	f.StringVar(&o.On, "on", "", "目标 box（名字或 id）")
	f.StringVar(&o.Name, "name", "", "app 名（同时是地址 <app>.<portal_domain>）")
	f.BoolVar(&o.Create, "create", false, "box 不存在时新建")
	f.StringVar(&o.Provider, "provider", "", "新建 box 时的 provider")
	f.StringVar(&o.Type, "type", "", "新建 box 时的 orb 类型")
	f.BoolVar(&o.Private, "private", false, "只允许主人登录后访问")
	f.BoolVar(&o.Public, "public", false, "任何人都能访问（缺省）")
	f.BoolVar(&o.Move, "move", false, "把 app 绑定到当前仓库 / 目录")
	f.BoolVar(&o.DiscardBoxChange, "discard-box-changes", false, "丢弃 box 上 live release 里未推送的改动")
	return c
}

// deployTarget 是解析后的目标：root 相对仓库根，public 为 nil = 新 app 公开、已有 app 不变。
type deployTarget struct {
	key, on, name, root string
	public              *bool
}

// resolveTarget 由位置参数 / 当前目录与 box-deploy.json、命令行参数定出部署目标。
func resolveTarget(repoRoot, cwd string, o deployOpts) (deployTarget, error) {
	targets, err := boxcfg.LoadTargets(repoRoot)
	if err != nil {
		return deployTarget{}, err
	}
	var t boxcfg.Target
	var root string
	if o.Target != "" {
		var ok bool
		t, ok = targets.Targets[o.Target]
		if !ok {
			names := make([]string, 0, len(targets.Targets))
			for k := range targets.Targets {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return deployTarget{}, usageErr("no target %q: %s has no targets yet", o.Target, boxcfg.TargetsFile)
			}
			return deployTarget{}, usageErr("no target %q in %s (have: %s)", o.Target, boxcfg.TargetsFile, strings.Join(names, ", "))
		}
		root = t.Root
	} else {
		root, _, err = boxcfg.Find(repoRoot, cwd)
		if errors.Is(err, boxcfg.ErrNotFound) {
			return deployTarget{}, usageErr("no .oryn/box.yaml found between here and the repository root; see the oryn-boxes skill")
		}
		if err != nil {
			return deployTarget{}, err
		}
		for _, v := range targets.Targets {
			if v.Root == root {
				t = v
				break
			}
		}
	}
	out := deployTarget{key: boxcfg.TargetKey(root), on: t.On, name: t.Name, root: root}
	if o.Target != "" {
		out.key = o.Target
	}
	if o.On != "" {
		out.on = o.On
	}
	if o.Name != "" {
		out.name = o.Name
	}
	if out.on == "" || out.name == "" {
		return deployTarget{}, usageErr("the first deploy needs --on <box> --name <app>")
	}
	switch {
	case o.Private:
		out.public = new(false)
	case o.Public:
		out.public = new(true)
	case t.Private:
		out.public = new(false)
	}
	return out, nil
}

// expandHome 把 `~/…` 换成 $HOME 下的绝对路径。
func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")), nil
	}
	return p, nil
}

// runCmd 在 dir 里跑命令，输出直通 stdout / stderr。
func runCmd(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if env != nil {
		cmd.Env = env
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// runDeploy 是 deploy 的全过程；prepare 之后任一步失败或收到信号都先取消部署再返回。
func runDeploy(ctx context.Context, a boxAPI, o deployOpts, cwd string, stdout, stderr io.Writer) (retErr error) {
	repoRoot, err := gitOut(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return usageErr("not a git checkout")
	}
	tgt, err := resolveTarget(repoRoot, cwd, o)
	if err != nil {
		return err
	}
	commit, err := gitOut(ctx, repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("the repository has no commits yet: %w", err)
	}
	branch, err := gitOut(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	origin, _ := gitOut(ctx, repoRoot, "config", "--get", "remote.origin.url")
	repo := ""
	if origin != "" {
		repo = boxcfg.NormalizeRepo(origin)
	} else {
		roots, err := gitOut(ctx, repoRoot, "rev-list", "--max-parents=0", "HEAD")
		if err != nil {
			return err
		}
		lines := strings.Split(roots, "\n")
		repo = "local:" + lines[len(lines)-1]
	}
	// 配置取 HEAD 提交里的版本：部署的是 HEAD，不是工作区
	cfgPath := path.Join(tgt.root, ".oryn/box.yaml")
	data, err := gitOut(ctx, repoRoot, "show", "HEAD:"+cfgPath)
	if err != nil {
		cfgPath = path.Join(tgt.root, ".amp/box.yaml")
		if data, err = gitOut(ctx, repoRoot, "show", "HEAD:"+cfgPath); err != nil {
			return usageErr("%s is not committed; commit it first (deploys build the HEAD commit)", path.Join(tgt.root, ".oryn/box.yaml"))
		}
	}
	cfg, warnings, err := boxcfg.Parse([]byte(data + "\n"))
	if err != nil {
		return usageErr("%v", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "oryn:", w)
	}
	includes, err := boxcfg.IncludeDirs(tgt.root, cfg.Include)
	if err != nil {
		return usageErr("box.yaml: %v", err)
	}

	pp := boxPrepareParams{
		Box: tgt.on, App: tgt.name, Create: o.Create, Provider: o.Provider, Type: o.Type,
		Repo: repo, Root: tgt.root, Branch: branch, Commit: commit, Source: cfg.Source, Move: o.Move, Public: tgt.public,
	}
	var prep boxPrepareResult
	if err := a.call(ctx, "deploy.prepare", toMap(pp), &prep); err != nil {
		return err
	}
	cancelOnExit := true
	defer func() {
		if retErr != nil && cancelOnExit {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = a.call(cctx, "deploy.cancel", map[string]any{"box": prep.Box, "app": prep.App}, nil)
		}
	}()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cacheDir := filepath.Join(home, ".cache", "oryn", "box-build")
	archive, err := expandHome(prep.ArchivePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		return err
	}
	defer os.Remove(archive)

	// 构建目录：工作区干净时就地构建（复用已装的依赖与缓存），否则在临时 worktree 里构建 HEAD
	buildDir := repoRoot
	status, err := gitOut(ctx, repoRoot, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		wt := filepath.Join(cacheDir, prep.DeployID)
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "uncommitted changes are not deployed; building commit %s in a clean worktree\n", shortSHA(commit))
		if err := runCmd(ctx, repoRoot, nil, io.Discard, stderr, "git", "worktree", "add", "--detach", wt, "HEAD"); err != nil {
			return err
		}
		defer func() {
			_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wt).Run()
			_ = os.RemoveAll(wt)
		}()
		buildDir = wt
	}
	appDir := filepath.Join(buildDir, filepath.FromSlash(tgt.root))

	if strings.TrimSpace(cfg.Build) != "" {
		env := os.Environ()
		for k, v := range prep.Env {
			env = append(env, k+"="+v)
		}
		fmt.Fprintf(stderr, "building %s…\n", prep.App)
		if err := runCmd(ctx, appDir, env, stdout, stderr, "bash", "-lc", cfg.Build); err != nil {
			return fmt.Errorf("build failed: %w", err)
		}
	}

	services := make([]apigen.OrbBoxService, 0, len(cfg.Services))
	if !cfg.Source {
		args := append([]string{"-C", appDir, "-czf", archive}, cfg.Artifacts...)
		if err := runCmd(ctx, buildDir, nil, io.Discard, stderr, "tar", args...); err != nil {
			return fmt.Errorf("packing artifacts: %w", err)
		}
		for _, s := range cfg.Services {
			services = append(services, apigen.OrbBoxService{Name: s.Name, Command: s.Command, Cwd: s.Cwd})
		}
	} else {
		stage := filepath.Join(cacheDir, prep.DeployID+"-stage")
		defer os.RemoveAll(stage)
		if err := stageSource(ctx, buildDir, stage, origin, branch, commit, tgt.root, includes, stderr); err != nil {
			return err
		}
		if err := runCmd(ctx, stage, nil, io.Discard, stderr, "tar", "-C", stage, "-czf", archive, "."); err != nil {
			return fmt.Errorf("packing the release: %w", err)
		}
		for _, s := range cfg.Services {
			services = append(services, apigen.OrbBoxService{Name: s.Name, Command: s.Command, Cwd: cleanJoin(tgt.root, s.Cwd)})
		}
	}

	size, sum, err := fileDigest(archive)
	if err != nil {
		return err
	}
	if prep.MaxBytes > 0 && size > prep.MaxBytes {
		return &boxErr{Code: "release_too_large", Message: fmt.Sprintf(
			"the release is %d MiB, the limit is %d MiB; use source: false with artifacts to ship only the build output", size>>20, prep.MaxBytes>>20)}
	}

	fmt.Fprintf(stderr, "uploading %s (%d KiB) to %s…\n", shortSHA(commit), (size+1023)>>10, prep.Box)
	var res boxCommitResult
	err = a.call(ctx, "deploy.commit", toMap(boxCommitParams{
		DeployID: prep.DeployID, SHA256: sum, SizeBytes: size, Services: services, DiscardBoxChanges: o.DiscardBoxChange,
	}), &res)
	if err != nil {
		return err
	}
	if !res.OK {
		fmt.Fprintf(stderr, "%s: %s\n", res.Code, res.Message)
		if res.Log != "" {
			fmt.Fprintln(stderr, strings.TrimRight(res.Log, "\n"))
		}
		switch {
		case res.Code == "activate_unknown":
			cancelOnExit = false
			fmt.Fprintf(stderr, "the box did not answer; check oryn boxes deployments %s in a minute\n", prep.Box)
		case o.Move && res.Code == "box_changes":
			fmt.Fprintln(stderr, "--move to a different repository cannot be checked against the code on the box; add --discard-box-changes once nothing there needs saving")
		}
		return &exitCodeError{code: 1, err: fmt.Errorf("deploy of %s failed", prep.App)}
	}

	targets, err := boxcfg.LoadTargets(repoRoot)
	if err == nil {
		private := tgt.public != nil && !*tgt.public
		targets.Targets[tgt.key] = boxcfg.Target{On: prep.Box, Name: prep.App, Root: tgt.root, Private: private}
		err = boxcfg.SaveTargets(repoRoot, targets)
	}
	if err != nil {
		fmt.Fprintf(stderr, "oryn: %s not saved: %v\n", boxcfg.TargetsFile, err)
	}
	url := res.URL
	if url == "" {
		url = "(no URL: portal_domain is not configured)"
	}
	fmt.Fprintf(stdout, "deployed %s to %s: %s\n", prep.App, prep.Box, url)
	return nil
}

// cleanJoin 把 app 目录里的相对路径换成相对 release 根（= 仓库根）的路径；"" = 根。
func cleanJoin(root, rel string) string {
	p := path.Join(root, rel)
	if p == "." {
		return ""
	}
	return p
}

// stageSource 组带源码的 release：从构建目录克隆出 HEAD（root 非空时只检出 root、根目录文件与 include），
// origin 指回原远端，再把构建目录里已装的依赖与构建产物叠上去（不覆盖已检出的文件）。
func stageSource(ctx context.Context, buildDir, stage, origin, branch, commit, root string, includes []string, stderr io.Writer) error {
	_ = os.RemoveAll(stage)
	if err := os.MkdirAll(filepath.Dir(stage), 0o755); err != nil {
		return err
	}
	q := io.Discard
	if err := runCmd(ctx, "", nil, q, stderr, "git", "clone", "--quiet", "--no-checkout", buildDir, stage); err != nil {
		return err
	}
	git := func(args ...string) error { return runCmd(ctx, stage, nil, q, stderr, "git", args...) }
	if origin != "" {
		if err := git("remote", "set-url", "origin", origin); err != nil {
			return err
		}
	} else if err := git("remote", "remove", "origin"); err != nil {
		return err
	}
	dirs := []string{root}
	if root != "" {
		patterns := []string{"/*", "!/*/", "/" + root + "/"}
		for _, inc := range includes {
			patterns = append(patterns, "/"+inc+"/")
		}
		if err := git(append([]string{"sparse-checkout", "set", "--no-cone"}, patterns...)...); err != nil {
			return err
		}
		dirs = append(dirs, includes...)
	}
	if branch != "HEAD" {
		if err := git("checkout", "--quiet", "-B", branch, commit); err != nil {
			return err
		}
	} else if err := git("checkout", "--quiet", "--detach", commit); err != nil {
		return err
	}
	if root == "" {
		dirs = []string{""}
	}
	for _, d := range slices.Compact(dirs) {
		src := filepath.Join(buildDir, filepath.FromSlash(d)) + "/"
		dst := filepath.Join(stage, filepath.FromSlash(d)) + "/"
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := runCmd(ctx, "", nil, q, stderr, "rsync", "-a", "--ignore-existing", "--exclude=.git", src, dst); err != nil {
			return err
		}
	}
	return nil
}

// fileDigest 返回文件的字节数与 SHA-256（十六进制）。
func fileDigest(p string) (int64, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
