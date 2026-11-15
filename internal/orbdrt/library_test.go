package orbdrt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/link"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

const libThread = "T-01932f6e-8d1a-7c3b-9f2e-0123456789ab"

func libEntry(path string, data []byte) apigen.LinkCtlLibrarySyncFile {
	sum := sha256.Sum256(data)
	return apigen.LinkCtlLibrarySyncFile{Path: path, Size: int64(len(data)), Sha256: hex.EncodeToString(sum[:])}
}

func librarySync(t *testing.T, cp *fakeCP, files ...apigen.LinkCtlLibrarySyncFile) []string {
	t.Helper()
	var res apigen.LinkCtlLibrarySyncResult
	require.NoError(t, cp.call(t, string(apigen.MethodLibrarySync), apigen.LinkCtlLibrarySyncParams{Files: files}, &res))
	return res.Missing
}

// pushLibrary 以控制面身份开一条同步用的 library 流推送 data，返回协调宿主的应答行。
func pushLibrary(t *testing.T, cp *fakeCP, e apigen.LinkCtlLibrarySyncFile, data []byte) apigen.LinkLibraryReply {
	t.Helper()
	cp.mu.Lock()
	sess := cp.sess
	cp.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := sess.Open(ctx, link.StreamHeader{T: link.StreamLibrary, Path: e.Path, Size: e.Size, SHA256: e.Sha256})
	require.NoError(t, err)
	defer st.Close()
	_, err = st.Write(data)
	require.NoError(t, err)
	_ = st.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := st.ReadLine(4096)
	require.NoError(t, err)
	var rep apigen.LinkLibraryReply
	require.NoError(t, json.Unmarshal(line, &rep))
	return rep
}

func replyCode(r apigen.LinkLibraryReply) string {
	if r.Code == nil {
		return ""
	}
	return *r.Code
}

// M13 4.3：library.sync 后沙箱 ~/library 与清单一致——清单外的文件、符号链接被删，缺失的经 library 流补齐；
// 内容被改的文件重新列为缺失；从清单移除的文件被清理；~/library/out/ 不受影响；不在清单里的推送被拒。
func TestCoordinatorLibrarySync(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	root := filepath.Join(rt.home, "library")

	err := cp.call(t, string(apigen.MethodLibrarySync), apigen.LinkCtlLibrarySyncParams{Files: []apigen.LinkCtlLibrarySyncFile{
		libEntry("~/library/shared/../x", []byte("x")),
	}}, nil)
	assert.Equal(t, link.CodeInvalidParams, ctlCode(err), "%v", err)

	// 清单之外的残留：普通文件、指向外面的符号链接；out/ 里助手写的文件
	stray := filepath.Join(root, "shared", ids.AssistantFile(), "old.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(stray), 0o755))
	require.NoError(t, os.WriteFile(stray, []byte("old"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "threads"), 0o755))
	link1 := filepath.Join(root, "threads", "escape")
	require.NoError(t, os.Symlink("/etc", link1))
	keep := filepath.Join(root, "out", "keep.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(keep), 0o755))
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o644))

	a, b := []byte("shared bytes"), []byte("# conversation notes\n")
	idA, idB := ids.AssistantFile(), ids.AssistantFile()
	fa := libEntry("~/library/shared/"+idA+"/a.txt", a)
	fb := libEntry("~/library/threads/"+libThread+"/"+idB+"/b.md", b)
	assert.ElementsMatch(t, []string{fa.Path, fb.Path}, librarySync(t, cp, fa, fb))
	_, err = os.Lstat(stray)
	assert.ErrorIs(t, err, os.ErrNotExist, "清单外的文件被删")
	_, err = os.Lstat(filepath.Dir(stray))
	assert.ErrorIs(t, err, os.ErrNotExist, "空目录被删")
	_, err = os.Lstat(link1)
	assert.ErrorIs(t, err, os.ErrNotExist, "符号链接被删")
	got, err := os.ReadFile(keep)
	require.NoError(t, err)
	assert.Equal(t, "mine", string(got), "out/ 不受同步影响")

	// 不在清单里的路径、字节与摘要不符的推送被拒
	other := libEntry("~/library/shared/"+ids.AssistantFile()+"/c.txt", []byte("c"))
	assert.Equal(t, "invalid_path", replyCode(pushLibrary(t, cp, other, []byte("c"))))
	assert.Equal(t, "mismatch", replyCode(pushLibrary(t, cp, fa, []byte("shared BYTES"))))

	assert.True(t, pushLibrary(t, cp, fa, a).Ok)
	assert.True(t, pushLibrary(t, cp, fb, b).Ok)
	pa := filepath.Join(root, "shared", idA, "a.txt")
	pb := filepath.Join(root, "threads", libThread, idB, "b.md")
	got, err = os.ReadFile(pa)
	require.NoError(t, err)
	assert.Equal(t, a, got)
	fi, err := os.Stat(pa)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o444), fi.Mode().Perm(), "同步进来的文件只读")
	got, err = os.ReadFile(pb)
	require.NoError(t, err)
	assert.Equal(t, b, got)
	assert.Empty(t, librarySync(t, cp, fa, fb), "与清单一致")

	// 助手改了同步进来的文件：再次同步时列为缺失（并已删除），重新推送后还原
	require.NoError(t, os.Chmod(pb, 0o644))
	require.NoError(t, os.WriteFile(pb, []byte("tampered"), 0o644))
	assert.Equal(t, []string{fb.Path}, librarySync(t, cp, fa, fb))
	_, err = os.Lstat(pb)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.True(t, pushLibrary(t, cp, fb, b).Ok)
	got, err = os.ReadFile(pb)
	require.NoError(t, err)
	assert.Equal(t, b, got)

	// 库里删掉了 a：再次同步时被清理
	assert.Empty(t, librarySync(t, cp, fb))
	_, err = os.Lstat(filepath.Join(root, "shared", idA))
	assert.ErrorIs(t, err, os.ErrNotExist, "已删除的库文件被清理")
	_, err = os.Stat(pb)
	assert.NoError(t, err)
}

