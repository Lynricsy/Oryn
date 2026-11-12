package orbdrt

import (
	"encoding/json"
	"log/slog"
	"maps"
	"os"
	"sync"
)

// registry 记录本 orb 拥有的线程及其会话目录（$HOME/.oryn/orbd/threads.json）。
//
// 文件带 orbId：从快照派生的新 orb 会继承源 orb 的会话目录与此文件，orbId 不符时整体忽略，
// 避免新 orb 把不属于自己的线程上报给控制面（控制面会因归属校验失败而断开 link）。
type registry struct {
	path  string
	orbID string

	mu      sync.Mutex
	threads map[string]string
}

type registryFile struct {
	OrbID   string            `json:"orbId"`
	Threads map[string]string `json:"threads"`
}

func loadRegistry(path, orbID string) *registry {
	r := &registry{path: path, orbID: orbID, threads: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var f registryFile
	if err := json.Unmarshal(b, &f); err != nil {
		slog.Warn("orbd: ignoring corrupt thread registry", "path", path, "err", err)
		return r
	}
	if f.OrbID != orbID {
		slog.Info("orbd: thread registry belongs to another orb (snapshot); starting fresh", "registryOrb", f.OrbID)
		return r
	}
	for id, dir := range f.Threads {
		r.threads[id] = dir
	}
	return r
}

func (r *registry) list() map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.threads)
}

func (r *registry) add(id, dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.threads[id] == dir {
		return nil
	}
	r.threads[id] = dir
	return r.saveLocked()
}

func (r *registry) remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.threads[id]; !ok {
		return nil
	}
	delete(r.threads, id)
	return r.saveLocked()
}

func (r *registry) saveLocked() error {
	b, err := json.Marshal(registryFile{OrbID: r.orbID, Threads: r.threads})
	if err != nil {
		return err
	}
	return writeFileAtomic(r.path, b, 0o600)
}
