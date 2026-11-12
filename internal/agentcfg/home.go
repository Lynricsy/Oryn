package agentcfg

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/secrets"
)

// HomeDirName 是 data_dir 下保存用户 omp 文本配置的目录。
const HomeDirName = "omp-home"

// 导入上限。
const (
	MaxFileSize  = 16 << 20
	MaxTotalSize = 128 << 20
	MaxFiles     = 10000
	maxPathLen   = 1024
)

// 警告类型（对应 AgentConfigWarning.kind）。
const (
	WarnPossibleSecret = "possible_secret"
	WarnEnvFallback    = "env_fallback"
	WarnMissingSecret  = "missing_secret"
	WarnSkipped        = "skipped"
)

var (
	// ErrInvalidPath 表示路径非法（绝对路径、含 ..、空等）。
	ErrInvalidPath = errors.New("路径无效")
	// ErrExcludedPath 表示路径属于不导入的运行时数据（sessions/、auth.json 等）。
	ErrExcludedPath = errors.New("该路径属于 omp 运行时数据，不纳入文本配置")
	// ErrTooLarge 表示超出大小上限。
	ErrTooLarge = errors.New("超出大小上限")
	// ErrInvalidContent 表示 tar 包或文件内容无法解析（如 models.yml 不是合法 YAML）。
	ErrInvalidContent = errors.New("内容无效")
)

// Home 管理 `<data_dir>/omp-home/`。
type Home struct {
	dataDir string
	dir     string
	mu      sync.RWMutex
}

// NewHome 打开（必要时创建，0700）omp-home 目录。
func NewHome(dataDir string) (*Home, error) {
	dir := filepath.Join(dataDir, HomeDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 %s: %w", dir, err)
	}
	return &Home{dataDir: dataDir, dir: dir}, nil
}

// Dir 返回 omp-home 目录的绝对路径。
func (h *Home) Dir() string { return h.dir }

// FileInfo 是一个配置文件的元数据。
type FileInfo struct {
	Path      string
	Size      int64
	Mode      fs.FileMode
	SHA256    string
	UpdatedAt int64
}

// Warning 是写入 / 导入时给用户的提示。
type Warning struct {
	Path    string
	Kind    string
	Message string
}

// WriteResult 是单文件写入结果。
type WriteResult struct {
	File             FileInfo
	ExtractedSecrets []string
	Warnings         []Warning
}

// ImportTarResult 是整包导入结果。
type ImportTarResult struct {
	Files            []FileInfo
	ExtractedSecrets []string
	Warnings         []Warning
}

// CleanPath 规范化相对路径（以 / 分隔），拒绝绝对路径、..、空路径与被排除的运行时数据。
func CleanPath(p string) (string, error) {
	if p == "" || len(p) > maxPathLen || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") {
		return "", ErrInvalidPath
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", ErrInvalidPath
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".." {
			return "", ErrInvalidPath
		}
	}
	if Excluded(c) {
		return "", ErrExcludedPath
	}
	return c, nil
}

func (h *Home) abs(rel string) string { return filepath.Join(h.dir, filepath.FromSlash(rel)) }

func fileInfo(rel, abs string) (FileInfo, error) {
	st, err := os.Lstat(abs)
	if err != nil {
		return FileInfo{}, err
	}
	if !st.Mode().IsRegular() {
		return FileInfo{}, fs.ErrNotExist
	}
	f, err := os.Open(abs)
	if err != nil {
		return FileInfo{}, err
	}
	defer f.Close()
	hs := sha256.New()
	if _, err := io.Copy(hs, f); err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Path: rel, Size: st.Size(), Mode: st.Mode().Perm(),
		SHA256: hex.EncodeToString(hs.Sum(nil)), UpdatedAt: st.ModTime().UnixMilli(),
	}, nil
}

// List 列出全部普通文件（按路径排序）。
func (h *Home) List() ([]FileInfo, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.listLocked()
}

