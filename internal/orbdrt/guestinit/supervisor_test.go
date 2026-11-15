//go:build linux

package guestinit

import (
	"bufio"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// nextLine 取替身写进 fifo 的下一行；超时即判定为没有启动。
func nextLine(t *testing.T, lines <-chan string, what string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(10 * time.Second):
		t.Fatalf("no %s", what)
		return ""
	}
}

// generation 解析替身写下的 "<orbd 替身 pid> <后台进程 pid>"。
func generation(t *testing.T, line string) (orbd, bg int) {
	t.Helper()
	f := strings.Fields(line)
	require.Len(t, f, 2, line)
	orbd, err := strconv.Atoi(f[0])
	require.NoError(t, err)
	bg, err = strconv.Atoi(f[1])
	require.NoError(t, err)
	return orbd, bg
}

// R5：orbd 替身起一个独立 session 的后台进程后被 SIGKILL；监督器重启前清掉上一代的残留进程，新一代只启动一次。
// 本测试进程设为 child subreaper，充当 guest 里的 PID 1（领养孤儿、SIGCHLD 时 reap）。
func TestSupervisorSweepsPreviousGeneration(t *testing.T) {
	require.NoError(t, unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0))
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0) })

	dir := t.TempDir()
	fifo := filepath.Join(dir, "gen")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	// O_RDWR：替身写入时不必等读端，读端也读不到 EOF。
	rf, err := os.OpenFile(fifo, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rf.Close() })
	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(rf)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	s := newSupervisor(nil)
	s.bin = "/bin/sh"
	// 后台进程用 setsid 自立 session，再由它自己报告 "<orbd 替身 pid> <自己的 pid>"。
	s.argv = []string{"sh", "-c", `setsid sh -c 'echo "$0 $$" > "$GEN"; exec sleep 1000' "$$" & exec sleep 1000`}
	s.backoff = time.Millisecond
	var sweeps atomic.Int32
	s.sweep = func() {
		sweeps.Add(1)
		sweepChildren()
	}

	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, syscall.SIGCHLD)
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		for range sigs {
			s.reap()
		}
	}()
	t.Cleanup(func() {
		signal.Stop(sigs)
		close(sigs)
		<-reaped
		sweepChildren()
	})

	require.NoError(t, s.Start([]string{"GEN=" + fifo, "PATH=/usr/bin:/bin"}, dir))
	orbd1, bg1 := generation(t, nextLine(t, lines, "first generation"))
	require.NoError(t, unix.Kill(bg1, 0), "the background process of the first generation runs")
	sid, err := unix.Getsid(bg1)
	require.NoError(t, err)
	require.Equal(t, bg1, sid, "the background process has its own session")

	require.NoError(t, unix.Kill(orbd1, unix.SIGKILL))
	orbd2, bg2 := generation(t, nextLine(t, lines, "restarted generation"))
	assert.NotEqual(t, orbd1, orbd2)
	assert.ErrorIs(t, unix.Kill(bg1, 0), unix.ESRCH, "the previous generation's background process was killed and reaped")
	assert.NoError(t, unix.Kill(bg2, 0), "the new generation is untouched")
	assert.True(t, s.Running())

	// 新一代只启动了一次：停掉监督器、清场后，fifo 里下一行是本测试写的哨兵。
	s.Stop(5 * time.Second)
	sweepChildren()
	assert.Equal(t, int32(1), sweeps.Load())
	_, err = rf.WriteString("sentinel\n")
	require.NoError(t, err)
	assert.Equal(t, "sentinel", nextLine(t, lines, "sentinel"), "no extra generation was started")
}
