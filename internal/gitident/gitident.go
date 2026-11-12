// Package gitident 校验 orb 内 git 提交身份（user.name / user.email）。
// 控制面 REST、orbd 与 CLI 共用同一规则：名字 1–200 字符，邮箱 3–320 字符且含 `@`，均不得含换行或控制字符。
package gitident

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validate 检查一组身份；返回的错误信息可直接给用户看。
func Validate(name, email string) error {
	if n := utf8.RuneCountInString(name); n < 1 || n > 200 {
		return errors.New("git 名字长度须为 1–200")
	}
	if n := utf8.RuneCountInString(email); n < 3 || n > 320 {
		return errors.New("git 邮箱长度须为 3–320")
	}
	if !strings.Contains(email, "@") {
		return errors.New("git 邮箱须包含 @")
	}
	if hasControl(name) || hasControl(email) {
		return errors.New("git 名字与邮箱不得含换行或控制字符")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("git 名字不能为空白")
	}
	return nil
}

func hasControl(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}
