package agentcfg

import (
	"archive/tar"
	"bytes"
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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

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
	// ErrProjectRemoved 表示项目已删除（ProjectHomes.Remove 之后对其配置根的写入）；按「不存在」处理（REST 404）。
	ErrProjectRemoved = fmt.Errorf("项目已删除：%w", fs.ErrNotExist)
)

// Home 管理一个 omp 文本配置根：全局为 `<data_dir>/omp-home/`，项目级为 `<data_dir>/omp-projects/<projectId>/`。
type Home struct {
	// parent 是 dir 的父目录（导入用的临时目录建在这里，与 dir 同一文件系统才能 rename）。
	parent string
	// name 是 dir 的末段（omp-home 或项目 id）。
	name string
	dir  string
	// project 为 true 时启用项目级规则：ProjectExcluded、Project* 配额。
	project bool
	mu      sync.RWMutex
	// removed 由 ProjectHomes.Remove 在写锁下置位：此后 Put / ImportTar / Delete 持锁后一律返回 ErrProjectRemoved，
	// 不会经 MkdirAll / rename 把已删项目的目录重新建出来。
	removed bool
}

// NewHome 打开（必要时创建，0700）omp-home 目录。
func NewHome(dataDir string) (*Home, error) {
	dir := filepath.Join(dataDir, HomeDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 %s: %w", dir, err)
	}
	return &Home{parent: dataDir, name: HomeDirName, dir: dir}, nil
}

// newProjectHome 返回项目级配置根 `<root>/<projectID>`；不创建目录（首次写入时才建）。
func newProjectHome(root, projectID string) *Home {
	return &Home{parent: root, name: projectID, dir: filepath.Join(root, projectID), project: true}
}

// MaxFileSize 返回该配置根的单文件上限（全局 MaxFileSize，项目级 ProjectMaxFileSize）。
func (h *Home) MaxFileSize() int {
	f, _, _ := h.limits()
	return f
}

// MaxTotalSize 返回该配置根的合计大小上限（全局 MaxTotalSize，项目级 ProjectMaxTotalSize）。
func (h *Home) MaxTotalSize() int {
	_, t, _ := h.limits()
	return t
}

// limits 返回单文件、合计大小与文件数上限。
func (h *Home) limits() (file, total, files int) {
	if h.project {
		return ProjectMaxFileSize, ProjectMaxTotalSize, ProjectMaxFiles
	}
	return MaxFileSize, MaxTotalSize, MaxFiles
}

// clean 是带该配置根规则的 CleanPath：项目级额外拒绝 ProjectExcluded 的路径。
func (h *Home) clean(p string) (string, error) {
	c, err := CleanPath(p)
	if err != nil {
		return "", err
	}
	if h.project && ProjectExcluded(c) {
		return "", fmt.Errorf("%w：项目级配置不支持 %s", ErrExcludedPath, c)
	}
	return c, nil
}

// checkContent 校验全局与项目层共用的内容规则：
//   - 必须是文本的路径（mustBeText）只收 UTF-8、不含 NUL 的内容，二进制或伪文件（如 /proc/self/environ）进不来；
//   - config.yml 只收一个 YAML 文档，且为空、null 或顶层映射（checkConfigYML）。项目层经 `--config` 覆盖层加载，
//     全局层是 omp 主配置，解析失败都会让 agent 起不来。
func checkContent(rel string, content []byte) error {
	if mustBeText(rel) && (bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)) {
		return fmt.Errorf("%w：%s 须是 UTF-8 文本（不能含 NUL 或非 UTF-8 字节）", ErrInvalidContent, rel)
	}
	if rel == ConfigFile {
		if err := checkConfigYML(content); err != nil {
			return fmt.Errorf("%w：%v", ErrInvalidContent, err)
		}
	}
	return nil
}

// mustBeText 报告 rel 是否必须是文本：rules/ 下的一切，以及 .md / .yml / .yaml / .json 文件（AGENTS.md、
// SYSTEM.md、config.yml、mcp.json…）。这些文件会被 omp 当作规则、提示词或配置读入。
func mustBeText(rel string) bool {
	if first, _, _ := strings.Cut(rel, "/"); first == "rules" {
		return true
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".md", ".yml", ".yaml", ".json":
		return true
	}
	return false
}

