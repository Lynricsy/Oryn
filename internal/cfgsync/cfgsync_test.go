package cfgsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/agentcfg"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/secrets"
	"github.com/Lynricsy/Oryn/internal/store"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
)

// fakeFS 是记录调用的 orbd fs 替身：按 path 查询参数返回 files 里的内容，没有的返回 404。
type fakeFS struct {
	mu    sync.Mutex
	files map[string][]byte
	calls []string // "<orbID> <path>"
	err   error
}

func (f *fakeFS) Do(_ context.Context, orbID string, req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := req.URL.Query().Get("path")
	f.calls = append(f.calls, orbID+" "+p)
	if f.err != nil {
		return nil, f.err
	}
	if req.Method != http.MethodGet || req.URL.Path != "/fs/file" {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	b, ok := f.files[p]
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":"not_found"}`))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func (f *fakeFS) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// failGlobal 是写入总是失败的全局存储（读取一律不存在）。
type failGlobal struct{ err error }

func (g failGlobal) Get(string) ([]byte, agentcfg.FileInfo, error) {
	return nil, agentcfg.FileInfo{}, fs.ErrNotExist
}

func (g failGlobal) Put(context.Context, string, []byte, fs.FileMode, *secrets.Vault) (agentcfg.WriteResult, error) {
	return agentcfg.WriteResult{}, g.err
}

type env struct {
	ctx      context.Context
	st       *store.Store
	dir      string
	fs       *fakeFS
	global   *agentcfg.Home
	projects *agentcfg.ProjectHomes
	s        *Saver
	prov     string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	global, err := agentcfg.NewHome(dir)
	require.NoError(t, err)
	projects, err := agentcfg.NewProjectHomes(dir)
	require.NoError(t, err)
	prov := ids.New("prov")
	require.NoError(t, st.Q.CreateProvider(ctx, db.CreateProviderParams{ID: prov, Name: prov, TokenHash: ids.Hash(prov), CreatedAt: 1}))
	f := &fakeFS{files: map[string][]byte{}}
	return &env{ctx: ctx, st: st, dir: dir, fs: f, global: global, projects: projects, prov: prov,
		s: &Saver{Store: st, Orbs: f, Global: global, Projects: projects}}
}

func (e *env) project(t *testing.T) string {
	t.Helper()
	id := ids.New("prj")
	require.NoError(t, e.st.Q.CreateProject(e.ctx, db.CreateProjectParams{ID: id, Name: id, CreatedAt: 1, UpdatedAt: 1}))
	return id
}

func (e *env) orb(t *testing.T, projectID string) string {
	t.Helper()
	id := ids.Orb()
	var prj *string
	if projectID != "" {
		prj = &projectID
	}
	require.NoError(t, e.st.Q.CreateOrb(e.ctx, db.CreateOrbParams{ID: id, ProviderID: e.prov, Type: "medium", Cpus: 4, MemoryMb: 8192, Image: "test",
		ProjectID: prj, State: "ready", StateChangedAt: 1, TokenHash: ids.Hash("oot_" + id), CreatedAt: 1}))
	return id
}

func (e *env) events(t *testing.T, orbID string) []string {
	t.Helper()
	evs, err := e.st.Q.ListOrbEvents(e.ctx, orbID)
	require.NoError(t, err)
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Detail)
	}
	return out
}

func get(h *agentcfg.Home, rel string) ([]byte, bool) {
	b, _, err := h.Get(rel)
	return b, err == nil
}

// omfgText 按 Core 的 assembleRuleMarkdown 生成 /omfg 规则文件（规则名 name，正文 body）。
func omfgText(name, body string) string {
	return "---\nname: " + name + "\ndescription: \"Never print the marker\"\ncondition: \"M122_MARKER\"\nscope: [\"text\", \"tool:bash\"]\n---\n\n" + body
}

// rule 是规则名为 x 的合法 /omfg 规则。
var rule = omfgText("x", "Do not output M122_MARKER.")

// params 是用户看着 content 的预览点了保存位置之后的回写请求。
func params(orbID, savedPath, content string, global bool) threads.ConfigSaveParams {
	return threads.ConfigSaveParams{OrbID: orbID, ThreadID: "T-1", SavedPath: savedPath, GlobalChosen: global, Preview: content}
}

