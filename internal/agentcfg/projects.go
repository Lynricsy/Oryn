package agentcfg

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ProjectsDirName 是 data_dir 下保存项目级 omp 配置的目录（每个项目一个子目录，M12.2）。
const ProjectsDirName = "omp-projects"

// 项目级配置的上限：随每次 setup.run / resume.run 整份下发，所以比全局小。
const (
	ProjectMaxFileSize  = 1 << 20
	ProjectMaxTotalSize = 16 << 20
	ProjectMaxFiles     = 2000
)

// projectIDRe 校验拼进路径的项目 id；不含 `.`，所以不会与 `<id>.import-*`、`<id>.old-*` 临时目录撞名。
var projectIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ProjectExcluded 判断路径是否不属于项目级配置：全局排除的运行时数据、models.yml
// （模型与密钥只在全局管理）与 plans/（计划是工作产物不是配置）。
func ProjectExcluded(rel string) bool {
	if Excluded(rel) {
		return true
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	first, _, _ := strings.Cut(rel, "/")
	return rel == ModelsFile || first == "plans"
}

// ProjectHomes 管理 `<data_dir>/omp-projects/<projectId>/`。同一项目共用一个 *Home（锁才有效）。
type ProjectHomes struct {
	root  string
	mu    sync.Mutex
	homes map[string]*Home
}

// NewProjectHomes 打开（必要时创建，0700）`<data_dir>/omp-projects`。
func NewProjectHomes(dataDir string) (*ProjectHomes, error) {
	root := filepath.Join(dataDir, ProjectsDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("创建 %s: %w", root, err)
	}
	return &ProjectHomes{root: root, homes: map[string]*Home{}}, nil
}

// For 返回项目的配置根；id 不合法返回 ErrInvalidPath。不创建目录（首次写入时才建）。
// 调用方负责确认项目存在。
func (p *ProjectHomes) For(projectID string) (*Home, error) {
	if !projectIDRe.MatchString(projectID) {
		return nil, ErrInvalidPath
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.homeLocked(projectID), nil
}

func (p *ProjectHomes) homeLocked(projectID string) *Home {
	h, ok := p.homes[projectID]
	if !ok {
		h = newProjectHome(p.root, projectID)
		p.homes[projectID] = h
	}
	return h
}

// Load 读取项目的全部文件与权限位（同一次读锁内取得，二者一致）；没有文件时返回空 map。
func (p *ProjectHomes) Load(projectID string) (map[string][]byte, map[string]os.FileMode, error) {
	h, err := p.For(projectID)
	if err != nil {
		return nil, nil, err
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	list, err := h.listLocked()
	if err != nil {
		return nil, nil, err
	}
	files := make(map[string][]byte, len(list))
	modes := make(map[string]os.FileMode, len(list))
	for _, f := range list {
		b, err := os.ReadFile(h.abs(f.Path))
		if err != nil {
			return nil, nil, err
		}
		files[f.Path], modes[f.Path] = b, f.Mode
	}
	return files, modes, nil
}

// Remove 删除项目的配置目录（项目删除时调用）；目录不存在不报错。
func (p *ProjectHomes) Remove(projectID string) error {
	if !projectIDRe.MatchString(projectID) {
		return ErrInvalidPath
	}
	// 锁序：p.mu → h.mu（Home 的方法从不取 p.mu）。持 p.mu 期间不会有新的 For 拿到将被丢弃的 *Home。
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.homeLocked(projectID)
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(p.homes, projectID)
	return os.RemoveAll(h.dir)
}