// checkConfigYML 校验 config.yml：空、null 或顶层映射，且只有一个 YAML 文档。Core 用 Bun.YAML.parse 读配置，
// 多文档（`---` 分隔，哪怕第二个是空文档）会被解析成数组而硬失败；yaml.v3 只看首文档，所以要再 Decode 一次确认已到 EOF。
func checkConfigYML(content []byte) error {
	if _, err := parseConfigRoot(content); err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	var doc yaml.Node
	if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
		return nil
	}
	if err := dec.Decode(&doc); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s 只能有一个 YAML 文档（不能用 --- 分隔多个文档）", ConfigFile)
	}
	return nil
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

// listLocked 列出全部普通文件；目录不存在（未写过的项目）时返回空列表。
func (h *Home) listLocked() ([]FileInfo, error) {
	out := []FileInfo{}
	if _, err := os.Lstat(h.dir); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
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
	rel, err := h.clean(p)
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
	rel, err := h.clean(p)
	if err != nil {
		return WriteResult{}, err
	}
	fileMax, totalMax, filesMax := h.limits()
	if len(content) > fileMax {
		return WriteResult{}, fmt.Errorf("%w（单文件 %d MiB）", ErrTooLarge, fileMax>>20)
	}
	if err := checkContent(rel, content); err != nil {
		return WriteResult{}, err
	}
	mode = normalizeMode(mode)
	out, extracted, warns, err := importOne(ctx, rel, content, vault)
	if err != nil {
		return WriteResult{}, err
	}
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed {
		return WriteResult{}, ErrProjectRemoved
	}
	if h.project {
		if err := h.checkQuotaLocked(rel, len(out), totalMax, filesMax); err != nil {
			return WriteResult{}, err
		}
	}
	own, err := layerSize(h.dir, rel)
	if err != nil {
		return WriteResult{}, err
	}
	if err := h.checkDelivery(own + EntrySize(rel, int64(len(out)))); err != nil {
		return WriteResult{}, err
	}
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

// checkQuotaLocked 检查写入 rel（size 字节）后项目配置仍在合计大小与文件数上限内（不计 rel 的现有内容）。持写锁调用。
func (h *Home) checkQuotaLocked(rel string, size, totalMax, filesMax int) error {
	list, err := h.listLocked()
	if err != nil {
		return err
	}
	total, n := int64(size), 1
	for _, f := range list {
		if f.Path == rel {
			continue
		}
		total += f.Size
		n++
	}
	if total > int64(totalMax) || n > filesMax {
		return fmt.Errorf("%w（合计 %d MiB、%d 个文件）", ErrTooLarge, totalMax>>20, filesMax)
	}
	return nil
}

// Delete 删除文件；不存在返回 fs.ErrNotExist。
func (h *Home) Delete(p string) error {
	rel, err := h.clean(p)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed {
		return ErrProjectRemoved
	}
	st, err := os.Lstat(h.abs(rel))
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fs.ErrNotExist
	}
	return h.removeLocked(rel)
}

// BatchFile 是批量变更里要写入的一个文件；Mode 为 0 时取 0644。
type BatchFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// Batch 是一批文件变更：先删 Delete（每项是一个文件，或一个目录下的全部文件），再写 Files（覆盖已有）。
type Batch struct {
	Delete []string
	Files  []BatchFile
}

// BatchResult 是批量变更结果：Files 为本批写入的文件（按路径排序），Deleted 为被删除且未被本批重新写入的文件（排序）。
type BatchResult struct {
	Files            []FileInfo
	Deleted          []string
	ExtractedSecrets []string
	Warnings         []Warning
}

