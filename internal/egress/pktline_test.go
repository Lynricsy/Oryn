package egress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// pkt 把若干行编码为 pkt-line，"0000" 原样作为 flush。
func pkt(lines ...string) []byte {
	var b []byte
	for _, l := range lines {
		if l == "0000" {
			b = append(b, l...)
			continue
		}
		b = fmt.Appendf(b, "%04x%s", len(l)+4, l)
	}
	return b
}

const (
	oidA = "1111111111111111111111111111111111111111"
	oidB = "2222222222222222222222222222222222222222"
	oidZ = "0000000000000000000000000000000000000000"
)

func TestParseReceivePack(t *testing.T) {
	packData := "PACK\x00\x00\x00\x02rest-of-pack"
	cases := []struct {
		name     string
		body     []byte
		max      int
		wantCmds []RefUpdate
		wantCaps []string
		wantErr  error
	}{
		{name: "正常",
			body: pkt(oidA+" "+oidB+" refs/heads/main\x00report-status side-band-64k agent=git/2.47\n", oidZ+" "+oidA+" refs/heads/feat\n", "0000"),
			max:  1 << 16,
			wantCmds: []RefUpdate{
				{Old: oidA, New: oidB, Ref: "refs/heads/main"},
				{Old: oidZ, New: oidA, Ref: "refs/heads/feat"},
			},
			wantCaps: []string{"report-status", "side-band-64k", "agent=git/2.47"}},
		{name: "空命令段", body: pkt("0000"), max: 1 << 16, wantCmds: []RefUpdate{}},
		{name: "shallow 行",
			body:     pkt("shallow "+oidA+"\n", oidA+" "+oidB+" refs/heads/x\x00report-status\n", "0000"),
			max:      1 << 16,
			wantCmds: []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/heads/x"}},
			wantCaps: []string{"report-status"}},
		{name: "无换行无能力", body: pkt(oidA+" "+oidB+" refs/tags/v1", "0000"), max: 1 << 16,
			wantCmds: []RefUpdate{{Old: oidA, New: oidB, Ref: "refs/tags/v1"}}},
		{name: "push-cert", body: pkt("push-cert\x00report-status\n", "certificate version 0.1\n"), max: 1 << 16, wantErr: ErrPushCert},
		{name: "shallow 后 push-cert", body: pkt("shallow "+oidA+"\n", "push-cert\x00report-status\n"), max: 1 << 16, wantErr: ErrPushCert},
		{name: "超限", body: pkt(oidA+" "+oidB+" refs/heads/main\x00report-status\n", "0000"), max: 50, wantErr: ErrUnparsable},
		{name: "截断", body: pkt(oidA + " " + oidB + " refs/heads/main\n")[:30], max: 1 << 16, wantErr: ErrUnparsable},
		{name: "缺 flush", body: pkt(oidA + " " + oidB + " refs/heads/main\n"), max: 1 << 16, wantErr: ErrUnparsable},
		{name: "坏长度", body: []byte("zzzz"), max: 1 << 16, wantErr: ErrUnparsable},
		{name: "长度过小", body: []byte("0002"), max: 1 << 16, wantErr: ErrUnparsable},
		{name: "坏 oid", body: pkt("xyz "+oidB+" refs/heads/main\n", "0000"), max: 1 << 16, wantErr: ErrUnparsable},
		{name: "非首条带 NUL", body: pkt(oidA+" "+oidB+" refs/heads/a\n", oidA+" "+oidB+" refs/heads/b\x00x\n", "0000"), max: 1 << 16, wantErr: ErrUnparsable},
		{name: "命令后 shallow", body: pkt(oidA+" "+oidB+" refs/heads/a\n", "shallow "+oidA+"\n", "0000"), max: 1 << 16, wantErr: ErrUnparsable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			full := append(slices.Clone(c.body), packData...)
			r := bytes.NewReader(full)
			cmds, caps, prefix, err := ParseReceivePack(r, c.max)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if !slices.Equal(cmds, c.wantCmds) || cmds == nil {
					t.Fatalf("cmds = %+v, want %+v", cmds, c.wantCmds)
				}
				if !slices.Equal(caps, c.wantCaps) {
					t.Fatalf("caps = %q, want %q", caps, c.wantCaps)
				}
				if !bytes.Equal(prefix, c.body) {
					t.Fatalf("prefix = %q, want %q", prefix, c.body)
				}
			}
			// 无论成败，prefix 加上剩余数据都能还原完整请求体
			got, err := io.ReadAll(io.MultiReader(bytes.NewReader(prefix), r))
			if err != nil || !bytes.Equal(got, full) {
				t.Fatalf("还原失败：%q", got)
			}
		})
	}
}

func TestReceivePackMaxBoundary(t *testing.T) {
	body := pkt(oidA+" "+oidB+" refs/heads/main\n", "0000")
	if _, _, _, err := ParseReceivePack(bytes.NewReader(body), len(body)); err != nil {
		t.Fatalf("恰好等于上限应通过：%v", err)
	}
	if _, _, _, err := ParseReceivePack(bytes.NewReader(body), len(body)-1); !errors.Is(err, ErrUnparsable) {
		t.Fatalf("超过上限应拒绝：%v", err)
	}
}

func TestReportStatusNG(t *testing.T) {
	refs := []string{"refs/heads/feature", "refs/heads/main"}
	plain := "000eunpack ok\n" +
		"0032ng refs/heads/feature oryn: prepare 未通过\n" +
		"002fng refs/heads/main oryn: prepare 未通过\n" +
		"0000"
	// "oryn: prepare 未通过" 为 23 字节（中文每字 3 字节）
	if got := string(ReportStatusNG(refs, "oryn: prepare 未通过", false)); got != plain {
		t.Fatalf("plain =\n%q\nwant\n%q", got, plain)
	}
	side := fmt.Sprintf("%04x\x01", len(plain)+5) + plain + "0000"
	if got := string(ReportStatusNG(refs, "oryn: prepare 未通过", true)); got != side {
		t.Fatalf("sideband =\n%q\nwant\n%q", got, side)
	}
	if got := string(ReportStatusNG([]string{"refs/heads/x"}, "a\nb", false)); !strings.Contains(got, "ng refs/heads/x a b\n") {
		t.Fatalf("原因中的换行未替换：%q", got)
	}

	// 字面字节核对（附录 B-7 的形态）
	want := "0033\x01000eunpack ok\n001cng refs/heads/f oryn: x\n0000" + "0000"
	if got := string(ReportStatusNG([]string{"refs/heads/f"}, "oryn: x", true)); got != want {
		t.Fatalf("sideband 字面 =\n%q\nwant\n%q", got, want)
	}

	// 超过单包上限时拆成多个 band 1 包
	many := make([]string, 3000)
	for i := range many {
		many[i] = fmt.Sprintf("refs/heads/branch-%04d", i)
	}
	inner := ReportStatusNG(many, "oryn: x", false)
	out := ReportStatusNG(many, "oryn: x", true)
	var data []byte
	rest := out
	for {
		var n int
		if _, err := fmt.Sscanf(string(rest[:4]), "%04x", &n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			if len(rest) != 4 {
				t.Fatalf("flush 后有多余字节")
			}
			break
		}
		if n > 65520 || rest[4] != 1 {
			t.Fatalf("包长 %d / band %d 非法", n, rest[4])
		}
		data = append(data, rest[5:n]...)
		rest = rest[n:]
	}
	if !bytes.Equal(data, inner) {
		t.Fatal("拆包后的数据与非 sideband 结果不一致")
	}
}
