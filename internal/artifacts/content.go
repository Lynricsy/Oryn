package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// contents 是按内容寻址的文件存储：<dir>/content/<sha256 前 2 位>/<sha256>，临时文件在 <dir>/.tmp/。
// 同内容只存一份；某个内容文件只要还有 artifact_files 行引用就不删。
type contents struct{ dir string }

func newContents(dir string) (*contents, error) {
	for _, d := range []string{dir, filepath.Join(dir, "content"), filepath.Join(dir, ".tmp")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	return &contents{dir: dir}, nil
}

func (c *contents) path(sha string) string {
	return filepath.Join(c.dir, "content", sha[:2], sha)
}

// stored 是 put 的结果。data 只在调用方要求保留时非空。
type stored struct {
	sha  string
	size int64
	data []byte
}

var errFileTooLarge = errors.New("artifacts: file too large")

// put 把 r 流式写入临时文件并算 sha256，再 rename 到内容路径；同内容已存在时丢弃临时文件并刷新其 mtime
// （避免清理在「刚复用、还没插行」的窗口里把它删掉）。超过 limit 字节返回 errFileTooLarge。keep 为真时
// 同时在内存里保留字节（HTML 校验、canvas.json 解析等需要）。
func (c *contents) put(r io.Reader, limit int64, keep bool) (stored, error) {
	tmp, err := os.CreateTemp(filepath.Join(c.dir, ".tmp"), "put-*")
	if err != nil {
		return stored{}, err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	var buf *bytes.Buffer
	if keep {
		buf = &bytes.Buffer{}
		w = io.MultiWriter(tmp, h, buf)
	}
	n, err := io.Copy(w, io.LimitReader(r, limit+1))
	if err != nil {
		return stored{}, err
	}
	if n > limit {
		return stored{}, errFileTooLarge
	}
	if err := tmp.Close(); err != nil {
		return stored{}, err
	}
	out := stored{sha: hex.EncodeToString(h.Sum(nil)), size: n}
	if buf != nil {
		out.data = buf.Bytes()
	}
	dst := c.path(out.sha)
	if _, err := os.Stat(dst); err == nil {
		now := time.Now()
		_ = os.Chtimes(dst, now, now)
		_ = os.Remove(tmp.Name())
		done = true
		return out, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return stored{}, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return stored{}, err
	}
	done = true
	return out, nil
}

// open 打开内容文件。
func (c *contents) open(sha string) (*os.File, error) {
	if len(sha) != 64 {
		return nil, os.ErrNotExist
	}
	return os.Open(c.path(sha))
}

// read 读出内容文件的全部字节（调用方保证文件不大于 MaxFileBytes）。
func (c *contents) read(sha string) ([]byte, error) {
	if len(sha) != 64 {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(c.path(sha))
}

// gc 删除早于 cutoff 且没有任何行引用的内容文件，以及早于 cutoff 的临时文件。
func (c *contents) gc(ctx context.Context, referenced func(ctx context.Context, sha string) (bool, error), cutoff time.Time) {
	root := filepath.Join(c.dir, "content")
	shards, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, sh := range shards {
		if !sh.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, sh.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil || !fi.ModTime().Before(cutoff) {
				continue
			}
			used, err := referenced(ctx, e.Name())
			if err != nil {
				slog.Warn("artifacts: 检查内容引用失败", "sha256", e.Name(), "err", err)
				continue
			}
			if !used {
				if err := os.Remove(filepath.Join(root, sh.Name(), e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
					slog.Warn("artifacts: 删除内容文件失败", "sha256", e.Name(), "err", err)
				}
			}
		}
	}
	tmpDir := filepath.Join(c.dir, ".tmp")
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(tmpDir, e.Name()))
		}
	}
}