func (h *Home) listLocked() ([]FileInfo, error) {
	out := []FileInfo{}
	err := filepath.WalkDir(h.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(h.dir, p)
		if err != nil {
			return err
		}
		fi, err := fileInfo(filepath.ToSlash(rel), p)
		if err != nil {
			return err
		}
		out = append(out, fi)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// Get 读取文件；不存在返回 fs.ErrNotExist。
func (h *Home) Get(p string) ([]byte, FileInfo, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return nil, FileInfo{}, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	fi, err := fileInfo(rel, h.abs(rel))
	if err != nil {
		return nil, FileInfo{}, err
	}
	b, err := os.ReadFile(h.abs(rel))
	return b, fi, err
}

// Put 写入单个文件。models.yml 先做密钥抽取（字面 apiKey 写入密钥库 mode=proxy，文件中替换为引用）。
// mode 为 0 时取 0644。
func (h *Home) Put(ctx context.Context, p string, content []byte, mode fs.FileMode, vault *secrets.Vault) (WriteResult, error) {
	rel, err := CleanPath(p)
	if err != nil {
		return WriteResult{}, err
	}
	if len(content) > MaxFileSize {
		return WriteResult{}, ErrTooLarge
	}
	mode = normalizeMode(mode)
	out, extracted, warns, err := importOne(ctx, rel, content, vault)
	if err != nil {
		return WriteResult{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := writeFileAtomic(h.abs(rel), out, mode); err != nil {
		return WriteResult{}, err
	}
	fi, err := fileInfo(rel, h.abs(rel))
	if err != nil {
		return WriteResult{}, err
	}
	if rel == ModelsFile {
		warns = append(warns, renderWarnings(out, vault)...)
	}
	return WriteResult{File: fi, ExtractedSecrets: extracted, Warnings: warns}, nil
}

// Delete 删除文件；不存在返回 fs.ErrNotExist。
func (h *Home) Delete(p string) error {
	rel, err := CleanPath(p)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	abs := h.abs(rel)
	st, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fs.ErrNotExist
	}
	if err := os.Remove(abs); err != nil {
		return err
	}
	// 顺手清掉变空的父目录
	for d := filepath.Dir(abs); d != h.dir && strings.HasPrefix(d, h.dir); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}

// ImportTar 用 tar 包整体替换 omp-home（不在包内的文件被删除）。只接受普通文件；
// 被排除的运行时数据静默跳过；models.yml 做密钥抽取。
func (h *Home) ImportTar(ctx context.Context, r io.Reader, vault *secrets.Vault) (ImportTarResult, error) {
	res := ImportTarResult{ExtractedSecrets: []string{}, Warnings: []Warning{}}
	type entry struct {
		content []byte
		mode    fs.FileMode
	}
	files := map[string]entry{}
	var total int64
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("%w：读取 tar: %v", ErrInvalidContent, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			res.Warnings = append(res.Warnings, Warning{Path: name, Kind: WarnSkipped, Message: "不是普通文件，已跳过"})
			continue
		}
		rel, err := CleanPath(name)
		if errors.Is(err, ErrExcludedPath) {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("%q: %w", hdr.Name, err)
		}
		if hdr.Size > MaxFileSize {
			return res, fmt.Errorf("%s: %w（单文件 %d MiB）", rel, ErrTooLarge, MaxFileSize>>20)
		}
		total += hdr.Size
		if total > MaxTotalSize || len(files) >= MaxFiles {
			return res, fmt.Errorf("%w（合计 %d MiB、%d 个文件）", ErrTooLarge, MaxTotalSize>>20, MaxFiles)
		}
		b, err := io.ReadAll(io.LimitReader(tr, MaxFileSize+1))
		if err != nil {
			return res, fmt.Errorf("读取 %s: %w", rel, err)
		}
		files[rel] = entry{content: b, mode: normalizeMode(fs.FileMode(hdr.Mode))}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	outs := map[string][]byte{}
	for _, p := range paths {
		out, extracted, warns, err := importOne(ctx, p, files[p].content, vault)
		if err != nil {
			return res, err
		}
		outs[p] = out
		res.ExtractedSecrets = append(res.ExtractedSecrets, extracted...)
		res.Warnings = append(res.Warnings, warns...)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	tmp, err := os.MkdirTemp(h.dataDir, HomeDirName+".import-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	for _, p := range paths {
		if err := writeFileAtomic(filepath.Join(tmp, filepath.FromSlash(p)), outs[p], files[p].mode); err != nil {
			return res, err
		}
	}
	old := h.dir + ".old-" + fmt.Sprint(time.Now().UnixNano())
	if err := os.Rename(h.dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	if err := os.Rename(tmp, h.dir); err != nil {
		_ = os.Rename(old, h.dir)
		return res, err
	}
	_ = os.RemoveAll(old)
	if b, ok := outs[ModelsFile]; ok {
		res.Warnings = append(res.Warnings, renderWarnings(b, vault)...)
	}
	res.Files, err = h.listLocked()
	return res, err
}

// LoadAll 读取全部文件（相对路径 → 内容），供 Render 下发到 orb。
func (h *Home) LoadAll() (map[string][]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list, err := h.listLocked()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(list))
	for _, f := range list {
		b, err := os.ReadFile(h.abs(f.Path))
		if err != nil {
			return nil, err
		}
		out[f.Path] = b
	}
	return out, nil
}

// Modes 返回全部文件的权限位（相对路径 → perm）。
func (h *Home) Modes() (map[string]os.FileMode, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list, err := h.listLocked()
	if err != nil {
		return nil, err
	}
	out := make(map[string]os.FileMode, len(list))
	for _, f := range list {
		out[f.Path] = f.Mode
	}
	return out, nil
}

func normalizeMode(m fs.FileMode) fs.FileMode {
	m &= 0o777
	if m == 0 {
		return 0o644
	}
	return m | 0o600 // 控制面自身必须可读写
}

// importOne 对单个文件执行 Import：models.yml 抽取密钥写入密钥库；其它文件只做形似密钥的扫描。
func importOne(ctx context.Context, rel string, content []byte, vault *secrets.Vault) ([]byte, []string, []Warning, error) {
	ir, err := Import(map[string][]byte{rel: content})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w：%v", ErrInvalidContent, err)
	}
	var warns []Warning
	for _, w := range ir.Warnings {
		warns = append(warns, Warning{Path: rel, Kind: WarnPossibleSecret, Message: w})
	}
	names := make([]string, 0, len(ir.Secrets))
	for name := range ir.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if vault == nil {
			return nil, nil, nil, errors.New("密钥库不可用，无法导入 models.yml 中的密钥")
		}
		// hosts 为 provider 上游主机；未知（nil）时 Put 保留密钥原有的 hosts
		if _, err := vault.Put(ctx, name, secrets.ModeProxy, ir.Secrets[name], ir.SecretHosts[name]); err != nil {
			return nil, nil, nil, fmt.Errorf("保存密钥 %s: %w", name, err)
		}
	}
	return ir.Files[rel], names, warns, nil
}

// renderWarnings 试渲染 models.yml，提前暴露环境变量降级与缺失的密钥引用。
func renderWarnings(models []byte, vault *secrets.Vault) []Warning {
	if vault == nil {
		return nil
	}
	r, err := Render(map[string][]byte{ModelsFile: models}, vault)
	if err != nil {
		return []Warning{{Path: ModelsFile, Kind: WarnMissingSecret, Message: err.Error()}}
	}
	var out []Warning
	for _, w := range r.Warnings {
		out = append(out, Warning{Path: ModelsFile, Kind: WarnEnvFallback, Message: w})
	}
	return out
}

func writeFileAtomic(abs string, content []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(abs), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(content); err != nil {
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
	return os.Rename(tmp, abs)
}