// Apply 原子地校验并执行一批文件变更（多文件技能、扩展、工具目录的导入 / 替换 / 删除）。整批先校验再落盘：
// 路径非法或属于运行时数据、Files 内路径重复、内容不合规则、变更后同一路径既是文件又是目录 → ErrInvalidPath /
// ErrExcludedPath / ErrInvalidContent；单文件、变更后整层的合计大小 / 文件数、两层下发预算超限 → ErrTooLarge；
// 校验失败时不改任何文件。Delete 不匹配任何文件时忽略；目录删除只删 listLocked 列出、且不属于运行时数据的文件。
// models.yml 做密钥抽取（同 Put）。
//
// 落盘分三步（见 commitBatchLocked）：新文件先全部写进与配置根同一文件系统的暂存目录（文件名过长、磁盘满等写入错误
// 在这一步暴露，现有文件不动）；再把要删除与要覆盖的旧文件移进备份目录；最后把暂存文件逐个 rename 到位。
// 后两步任一失败都按相反顺序撤回，旧文件原样回到原处。
func (h *Home) Apply(ctx context.Context, b Batch, vault *secrets.Vault) (BatchResult, error) {
	res := BatchResult{Files: []FileInfo{}, Deleted: []string{}, ExtractedSecrets: []string{}, Warnings: []Warning{}}
	if len(b.Delete) == 0 && len(b.Files) == 0 {
		return res, fmt.Errorf("%w：delete 与 files 不能都为空", ErrInvalidContent)
	}
	dels := make([]string, 0, len(b.Delete))
	for _, p := range b.Delete {
		rel, err := h.clean(p)
		if err != nil {
			return res, fmt.Errorf("%q: %w", p, err)
		}
		dels = append(dels, rel)
	}
	type entry struct {
		content []byte
		mode    fs.FileMode
	}
	writes := make(map[string]entry, len(b.Files))
	paths := make([]string, 0, len(b.Files))
	fileMax, totalMax, filesMax := h.limits()
	for _, f := range b.Files {
		rel, err := h.clean(f.Path)
		if err != nil {
			return res, fmt.Errorf("%q: %w", f.Path, err)
		}
		if _, dup := writes[rel]; dup {
			return res, fmt.Errorf("%w：%s 在 files 里出现多次", ErrInvalidPath, rel)
		}
		if len(f.Content) > fileMax {
			return res, fmt.Errorf("%s: %w（单文件 %d MiB）", rel, ErrTooLarge, fileMax>>20)
		}
		if err := checkContent(rel, f.Content); err != nil {
			return res, err
		}
		writes[rel] = entry{content: f.Content, mode: normalizeMode(f.Mode)}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	// 密钥抽取在锁外做（同 Put）：抽取后的正文才是落盘大小，配额与预算按它算
	for _, p := range paths {
		out, extracted, warns, err := importOne(ctx, p, writes[p].content, vault)
		if err != nil {
			return res, err
		}
		writes[p] = entry{content: out, mode: writes[p].mode}
		res.ExtractedSecrets = append(res.ExtractedSecrets, extracted...)
		res.Warnings = append(res.Warnings, warns...)
	}

	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed {
		return res, ErrProjectRemoved
	}
	list, err := h.listLocked()
	if err != nil {
		return res, err
	}
	// 变更后的最终文件集合（相对路径 → 大小）
	final := make(map[string]int64, len(list)+len(paths))
	var removed []string
	for _, f := range list {
		if h.batchDeletes(dels, f.Path) {
			removed = append(removed, f.Path)
			continue
		}
		final[f.Path] = f.Size
	}
	for _, p := range paths {
		final[p] = int64(len(writes[p].content))
	}
	// 同一路径不能既是文件又是目录（`a` 与 `a/b` 并存）
	dirs := map[string]struct{}{}
	for p := range final {
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			dirs[d] = struct{}{}
		}
	}
	for p := range final {
		if _, ok := dirs[p]; ok {
			return res, fmt.Errorf("%w：%s 在变更后既是文件又是目录", ErrInvalidPath, p)
		}
	}
	// 只删不写的批次不会让本层变大，不查配额与下发预算（已超限的配置也要能删）
	if len(paths) > 0 {
		var total, size int64
		for p, n := range final {
			total += n
			size += EntrySize(p, n)
		}
		if total > int64(totalMax) || len(final) > filesMax {
			return res, fmt.Errorf("%w（合计 %d MiB、%d 个文件）", ErrTooLarge, totalMax>>20, filesMax)
		}
		if err := h.checkDelivery(size); err != nil {
			return res, err
		}
	}

	if err := h.commitBatchLocked(removed, paths, func(p string) ([]byte, fs.FileMode) {
		return writes[p].content, writes[p].mode
	}); err != nil {
		return res, err
	}
	for _, p := range removed {
		if _, ok := writes[p]; !ok {
			res.Deleted = append(res.Deleted, p)
		}
	}
	for _, p := range paths {
		fi, err := fileInfo(p, h.abs(p))
		if err != nil {
			return res, err
		}
		res.Files = append(res.Files, fi)
	}
	if e, ok := writes[ModelsFile]; ok {
		res.Warnings = append(res.Warnings, renderWarnings(e.content, vault)...)
	}
	return res, nil
}

// batchDeletes 报告现有文件 rel 是否被 dels 中某项（文件本身或其所在目录）删除；运行时数据（clean 拒绝的路径）不删。
func (h *Home) batchDeletes(dels []string, rel string) bool {
	for _, d := range dels {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			_, err := h.clean(rel)
			return err == nil
		}
	}
	return false
}

