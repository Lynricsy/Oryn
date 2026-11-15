package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "omp 文本配置（全局 omp-home 或项目级 Agent 配置）",
}

var configPushCmd = &cobra.Command{
	Use:   "push [dir]",
	Short: "打包上传本机 omp 配置目录，整体替换控制面上的 omp-home（--project 时替换该项目的 Agent 配置）",
	Long: `不给 --project 时上传 dir（默认 ~/.omp/agent），整体替换控制面上的全局 omp-home。
排除 sessions/ blobs/ cache/ logs/ natives/ terminal-sessions/ *.db *.db-* auth.json history*。
models.yml 中的字面 apiKey 会被控制面抽取进密钥库，磁盘上只留引用；其它文件原样保存，
其中形似密钥的字符串只给出警告。

--project <名称或 id> 时上传 dir（默认当前目录下的 .omp），整体替换该项目的 Agent 配置；
目录结构同仓库里的 .omp/（rules/、skills/、mcp.json、config.yml…），在项目的每台 orb 里生效。
项目级配置另外不收 models.yml（模型与密钥只在全局管理）与 plans/，
上限为单文件 1 MiB、合计 16 MiB、2000 个文件。

额外排除：--exclude <glob>（可重复），以及 dir/.orynignore 中每行一个 glob（# 开头为注释）。
glob 按相对路径匹配，匹配到目录即排除整个目录，例如 extensions/omp-notify、omp-notify*。

只上传普通文件：设备、FIFO、socket 会直接报错（可用 --exclude 排除）；rules/ 下的文件与
*.md *.yml *.yaml *.json 必须是 UTF-8 文本，含 NUL 或非 UTF-8 字节时拒绝上传。
--project 时符号链接解析后还必须仍在 dir 之内，指向 dir 之外、无法解析或指向被排除路径的链接
直接报错；全局时照常跟随链接（含指向 dir 之外的目录），悬空链接与指向被排除路径的链接跳过并提示。`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPush,
}

var configPullCmd = &cobra.Command{
	Use:   "pull [dir]",
	Short: "把控制面上的 omp-home（--project 时为该项目的 Agent 配置）下载到本机目录",
	Long: `逐个下载文件并按服务端记录的权限写入 dir，不删除本地多出的文件。

不给 --project 时下载全局 omp-home 到 dir（默认 ~/.omp/agent）。服务端的 models.yml 是模板
（密钥已替换为 ${oryn:secret:…} 引用），默认写成 models.oryn.yml，避免覆盖本地带真实密钥的
models.yml；--force 时直接覆盖 models.yml。

--project <名称或 id> 时下载该项目的 Agent 配置到 dir（默认当前目录下的 .omp）；
项目级配置没有 models.yml，--force 不起作用。

写入前检查本地目录：父目录经符号链接指向 dir 之外、或要写的文件本身是符号链接时拒绝（不覆盖链接，
请先删除该链接）。`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPull,
}

var configPushOpts struct {
	exclude []string
	project string
}

var configPullOpts struct {
	force   bool
	dryRun  bool
	project string
}

const configProjectFlagUsage = "项目名或 id；给出时操作该项目的 Agent 配置"

func init() {
	configPushCmd.Flags().StringArrayVar(&configPushOpts.exclude, "exclude", nil, "额外排除的相对路径 glob（可重复）")
	configPushCmd.Flags().StringVar(&configPushOpts.project, "project", "", configProjectFlagUsage)
	configPullCmd.Flags().BoolVar(&configPullOpts.force, "force", false, "直接覆盖 models.yml（其中只有密钥引用，没有真实值）")
	configPullCmd.Flags().BoolVar(&configPullOpts.dryRun, "dry-run", false, "只列出将写入的文件")
	configPullCmd.Flags().StringVar(&configPullOpts.project, "project", "", configProjectFlagUsage)
	configCmd.AddCommand(configPushCmd, configPullCmd)
	root.AddCommand(configCmd)
}

// pulledModelsFile 是不带 --force 时 models.yml 模板的落盘名。
const pulledModelsFile = "models.oryn.yml"

// configScope 是 push / pull 的配置根：全局 omp-home（project 为空），
// 或 project（名称或 id）所指项目的 Agent 配置（M12.2）。
type configScope struct {
	project string
}

