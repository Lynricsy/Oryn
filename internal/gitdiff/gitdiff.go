// Package gitdiff 解析 git diff 的输出（M24）：orbd 的改动接口与控制面的项目仓库读接口共用。
// 只有纯函数，不执行 git：两边的执行层（orb 内的二进制与环境、控制面的隔离环境与凭据注入）各自不同。
package gitdiff

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// NULFields 按 NUL 切分 `-z` 输出（去掉末尾的 NUL）；空输出返回 nil。
func NULFields(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

var statusOf = map[byte]apigen.GitFileChangeStatus{
	'A': apigen.GitFileChangeStatusAdded,
	'M': apigen.GitFileChangeStatusModified,
	'D': apigen.GitFileChangeStatusDeleted,
	'R': apigen.GitFileChangeStatusRenamed,
	'C': apigen.GitFileChangeStatusCopied,
	'T': apigen.GitFileChangeStatusTypechange,
	'U': apigen.GitFileChangeStatusConflicted,
}

// Status 把 name-status / porcelain 的状态字母映射成改动状态；未知字母按 modified。
func Status(code byte) apigen.GitFileChangeStatus {
	if s, ok := statusOf[code]; ok {
		return s
	}
	return apigen.GitFileChangeStatusModified
}

// ParseChanges 解析同一范围的 `diff -M --name-status -z` 与 `diff -M --numstat -z` 输出：byPath 按新路径索引，
// order 是 name-status 中的出现顺序。Staged 恒为 none；二进制文件置 Binary，不给行数。
func ParseChanges(nameStatus, numstat []byte) (byPath map[string]*apigen.GitFileChange, order []string) {
	byPath = map[string]*apigen.GitFileChange{}
	toks := NULFields(nameStatus)
	for i := 0; i < len(toks); i++ {
		code := toks[i]
		if code == "" || i+1 >= len(toks) {
			break
		}
		c := &apigen.GitFileChange{Staged: apigen.GitFileChangeStagedNone, Status: Status(code[0])}
		if code[0] == 'R' || code[0] == 'C' {
			if i+2 >= len(toks) {
				break
			}
			old := toks[i+1]
			c.OldPath, c.Path = &old, toks[i+2]
			i += 2
		} else {
			c.Path = toks[i+1]
			i++
		}
		if _, ok := byPath[c.Path]; !ok {
			order = append(order, c.Path)
		}
		byPath[c.Path] = c
	}
	toks = NULFields(numstat)
	for i := 0; i < len(toks); i++ {
		parts := strings.SplitN(toks[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" { // 重命名 / 复制：其后两个字段是旧路径与新路径
			if i+2 >= len(toks) {
				break
			}
			path = toks[i+2]
			i += 2
		}
		c := byPath[path]
		if c == nil {
			continue
		}
		if parts[0] == "-" && parts[1] == "-" {
			c.Binary = true
			continue
		}
		a, aerr := strconv.Atoi(parts[0])
		d, derr := strconv.Atoi(parts[1])
		if aerr == nil && derr == nil {
			c.Additions, c.Deletions = &a, &d
		}
	}
	return byPath, order
}

// IsBinaryPatch 识别 git 对二进制文件输出的 `Binary files … differ` 行。
func IsBinaryPatch(patch []byte) bool {
	for line := range bytes.SplitSeq(patch, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte("Binary files ")) && bytes.HasSuffix(line, []byte(" differ")) {
			return true
		}
		if bytes.HasPrefix(line, []byte("@@")) {
			return false
		}
	}
	return false
}

// CutTruncated 把被截断的 patch 截到最后一个换行（不留半行）；没有换行时返回 nil。
func CutTruncated(out []byte) []byte {
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		return out[:i+1]
	}
	return nil
}