// 全局路径 + 用户点了全局：写进全局 Home，结果 synced，有 agent_config_saved 审计事件（不带 projectId）。
func TestSaveRuleGlobal(t *testing.T) {
	e := newEnv(t)
	var applied []string
	e.s.Applied = func(p string) { applied = append(applied, p) }
	prj := e.project(t)
	orb := e.orb(t, prj)
	e.fs.files["~/.omp/agent/rules/x.md"] = []byte(rule)

	res := e.s.SaveRule(e.ctx, params(orb, "~/.omp/agent/rules/x.md", rule, true))
	assert.Equal(t, threads.ConfigSaveResult{Synced: true, Scope: "global", Path: "rules/x.md"}, res)
	assert.Equal(t, []string{""}, applied, "写入全局后下发全局层")
	b, ok := get(e.global, "rules/x.md")
	require.True(t, ok)
	assert.Equal(t, rule, string(b))
	ph, err := e.projects.For(prj)
	require.NoError(t, err)
	_, ok = get(ph, "rules/x.md")
	assert.False(t, ok, "全局保存不进项目层")
	assert.Equal(t, []string{orb + " ~/.omp/agent/rules/x.md"}, e.fs.calls)

	evs := e.events(t, orb)
	require.Len(t, evs, 1)
	assert.Contains(t, evs[0], `"event":"agent_config_saved"`)
	assert.Contains(t, evs[0], `"scope":"global"`)
	assert.Contains(t, evs[0], `"path":"rules/x.md"`)
	assert.Contains(t, evs[0], `"threadId":"T-1"`)
	assert.Contains(t, evs[0], `"sha256":"`)
	assert.NotContains(t, evs[0], "projectId")
}

// 全局路径但没有记录到用户点选全局：scope_unconfirmed，不读 orb、不写中央。
func TestSaveRuleGlobalUnconfirmed(t *testing.T) {
	e := newEnv(t)
	e.s.Applied = func(string) { t.Fatal("没写入中央不应下发") }
	orb := e.orb(t, e.project(t))
	e.fs.files["~/.omp/agent/rules/x.md"] = []byte(rule)

	res := e.s.SaveRule(e.ctx, params(orb, "~/.omp/agent/rules/x.md", rule, false))
	assert.Equal(t, threads.ConfigSaveResult{Scope: "global", Path: "rules/x.md", Reason: "scope_unconfirmed"}, res)
	assert.Zero(t, e.fs.n())
	_, ok := get(e.global, "rules/x.md")
	assert.False(t, ok)
	assert.Empty(t, e.events(t, orb))
}

// 新旧两种项目路径：写进 orb 所属项目的 Home（项目 id 取自 orb 行），全局与别的项目都没有。
func TestSaveRuleProject(t *testing.T) {
	text := omfgText("my-rule_1", "Do not output M122_MARKER.")
	for _, saved := range []string{
		"~/.oryn/project-config/.omp/rules/my-rule_1.md", // Core ≥ oryn-v18.19.0-8
		"~/workspace/repo/.omp/rules/my-rule_1.md",       // 旧 Core，仓库
		"~/workspace/.omp/rules/my-rule_1.md",            // 旧 Core，无仓库的工作区
	} {
		t.Run(saved, func(t *testing.T) {
			e := newEnv(t)
			var applied []string
			e.s.Applied = func(p string) { applied = append(applied, p) }
			prj, other := e.project(t), e.project(t)
			orb := e.orb(t, prj)
			e.fs.files[saved] = []byte(text)

			res := e.s.SaveRule(e.ctx, params(orb, saved, text, false))
			assert.Equal(t, threads.ConfigSaveResult{Synced: true, Scope: "project", Path: "rules/my-rule_1.md"}, res)
			assert.Equal(t, []string{prj}, applied, "写入项目层后只下发该项目")
			ph, err := e.projects.For(prj)
			require.NoError(t, err)
			b, ok := get(ph, "rules/my-rule_1.md")
			require.True(t, ok)
			assert.Equal(t, text, string(b))
			_, ok = get(e.global, "rules/my-rule_1.md")
			assert.False(t, ok, "项目保存不进全局")
			oh, err := e.projects.For(other)
			require.NoError(t, err)
			_, ok = get(oh, "rules/my-rule_1.md")
			assert.False(t, ok, "不写别的项目")

			evs := e.events(t, orb)
			require.Len(t, evs, 1)
			assert.Contains(t, evs[0], `"scope":"project"`)
			assert.Contains(t, evs[0], fmt.Sprintf(`"projectId":%q`, prj))
		})
	}
}

