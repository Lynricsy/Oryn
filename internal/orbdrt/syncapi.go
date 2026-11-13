package orbdrt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// sync 接口（M5 计划 4.3，docs/protocol.md「orbd 本机 API（sync 特性）」）：把工作区（含未跟踪、遵守 .gitignore）
// 写成一个以 HEAD 为父的悬空快照提交，再把本地没有的对象打成 git bundle 供 `oryn sync` 取回。
// 全程不碰真实 index、不跑钩子。

const (
	// syncGitTimeout 是 sync 相关 git 调用的上限（首次 add -A 需要全量 hash，bundle 可能很大）。
	syncGitTimeout = 5 * time.Minute
	maxSyncHave    = 256
	maxSyncBundle  = 1 << 30
	// syncBundleTTL 之前的 sync-*.bundle 在下一次 prepare 时删除。
	syncBundleTTL = 10 * time.Minute
	// syncIndexName 是 <git-dir> 下的临时 index 文件名。
	syncIndexName   = "oryn-sync.index"
	syncRefSnapshot = "refs/oryn/sync/snapshot"
	syncRefHead     = "refs/oryn/sync/head"
)

var (
	fullShaRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	bundleIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// syncCommitEnv 固定快照提交的身份与时间：同一棵树 + 同一父提交 → 同一个 sha（客户端据此判断「没变化」）。
var syncCommitEnv = map[string]string{
	"GIT_AUTHOR_NAME": "Oryn Sync", "GIT_AUTHOR_EMAIL": "sync@oryn.invalid", "GIT_AUTHOR_DATE": "@0 +0000",
	"GIT_COMMITTER_NAME": "Oryn Sync", "GIT_COMMITTER_EMAIL": "sync@oryn.invalid", "GIT_COMMITTER_DATE": "@0 +0000",
}

// gitLong 同 git，但超时 5 分钟、可追加环境变量、stdout 上限 64MiB；非零退出码映射为 git_failed / git_busy。
func (r *Runtime) gitLong(ctx context.Context, dir string, env map[string]string, args ...string) (gitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, syncGitTimeout)
	defer cancel()
	full := append([]string{"-C", dir, "-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	e := r.baseEnv()
	e["GIT_OPTIONAL_LOCKS"] = "0"
	e["GIT_TERMINAL_PROMPT"] = "0"
	e["LC_ALL"] = "C"
	for k, v := range env {
		e[k] = v
	}
	cmd.Env = envList(e)
	var stderr tailBuffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return gitResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return gitResult{}, err
	}
	out, rerr := io.ReadAll(io.LimitReader(pipe, maxGitOutput+1))
	res := gitResult{out: out}
	if len(out) > maxGitOutput {
		res.out, res.truncated = out[:maxGitOutput], true
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, pipe)
	}
	werr := cmd.Wait()
	res.stderr = stderr.String()
	if ctx.Err() != nil {
		return res, errf(http.StatusInternalServerError, "git_failed", "git %s timed out", args[0])
	}
	if rerr != nil {
		return res, rerr
	}
	if res.truncated {
		return res, errf(http.StatusInternalServerError, "git_failed", "git %s: output exceeds %d bytes", args[0], maxGitOutput)
	}
	var ee *exec.ExitError
	switch {
	case werr == nil:
	case errors.As(werr, &ee):
		res.code = ee.ExitCode()
		if res.code < 0 {
			return res, errf(http.StatusInternalServerError, "git_failed", "git %s: %v", args[0], werr)
		}
		return res, gitFailure(args[0], res)
	default:
		return res, werr
	}
	return res, nil
}

