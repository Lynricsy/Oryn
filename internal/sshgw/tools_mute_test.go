package sshgw

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/sshgw/sshgwtest"
)

// muteSSHD 是握手与登录都正常、之后再不回话的目标机：mode "open" 时不应答开会话；
// mode "exec" 时接受会话，但对 exec 等请求与 close 都不回应。用来验证超时不依赖对端配合。
func muteSSHD(t *testing.T, mode string) (addr string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(sshgwtest.RandomSigner(t))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Go(func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if mode != "exec" {
						continue // 不 Accept 也不 Reject：客户端的开会话一直等
					}
					ch, chReqs, err := nc.Accept()
					if err != nil {
						return
					}
					_ = ch
					go func() {
						for range chReqs { // 收下请求但从不 Reply，也不回 close
						}
					}()
				}
			})
		}
	})
	a := ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func (e *gwEnv) muteHost(t *testing.T, name, mode string) apigen.SSHHost {
	t.Helper()
	addr, port := muteSSHD(t, mode)
	return e.mustHost(t, apigen.SSHHostCreate{Name: name, Address: addr, Port: &port, Username: "u", AuthType: apigen.SSHAuthKey, KeyID: &e.key.ID})
}

// 对端握手后不再回话：ssh_exec_many 仍在 timeout_s + killGrace 内返回超时（开会话卡住与 exec/close 不回应两种）。
func TestExecManyMuteUpstreamTimesOut(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	th := e.toolThread(t, "T-ok", "work")
	e.muteHost(t, "mute-open", "open")
	e.muteHost(t, "mute-exec", "exec")

	start := time.Now()
	res, err := e.call(t, th, toolExecMany, map[string]any{"hosts": []string{"mute-open", "mute-exec"}, "command": "true", "timeout_s": 1})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), time.Second+killGrace+2*time.Second)
	assert.Contains(t, res.Text, "## mute-open — 超时（1s）")
	assert.Contains(t, res.Text, "## mute-exec — 超时（1s）")
}

// 主机测试的采样会话同样有界：对端不回话时测试照样返回（连接本身成功，ok=true、没有快照）。
func TestProbeMuteUpstreamBounded(t *testing.T) {
	e := newGwEnv(t, sshgwtest.Options{})
	h := e.muteHost(t, "mute-exec", "exec")

	start := time.Now()
	res, err := e.svc.Test(context.Background(), h.ID)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), probeTimeout+killGrace+3*time.Second)
	assert.True(t, res.Ok)
	assert.Nil(t, res.Snapshot)
}