// 中央已有同名规则：内容相同 → synced（不重写、不下发、不再审计）；内容不同 → conflict，中央原样保留。
func TestSaveRuleConflict(t *testing.T) {
	for _, c := range []struct {
		name, saved string
		global      bool
	}{
		{"global", "~/.omp/agent/rules/x.md", true},
		{"project", "~/.oryn/project-config/.omp/rules/x.md", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			applied := 0
			e.s.Applied = func(string) { applied++ }
			prj := e.project(t)
			orb := e.orb(t, prj)
			central := e.global
			if !c.global {
				h, err := e.projects.For(prj)
				require.NoError(t, err)
				central = h
			}
			e.fs.files[c.saved] = []byte(rule)
			require.True(t, e.s.SaveRule(e.ctx, params(orb, c.saved, rule, c.global)).Synced)
			require.Equal(t, 1, applied)

			// 内容相同：成功，不重写
			res := e.s.SaveRule(e.ctx, params(orb, c.saved, rule, c.global))
			assert.True(t, res.Synced)
			assert.Empty(t, res.Reason)
			assert.Equal(t, 1, applied, "内容相同不再下发")
			assert.Len(t, e.events(t, orb), 1, "内容相同不再审计")

			// 内容不同：不覆盖
			v2 := omfgText("x", "Version two.")
			e.fs.files[c.saved] = []byte(v2)
			res = e.s.SaveRule(e.ctx, params(orb, c.saved, v2, c.global))
			assert.Equal(t, threads.ConfigSaveResult{Scope: c.name, Path: "rules/x.md", Reason: "conflict"}, res)
			b, _ := get(central, "rules/x.md")
			assert.Equal(t, rule, string(b), "中央原有规则不被覆盖")
			assert.Equal(t, 1, applied)
			assert.Len(t, e.events(t, orb), 1)
		})
	}
}

// 项目路径但 orb 不属于项目（或项目已删除，orbs.project_id 随之置空）：no_project，不读 orb。
func TestSaveRuleNoProject(t *testing.T) {
	e := newEnv(t)
	orb := e.orb(t, "")
	res := e.s.SaveRule(e.ctx, params(orb, "~/.oryn/project-config/.omp/rules/x.md", rule, true))
	assert.Equal(t, threads.ConfigSaveResult{Scope: "project", Path: "rules/x.md", Reason: "no_project"}, res)

	prj := e.project(t)
	orb2 := e.orb(t, prj)
	_, err := e.st.DB.ExecContext(e.ctx, `DELETE FROM projects WHERE id = ?`, prj)
	require.NoError(t, err)
	res = e.s.SaveRule(e.ctx, params(orb2, "~/workspace/repo/.omp/rules/x.md", rule, false))
	assert.Equal(t, "no_project", res.Reason)
	assert.Zero(t, e.fs.n())
}

// 保存位置白名单全串锚定：穿越、子目录、非 rules/、大写、符号、末尾换行等一律 unsupported_path，且不读 orb。
func TestSaveRuleUnsupportedPath(t *testing.T) {
	e := newEnv(t)
	orb := e.orb(t, e.project(t))
	for _, p := range []string{
		"",
		"~/.omp/agent/rules/../x.md",
		"~/.omp/agent/rules/../../.ssh/x.md",
		"~/.omp/agent/rules/a/b.md",
		"~/.omp/agent/rules/.md",
		"~/.omp/agent/rules/-x.md",
		"~/.omp/agent/rules/X.md",
		"~/.omp/agent/rules/x y.md",
		"~/.omp/agent/rules/x$.md",
		"~/.omp/agent/rules/x.md.md/../y.md",
		"~/.omp/agent/rules/x.txt",
		"~/.omp/agent/rules/x.md\n",
		"~/.omp/agent/rules/x.md\x00",
		" ~/.omp/agent/rules/x.md",
		"/etc/passwd",
		"/home/oryn/.omp/agent/rules/x.md",
		"~/.omp/agent/models.yml",
		"~/.omp/agent/extensions/x.md",
		"~/.omp/agent/hooks/x.md",
		"~/.omp/agent/mcp.json",
		"~/.oryn/project-config/.omp/mcp.json",
		"~/.oryn/project-config/.omp/skills/x.md",
		"~/.oryn/project-config/.omp/rules/../hooks/x.md",
		"~/workspace/repo/sub/.omp/rules/x.md",
		"~/workspace/other/.omp/rules/x.md",
		"~/workspace/repo/.omp/rules/x.md/",
		"x~/.omp/agent/rules/x.md",
		"~/.omp/agent/rules/" + strings.Repeat("a", 129) + ".md",
	} {
		res := e.s.SaveRule(e.ctx, params(orb, p, rule, true))
		assert.Equal(t, threads.ConfigSaveResult{Reason: "unsupported_path"}, res, "%q", p)
	}
	assert.Zero(t, e.fs.n(), "白名单之外不读 orb")
	assert.Empty(t, e.events(t, orb))
	// 名字长度上限本身可以保存
	long := strings.Repeat("a", 128)
	text := omfgText(long, "body")
	e.fs.files["~/.omp/agent/rules/"+long+".md"] = []byte(text)
	res := e.s.SaveRule(e.ctx, params(orb, "~/.omp/agent/rules/"+long+".md", text, true))
	assert.True(t, res.Synced)
}

