package coordhost

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SelfTest 是 `orynd --config <f> coordinator check`：可用性检查与目录准备之后，用与 spawn 相同的参数与
// 命名空间起 `coordinator-host --selftest`（HOME 为一次性的 <dir>/selftest-<随机>，结束后删除），
// 把探测结论、沙箱报告与自检各行（JSON）写到 out。全部通过返回 nil。
func SelfTest(ctx context.Context, cfg Config, out io.Writer) error {
	return selfTest(ctx, New(cfg, nil, nil, nil), out)
}

func selfTest(ctx context.Context, h *Host, out io.Writer) error {
	enc := json.NewEncoder(out)
	h.probe()
	probe := map[string]any{"netNamespace": h.useNS, "landlockAbi": h.abi}
	if h.probeErr != nil {
		probe["error"] = h.probeErr.Error()
	}
	_ = enc.Encode(map[string]any{"probe": probe})
	uid, gid, err := h.prepare(ctx)
	if err != nil {
		_ = enc.Encode(map[string]any{"available": false, "reason": err.Error()})
		return err
	}
	_ = enc.Encode(map[string]any{"available": true})

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	home := filepath.Join(h.cfg.Dir, "selftest-"+hex.EncodeToString(rnd[:]))
	if err := prepareHome(home, uid, gid); err != nil {
		return err
	}
	defer os.RemoveAll(home)

	paths := []string{"/etc/shadow"}
	if h.cfg.DataDir != "" {
		paths = append(paths, filepath.Join(h.cfg.DataDir, "master.key"), filepath.Join(h.cfg.DataDir, "oryn.db"))
	}
	if h.cfg.ConfigPath != "" {
		paths = append(paths, h.cfg.ConfigPath)
	}
	var deny []string
	for _, p := range paths {
		// 子进程的 cwd 是 /：相对路径必须先转成绝对路径，否则「读不到」只是因为找不到。
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		deny = append(deny, "--deny-read", p)
	}
	args := h.childArgs(uid, gid, h.useNS, append([]string{"--selftest", "--home", home}, deny...)...)
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	defer pr.Close()
	cmd := exec.CommandContext(ctx, h.cfg.Command[0], args...)
	cmd.Env = childEnv()
	cmd.Dir = "/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.ExtraFiles = []*os.File{nil, pw} // fd 3（link）不提供；fd 4 = 状态管道
	cmd.SysProcAttr = sysProcAttr(h.useNS)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return fmt.Errorf("start selftest: %w", err)
	}
	_ = pw.Close()
	report, _ := bufio.NewReader(pr).ReadBytes('\n')
	runErr := cmd.Wait()
	if r := bytes.TrimSpace(report); len(r) > 0 {
		_ = enc.Encode(map[string]json.RawMessage{"sandbox": r})
	}
	_, _ = out.Write(stdout.Bytes())
	if runErr != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			_ = enc.Encode(map[string]string{"stderr": s})
		}
		return fmt.Errorf("coordinator host selftest failed: %w", runErr)
	}
	return nil
}