// commitBatchLocked 落盘一批已校验的变更：removed 是要删除的现有文件，paths 是要写入的文件（内容由 content 给出）。
// 暂存 → 备份 → 就位；失败时撤回已就位的新文件并把备份移回原处，配置根回到调用前的样子。持写锁调用。
func (h *Home) commitBatchLocked(removed, paths []string, content func(string) ([]byte, fs.FileMode)) error {
	if len(paths) == 0 && len(removed) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp(h.parent, h.name+".batch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, p := range paths {
		b, mode := content(p)
		if err := writeFileAtomic(filepath.Join(stage, "new", filepath.FromSlash(p)), b, mode); err != nil {
			return err
		}
	}

	// 旧文件：要删除的，以及将被覆盖的
	old := slices.Clone(removed)
	inOld := make(map[string]struct{}, len(removed))
	for _, p := range removed {
		inOld[p] = struct{}{}
	}
	for _, p := range paths {
		if _, ok := inOld[p]; ok {
			continue
		}
		if st, err := os.Lstat(h.abs(p)); err == nil && st.Mode().IsRegular() {
			old = append(old, p)
		}
	}
	type move struct{ from, to string }
	var backedUp []move
	var placed []string
	rollback := func() {
		for i := len(placed) - 1; i >= 0; i-- {
			_ = os.Remove(placed[i])
			pruneEmptyParents(h.dir, placed[i])
		}
		for i := len(backedUp) - 1; i >= 0; i-- {
			m := backedUp[i]
			_ = os.MkdirAll(filepath.Dir(m.from), 0o700)
			_ = os.Rename(m.to, m.from)
		}
	}
	for _, p := range old {
		from, to := h.abs(p), filepath.Join(stage, "old", filepath.FromSlash(p))
		err := os.MkdirAll(filepath.Dir(to), 0o700)
		if err == nil {
			err = os.Rename(from, to)
		}
		if err != nil {
			rollback()
			return err
		}
		backedUp = append(backedUp, move{from: from, to: to})
	}
	for _, p := range paths {
		to := h.abs(p)
		err := os.MkdirAll(filepath.Dir(to), 0o700)
		if err == nil {
			err = os.Rename(filepath.Join(stage, "new", filepath.FromSlash(p)), to)
		}
		if err != nil {
			rollback()
			return err
		}
		placed = append(placed, to)
	}
	for _, p := range removed {
		pruneEmptyParents(h.dir, h.abs(p))
	}
	return nil
}

// removeLocked 删除文件 rel，并顺手清掉变空的父目录。持写锁调用。
func (h *Home) removeLocked(rel string) error {
	abs := h.abs(rel)
	if err := os.Remove(abs); err != nil {
		return err
	}
	pruneEmptyParents(h.dir, abs)
	return nil
}

// pruneEmptyParents 自 abs 的父目录向上删除空目录，止于 root（不含）。
func pruneEmptyParents(root, abs string) {
	for d := filepath.Dir(abs); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
}

// ImportTar 用 tar 包整体替换该配置根（不在包内的文件被删除）。只接受普通文件；
// 被排除的路径（运行时数据；项目级还有 models.yml 与 plans/）静默跳过；models.yml 做密钥抽取。
func (h *Home) ImportTar(ctx context.Context, r io.Reader, vault *secrets.Vault) (ImportTarResult, error) {
	res := ImportTarResult{ExtractedSecrets: []string{}, Warnings: []Warning{}}
	type entry struct {
		content []byte
		mode    fs.FileMode
	}
	files := map[string]entry{}
	var total int64
	fileMax, totalMax, filesMax := h.limits()
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
		rel, err := h.clean(name)
		if errors.Is(err, ErrExcludedPath) {
			continue
		}
		if err != nil {
			return res, fmt.Errorf("%q: %w", hdr.Name, err)
		}
		if hdr.Size > int64(fileMax) {
			return res, fmt.Errorf("%s: %w（单文件 %d MiB）", rel, ErrTooLarge, fileMax>>20)
		}
		total += hdr.Size
		if total > int64(totalMax) || len(files) >= filesMax {
			return res, fmt.Errorf("%w（合计 %d MiB、%d 个文件）", ErrTooLarge, totalMax>>20, filesMax)
		}
		b, err := io.ReadAll(io.LimitReader(tr, int64(fileMax)+1))
		if err != nil {
			return res, fmt.Errorf("读取 %s: %w", rel, err)
		}
		if err := checkContent(rel, b); err != nil {
			return res, err
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

	var size int64
	for _, p := range paths {
		size += EntrySize(p, int64(len(outs[p])))
	}
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.removed {
		return res, ErrProjectRemoved
	}
	if err := h.checkDelivery(size); err != nil {
		return res, err
	}
	tmp, err := os.MkdirTemp(h.parent, h.name+".import-*")
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