// 读 orb 与内容校验：fs 出错 / 非 200 → orb_read_failed；> 256 KiB → too_large；空、NUL、非 UTF-8 → invalid_content。
func TestSaveRuleContent(t *testing.T) {
	const saved = "~/.oryn/project-config/.omp/rules/x.md"
	cases := []struct {
		name    string
		content []byte // nil = 文件不存在（404）
		fsErr   error
		reason  string
	}{
		{"not_found", nil, nil, "orb_read_failed"},
		{"fs_error", []byte(rule), errors.New("link down"), "orb_read_failed"},
		{"too_large", bytes.Repeat([]byte("a"), 300<<10), nil, "too_large"},
		{"just_over", bytes.Repeat([]byte("a"), MaxRuleBytes+1), nil, "too_large"},
		{"empty", []byte{}, nil, "invalid_content"},
		{"nul", []byte("a\x00b"), nil, "invalid_content"},
		{"non_utf8", []byte{0xff, 0xfe, 'a'}, nil, "invalid_content"},
	}
	e := newEnv(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prj := e.project(t)
			orb := e.orb(t, prj)
			e.fs = &fakeFS{files: map[string][]byte{}, err: c.fsErr}
			e.s.Orbs = e.fs
			if c.content != nil {
				e.fs.files[saved] = c.content
			}
			res := e.s.SaveRule(e.ctx, params(orb, saved, string(c.content), false))
			assert.Equal(t, threads.ConfigSaveResult{Scope: "project", Path: "rules/x.md", Reason: c.reason}, res)
			assert.Equal(t, 1, e.fs.n())
			ph, err := e.projects.For(prj)
			require.NoError(t, err)
			_, ok := get(ph, "rules/x.md")
			assert.False(t, ok)
			assert.Empty(t, e.events(t, orb))
		})
	}
	// 恰好上限可以保存
	orb := e.orb(t, e.project(t))
	head := omfgText("x", "")
	full := head + strings.Repeat("a", MaxRuleBytes-len(head))
	e.fs = &fakeFS{files: map[string][]byte{saved: []byte(full)}}
	e.s.Orbs = e.fs
	assert.True(t, e.s.SaveRule(e.ctx, params(orb, saved, full, false)).Synced)
}