// isProject 报告是否操作项目级配置。
func (s configScope) isProject() bool { return s.project != "" }

// excluded 判断相对路径是否不纳入该配置根。
func (s configScope) excluded(rel string) bool {
	if s.isProject() {
		return agentcfg.ProjectExcluded(rel)
	}
	return agentcfg.Excluded(rel)
}

// limits 返回单文件、合计大小与文件数上限（与服务端 agentcfg.Home 的配额一致）。
func (s configScope) limits() (file, total, files int) {
	if s.isProject() {
		return agentcfg.ProjectMaxFileSize, agentcfg.ProjectMaxTotalSize, agentcfg.ProjectMaxFiles
	}
	return agentcfg.MaxFileSize, agentcfg.MaxTotalSize, agentcfg.MaxFiles
}

// apiBase 返回该配置根的 REST 前缀；项目级先把名称或 id 解析为项目 id。
func (s configScope) apiBase(ctx context.Context, c *apiClient) (string, error) {
	if !s.isProject() {
		return "/api/v1/agent-config", nil
	}
	id, err := resolveProject(ctx, c, s.project)
	if err != nil {
		return "", err
	}
	return "/api/v1/projects/" + escapePathParam(id) + "/agent-config", nil
}

// defaultDir 返回本地目录：给出 dir 时用它，否则全局为 ~/.omp/agent、项目级为当前目录下的 .omp。
func (s configScope) defaultDir(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	if s.isProject() {
		return ".omp", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".omp", "agent"), nil
}

// pullTarget 计算服务端相对路径在本地的落盘位置；拒绝逃出 dir 的路径。
// renamed 表示 models.yml 被改写为 models.oryn.yml。
// 除词法检查外还看本地文件系统：父目录解析符号链接后必须仍在 dir 之内；
// 目标本身是符号链接时拒绝（不删除也不替换用户的链接，由用户自行处理）。
func pullTarget(dir, rel string, force bool) (dst string, renamed bool, err error) {
	clean, err := agentcfg.CleanPath(rel)
	if err != nil {
		return "", false, fmt.Errorf("拒绝服务端路径 %q: %w", rel, err)
	}
	if clean == agentcfg.ModelsFile && !force {
		clean, renamed = pulledModelsFile, true
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", false, err
	}
	dst = filepath.Join(root, filepath.FromSlash(clean))
	if !within(root, dst) {
		return "", false, fmt.Errorf("拒绝服务端路径 %q：逃出目标目录", rel)
	}
	realRoot, err := resolveExisting(root)
	if err != nil {
		return "", false, err
	}
	parent, err := resolveExisting(filepath.Dir(dst))
	if err != nil {
		return "", false, fmt.Errorf("拒绝写入 %s: %w", dst, err)
	}
	if !within(realRoot, parent) {
		return "", false, fmt.Errorf("拒绝写入 %s：父目录经符号链接指向目标目录之外（%s）", dst, parent)
	}
	st, err := os.Lstat(dst)
	switch {
	case err == nil && st.Mode()&fs.ModeSymlink != 0:
		return "", false, fmt.Errorf("拒绝写入 %s：目标是符号链接，请先删除该链接再拉取", dst)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", false, err
	}
	return dst, renamed, nil
}

// within 报告 p 是否为 root 本身或位于 root 之下（纯词法比较，调用方负责先解析符号链接）。
func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// resolveExisting 解析 p 中已存在部分的符号链接，拼回尚不存在的尾部（之后由 MkdirAll 新建为真实目录）。
// 已存在却无法解析的链接（悬空、成环）报错。
func resolveExisting(p string) (string, error) {
	tail := ""
	for cur := p; ; {
		_, err := os.Lstat(cur)
		if err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, tail), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		tail = filepath.Join(filepath.Base(cur), tail)
		cur = parent
	}
}

