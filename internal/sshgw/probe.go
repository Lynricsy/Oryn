package sshgw

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

const (
	probeTimeout = 10 * time.Second
	maxSnapDisks = 16
	probeSep     = "__ORYN__"
)

// probeScript 采一次系统与状态快照：五段以 __ORYN__ 分隔（uname、两次 /proc/stat、meminfo、loadavg、df）。
const probeScript = "uname -s 2>/dev/null; echo " + probeSep + "; head -n1 /proc/stat 2>/dev/null; sleep 1; head -n1 /proc/stat 2>/dev/null; echo " + probeSep +
	"; cat /proc/meminfo 2>/dev/null; echo " + probeSep + "; cat /proc/loadavg 2>/dev/null; echo " + probeSep + "; df -P -k 2>/dev/null"

// Test 测试主机连接：拨号（含跳板与登录，首次成功会固定指纹），再采一次状态快照。连接失败不算错误，
// 以 ok=false 返回；结果写回主机、记审计并通知页面刷新。
func (s *Service) Test(ctx context.Context, id string) (apigen.SSHTestResult, error) {
	h, err := s.host(ctx, id)
	if err != nil {
		return apigen.SSHTestResult{}, err
	}
	start := time.Now()
	up, err := s.dial(ctx, h)
	res := apigen.SSHTestResult{CheckedAt: s.now(), LatencyMs: int(time.Since(start).Milliseconds())}
	if err != nil {
		res.Error = new(err.Error())
	} else {
		res.Ok = true
		if cur, err := s.st.Q.GetSshHost(ctx, id); err == nil && cur.HostKey != nil {
			if pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(*cur.HostKey)); err == nil {
				res.HostKeyFingerprint = new(ssh.FingerprintSHA256(pub))
			}
		}
		if out, ok := runProbe(up.client); ok {
			osName, snap := parseSnapshot(out)
			if osName != "" {
				res.Os = &osName
			}
			res.Snapshot = snap
		}
		up.Close()
	}
	if err := s.st.Q.SetSshHostTest(context.WithoutCancel(ctx), db.SetSshHostTestParams{
		LastTestAt: &res.CheckedAt, LastTestOk: &res.Ok, LastTestError: res.Error, LastTestOs: res.Os, ID: id,
	}); err != nil {
		return apigen.SSHTestResult{}, err
	}
	row := auditRow{source: srcOwner, host: h, kind: "test", ok: res.Ok, durationMs: new(int64(res.LatencyMs))}
	if res.Error != nil {
		row.detail = *res.Error
	} else if res.Os != nil {
		row.detail = *res.Os
	}
	s.audit(ctx, row)
	s.cfg.Changed()
	return res, nil
}

// runProbe 在新会话里跑采样脚本（probeTimeout 内）；失败或超时返回 ok=false。
func runProbe(c *ssh.Client) (string, bool) {
	sess, err := c.NewSession()
	if err != nil {
		return "", false
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &out
	done := make(chan error, 1)
	go func() { done <- sess.Run(probeScript) }()
	select {
	case err := <-done:
		// 脚本以非零退出（比如 df 部分失败）时输出仍然可用。
		var exitErr *ssh.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			return "", false
		}
		return out.String(), true
	case <-time.After(probeTimeout):
		_ = sess.Close()
		<-done
		return "", false
	}
}

// parseSnapshot 解析采样脚本的输出：返回系统名（linux / macos / uname -s 的小写）与快照；什么都没采到时快照为 nil。
func parseSnapshot(out string) (string, *apigen.SSHSnapshot) {
	parts := strings.Split(out, probeSep)
	seg := func(i int) string {
		if i < len(parts) {
			return strings.TrimSpace(parts[i])
		}
		return ""
	}
	var osName string
	switch u := seg(0); u {
	case "":
	case "Linux":
		osName = "linux"
	case "Darwin":
		osName = "macos"
	default:
		osName = strings.ToLower(u)
	}
	snap := &apigen.SSHSnapshot{Disks: []apigen.SSHSnapshotDisk{}}
	found := false
	if cpu, ok := parseCPU(seg(1)); ok {
		snap.CPUPct, found = &cpu, true
	}
	if total, avail, ok := parseMeminfo(seg(2)); ok {
		used := total - avail
		snap.MemTotalKb, snap.MemUsedKb, found = &total, &used, true
	}
	if f := strings.Fields(seg(3)); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 32); err == nil {
			load := float32(v)
			snap.Load1, found = &load, true
		}
	}
	snap.Disks = parseDf(seg(4))
	if len(snap.Disks) > 0 {
		found = true
	}
	if !found {
		return osName, nil
	}
	return osName, snap
}

// parseCPU 由两行 /proc/stat 的 cpu 汇总算使用率：(Δ总 − Δ(idle + iowait)) / Δ总 × 100，保留一位小数。
func parseCPU(s string) (float32, bool) {
	var samples [][2]uint64 // {总, 空闲}
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var total, idle uint64
		// user nice system idle iowait irq softirq steal（guest 已计入 user，不再相加）
		for i, v := range f[1:min(len(f), 9)] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, false
			}
			total += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
		samples = append(samples, [2]uint64{total, idle})
	}
	if len(samples) != 2 || samples[1][0] <= samples[0][0] || samples[1][1] < samples[0][1] {
		return 0, false
	}
	dt := float64(samples[1][0] - samples[0][0])
	di := float64(samples[1][1] - samples[0][1])
	pct := math.Round((dt-di)/dt*1000) / 10
	return float32(max(0, min(100, pct))), true
}

// parseMeminfo 取 MemTotal 与 MemAvailable（kB）。
func parseMeminfo(s string) (total, avail int64, ok bool) {
	var haveT, haveA bool
	for line := range strings.SplitSeq(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total, haveT = n, true
		case "MemAvailable:":
			avail, haveA = n, true
		}
	}
	if !haveT || !haveA || avail > total {
		return 0, 0, false
	}
	return total, avail, true
}

// parseDf 解析 `df -P -k`：跳过表头，只留 /dev/ 开头的文件系统，最多 maxSnapDisks 项。
func parseDf(s string) []apigen.SSHSnapshotDisk {
	disks := []apigen.SSHSnapshotDisk{}
	for i, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 6 || !strings.HasPrefix(f[0], "/dev/") {
			continue
		}
		total, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		disks = append(disks, apigen.SSHSnapshotDisk{Mount: strings.Join(f[5:], " "), UsedKb: used, TotalKb: total})
		if len(disks) == maxSnapDisks {
			break
		}
	}
	return disks
}