// M13 4.3：library.fetch 只服务 ~/library/out/ 下的普通文件（~/…、绝对路径、相对 out/ 的路径都可）；
// `..`、out/ 之外、指向外面的符号链接一律拒绝。
func TestCoordinatorLibraryFetch(t *testing.T) {
	rt, cp, _, _ := startCoordinator(t)
	cp.waitNotif(t, 0, "hello", nil)
	root := filepath.Join(rt.home, "library")
	out := filepath.Join(root, "out")
	require.NoError(t, os.MkdirAll(filepath.Join(out, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(out, "report.md"), []byte("hello"), 0o644))
	shared := filepath.Join(root, "shared", ids.AssistantFile(), "a.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o755))
	require.NoError(t, os.WriteFile(shared, []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(shared, filepath.Join(out, "evil.txt")))
	outside := filepath.Join(rt.home, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("nope"), 0o644))

	for i, p := range []string{"~/library/out/report.md", "report.md", filepath.Join(out, "report.md")} {
		id := "f" + string(rune('0'+i))
		var res apigen.LinkCtlLibraryFetchResult
		require.NoError(t, cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: id, Path: p}, &res), p)
		assert.Equal(t, int64(5), res.Size)
		select {
		case st := <-cp.libs:
			assert.Equal(t, id, st.Header.ID)
			assert.Equal(t, int64(5), st.Header.Size)
			b, err := io.ReadAll(st)
			require.NoError(t, err)
			assert.Equal(t, "hello", string(b))
		case <-time.After(10 * time.Second):
			t.Fatal("no library stream")
		}
	}

	for _, tc := range []struct{ path, code string }{
		{"../shared/x", link.CodeInvalidParams},
		{"~/library/out/../shared/a.txt", link.CodeInvalidParams},
		{"~/library/shared/" + filepath.Base(filepath.Dir(shared)) + "/a.txt", link.CodeInvalidParams},
		{outside, link.CodeInvalidParams},
		{"evil.txt", link.CodeInvalidParams},
		{"~/library/out/missing.md", "not_found"},
		{"~/library/out/sub", "not_found"},
		{"", link.CodeInvalidParams},
	} {
		err := cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "fx", Path: tc.path}, nil)
		assert.Equal(t, tc.code, ctlCode(err), "%q: %v", tc.path, err)
	}
	select {
	case st := <-cp.libs:
		t.Fatalf("unexpected library stream %+v", st.Header)
	default:
	}
}

