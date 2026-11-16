package orbdrt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// confine：真实路径必须在目录之内（按路径段比较）且不进 .git；范围外的读取回 404、列表里不出现。
func TestFsConfine(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	home, err := filepath.EvalSymlinks(rt.home)
	require.NoError(t, err)
	wd := filepath.Join(home, "w", "a")
	sib := filepath.Join(home, "w", "ab")
	for _, d := range []string{filepath.Join(wd, "sub"), filepath.Join(wd, ".git"), sib} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	write := func(p, s string) { require.NoError(t, os.WriteFile(p, []byte(s), 0o600)) }
	write(filepath.Join(wd, "f.txt"), "inside")
	write(filepath.Join(wd, "sub", "g.txt"), "nested")
	write(filepath.Join(wd, ".git", "config"), "[remote] token")
	write(filepath.Join(sib, "secret.txt"), "sibling secret")
	link := func(target, name string) { require.NoError(t, os.Symlink(target, filepath.Join(wd, name))) }
	link("../ab/secret.txt", "out")      // 指到前缀相似的兄弟目录
	link(filepath.Join(home, "w"), "up") // 指到上一级目录
	link("f.txt", "in")                  // 范围内的文件
	link("sub", "subl")                  // 范围内的目录
	link(".git", "g")                    // 指进 .git
	link("missing", "dangling")          // 悬空

	get := func(route, p, confine string) *httptest.ResponseRecorder {
		q := url.Values{"path": {p}}
		if confine != "" {
			q.Set("confine", confine)
		}
		return serveLocal(rt, httptest.NewRequest(http.MethodGet, route+"?"+q.Encode(), nil))
	}
	read := func(p string) (int, string) {
		rec := get("/fs/file", p, wd)
		if rec.Code == http.StatusOK {
			assert.Equal(t, "1", rec.Header().Get(ConfinedHeader), p)
		}
		return rec.Code, rec.Body.String()
	}

	code, body := read(filepath.Join(wd, "f.txt"))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "inside", body)
	code, body = read(filepath.Join(wd, "in"))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "inside", body)
	code, body = read(filepath.Join(wd, "subl", "g.txt"))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "nested", body)
	for _, p := range []string{
		filepath.Join(wd, "out"),
		filepath.Join(wd, "up", "ab", "secret.txt"),
		filepath.Join(sib, "secret.txt"),
		filepath.Join(wd, ".git", "config"),
		filepath.Join(wd, ".GIT", "config"),
		filepath.Join(wd, "g", "config"),
	} {
		code, body := read(p)
		assert.Equal(t, http.StatusNotFound, code, p)
		assert.NotContains(t, body, "sibling secret", p)
		assert.NotContains(t, body, "[remote]", p)
	}
	// 不带 confine 时行为不变：$HOME 之内的链接照常可读
	rec := get("/fs/file", filepath.Join(wd, "out"), "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "sibling secret", rec.Body.String())
	assert.Empty(t, rec.Header().Get(ConfinedHeader))

	// 列表：范围外、指进 .git 与悬空的链接都不出现；指向文件的链接 size 是目标的大小
	rec = get("/fs/list", wd, wd)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "1", rec.Header().Get(ConfinedHeader))
	var ls apigen.FsListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ls))
	names := map[string]apigen.FsEntry{}
	for _, e := range ls.Entries {
		names[e.Name] = e
	}
	assert.ElementsMatch(t, []string{"sub", "subl", "f.txt", "in"}, keys(names))
	assert.Equal(t, int64(len("inside")), names["in"].Size)
	assert.Equal(t, apigen.FsEntryTypeSymlink, names["in"].Type)
	for _, p := range []string{sib, filepath.Join(home, "w"), filepath.Join(wd, ".git"), filepath.Join(wd, "up")} {
		assert.Equal(t, http.StatusNotFound, get("/fs/list", p, wd).Code, p)
	}
	rec = get("/fs/list", filepath.Join(wd, "subl"), wd)
	assert.Equal(t, http.StatusOK, rec.Code)

	// confine 本身：相对路径 400，不存在 404，不是目录 409
	assert.Equal(t, http.StatusBadRequest, get("/fs/list", wd, "w/a").Code)
	assert.Equal(t, http.StatusNotFound, get("/fs/list", wd, filepath.Join(home, "nope")).Code)
	assert.Equal(t, http.StatusConflict, get("/fs/file", filepath.Join(wd, "f.txt"), filepath.Join(wd, "f.txt")).Code)
}

func TestFsConfinedSegments(t *testing.T) {
	for _, c := range []struct {
		real string
		ok   bool
	}{
		{"/w/a", true},
		{"/w/a/b/c", true},
		{"/w/ab", false},
		{"/w/ab/c", false},
		{"/w", false},
		{"/w/a/.git", false},
		{"/w/a/x/.Git/y", false},
		{"/w/a/.gitignore", true},
		{"/w/a/x.git", true},
	} {
		assert.Equal(t, c.ok, confined(c.real, "/w/a"), c.real)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
