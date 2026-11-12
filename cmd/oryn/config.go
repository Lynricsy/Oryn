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

	"github.com/spf13/cobra"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/apigen"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "omp 文本配置（omp-home）",
}

var configPushCmd = &cobra.Command{
	Use:   "push [dir]",
	Short: "打包上传本机 omp 配置目录（默认 ~/.omp/agent），整体替换控制面上的 omp-home",
	Long: `排除 sessions/ blobs/ cache/ logs/ natives/ terminal-sessions/ *.db *.db-* auth.json history*。
models.yml 中的字面 apiKey 会被控制面抽取进密钥库，磁盘上只留引用；其它文件原样保存，
其中形似密钥的字符串只给出警告。

额外排除：--exclude <glob>（可重复），以及 dir/.orynignore 中每行一个 glob（# 开头为注释）。
glob 按相对路径匹配，匹配到目录即排除整个目录，例如 extensions/omp-notify、omp-notify*。`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPush,
}

var configPullCmd = &cobra.Command{
	Use:   "pull [dir]",
	Short: "把控制面上的 omp-home 下载到本机目录（默认 ~/.omp/agent）",
	Long: `逐个下载 omp-home 文件并按服务端记录的权限写入 dir，不删除本地多出的文件。
服务端的 models.yml 是模板（密钥已替换为 ${oryn:secret:…} 引用），默认写成 models.oryn.yml，
避免覆盖本地带真实密钥的 models.yml；--force 时直接覆盖 models.yml。`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPull,
}

var configPushOpts struct {
	exclude []string
}

var configPullOpts struct {
	force  bool
	dryRun bool
}

func init() {
	configPushCmd.Flags().StringArrayVar(&configPushOpts.exclude, "exclude", nil, "额外排除的相对路径 glob（可重复）")
	configPullCmd.Flags().BoolVar(&configPullOpts.force, "force", false, "直接覆盖 models.yml（其中只有密钥引用，没有真实值）")
	configPullCmd.Flags().BoolVar(&configPullOpts.dryRun, "dry-run", false, "只列出将写入的文件")
	configCmd.AddCommand(configPushCmd, configPullCmd)
	root.AddCommand(configCmd)
}

// pulledModelsFile 是不带 --force 时 models.yml 模板的落盘名。
const pulledModelsFile = "models.oryn.yml"

func defaultAgentDir(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".omp", "agent"), nil
}

// pullTarget 计算服务端相对路径在本地的落盘位置；拒绝逃出 dir 的路径。
// renamed 表示 models.yml 被改写为 models.oryn.yml。
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
	if r, err := filepath.Rel(root, dst); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false, fmt.Errorf("拒绝服务端路径 %q：逃出目标目录", rel)
	}
	return dst, renamed, nil
}

