package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
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
其中形似密钥的字符串只给出警告。`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPush,
}

func init() {
	configCmd.AddCommand(configPushCmd)
	root.AddCommand(configCmd)
}

type pushFile struct {
	rel  string
	abs  string
	mode fs.FileMode
}

func runConfigPush(cmd *cobra.Command, args []string) error {
	dir := ""
	if len(args) == 1 {
		dir = args[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".omp", "agent")
	}
	files, err := collectPushFiles(dir)
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

// collectPushFiles 遍历目录（跟随符号链接，防环），返回未被排除的普通文件。
func collectPushFiles(root string) ([]pushFile, error) {
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
			if agentcfg.Excluded(childRel) {
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
