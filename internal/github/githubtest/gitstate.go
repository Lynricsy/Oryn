package githubtest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 本文件让 GitRoot 下的裸仓库参与 API 应答（M15 步骤 9.2 端到端）：经 git smart-HTTP 推送的分支与 tag 在
// GET …/git/ref/{ref} 里可见（Repo.Refs 没有该 ref 时才读裸仓库）、fork 带上自己的裸仓库、createPullRequest
// 按 head 仓库的裸仓库填 head 提交。GitRoot 为空时行为与之前完全相同。

// InitRepo 在 GitRoot 下为仓库 fullName 建裸仓库（main 上一个含 README.md 的初始提交；已存在则不动）。
func (s *Server) InitRepo(fullName string) error {
	if s.GitRoot == "" {
		return errors.New("githubtest: GitRoot 为空")
	}
	return s.initBare(fullName)
}

// BarePath 返回仓库 fullName 的裸仓库目录（一律小写）；GitRoot 为空时为空串。
func (s *Server) BarePath(fullName string) string {
	if s.GitRoot == "" {
		return ""
	}
	return s.barePath(fullName)
}

// gitOut 在裸仓库 dir 上执行 git 并返回去掉首尾空白的标准输出（不读用户与系统配置）。
func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--git-dir", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+os.TempDir())
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// bareRef 读仓库 fullName 的裸仓库里的 ref（键形如 heads/main、tags/v1）：返回对象 id 与类型（commit / tag）。
// 没有裸仓库或没有该 ref 时 ok 为假。
func (s *Server) bareRef(fullName, ref string) (sha, typ string, ok bool) {
	if s.GitRoot == "" {
		return "", "", false
	}
	out, err := gitOut(s.barePath(fullName), "for-each-ref", "--format=%(refname) %(objectname) %(objecttype)", "refs/"+ref)
	if err != nil {
		return "", "", false
	}
	// for-each-ref 的模式按前缀匹配（refs/heads/a 也匹配 refs/heads/a/b），只认完全相同的 ref
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[0] == "refs/"+ref {
			return f[1], f[2], true
		}
	}
	return "", "", false
}

// bareTagCommit 把仓库 fullName 裸仓库里的附注 tag 对象 oid 剥到提交；oid 不是 tag 对象时 ok 为假。
func (s *Server) bareTagCommit(fullName, oid string) (string, bool) {
	if s.GitRoot == "" {
		return "", false
	}
	dir := s.barePath(fullName)
	if typ, err := gitOut(dir, "cat-file", "-t", oid); err != nil || typ != "tag" {
		return "", false
	}
	commit, err := gitOut(dir, "rev-parse", "--verify", "-q", oid+"^{commit}")
	return commit, err == nil && commit != ""
}

// forkBare 为 fork 建裸仓库：从上游的裸仓库克隆并开放推送（已存在则不动）。GitRoot 为空或上游没有裸仓库时什么都不做。
func (s *Server) forkBare(parent, fork string) error {
	if s.GitRoot == "" {
		return nil
	}
	src, dst := s.barePath(parent), s.barePath(fork)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	cmd := exec.Command("git", "clone", "--bare", "-q", src, dst)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+os.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone --bare %s: %v: %s", parent, err, out)
	}
	_, err := gitOut(dst, "config", "http.receivepack", "true")
	return err
}

// pullHead 按 createPullRequest 的 input 求 head 所在仓库与 head 提交：headRepositoryId 优先，其次 headRefName 的
// `owner:` 前缀（owner 名下与 base 同名的仓库），否则与 base 同仓库；head 提交取该仓库裸仓库里的分支（没有时为空）。调用方持锁。
func (s *Server) pullHead(base *Repo, headRepoNode, headRefName string) (repoID int64, branch, sha string) {
	head := base
	owner, branch, fork := strings.Cut(headRefName, ":")
	if !fork {
		branch = headRefName
	}
	switch {
	case headRepoNode != "":
		if rp := s.repoByNode(headRepoNode); rp != nil {
			head = rp
		}
	case fork && !strings.EqualFold(owner, strings.Split(base.FullName, "/")[0]):
		_, name, _ := strings.Cut(base.FullName, "/")
		if rp, _ := s.lookupRepo(owner, name); rp != nil {
			head = rp
		}
	}
	if sha, typ, ok := s.bareRef(head.FullName, "heads/"+branch); ok && typ == "commit" {
		return head.ID, branch, sha
	}
	return head.ID, branch, ""
}
