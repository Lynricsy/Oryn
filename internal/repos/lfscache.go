package repos

import (
	"container/list"
	"errors"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Lynricsy/Oryn/internal/lfs"
)

// lfsCache 是镜像项目的 LFS 对象缓存 <Dir>/.lfs-cache/<projectID>/<oid[0:2]>/<oid[2:4]>/<oid>（M24 阶段 ② 补充）。
// 所有项目合计不超过 max（软上限：并发下载期间最多再超出 lfsConcurrency×lfsInlineMax），淘汰最久未用的；
// 命中时更新文件 mtime，重启后按 mtime 恢复顺序。托管仓库的对象不在这里（步骤 3.13）。
type lfsCache struct {
	dir string
	max int64

	mu    sync.Mutex
	total int64
	lru   *list.List               // *lfsEntry，表头 = 最近使用
	byKey map[string]*list.Element // projectID + "/" + oid

	bg sync.WaitGroup // removeProject 的后台删除（测试等它结束）
}

type lfsEntry struct {
	key  string
	size int64
}

// trashMarker 是待删目录名里的标记：<projectID>.trash-<rand>。
const trashMarker = ".trash-"

// openLFSCache 打开（必要时新建）缓存目录：删掉上次没删完的 *.trash-* 目录与各项目下的 tmp-* 残留，
// 其余合法对象按 mtime 从新到旧入表，超出上限的立即淘汰。
func openLFSCache(dir string, max int64) (*lfsCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &lfsCache{dir: dir, max: max, lru: list.New(), byKey: map[string]*list.Element{}}
	top, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type found struct {
		key   string
		size  int64
		mtime time.Time
	}
	var objs []found
	for _, d := range top {
		p := filepath.Join(dir, d.Name())
		if strings.Contains(d.Name(), trashMarker) {
			if err := os.RemoveAll(p); err != nil {
				slog.Warn("删除 LFS 缓存残留目录失败", "path", p, "err", err)
			}
			continue
		}
		if !d.IsDir() {
			continue
		}
		pid := d.Name()
		err := filepath.WalkDir(p, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			name := e.Name()
			if strings.HasPrefix(name, "tmp-") {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					slog.Warn("删除 LFS 缓存临时文件失败", "path", path, "err", err)
				}
				return nil
			}
			if !lfs.ValidOID(name) || path != c.path(pid, name) || !e.Type().IsRegular() {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil // 扫描期间被删
			}
			objs = append(objs, found{pid + "/" + name, info.Size(), info.ModTime()})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.SortFunc(objs, func(a, b found) int { return b.mtime.Compare(a.mtime) })
	for _, o := range objs {
		c.byKey[o.key] = c.lru.PushBack(&lfsEntry{key: o.key, size: o.size})
		c.total += o.size
	}
	c.mu.Lock()
	c.evictLocked(0)
	c.mu.Unlock()
	return c, nil
}

// path 是对象在缓存里的位置（文件可能不存在）。
func (c *lfsCache) path(projectID, oid string) string {
	return lfs.ObjectPath(filepath.Join(c.dir, projectID), oid)
}

// get 返回已缓存对象的路径并把它记为最近使用；文件可能已被外部删除，打开时 ENOENT 的调用方应 drop。
func (c *lfsCache) get(projectID, oid string) (string, bool) {
	c.mu.Lock()
	el, ok := c.byKey[projectID+"/"+oid]
	if ok {
		c.lru.MoveToFront(el)
	}
	c.mu.Unlock()
	if !ok {
		return "", false
	}
	p := c.path(projectID, oid)
	now := time.Now()
	_ = os.Chtimes(p, now, now) // 只影响重启后的淘汰顺序，失败无妨
	return p, true
}

// reserve 从最久未用的一端淘汰，直到再放入 size 字节也不超过上限（或缓存已空）。
func (c *lfsCache) reserve(size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictLocked(size)
}

// add 登记一个已落盘的对象（调用方先 reserve）。已登记时只更新大小并记为最近使用。
func (c *lfsCache) add(projectID, oid string, size int64) {
	key := projectID + "/" + oid
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[key]; ok {
		e := el.Value.(*lfsEntry)
		c.total += size - e.size
		e.size = size
		c.lru.MoveToFront(el)
		return
	}
	c.byKey[key] = c.lru.PushFront(&lfsEntry{key: key, size: size})
	c.total += size
}

// drop 忘掉一个对象（文件已被外部删除时用），不动磁盘。
func (c *lfsCache) drop(projectID, oid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[projectID+"/"+oid]; ok {
		c.unlinkLocked(el)
	}
}

// removeProject 去掉项目的全部条目；目录改名为 <projectID>.trash-<rand> 后在后台删除。
func (c *lfsCache) removeProject(projectID string) {
	prefix := projectID + "/"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.byKey {
		if strings.HasPrefix(key, prefix) {
			c.unlinkLocked(el)
		}
	}
	src := filepath.Join(c.dir, projectID)
	trash := src + trashMarker + strconv.FormatUint(rand.Uint64(), 36)
	if err := os.Rename(src, trash); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("移走项目的 LFS 缓存失败", "project", projectID, "err", err)
		}
		return
	}
	c.bg.Go(func() {
		if err := os.RemoveAll(trash); err != nil {
			slog.Warn("删除项目的 LFS 缓存失败", "path", trash, "err", err)
		}
	})
}

// prune 删掉 keep 返回假的项目的缓存目录（项目已不存在时）。keep 在锁外调用，可以读库。
func (c *lfsCache) prune(keep func(projectID string) bool) {
	ents, err := os.ReadDir(c.dir)
	if err != nil {
		slog.Warn("读取 LFS 缓存目录失败", "dir", c.dir, "err", err)
		return
	}
	for _, d := range ents {
		if d.IsDir() && !strings.Contains(d.Name(), trashMarker) && !keep(d.Name()) {
			c.removeProject(d.Name())
		}
	}
}

// evictLocked 从表尾删除对象，直到 total+size ≤ max 或表空。正被读取的文件删除后仍可读完（已打开的句柄）。
func (c *lfsCache) evictLocked(size int64) {
	for c.total+size > c.max {
		el := c.lru.Back()
		if el == nil {
			return
		}
		key := el.Value.(*lfsEntry).key
		c.unlinkLocked(el)
		pid, oid, _ := strings.Cut(key, "/")
		if err := os.Remove(c.path(pid, oid)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("淘汰 LFS 缓存对象失败", "key", key, "err", err)
		}
	}
}

func (c *lfsCache) unlinkLocked(el *list.Element) {
	e := el.Value.(*lfsEntry)
	c.lru.Remove(el)
	delete(c.byKey, e.key)
	c.total -= e.size
}
