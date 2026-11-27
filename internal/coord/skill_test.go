package coord

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSkillFiles(t *testing.T) {
	files := SkillFiles()
	allowed := AllowedAPIOps()
	require.Len(t, files, 1+len(apiDomains)+len(allowed)+len(skillNativeDomain))
	assert.Len(t, allowed, 290)
	assert.Len(t, skillNativeDomain, 23)

	skill := string(files[skillDir+"SKILL.md"])
	assert.True(t, strings.HasPrefix(skill, "---\nname: oryn\n"), skill[:min(len(skill), 40)])
	assert.NotContains(t, skill, "{{DOMAINS}}")

	section := func(doc, head string) string {
		_, rest, ok := strings.Cut(doc, "\n"+head+"\n")
		if !ok {
			return ""
		}
		if i := strings.Index(rest, "\n## "); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	for _, op := range apiOps() {
		pol := apiPolicy[op]
		if pol.Class == opDeny {
			if pol.Deny != "" {
				dom := string(files[skillDir+"domains/"+pol.Domain+".md"])
				assert.Contains(t, section(dom, "## Not available"), "- `"+op+"` — ", op)
			}
			assert.NotContains(t, files, skillDir+"ops/"+op+".md", op)
			continue
		}
		dom := string(files[skillDir+"domains/"+pol.Domain+".md"])
		assert.Equal(t, 1, strings.Count(section(dom, "## Operations"), "- `"+op+"("), op)
		assert.Contains(t, files, skillDir+"ops/"+op+".md", op)
	}
	// 原生操作的名字集合恰为 skillNativeDomain，且出现在各自的域文件里
	var natives []string
	for _, d := range skillNativeDefs() {
		natives = append(natives, frameName(d))
	}
	var want []string
	for n := range skillNativeDomain {
		want = append(want, n)
	}
	assert.ElementsMatch(t, want, natives)
	for n, dom := range skillNativeDomain {
		assert.Contains(t, section(string(files[skillDir+"domains/"+dom+".md"]), "## Operations"), "- `"+n+"(", n)
	}

	// 操作文档里的 args 是合法 JSON Schema
	doc := string(files[skillDir+"ops/updateProject.md"])
	_, js, ok := strings.Cut(doc, "## args\n```json\n")
	require.True(t, ok, doc)
	js, _, _ = strings.Cut(js, "\n```")
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	require.NoError(t, json.Unmarshal([]byte(js), &schema))
	assert.Contains(t, schema.Properties, "id")
	assert.Contains(t, schema.Properties, "body")
	assert.Contains(t, schema.Required, "id")

	// skill://oryn/… 引用都指向存在的文件（以 / 结尾的是 <operation> 之类的占位）
	ref := regexp.MustCompile(`skill://oryn/[A-Za-z0-9_./-]*`)
	total := 0
	for path, b := range files {
		total += len(b)
		for _, m := range ref.FindAllString(string(b), -1) {
			m = strings.TrimRight(m, ".")
			if strings.HasSuffix(m, "/") {
				continue
			}
			assert.Contains(t, files, skillDir+strings.TrimPrefix(m, skillRef), "%s 引用了 %s", path, m)
		}
	}
	assert.Less(t, total, 2<<20, "技能文件合计应小于 2 MiB")
}
