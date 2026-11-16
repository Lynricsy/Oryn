package memory

import "strings"

// TestedMinor 是 Oryn 已验证的 Hindsight 小版本线（R8：锁定 1.2，补丁版本自动跟随）。
const TestedMinor = "1.2"

// MinorOf 取版本号的「主.次」部分（"1.2.1" → "1.2"，"v1.3.0rc1" → "1.3"）；无法解析时返回空串。
func MinorOf(version string) string {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 || !isDigits(parts[0]) {
		return ""
	}
	minor := parts[1]
	if i := strings.IndexFunc(minor, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		minor = minor[:i]
	}
	if minor == "" {
		return ""
	}
	return parts[0] + "." + minor
}

// VersionMismatch 判断服务端版本是否不在已验证的小版本线上；版本无法解析时也视为不一致。
func VersionMismatch(version string) bool { return MinorOf(version) != TestedMinor }

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