// frontmatter 必须是 /omfg 生成器的形状：有 TTSR 触发条件，只有生成器会写的键；否则 invalid_content，不写中央。
func TestSaveRuleShape(t *testing.T) {
	const saved = "~/.oryn/project-config/.omp/rules/x.md"
	fm := func(lines ...string) string { return "---\n" + strings.Join(lines, "\n") + "\n---\n\nbody" }
	for name, text := range map[string]string{
		"no_frontmatter":   "Do not output M122_MARKER.",
		"unterminated":     "---\nname: x\ncondition: \"a\"\n",
		"leading_comment":  "<!-- c -->\n" + rule,
		"always_apply":     fm(`name: x`, `description: "d"`, `condition: "a"`, `alwaysApply: true`),
		"always_apply_kc":  fm(`name: x`, `condition: "a"`, `always-apply: true`),
		"only_always":      fm(`name: x`, `description: "d"`, `alwaysApply: true`),
		"globs":            fm(`name: x`, `condition: "a"`, `globs: ["**"]`),
		"agents":           fm(`name: x`, `condition: "a"`, `agents: "main"`),
		"interrupt_mode":   fm(`name: x`, `condition: "a"`, `interruptMode: "always"`),
		"no_trigger":       fm(`name: x`, `description: "d"`, `scope: "text"`),
		"empty_condition":  fm(`name: x`, `condition: ""`),
		"empty_list":       fm(`name: x`, `condition: []`),
		"numeric_cond":     fm(`name: x`, `condition: 123`),
		"null_cond":        fm(`name: x`, `condition: ~`),
		"bool_question":    fm(`name: x`, `question: true`),
		"map_cond":         fm(`name: x`, `condition: {a: b}`),
		"no_name":          fm(`description: "d"`, `condition: "a"`),
		"dup_key":          fm(`name: x`, `condition: "a"`, `condition: "b"`),
		"merge_key":        fm(`name: x`, `condition: "a"`, `<<: {alwaysApply: true}`),
		"quoted_key":       fm(`name: x`, `condition: "a"`, `"alwaysApply": true`),
		"multiline_value":  fm(`name: x`, `condition:`, `  - "a"`, `alwaysApply: true`),
		"indented_key":     fm(`name: x`, `condition: "a"`, `  alwaysApply: true`),
		"tab":              fm(`name: x`, "condition:\t\"a\""),
		"html_comment":     fm(`name: x`, `condition: "a" <!-- c -->`),
		"yaml_comment_ln":  fm(`name: x`, `condition: "a"`, `# alwaysApply: true`),
		"anchor_alias":     fm(`name: &n x`, `condition: *n`),
		"not_mapping":      "---\n- a\n---\nbody",
		"empty_meta":       "---\n---\nbody",
		"crlf_always":      strings.ReplaceAll(fm(`name: x`, `condition: "a"`, `alwaysApply: true`), "\n", "\r\n"),
		"blockscalar_desc": fm(`name: x`, `condition: "a"`, `description: |`, `  alwaysApply: true`),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.s.Applied = func(string) { t.Fatal("不合规的规则不应写入中央") }
			prj := e.project(t)
			orb := e.orb(t, prj)
			e.fs.files[saved] = []byte(text)
			res := e.s.SaveRule(e.ctx, params(orb, saved, text, false))
			assert.Equal(t, threads.ConfigSaveResult{Scope: "project", Path: "rules/x.md", Reason: "invalid_content"}, res)
			ph, err := e.projects.For(prj)
			require.NoError(t, err)
			_, ok := get(ph, "rules/x.md")
			assert.False(t, ok)
		})
	}
	// 生成器能产出的几种形状都接受：只有 astCondition / 只有 question、多条件数组、规则名是数字或 true
	for name, text := range map[string]string{
		"ast":         fm(`name: x`, `description: "d"`, `astCondition: "console.log($A)"`, `scope: "tool:edit(*.ts)"`),
		"question":    fm(`name: x`, `description: "d"`, `question: "Does it print the marker?"`, `scope: ["text"]`),
		"multi_cond":  fm(`name: x`, `description: "d"`, `condition: ["a", "b"]`, `scope: ["text", "thinking"]`),
		"crlf":        strings.ReplaceAll(rule, "\n", "\r\n"),
		"numeric_nm":  strings.Replace(rule, "name: x", "name: 123", 1),
		"bool_name":   strings.Replace(rule, "name: x", "name: true", 1),
		"blank_lines": "---\nname: x\n\ncondition: \"a\"\n---\nbody",
	} {
		t.Run("ok_"+name, func(t *testing.T) {
			nm := "x"
			switch name {
			case "numeric_nm":
				nm = "123"
			case "bool_name":
				nm = "true"
			}
			e := newEnv(t)
			orb := e.orb(t, e.project(t))
			p := "~/.oryn/project-config/.omp/rules/" + nm + ".md"
			e.fs.files[p] = []byte(text)
			res := e.s.SaveRule(e.ctx, params(orb, p, text, false))
			assert.True(t, res.Synced, "%+v", res)
		})
	}
}