func (r *Runtime) handleSyncPrepare(w http.ResponseWriter, req *http.Request) {
	var body apigen.OrbSyncPrepareRequest
	if err := readJSON(w, req, &body); err != nil {
		writeErr(w, err)
		return
	}
	st, err := r.syncPrepare(req.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// syncPrepare 实现 POST /sync/prepare：快照工作区、更新 refs/oryn/sync/*、求共同提交并按需打 bundle。
func (r *Runtime) syncPrepare(ctx context.Context, body apigen.OrbSyncPrepareRequest) (apigen.OrbSyncState, error) {
	var st apigen.OrbSyncState
	if len(body.Have) > maxSyncHave {
		return st, badRequest("have has %d entries (max %d)", len(body.Have), maxSyncHave)
	}
	for _, h := range body.Have {
		if !fullShaRe.MatchString(h) {
			return st, badRequest("invalid commit %q in have", h)
		}
	}
	root := ""
	if body.Root != nil {
		root = *body.Root
	}
	top, err := r.requireRepo(ctx, root)
	if err != nil {
		return st, err
	}
	r.syncMu.Lock()
	defer r.syncMu.Unlock()

	head, err := r.revParse(ctx, top, "HEAD^{commit}")
	if err != nil {
		return st, err
	}
	if head == "" {
		return st, errf(http.StatusConflict, "sync_no_commits", "repository %s has no commits", top)
	}
	st = apigen.OrbSyncState{Root: top, Head: head, Snapshot: head}
	res, err := r.git(ctx, top, nil, 4<<10, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return st, err
	}
	if b := strings.TrimSpace(string(res.out)); res.code == 0 && b != "" {
		st.Branch = &b
	}
	gitDirOut, _, err := r.gitOK(ctx, top, nil, 64<<10, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return st, err
	}
	tree, err := r.syncTree(ctx, top, strings.TrimSpace(string(gitDirOut)))
	if err != nil {
		return st, err
	}
	headTree, err := r.revParse(ctx, top, "HEAD^{tree}")
	if err != nil {
		return st, err
	}
	if tree != headTree {
		res, err := r.gitLong(ctx, top, syncCommitEnv, "commit-tree", "--no-gpg-sign", tree, "-p", head, "-m", "oryn sync snapshot")
		if err != nil {
			return st, err
		}
		st.Snapshot, st.Dirty = strings.TrimSpace(string(res.out)), true
	}
	for ref, sha := range map[string]string{syncRefSnapshot: st.Snapshot, syncRefHead: head} {
		if _, _, err := r.gitOK(ctx, top, nil, 4<<10, "update-ref", ref, sha); err != nil {
			return st, err
		}
	}
	if st.Common, err = r.syncCommon(ctx, top, body.Have); err != nil {
		return st, err
	}
	r.sweepSyncBundles()
	if slices.Contains(body.Have, st.Snapshot) {
		return st, nil
	}
	if len(st.Common) == 0 {
		if body.Force == nil || !*body.Force {
			return st, errf(http.StatusConflict, "sync_no_common", "none of the local commits exist in the orb repository")
		}
		out, _, err := r.gitOK(ctx, top, nil, 4<<10, "rev-parse", "--is-shallow-repository")
		if err != nil {
			return st, err
		}
		if strings.TrimSpace(string(out)) == "true" {
			return st, errf(http.StatusConflict, "sync_shallow", "the orb repository is a shallow clone; a full bundle cannot be built")
		}
	}
	bundle, err := r.syncBundle(ctx, top, st.Common)
	if err != nil {
		return st, err
	}
	st.Bundle = bundle
	return st, nil
}

// syncTree 用临时 index 把工作区写成树：以真实 index 的副本为初值（带 stat 缓存，且已跟踪集合与真实 index 一致——
// 被忽略但已跟踪的文件仍在树里），没有真实 index 时 read-tree HEAD；用完删除。
func (r *Runtime) syncTree(ctx context.Context, top, gitDir string) (string, error) {
	idx := filepath.Join(gitDir, syncIndexName)
	defer os.Remove(idx)
	env := map[string]string{"GIT_INDEX_FILE": idx}
	if b, err := os.ReadFile(filepath.Join(gitDir, "index")); err == nil {
		if err := os.WriteFile(idx, b, 0o600); err != nil {
			return "", fsError(err)
		}
	} else if _, err := r.gitLong(ctx, top, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := r.gitLong(ctx, top, env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	res, err := r.gitLong(ctx, top, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.out)), nil
}

// syncCommon 返回 have 中本仓库已有的提交（保持 have 的顺序、去重）；一次 cat-file --batch-check 判定。
func (r *Runtime) syncCommon(ctx context.Context, top string, have []string) ([]string, error) {
	common := []string{}
	if len(have) == 0 {
		return common, nil
	}
	out, _, err := r.gitOK(ctx, top, []byte(strings.Join(have, "\n")+"\n"), 1<<20, "cat-file", "--batch-check")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "commit" && fullShaRe.MatchString(f[0]) && !seen[f[0]] {
			seen[f[0]] = true
			common = append(common, f[0])
		}
	}
	return common, nil
}

// syncBundle 打包 refs/oryn/sync/{snapshot,head}，排除 common 可达的对象；没有需要传的对象时返回 nil。
func (r *Runtime) syncBundle(ctx context.Context, top string, common []string) (*apigen.OrbSyncBundle, error) {
	if err := os.MkdirAll(r.tmpDir(), 0o700); err != nil {
		return nil, fsError(err)
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:])
	path := r.syncBundlePath(id)
	args := []string{"bundle", "create", "--quiet", path, syncRefSnapshot, syncRefHead}
	if len(common) > 0 {
		args = append(append(args, "--not"), common...)
	}
	if _, err := r.gitLong(ctx, top, nil, args...); err != nil {
		_ = os.Remove(path)
		var ae *apiError
		if errors.As(err, &ae) && strings.Contains(ae.msg, "empty bundle") {
			// 对端已有全部对象（快照可从某个共同提交到达）
			return nil, nil
		}
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fsError(err)
	}
	if fi.Size() > maxSyncBundle {
		_ = os.Remove(path)
		return nil, errf(http.StatusRequestEntityTooLarge, "too_large", "bundle is %d bytes (max %d)", fi.Size(), maxSyncBundle)
	}
	return &apigen.OrbSyncBundle{ID: id, Size: fi.Size()}, nil
}

func (r *Runtime) syncBundlePath(id string) string {
	return filepath.Join(r.tmpDir(), "sync-"+id+".bundle")
}

// sweepSyncBundles 删除 10 分钟前的 sync-*.bundle。
func (r *Runtime) sweepSyncBundles() {
	names, _ := filepath.Glob(filepath.Join(r.tmpDir(), "sync-*.bundle"))
	for _, n := range names {
		if fi, err := os.Stat(n); err == nil && time.Since(fi.ModTime()) > syncBundleTTL {
			_ = os.Remove(n)
		}
	}
}

// handleSyncBundle 实现 GET /sync/bundle?id=。
func (r *Runtime) handleSyncBundle(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	if !bundleIDRe.MatchString(id) {
		writeErr(w, badRequest("invalid bundle id %q", id))
		return
	}
	f, err := os.Open(r.syncBundlePath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeErr(w, errf(http.StatusNotFound, "sync_bundle_gone", "bundle %s no longer exists", id))
			return
		}
		writeErr(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, req, "", fi.ModTime(), f)
}