func runConfigPull(cmd *cobra.Command, args []string) error {
	scope := configScope{project: configPullOpts.project}
	dir, err := scope.defaultDir(args)
	if err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	return configPull(cmd.Context(), c, scope, dir, configPullOpts.force, configPullOpts.dryRun, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

// configPull 把 scope 对应的服务端配置逐个下载到 dir。
func configPull(ctx context.Context, c *apiClient, scope configScope, dir string, force, dryRun bool, stdout, stderr io.Writer) error {
	base, err := scope.apiBase(ctx, c)
	if err != nil {
		return err
	}
	var list apigen.AgentConfigFileList
	if err := c.do(ctx, http.MethodGet, base+"/files", nil, &list); err != nil {
		return err
	}
	type planned struct {
		f       apigen.AgentConfigFile
		dst     string
		renamed bool
	}
	plan := make([]planned, 0, len(list.Files))
	for _, f := range list.Files {
		// 项目级配置不含 models.yml 等文件（所以也没有 models.oryn.yml 改名）；服务端给出则拒绝
		if scope.isProject() && agentcfg.ProjectExcluded(f.Path) {
			return fmt.Errorf("拒绝服务端路径 %q：项目级配置不支持该路径", f.Path)
		}
		dst, renamed, err := pullTarget(dir, f.Path, force)
		if err != nil {
			return err
		}
		plan = append(plan, planned{f: f, dst: dst, renamed: renamed})
	}
	maxFile, _, _ := scope.limits()
	for _, p := range plan {
		mode := fs.FileMode(p.f.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if dryRun {
			fmt.Fprintf(stdout, "%s\t%d 字节\t%04o\n", p.dst, p.f.Size, mode)
		} else {
			b, err := getRaw(ctx, c, base+"/files/"+escapePathParam(p.f.Path), maxFile)
			if err != nil {
				return fmt.Errorf("下载 %s: %w", p.f.Path, err)
			}
			if err := writeFileMode(p.dst, b, mode); err != nil {
				return err
			}
		}
		if p.renamed {
			verb := "已写为"
			if dryRun {
				verb = "将写为"
			}
			fmt.Fprintf(stderr, "注意：服务端 models.yml 只含 ${oryn:secret:…} 引用，%s %s，本地 models.yml 不改动（--force 可直接覆盖）\n", verb, pulledModelsFile)
		}
	}
	if dryRun {
		fmt.Fprintf(stderr, "（dry-run）将写入 %d 个文件到 %s\n", len(plan), dir)
	} else {
		fmt.Fprintf(stderr, "已下载 %d 个文件到 %s\n", len(plan), dir)
	}
	return nil
}

// getRaw 以 GET 读取原始响应体（至多 limit 字节，超出时视为错误）。
func getRaw(ctx context.Context, c *apiClient, path string, limit int) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
	}
	if len(b) > limit {
		return nil, fmt.Errorf("超过单文件上限 %d MiB", limit>>20)
	}
	return b, nil
}

// writeFileMode 原子写入文件并设置权限。
func writeFileMode(dst string, b []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".oryn-pull-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type pushFile struct {
	rel  string
	abs  string
	mode fs.FileMode
}

func runConfigPush(cmd *cobra.Command, args []string) error {
	scope := configScope{project: configPushOpts.project}
	dir, err := scope.defaultDir(args)
	if err != nil {
		return err
	}
	// 先在本地打包与预检，出错时不需要登录凭据，也不发任何请求
	tarball, err := buildConfigTar(scope, dir, configPushOpts.exclude, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	return uploadConfigTar(cmd.Context(), c, scope, tarball, cmd.ErrOrStderr())
}

// buildConfigTar 收集 dir 中未被排除的文件并打成 tar；超出 scope 的配额时在本地报错。
func buildConfigTar(scope configScope, dir string, exclude []string, stderr io.Writer) ([]byte, error) {
	ignore, err := readOrynIgnore(dir)
	if err != nil {
		return nil, err
	}
	exclude = append(ignore, exclude...)
	if scope.isProject() && !userExcluded(agentcfg.ModelsFile, exclude) {
		if st, err := os.Stat(filepath.Join(dir, agentcfg.ModelsFile)); err == nil && st.Mode().IsRegular() {
			fmt.Fprintf(stderr, "项目级配置不支持 %s，已跳过\n", agentcfg.ModelsFile)
		}
	}
	files, err := collectPushFiles(dir, scope.excluded, exclude, scope.isProject(), stderr)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s 中没有可上传的文件", dir)
	}
	maxFile, maxTotal, maxFiles := scope.limits()
	if len(files) > maxFiles {
		return nil, fmt.Errorf("文件数 %d 超过上限 %d", len(files), maxFiles)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var total int64
	for _, f := range files {
		b, err := os.ReadFile(f.abs)
		if err != nil {
			return nil, err
		}
		if len(b) > maxFile {
			return nil, fmt.Errorf("%s 超过单文件上限 %d MiB", f.rel, maxFile>>20)
		}
		total += int64(len(b))
		if total > int64(maxTotal) {
			return nil, fmt.Errorf("合计超过上限 %d MiB", maxTotal>>20)
		}
		if textRequired(f.rel) && (bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b)) {
			return nil, fmt.Errorf("%s 必须是 UTF-8 文本（含 NUL 或非 UTF-8 字节），拒绝上传", f.rel)
		}
		// 二进制内容同样扫描：含 NUL 不再是跳过告警的理由
		if f.rel != agentcfg.ModelsFile {
			if hits := agentcfg.ScanSecretLike(b); len(hits) > 0 {
				fmt.Fprintf(stderr, "警告：%s 含形似密钥的字符串（%s），该文件会原样下发到 orb\n", f.rel, strings.Join(hits, ", "))
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.rel, Mode: int64(f.mode.Perm()), Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(b); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// textRequired 报告该相对路径是否必须是文本：rules/ 下的一切与 *.md *.yml *.yaml *.json。
func textRequired(rel string) bool {
	if strings.HasPrefix(rel, "rules/") {
		return true
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".md", ".yml", ".yaml", ".json":
		return true
	}
	return false
}

// uploadConfigTar 把 tar 包导入 scope 对应的配置根并打印结果。
func uploadConfigTar(ctx context.Context, c *apiClient, scope configScope, tarball []byte, stderr io.Writer) error {
	base, err := scope.apiBase(ctx, c)
	if err != nil {
		return err
	}
	var res apigen.AgentConfigImportResult
	if err := c.do(ctx, http.MethodPost, base+"/import", tarball, &res); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "已上传 %d 个文件\n", len(res.Files))
	if len(res.ExtractedSecrets) > 0 {
		fmt.Fprintf(stderr, "已抽取进密钥库：%s\n", strings.Join(res.ExtractedSecrets, ", "))
	}
	for _, w := range res.Warnings {
		if w.Kind == agentcfg.WarnPossibleSecret {
			continue // 本地已提示
		}
		fmt.Fprintf(stderr, "警告（%s）：%s: %s\n", w.Kind, w.Path, w.Message)
	}
	return nil
}

// orynIgnoreFile 是推送目录中的额外排除表。
const orynIgnoreFile = ".orynignore"

// readOrynIgnore 读取 dir/.orynignore（不存在时为空）。
func readOrynIgnore(dir string) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(dir, orynIgnoreFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, strings.Trim(line, "/"))
		}
	}
	return out, nil
}

// userExcluded 判断相对路径是否命中用户排除 glob（任一前缀段匹配即命中）。
func userExcluded(rel string, globs []string) bool {
	segs := strings.Split(rel, "/")
	for i := range segs {
		prefix := strings.Join(segs[:i+1], "/")
		for _, g := range globs {
			if ok, _ := path.Match(g, prefix); ok {
				return true
			}
		}
	}
	return false
}

// collectPushFiles 遍历目录，返回未被 excluded（配置根的排除规则）与用户 glob 排除的普通文件。
// 两种模式都只收普通文件（设备、FIFO、socket 拒绝），且指向目录的链接照常遍历、防环。
// strict（--project，目录来自可能不可信的仓库）时符号链接解析后必须仍在 root 之内且目标不被排除，
// 指向 root 之外、无法解析或指向被排除路径的链接一律拒绝，避免把本机文件（如 /proc/self/environ、
// ~/.ssh）带进上传包；非 strict（全局，用户自己的 ~/.omp/agent）时照常跟随指向 root 之外的链接，
// 悬空链接与指向被排除路径的链接在 stderr 提示后跳过。被拒绝的路径汇总列出后报错。
func collectPushFiles(root string, excluded func(rel string) bool, exclude []string, strict bool, stderr io.Writer) ([]pushFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, err
	}
	skip := func(rel string) bool {
		return excluded(rel) || rel == orynIgnoreFile || userExcluded(rel, exclude)
	}
	var out []pushFile
	var rejected []string
	// visited 记录已遍历过的真实目录。非链接子目录总是遍历（它们构成一棵树，不会成环）；
	// 目录链接只在目标从未遍历过时展开一次，既截断成环，又避免别名按遍历顺序吞掉原目录，
	// 还让链接展开的总量有界（每个目标至多展开一次）
	visited := map[string]bool{}
	// dir 总是已解析符号链接的真实路径，因此其下非链接条目的路径也是真实路径。
	// rel 是别名路径（tar 里的名字）；hasReal 时 realRel 是 dir 相对 realRoot 的真实路径。
	// 经目录链接进入后两者不同，后代要按两者各查一次排除，否则 a -> skills 会让
	// skills/private、sessions/ 等被排除的内容以 a/private/… 的名字漏出去。
	// 跟随指向根外的链接（仅全局）后没有真实 rel，只按别名检查。
	var walk func(dir, rel, realRel string, hasReal bool) error
	walk = func(dir, rel, realRel string, hasReal bool) error {
		visited[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRel := joinRel(rel, e.Name())
			if skip(childRel) {
				continue
			}
			childReal, childHasReal := joinRel(realRel, e.Name()), hasReal
			if hasReal && skip(childReal) {
				// 与入口处链接指向被排除路径的处理一致：--project 拒绝，全局提示后跳过
				if !strict {
					fmt.Fprintf(stderr, "跳过 %s：经符号链接指向被排除的路径 %s\n", childRel, childReal)
					continue
				}
				rejected = append(rejected, fmt.Sprintf("%s → %s（经符号链接指向被排除的路径）", childRel, childReal))
				continue
			}
			childAbs := filepath.Join(dir, e.Name())
			st, err := os.Lstat(childAbs)
			if err != nil {
				return err
			}
			if st.Mode()&fs.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(childAbs)
				if err != nil {
					if !strict {
						fmt.Fprintf(stderr, "跳过 %s：%v\n", childRel, err)
						continue
					}
					rejected = append(rejected, fmt.Sprintf("%s（符号链接无法解析：%v）", childRel, err))
					continue
				}
				inRoot := within(realRoot, target)
				if !inRoot && strict {
					rejected = append(rejected, fmt.Sprintf("%s → %s（符号链接指向目录之外）", childRel, target))
					continue
				}
				tr, err := filepath.Rel(realRoot, target)
				if err != nil {
					return err
				}
				// 链接目标决定后代的真实 rel：根内为目标相对根的路径，根外（仅全局）则没有
				childReal, childHasReal = filepath.ToSlash(tr), inRoot
				if tr == "." {
					childReal = ""
				}
				if inRoot && tr != "." && skip(childReal) {
					if !strict {
						fmt.Fprintf(stderr, "跳过 %s：符号链接指向被排除的路径 %s\n", childRel, target)
						continue
					}
					rejected = append(rejected, fmt.Sprintf("%s → %s（符号链接指向被排除的路径）", childRel, target))
					continue
				}
				if st, err = os.Stat(target); err != nil {
					return err
				}
				if st.IsDir() && visited[target] {
					continue // 目录链接的目标已遍历过（含成环）
				}
				childAbs = target
			}
			switch {
			case st.IsDir():
				if err := walk(childAbs, childRel, childReal, childHasReal); err != nil {
					return err
				}
			case st.Mode().IsRegular():
				out = append(out, pushFile{rel: childRel, abs: childAbs, mode: st.Mode()})
			default:
				rejected = append(rejected, fmt.Sprintf("%s（%s，不是普通文件）", childRel, fileKind(st.Mode())))
			}
		}
		return nil
	}
	if err := walk(realRoot, "", "", true); err != nil {
		return nil, err
	}
	if len(rejected) > 0 {
		return nil, fmt.Errorf("拒绝上传 %s 中的以下路径（可用 --exclude 或 .orynignore 排除）：\n  %s",
			root, strings.Join(rejected, "\n  "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// joinRel 拼接以 / 分隔的相对路径（rel 为空表示根）。
func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

// fileKind 用于报错的特殊文件类型名。
func fileKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeNamedPipe != 0:
		return "FIFO"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&(fs.ModeDevice|fs.ModeCharDevice) != 0:
		return "设备文件"
	default:
		return "特殊文件"
	}
}