// M13 4.6：普通 orbd 上 orbd.sock 的 library.put 转成 ctl library.put；转发期间控制面能经 library.fetch 取回该路径，
// 之前与之后都不能；相对路径与目录被拒；协调宿主之外不接受 library.sync；oryn-library 技能写进 ~/.agents/skills/。
func TestSockLibraryPut(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	cp.waitNotif(t, 0, "hello", nil)
	ctl := cp.ctlNow(t)
	file := filepath.Join(rt.home, "workspace", "report.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	content := []byte("quarterly numbers\n")
	require.NoError(t, os.WriteFile(file, content, 0o644))

	type putCall struct {
		params apigen.LinkCtlLibraryPutParams
		data   []byte
		err    error
	}
	puts := make(chan putCall, 4)
	cp.extra = func(method string, params json.RawMessage) (any, bool, error) {
		if method != string(apigen.MethodLibraryPut) {
			return nil, false, nil
		}
		var c putCall
		_ = json.Unmarshal(params, &c.params)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var res apigen.LinkCtlLibraryFetchResult
		if c.err = ctl.Call(ctx, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "put-1", Path: c.params.Path}, &res); c.err == nil {
			st := <-cp.libs
			c.data, c.err = io.ReadAll(st)
		}
		puts <- c
		if c.err != nil {
			return nil, true, c.err
		}
		return apigen.AssistantFile{ID: "afl_X", Name: *c.params.Name, Size: res.Size, Scope: apigen.Shared,
			Source: apigen.AssistantFileSourceOrb, SourceThreadID: &c.params.ThreadID}, true, nil
	}
	sock := orbsock.Path(rt.home)
	waitSock(t, sock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "early", Path: file}, nil)
	assert.Equal(t, "not_found", ctlCode(err), "未在存入的路径不能取回")

	raw, err := orbsock.Call(ctx, sock, orbsock.MethodLibraryPut, apigen.LinkCtlLibraryPutParams{ThreadID: libThread, Path: file, Name: new("r.txt")}, nil)
	require.NoError(t, err)
	var f apigen.AssistantFile
	require.NoError(t, json.Unmarshal(raw, &f))
	assert.Equal(t, "afl_X", f.ID)
	assert.Equal(t, int64(len(content)), f.Size)
	c := <-puts
	require.NoError(t, c.err)
	assert.Equal(t, libThread, c.params.ThreadID)
	assert.Equal(t, file, c.params.Path)
	assert.Equal(t, content, c.data)

	err = cp.call(t, string(apigen.MethodLibraryFetch), apigen.LinkCtlLibraryFetchParams{ID: "late", Path: file}, nil)
	assert.Equal(t, "not_found", ctlCode(err), "存入结束后不能再取回")

	for _, p := range []string{"workspace/report.txt", filepath.Dir(file), filepath.Join(rt.home, "missing")} {
		_, err := orbsock.Call(ctx, sock, orbsock.MethodLibraryPut, apigen.LinkCtlLibraryPutParams{ThreadID: libThread, Path: p}, nil)
		var se *orbsock.Error
		require.ErrorAs(t, err, &se, p)
		assert.Equal(t, orbsock.CodeInvalidParams, se.Code, p)
	}

	err = cp.call(t, string(apigen.MethodLibrarySync), apigen.LinkCtlLibrarySyncParams{Files: []apigen.LinkCtlLibrarySyncFile{}}, nil)
	assert.Equal(t, link.CodeMethodNotFound, ctlCode(err))
	skill, err := os.ReadFile(rt.librarySkillPath())
	require.NoError(t, err)
	assert.Contains(t, string(skill), "oryn library put")
}
