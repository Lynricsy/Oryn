package orbdrt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// M31 4.2：每条登记带取回上限，同一路径有几条登记时取最大；撤销只去掉自己那一条。文件库登记的路径超过
// libraryMaxBytes 时 library.fetch 回 invalid_params（artifact source 的登记放宽到 artifactSourceMaxBytes）。
func TestStageLibraryPathLimits(t *testing.T) {
	r := &Runtime{}
	undoLib := r.stageLibraryPaths([]string{"/a"}, libraryMaxBytes)
	undoArt := r.stageLibraryPaths([]string{"/a", "/b"}, artifactSourceMaxBytes)
	assert.Equal(t, map[string][]int64{"/a": {libraryMaxBytes, artifactSourceMaxBytes}, "/b": {artifactSourceMaxBytes}}, r.lib.staged)
	undoArt()
	assert.Equal(t, map[string][]int64{"/a": {libraryMaxBytes}}, r.lib.staged)
	undoLib()
	assert.Empty(t, r.lib.staged)

	big := filepath.Join(t.TempDir(), "big.bin")
	f, err := os.Create(big)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(libraryMaxBytes+1))
	require.NoError(t, f.Close())
	defer r.stageLibraryPaths([]string{big}, libraryMaxBytes)()
	_, err = r.libraryFetch(context.Background(), nil, apigen.LinkCtlLibraryFetchParams{ID: "x", Path: big})
	assert.Equal(t, "invalid_params", ctlCode(err))
}

// M31 4.4：orb 把 oryn-office 写到 ~/.agents/skills/oryn-office/（SKILL.md 与 scripts/）。
func TestOfficeSkillWrittenOnOrb(t *testing.T) {
	cp := newFakeCP(t)
	rt := startRuntime(t, cp, nil)
	dir := filepath.Join(rt.home, ".agents", "skills", "oryn-office")
	assert.Equal(t, dir, rt.officeSkillDir())
	assert.Equal(t, string(officeSkill), readFile(t, filepath.Join(dir, "SKILL.md")))
	for name, data := range map[string][]byte{"render.sh": officeRenderScript, "recalc.py": officeRecalcScript, "unpack.py": officeUnpackScript, "pack.py": officePackScript} {
		p := filepath.Join(dir, "scripts", name)
		assert.Equal(t, string(data), readFile(t, p))
		fi, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), fi.Mode().Perm(), name)
	}
}

// M31 4.4：runner 把 Runner 版 oryn-office 写到 ~/.oryn/omp/agent/skills/oryn-office/。
func TestRunnerOfficeSkillWritten(t *testing.T) {
	f := newRunnerHome(t)
	startRunnerRT(t, f, nil)
	dir := filepath.Join(f.home, ".oryn", "omp", "agent", "skills", "oryn-office")
	assert.Equal(t, dir, runnerOfficeSkillDir(f.home))
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "SKILL.md")); return err == nil }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, string(runnerOfficeSkill), readFile(t, filepath.Join(dir, "SKILL.md")))
	assert.Equal(t, string(officeRecalcScript), readFile(t, filepath.Join(dir, "scripts", "recalc.py")))
}

// M31 4.4：技能正文含计划要求的规则；Runner 版先检查本机工具，路径指向 Runner 的技能目录。
func TestOfficeSkillContent(t *testing.T) {
	for name, skill := range map[string]string{"orb": string(officeSkill), "runner": string(runnerOfficeSkill)} {
		assert.True(t, strings.HasPrefix(skill, "---\nname: oryn-office\n"), name)
		for _, s := range []string{
			"Word / Excel / PowerPoint / PDF", "document.docx", "workbook.xlsx", "presentation.pptx", "document.pdf",
			"create_artifact", "source", "pull_artifact", "update_artifact", "baseVersion",
			"python-docx", "pandoc -f docx -t markdown --track-changes=all", "openpyxl", "pandas", "python-pptx", "ReportLab",
			"pypdf", "qpdf", "pdftotext -layout", "unpack.py", "pack.py",
			"在原文件上改", "_xlfn.", "recalc.py", "Arial", "Times New Roman", "Courier New", "Noto Sans CJK",
			"render.sh", "占位文字", "soffice --headless --convert-to", "不编造数字",
			"read_artifact_comments", "`page`", "`point`", "`range`", "`block`",
		} {
			assert.Contains(t, skill, s, name)
		}
	}
	assert.Contains(t, string(officeSkill), "bash ~/.agents/skills/oryn-office/scripts/render.sh")
	assert.Contains(t, string(officeSkill), "python ~/.agents/skills/oryn-office/scripts/recalc.py")
	runner := string(runnerOfficeSkill)
	assert.Contains(t, runner, "command -v soffice python3")
	assert.Contains(t, runner, `python3 -c "import docx, openpyxl, pptx"`)
	assert.Contains(t, runner, "bash ~/.oryn/omp/agent/skills/oryn-office/scripts/render.sh")
	assert.NotContains(t, runner, "~/.agents/")
	assert.NotContains(t, runner, "oryn artifact pull")
}

// M31 C3：真跑扩展的 pull_artifact 工具（testdata/pull_artifact.ts）：分页拼回、钉住版本、overwrite 与校验失败。
func TestPullArtifactTool(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("没有 bun，跳过扩展工具实跑检查")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bun, "testdata/pull_artifact.ts", "assets/oryn-orb.ts").CombinedOutput()
	require.NoError(t, err, "pull_artifact.ts 失败：\n%s", string(out))
	assert.Equal(t, "OK", strings.TrimSpace(string(out)))
}
