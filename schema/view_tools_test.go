package schema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// 视图工具契约：每个请求都带这两份 schema 与指引，压缩后的体积是 token 成本的上限。
const maxViewToolsCompact = 20 << 10

type viewToolsDoc struct {
	Guide string `json:"guide"`
	Tools []struct {
		Name        string         `json:"name"`
		Label       string         `json:"label"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"tools"`
}

func TestViewToolsContract(t *testing.T) {
	var doc viewToolsDoc
	dec := json.NewDecoder(bytes.NewReader(ViewTools))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("解析 view-tools.json: %v", err)
	}
	if !strings.HasPrefix(doc.Guide, "# Views") {
		t.Fatalf("guide 开头不对: %q", doc.Guide[:min(len(doc.Guide), 40)])
	}
	if len(doc.Tools) != 2 || doc.Tools[0].Name != "render_ui" || doc.Tools[1].Name != "render_html" {
		t.Fatalf("工具名单不对: %+v", doc.Tools)
	}
	for _, tool := range doc.Tools {
		if tool.Label == "" || tool.Description == "" {
			t.Fatalf("%s 缺 label / description", tool.Name)
		}
		if tool.Parameters["type"] != "object" || tool.Parameters["additionalProperties"] != false {
			t.Fatalf("%s 的参数必须是 additionalProperties:false 的对象", tool.Name)
		}
		props, _ := tool.Parameters["properties"].(map[string]any)
		if _, ok := props["await"]; !ok {
			t.Fatalf("%s 缺 await 参数", tool.Name)
		}
		defs, _ := tool.Parameters["$defs"].(map[string]any)
		refs := map[string][]string{} // 定义名 → 它引用的定义
		collectRefs(t, tool.Name, tool.Parameters, defs, "", refs)
		// 没有递归：从任意定义出发都走不回自己。
		for name := range defs {
			if reaches(name, name, refs, map[string]bool{}) {
				t.Fatalf("%s 的 $defs/%s 递归引用了自己", tool.Name, name)
			}
		}
	}

	var compact bytes.Buffer
	if err := json.Compact(&compact, ViewTools); err != nil {
		t.Fatal(err)
	}
	if compact.Len() > maxViewToolsCompact {
		t.Fatalf("view-tools.json 压缩后 %d 字节，超过 %d", compact.Len(), maxViewToolsCompact)
	}
}

// collectRefs 检查每个 $ref 都指向存在的 $defs，并记下定义之间的引用关系。
func collectRefs(t *testing.T, tool string, node any, defs map[string]any, owner string, refs map[string][]string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if key == "$ref" {
				ref, _ := child.(string)
				name, ok := strings.CutPrefix(ref, "#/$defs/")
				if !ok || defs[name] == nil {
					t.Fatalf("%s: $ref %q 指向不存在的定义", tool, ref)
				}
				if owner != "" {
					refs[owner] = append(refs[owner], name)
				}
				continue
			}
			if key == "$defs" && owner == "" {
				for name, def := range child.(map[string]any) {
					collectRefs(t, tool, def, defs, name, refs)
				}
				continue
			}
			collectRefs(t, tool, child, defs, owner, refs)
		}
	case []any:
		for _, child := range v {
			collectRefs(t, tool, child, defs, owner, refs)
		}
	}
}

func reaches(from, target string, refs map[string][]string, seen map[string]bool) bool {
	for _, next := range refs[from] {
		if next == target {
			return true
		}
		if !seen[next] {
			seen[next] = true
			if reaches(next, target, refs, seen) {
				return true
			}
		}
	}
	return false
}
