package egress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// ErrPushCert 表示推送带签名证书（push-cert）；其命令段格式不同，门禁不放行。
var ErrPushCert = errors.New("egress: 不支持 push-cert 推送")

// RefUpdate 是 git-receive-pack 命令段里的一条 ref 更新。
type RefUpdate struct{ Old, New, Ref string }

// sidebandMax 是 side-band-64k 单个 pkt 的数据上限（LARGE_PACKET_MAX 65520 - 4 字节长度 - 1 字节 band）。
const sidebandMax = 65520 - 4 - 1

// ParseReceivePack 读取 git-receive-pack 请求体开头的命令段（至 flush pkt），最多 max 字节。
// 规则：空命令段（直接 0000）返回空切片；跳过 `shallow <sha>` 行；去掉首条命令 NUL 后的能力串并返回；
// 遇到 push-cert 返回 ErrPushCert；超过 max 或格式错误返回 ErrUnparsable。prefix 是已读取的字节，调用方用
// io.MultiReader(bytes.NewReader(prefix), r) 还原请求体。
func ParseReceivePack(r io.Reader, max int) (cmds []RefUpdate, caps []string, prefix []byte, err error) {
	cmds = []RefUpdate{}
	// read 精确读取 n 字节追加到 prefix；超过 max 时不读，保证 prefix 与已消费的字节一致。
	read := func(n int) ([]byte, error) {
		if len(prefix)+n > max {
			return nil, ErrUnparsable
		}
		start := len(prefix)
		prefix = slices.Grow(prefix, n)[:start+n]
		k, err := io.ReadFull(r, prefix[start:])
		prefix = prefix[:start+k]
		if err != nil {
			return nil, ErrUnparsable
		}
		return prefix[start:], nil
	}
	for {
		h, err := read(4)
		if err != nil {
			return nil, nil, prefix, err
		}
		n, perr := strconv.ParseUint(string(h), 16, 16)
		if perr != nil {
			return nil, nil, prefix, ErrUnparsable
		}
		if n == 0 {
			return cmds, caps, prefix, nil
		}
		if n < 4 {
			return nil, nil, prefix, ErrUnparsable
		}
		payload, err := read(int(n) - 4)
		if err != nil {
			return nil, nil, prefix, err
		}
		line := string(bytes.TrimSuffix(payload, []byte("\n")))
		if sha, ok := strings.CutPrefix(line, "shallow "); ok {
			// shallow 行只能出现在命令之前
			if len(cmds) > 0 || !isOID(sha) {
				return nil, nil, prefix, ErrUnparsable
			}
			continue
		}
		cmd, capStr, hasCaps := strings.Cut(line, "\x00")
		if cmd == "push-cert" {
			return nil, nil, prefix, ErrPushCert
		}
		if hasCaps {
			if len(cmds) > 0 {
				return nil, nil, prefix, ErrUnparsable
			}
			caps = strings.Fields(capStr)
		}
		parts := strings.Split(cmd, " ")
		if len(parts) != 3 || !isOID(parts[0]) || len(parts[0]) != len(parts[1]) || !isOID(parts[1]) || parts[2] == "" {
			return nil, nil, prefix, ErrUnparsable
		}
		cmds = append(cmds, RefUpdate{Old: parts[0], New: parts[1], Ref: parts[2]})
	}
}

// isOID 报告 s 是否为 SHA-1（40）或 SHA-256（64）的小写十六进制对象名。
func isOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ReportStatusNG 生成拒绝推送的 receive-pack 结果：`unpack ok` + 每个 ref 的 `ng <ref> <reason>` + flush；
// sideband 为真时包在 band 1 里并以 flush 结束（与附录 B-7 实测格式一致）。
func ReportStatusNG(refs []string, reason string, sideband bool) []byte {
	reason = strings.ReplaceAll(reason, "\n", " ")
	var b []byte
	b = appendPkt(b, "unpack ok\n")
	for _, ref := range refs {
		b = appendPkt(b, "ng "+ref+" "+reason+"\n")
	}
	b = append(b, "0000"...)
	if !sideband {
		return b
	}
	out := make([]byte, 0, len(b)+len(b)/sidebandMax*5+9)
	for len(b) > 0 {
		k := min(len(b), sidebandMax)
		out = fmt.Appendf(out, "%04x\x01", k+5)
		out = append(out, b[:k]...)
		b = b[k:]
	}
	return append(out, "0000"...)
}

func appendPkt(b []byte, s string) []byte {
	b = fmt.Appendf(b, "%04x", len(s)+4)
	return append(b, s...)
}
