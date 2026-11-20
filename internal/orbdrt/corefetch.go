package orbdrt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/core"
)

// Runner 的 Oryn Core（M21 步骤 3）：orbd 按编进自身的 core/source.json 从 GitHub Release（或 ORYN_CORE_BASE_URL
// 指向的镜像）下载钉住的 omp，校验 sha256 后缓存在 ~/.oryn/core/<tag>/omp。不回退到 PATH 里的 omp（D1）：
// 官方 omp 不认 Oryn 的 RPC 扩展，agent 初始化必然失败。

// coreFetchTimeout 是启动时下载 Core 的总时限。
const coreFetchTimeout = 10 * time.Minute

// coreBaseURLEnv 覆盖 Core 的下载根（镜像）；缺省为 https://github.com/<repo>/releases/download。
const coreBaseURLEnv = "ORYN_CORE_BASE_URL"

// corePlatform 返回当前平台在 source.json 里的键（"<goos>-<arch>"，amd64 记作 x64）。
func corePlatform() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return runtime.GOOS + "-" + arch
}

// ensureCore 确保 ~/.oryn/core/<tag>/omp 是 rel 钉住的那个文件并返回其绝对路径：已缓存且 sha256 一致直接用，
// 否则下载到同目录临时文件、校验、chmod 0755 后 rename；成功后删掉 ~/.oryn/core 下其它 tag 的目录。
// client 为 nil 时用 http.DefaultClient。
func ensureCore(ctx context.Context, home string, rel core.Release, client *http.Client) (string, error) {
	plat := corePlatform()
	want := strings.ToLower(rel.SHA256[plat])
	if want == "" {
		return "", fmt.Errorf("此平台没有 Oryn Core 构建（%s，%s@%s）", plat, rel.Repo, rel.Tag)
	}
	if rel.Tag == "" || strings.ContainsAny(rel.Tag, `/\`) || rel.Tag == "." || rel.Tag == ".." {
		return "", fmt.Errorf("Oryn Core 的 tag 无效：%q", rel.Tag)
	}
	root := filepath.Join(home, ".oryn", "core")
	dir := filepath.Join(root, rel.Tag)
	bin := filepath.Join(dir, "omp")
	if got, err := fileSHA256(bin); err == nil && got == want {
		pruneCoreTags(root, rel.Tag)
		return bin, nil
	}
	base := strings.TrimRight(os.Getenv(coreBaseURLEnv), "/")
	if base == "" {
		base = "https://github.com/" + rel.Repo + "/releases/download"
	}
	url := base + "/" + rel.Tag + "/omp-" + plat
	if err := downloadCore(ctx, client, url, dir, bin, want); err != nil {
		return "", fmt.Errorf("下载 Oryn Core 失败（%s）：%w；无法直连 GitHub 时设置 %s 指向镜像（<镜像>/%s/omp-%s）",
			url, err, coreBaseURLEnv, rel.Tag, plat)
	}
	pruneCoreTags(root, rel.Tag)
	return bin, nil
}

// downloadCore 把 url 下载到 dir 下的临时文件，sha256 与 want 一致时以 0755 rename 成 bin；任何失败都不留临时文件。
func downloadCore(ctx context.Context, client *http.Client, url, dir, bin, want string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".omp-*.tmp")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("sha256 不符（期望 %s，实际 %s）", want, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return err
	}
	ok = true
	return nil
}

// pruneCoreTags 尽力删掉 root 下除 keep 之外的目录（旧 tag 的 Core）；失败不影响启动。
func pruneCoreTags(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