func runConfigPull(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	dir, err := defaultAgentDir(args)
	if err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var list apigen.AgentConfigFileList
	if err := c.do(ctx, http.MethodGet, "/api/v1/agent-config/files", nil, &list); err != nil {
		return err
	}
	type planned struct {
		f       apigen.AgentConfigFile
		dst     string
		renamed bool
	}
	plan := make([]planned, 0, len(list.Files))
	for _, f := range list.Files {
		dst, renamed, err := pullTarget(dir, f.Path, configPullOpts.force)
		if err != nil {
			return err
		}
		plan = append(plan, planned{f: f, dst: dst, renamed: renamed})
	}
	for _, p := range plan {
		mode := fs.FileMode(p.f.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if configPullOpts.dryRun {
			fmt.Printf("%s\t%d 字节\t%04o\n", p.dst, p.f.Size, mode)
		} else {
			b, err := getRaw(ctx, c, "/api/v1/agent-config/files/"+escapePathParam(p.f.Path))
			if err != nil {
				return fmt.Errorf("下载 %s: %w", p.f.Path, err)
			}
			if err := writeFileMode(p.dst, b, mode); err != nil {
				return err
			}
		}
		if p.renamed {
			verb := "已写为"
			if configPullOpts.dryRun {
				verb = "将写为"
			}
			fmt.Fprintf(os.Stderr, "注意：服务端 models.yml 只含 ${oryn:secret:…} 引用，%s %s，本地 models.yml 不改动（--force 可直接覆盖）\n", verb, pulledModelsFile)
		}
	}
	if configPullOpts.dryRun {
		fmt.Fprintf(os.Stderr, "（dry-run）将写入 %d 个文件到 %s\n", len(plan), dir)
	} else {
		fmt.Fprintf(os.Stderr, "已下载 %d 个文件到 %s\n", len(plan), dir)
	}
	return nil
}

// getRaw 以 GET 读取原始响应体。
func getRaw(ctx context.Context, c *apiClient, path string) ([]byte, error) {
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
	b, err := io.ReadAll(io.LimitReader(resp.Body, agentcfg.MaxFileSize+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, Message: strings.TrimSpace(string(b))}
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
	dir, err := defaultAgentDir(args)
	if err != nil {
		return err
	}
	ignore, err := readOrynIgnore(dir)
	if err != nil {
		return err
	}
	files, err := collectPushFiles(dir, append(ignore, configPushOpts.exclude...))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s 中没有可上传的文件", dir)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var total int64
	for _, f := range files {
		b, err := os.ReadFile(f.abs)
		if err != nil {
			return err
		}
		if len(b) > agentcfg.MaxFileSize {
			return fmt.Errorf("%s 超过单文件上限 %d MiB", f.rel, agentcfg.MaxFileSize>>20)
		}
		total += int64(len(b))
		if total > agentcfg.MaxTotalSize {
			return fmt.Errorf("合计超过上限 %d MiB", agentcfg.MaxTotalSize>>20)
		}
		if f.rel != agentcfg.ModelsFile {
			if hits := agentcfg.ScanSecretLike(b); len(hits) > 0 && !bytes.Contains(b, []byte{0}) {
				fmt.Fprintf(os.Stderr, "警告：%s 含形似密钥的字符串（%s），该文件会原样下发到 orb\n", f.rel, strings.Join(hits, ", "))
			}
		}
		if err := tw.WriteHeader(&tar.Header{Name: f.rel, Mode: int64(f.mode.Perm()), Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var res apigen.AgentConfigImportResult
	if err := c.do(cmd.Context(), http.MethodPost, "/api/v1/agent-config/import", buf.Bytes(), &res); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "已上传 %d 个文件\n", len(res.Files))
	if len(res.ExtractedSecrets) > 0 {
		fmt.Fprintf(os.Stderr, "已抽取进密钥库：%s\n", strings.Join(res.ExtractedSecrets, ", "))
	}
	for _, w := range res.Warnings {
		if w.Kind == agentcfg.WarnPossibleSecret {
			continue // 本地已提示
		}
		fmt.Fprintf(os.Stderr, "警告（%s）：%s: %s\n", w.Kind, w.Path, w.Message)
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

// collectPushFiles 遍历目录（跟随符号链接，防环），返回未被排除的普通文件。
func collectPushFiles(root string, exclude []string) ([]pushFile, error) {
	var out []pushFile
	visited := map[string]bool{}
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return err
		}
		if visited[real] {
			return nil
		}
		visited[real] = true
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			if agentcfg.Excluded(childRel) || childRel == orynIgnoreFile || userExcluded(childRel, exclude) {
				continue
			}
			childAbs := filepath.Join(abs, e.Name())
			st, err := os.Stat(childAbs) // 跟随符号链接
			if err != nil {
				fmt.Fprintf(os.Stderr, "跳过 %s：%v\n", childRel, err)
				continue
			}
			switch {
			case st.IsDir():
				if err := walk(childAbs, childRel); err != nil {
					return err
				}
			case st.Mode().IsRegular():
				out = append(out, pushFile{rel: childRel, abs: childAbs, mode: st.Mode()})
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}