// 写进中央的必须是用户点选保存位置时看到的规则：没有预览、要素不同、规则名与保存位置不符 → content_mismatch，不写；
// Core 的确定性格式差异（引号、单元素数组与标量、首尾空白、换行风格）不算不同。
func TestSaveRuleContentMismatch(t *testing.T) {
	const saved = "~/.oryn/project-config/.omp/rules/x.md"
	for name, preview := range map[string]string{
		"no_preview":      "",
		"bad_preview":     "not a rule",
		"body":            omfgText("x", "Something else."),
		"condition":       strings.Replace(rule, `condition: "M122_MARKER"`, `condition: "OTHER"`, 1),
		"extra_condition": strings.Replace(rule, `condition: "M122_MARKER"`, `condition: ["M122_MARKER", "OTHER"]`, 1),
		"scope":           strings.Replace(rule, `scope: ["text", "tool:bash"]`, `scope: "text"`, 1),
		"description":     strings.Replace(rule, `Never print`, `Always print`, 1),
		"name":            omfgText("y", "Do not output M122_MARKER."),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.s.Applied = func(string) { t.Fatal("与预览不一致不应写入中央") }
			prj := e.project(t)
			orb := e.orb(t, prj)
			text := rule
			if name == "name" {
				// 读回的规则名与预览一致（y），但保存位置是 x
				text = preview
			}
			e.fs.files[saved] = []byte(text)
			res := e.s.SaveRule(e.ctx, params(orb, saved, preview, false))
			assert.Equal(t, threads.ConfigSaveResult{Scope: "project", Path: "rules/x.md", Reason: "content_mismatch"}, res)
			ph, err := e.projects.For(prj)
			require.NoError(t, err)
			_, ok := get(ph, "rules/x.md")
			assert.False(t, ok)
			assert.Empty(t, e.events(t, orb))
		})
	}
	for name, preview := range map[string]string{
		"same":         rule,
		"scalar_array": strings.Replace(rule, `condition: "M122_MARKER"`, `condition: ["M122_MARKER"]`, 1),
		"unquoted":     strings.Replace(rule, `description: "Never print the marker"`, `description: Never print the marker`, 1),
		"crlf":         strings.ReplaceAll(rule, "\n", "\r\n"),
		"trailing_ws":  rule + "\n\n",
	} {
		t.Run("ok_"+name, func(t *testing.T) {
			e := newEnv(t)
			orb := e.orb(t, e.project(t))
			e.fs.files[saved] = []byte(rule)
			res := e.s.SaveRule(e.ctx, params(orb, saved, preview, false))
			assert.True(t, res.Synced, "%+v", res)
		})
	}
}

// 写中央失败：配额 → too_large；中央拒收内容 → invalid_content；其它 → store_failed；orb 行不存在 → store_failed；
// 项目在读 orb 与写入之间被删除 → no_project。
func TestSaveRuleStoreErrors(t *testing.T) {
	e := newEnv(t)
	orb := e.orb(t, e.project(t))
	e.fs.files["~/.omp/agent/rules/x.md"] = []byte(rule)
	p := params(orb, "~/.omp/agent/rules/x.md", rule, true)

	e.s.Global = failGlobal{fmt.Errorf("%w（合计）", agentcfg.ErrTooLarge)}
	assert.Equal(t, "too_large", e.s.SaveRule(e.ctx, p).Reason)
	e.s.Global = failGlobal{errors.New("disk full")}
	assert.Equal(t, "store_failed", e.s.SaveRule(e.ctx, p).Reason)
	e.s.Global = failGlobal{fmt.Errorf("%w：x", agentcfg.ErrInvalidContent)}
	assert.Equal(t, "invalid_content", e.s.SaveRule(e.ctx, p).Reason)
	assert.Empty(t, e.events(t, orb))

	// 项目行还在，但配置根已被 ProjectHomes.Remove 作废
	gone := e.project(t)
	orbGone := e.orb(t, gone)
	require.NoError(t, e.projects.Remove(gone))
	e.fs.files["~/.oryn/project-config/.omp/rules/x.md"] = []byte(rule)
	res := e.s.SaveRule(e.ctx, params(orbGone, "~/.oryn/project-config/.omp/rules/x.md", rule, false))
	assert.Equal(t, threads.ConfigSaveResult{Scope: "project", Path: "rules/x.md", Reason: "no_project"}, res)

	p.OrbID = "orb-missing"
	e.s.Global = e.global
	res = e.s.SaveRule(e.ctx, p)
	assert.Equal(t, threads.ConfigSaveResult{Scope: "global", Path: "rules/x.md", Reason: "store_failed"}, res)

	// 项目配额：现有文件（直接落盘，省去逐个 Put）合计已到上限时再写一条 → too_large
	prj := e.project(t)
	orb2 := e.orb(t, prj)
	fill := filepath.Join(e.dir, agentcfg.ProjectsDirName, prj, "rules")
	require.NoError(t, os.MkdirAll(fill, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fill, "fill.md"), bytes.Repeat([]byte("a"), agentcfg.ProjectMaxTotalSize), 0o644))
	e.fs.files["~/.oryn/project-config/.omp/rules/x.md"] = []byte(rule)
	res = e.s.SaveRule(e.ctx, params(orb2, "~/.oryn/project-config/.omp/rules/x.md", rule, false))
	assert.Equal(t, "too_large", res.Reason)
}
