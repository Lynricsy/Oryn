package orbdrt

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/link"
)

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// blobFetch 查找 $HOME/.omp/agent/blobs/<sha256>；找到时在同一 link 上另开 `blob` 流发送原始字节。
func (r *Runtime) blobFetch(ctx context.Context, c *conn, p apigen.LinkCtlBlobFetchParams) (apigen.LinkCtlBlobFetchResult, error) {
	if !sha256Re.MatchString(p.Sha256) {
		return apigen.LinkCtlBlobFetchResult{}, ctlErr(link.CodeInvalidParams, "invalid sha256")
	}
	path := filepath.Join(r.home, ".omp", "agent", "blobs", p.Sha256)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return apigen.LinkCtlBlobFetchResult{Found: false}, nil
		}
		return apigen.LinkCtlBlobFetchResult{}, ctlErr("failed", "%v", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return apigen.LinkCtlBlobFetchResult{Found: false}, nil
	}
	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := c.sess.Open(octx, link.StreamHeader{T: link.StreamBlob, SHA256: p.Sha256, Size: fi.Size()})
	cancel()
	if err != nil {
		f.Close()
		return apigen.LinkCtlBlobFetchResult{}, ctlErr("failed", "open blob stream: %v", err)
	}
	go func() {
		defer f.Close()
		defer st.Close()
		if _, err := io.CopyN(st, f, fi.Size()); err != nil {
			r.log.Warn("blob stream failed", "sha256", p.Sha256, "err", err)
		}
	}()
	return apigen.LinkCtlBlobFetchResult{Found: true}, nil
}

// fsCopyTree 把 from（文件或目录）复制到 to，已存在的文件被覆盖；两端都必须在 $HOME 之下。
func (r *Runtime) fsCopyTree(from, to string) (apigen.LinkCtlOkResult, error) {
	src, dst, err := r.fsPair(from, to)
	if err != nil {
		return apigen.LinkCtlOkResult{}, err
	}
	if err := copyTree(src, dst); err != nil {
		return apigen.LinkCtlOkResult{}, ctlErr("failed", "copy: %v", err)
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

// fsMove 移动 from 到 to。from 是文件且 to 是已存在目录时移入该目录；
// 移动会话文件 `X.jsonl` 时同名产物目录 `X/` 随之移动。
func (r *Runtime) fsMove(from, to string) (apigen.LinkCtlOkResult, error) {
	src, dst, err := r.fsPair(from, to)
	if err != nil {
		return apigen.LinkCtlOkResult{}, err
	}
	sfi, err := os.Lstat(src)
	if err != nil {
		return apigen.LinkCtlOkResult{}, ctlErr("not_found", "%v", err)
	}
	if dfi, err := os.Stat(dst); err == nil && dfi.IsDir() && !sfi.IsDir() {
		dst = filepath.Join(dst, filepath.Base(src))
	}
	if err := movePath(src, dst); err != nil {
		return apigen.LinkCtlOkResult{}, ctlErr("failed", "move: %v", err)
	}
	if !sfi.IsDir() && strings.HasSuffix(src, ".jsonl") && strings.HasSuffix(dst, ".jsonl") {
		art := strings.TrimSuffix(src, ".jsonl")
		if fi, err := os.Stat(art); err == nil && fi.IsDir() {
			if err := movePath(art, strings.TrimSuffix(dst, ".jsonl")); err != nil {
				return apigen.LinkCtlOkResult{}, ctlErr("failed", "move artifacts: %v", err)
			}
		}
	}
	return apigen.LinkCtlOkResult{Ok: true}, nil
}

func (r *Runtime) fsPair(from, to string) (string, string, error) {
	src, err := r.safePath(from)
	if err != nil {
		return "", "", ctlErr(link.CodeInvalidParams, "from: %v", err)
	}
	dst, err := r.safePath(to)
	if err != nil {
		return "", "", ctlErr(link.CodeInvalidParams, "to: %v", err)
	}
	if _, err := os.Lstat(src); err != nil {
		return "", "", ctlErr("not_found", "%v", err)
	}
	if rel, err := filepath.Rel(src, dst); err == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
		return "", "", ctlErr(link.CodeInvalidParams, "destination inside source")
	}
	return src, dst, nil
}

func movePath(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := os.Rename(src, dst)
	if errors.Is(err, syscall.EXDEV) {
		if err := copyTree(src, dst); err != nil {
			return err
		}
		return os.RemoveAll(src)
	}
	return err
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return nil // 设备、管道、套接字跳过
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(dst, mode)
	}
	return err
}
