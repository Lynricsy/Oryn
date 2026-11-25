package coord

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 内置技能 oryn：全局助手按需读取的操作文档（SKILL.md 索引 + 每域一份 + 每个操作一份），全部由规格、策略表与
// 原生工具定义在运行时生成，经协调宿主的 omp-home 下发到 ~/.omp/agent/skills/oryn/。
const (
	// SkillName 是技能名（与 orbdrt 的 coordinatorSkill 一致）。
	SkillName = "oryn"
	// skillRef 是文档互相引用的前缀。
	skillRef = "skill://oryn/"
	// skillDir 是技能文件在 omp-home 里的相对目录。
	skillDir = "skills/oryn/"
)

//go:embed skilldocs/*.md
var skillDocs embed.FS

// topLevelArtifactTools 是留在顶层的 artifact 工具；其余 artifact 定义属于技能原生操作。
var topLevelArtifactTools = map[string]bool{
	ToolCreateArtifact: true, ToolReadArtifact: true, ToolUpdateArtifact: true, ToolListArtifacts: true,
	ToolReadArtifactGuide: true, ToolScreenshotArtifact: true,
}

// skillNativeDomain 是技能原生操作 → 域：这些操作经 oryn 转给现有的宿主工具实现（语义比对应的 REST 操作完整）。
var skillNativeDomain = map[string]string{
	ToolAbortThread: "threads", ToolSetThreadModel: "threads",
	ToolListThreadFiles: "thread-files", ToolReadThreadFile: "thread-files", ToolWriteThreadFile: "thread-files",
	ToolFetchThreadFile: "thread-files", ToolPutThreadFile: "thread-files", ToolGetThreadChanges: "thread-files",
	ToolDeleteArtifact: "artifacts", ToolListArtifactVersions: "artifacts", ToolRestoreArtifactVersion: "artifacts",
	ToolReadArtifactComments: "artifacts", ToolReplyArtifactComment: "artifacts", ToolResolveArtifactComment: "artifacts",
	ToolReadArtifactData: "artifacts", ToolWriteArtifactData: "artifacts", ToolExportArtifact: "artifacts",
	ToolImportArtifact: "artifacts", ToolCheckArtifact: "artifacts", ToolDesignDirections: "artifacts",
	ToolDesignDirection: "artifacts", ToolDesignNotes: "artifacts", ToolDesignResolveNote: "artifacts",
}

// domainSupplements 是追加在域说明（skilldocs/<域>.md）之后的专题段落：skilldocs/ 下的文件名，按顺序拼接。
// design.md 是设计画布的做法（M29 C9 的助手版：没有工作副本，用 HTML 画框）。
var domainSupplements = map[string][]string{
	"artifacts": {"design.md"},
}

// skillNativeDefs 返回技能原生操作的定义：artifactToolDefs() 里不在 topLevelArtifactTools 的、threadToolDefs()、fileToolDefs()。
func skillNativeDefs() []apigen.OmpFrame {
	var out []apigen.OmpFrame
	for _, d := range artifactToolDefs() {
		if !topLevelArtifactTools[frameName(d)] {
			out = append(out, d)
		}
	}
	out = append(out, threadToolDefs()...)
	return append(out, fileToolDefs()...)
}

func frameName(d apigen.OmpFrame) string {
	n, _ := d["name"].(string)
	return n
}

// nativeClass 是原生操作在文档里的级别：list_ / read_ / get_ 开头的只读，其余是写。
func nativeClass(name string) opClass {
	for _, p := range []string{"list_", "read_", "get_"} {
		if strings.HasPrefix(name, p) {
			return opRead
		}
	}
	return opWrite
}

// SkillFiles 返回技能的全部文件：omp-home 相对路径（skills/oryn/…）→ 内容。
func SkillFiles() map[string][]byte { return skillFiles() }

var skillFiles = sync.OnceValue(buildSkillFiles)

func buildSkillFiles() map[string][]byte {
	files := map[string][]byte{}
	routes := apiRoutes()

	natives := map[string][]apigen.OmpFrame{} // 域 → 原生操作定义
	for _, d := range skillNativeDefs() {
		name := frameName(d)
		dom := skillNativeDomain[name]
		natives[dom] = append(natives[dom], d)
		files[skillDir+"ops/"+name+".md"] = []byte(nativeOpDoc(d))
	}

	var index strings.Builder
	index.WriteString("| Domain | File | Covers | Operations |\n|---|---|---|---|\n")
	for _, dom := range apiDomains {
		var ops, denied strings.Builder
		n := 0
		for _, d := range natives[dom.Name] {
			name := frameName(d)
			fmt.Fprintf(&ops, "- `%s(%s)` · %s — %s\n", name, nativeParams(d), nativeClass(name), firstSentence(d["description"]))
			n++
		}
		for _, op := range apiOps() {
			pol := apiPolicy[op]
			if pol.Domain != dom.Name {
				continue
			}
			rt := routes[op]
			if pol.Class == opDeny {
				if pol.Deny != "" {
					fmt.Fprintf(&denied, "- `%s` — %s\n", op, pol.Deny)
				}
				continue
			}
			fmt.Fprintf(&ops, "- `%s(%s)` · %s — %s\n", op, restParams(rt), classLabel(pol), rt.Summary)
			files[skillDir+"ops/"+op+".md"] = []byte(restOpDoc(rt, pol))
			n++
		}
		var doc strings.Builder
		fmt.Fprintf(&doc, "# %s — %s\n\n", dom.Name, dom.Desc)
		if intro, err := skillDocs.ReadFile("skilldocs/" + dom.Name + ".md"); err == nil {
			doc.Write(intro)
			doc.WriteString("\n")
		}
		for _, name := range domainSupplements[dom.Name] {
			sup, err := skillDocs.ReadFile("skilldocs/" + name)
			if err != nil {
				panic("coord: 缺少内嵌的 " + name + ": " + err.Error())
			}
			doc.Write(sup)
			doc.WriteString("\n")
		}
		doc.WriteString("## Operations\n")
		doc.WriteString(ops.String())
		if denied.Len() > 0 {
			doc.WriteString("\n## Not available\n")
			doc.WriteString(denied.String())
		}
		files[skillDir+"domains/"+dom.Name+".md"] = []byte(doc.String())
		fmt.Fprintf(&index, "| %s | %sdomains/%s.md | %s | %d |\n", dom.Name, skillRef, dom.Name, dom.Desc, n)
	}

	tmpl, err := skillDocs.ReadFile("skilldocs/SKILL.md")
	if err != nil {
		panic("coord: 缺少内嵌的 SKILL.md: " + err.Error())
	}
	skill := strings.Replace(string(tmpl), "{{DOMAINS}}", strings.TrimSuffix(index.String(), "\n"), 1)
	files[skillDir+"SKILL.md"] = []byte(strings.ReplaceAll(skill, "skill://oryn/", skillRef))
	return files
}

func classLabel(pol opPolicy) string {
	if pol.ConfirmNote != "" {
		return pol.Class.String() + " (" + pol.ConfirmNote + ")"
	}
	return pol.Class.String()
}

// restParams 是域文件里的参数写法：path 参数直接写名字，query 参数加 ? 后缀，有请求体写 body。
func restParams(rt apiRoute) string {
	var ps []string
	for _, p := range rt.PathParams {
		ps = append(ps, p.Name)
	}
	for _, p := range rt.QueryParams {
		ps = append(ps, p.Name+"?")
	}
	if rt.BodyType != "" {
		ps = append(ps, "body")
	}
	return strings.Join(ps, ", ")
}

// nativeParams 是原生操作的参数写法：schema 的属性名，非必填加 ?。
func nativeParams(d apigen.OmpFrame) string {
	params, _ := d["parameters"].(obj)
	props, _ := params["properties"].(obj)
	req, _ := params["required"].([]string)
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	slices.Sort(names)
	// 必填在前
	slices.SortStableFunc(names, func(a, b string) int {
		ra, rb := slices.Contains(req, a), slices.Contains(req, b)
		switch {
		case ra && !rb:
			return -1
		case rb && !ra:
			return 1
		}
		return 0
	})
	for i, n := range names {
		if !slices.Contains(req, n) {
			names[i] = n + "?"
		}
	}
	return strings.Join(names, ", ")
}

// firstSentence 取工具说明的第一句（域文件里一行一个操作）。
func firstSentence(v any) string {
	s, _ := v.(string)
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	if i := strings.Index(s, "。"); i > 0 {
		return s[:i+len("。")]
	}
	return s
}

func restOpDoc(rt apiRoute, pol opPolicy) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n", rt.Op, rt.Summary)
	if d := strings.TrimSpace(rt.Description); d != "" {
		b.WriteString(d + "\n\n")
	}
	fmt.Fprintf(&b, "- Class: %s\n- REST: %s %s\n- Call: `oryn{op: %q, args: {%s}}`\n", classLabel(pol), rt.Method, rt.Path, rt.Op, callSketch(rt))
	switch pol.Guard {
	case guardThread:
		b.WriteString("- Target: the thread must be a work thread (not an assistant or Space conversation)\n")
	case guardOrb:
		b.WriteString("- Target: the orb must not be the coordinator host, your computer, a box or a warm-pool orb\n")
	}
	if pol.Note != "" {
		b.WriteString("- Note: " + pol.Note + "\n")
	}
	b.WriteString("\n## args\n```json\n" + indentJSON(restArgsSchema(rt)) + "\n```\n")
	return b.String()
}

// callSketch 是调用示例里的 args：path 参数与请求体。
func callSketch(rt apiRoute) string {
	var ps []string
	for _, p := range rt.PathParams {
		ps = append(ps, p.Name+`: "…"`)
	}
	switch rt.BodyType {
	case "":
	case "application/octet-stream":
		ps = append(ps, `body: "…"`)
	default:
		ps = append(ps, "body: {…}")
	}
	return strings.Join(ps, ", ")
}

// restArgsSchema 是 oryn 的 args 对一个 REST 操作的 JSON Schema。
func restArgsSchema(rt apiRoute) map[string]any {
	props := map[string]any{}
	required := []string{}
	param := func(p apiParam) map[string]any {
		s := map[string]any{}
		for k, v := range p.Schema {
			s[k] = v
		}
		if p.Description != "" {
			s["description"] = p.Description
		}
		return s
	}
	for _, p := range rt.PathParams {
		props[p.Name] = param(p)
		required = append(required, p.Name)
	}
	for _, p := range rt.QueryParams {
		props[p.Name] = param(p)
		if p.Required {
			required = append(required, p.Name)
		}
	}
	switch rt.BodyType {
	case "":
	case "application/octet-stream":
		props["body"] = map[string]any{"type": "string", "description": "file text"}
	default:
		props["body"] = rt.BodySchema
	}
	if rt.BodyType != "" && rt.BodyRequired {
		required = append(required, "body")
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func nativeOpDoc(d apigen.OmpFrame) string {
	name := frameName(d)
	desc, _ := d["description"].(string)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n\n- Class: %s\n- Call: `oryn{op: %q, args: {…}}`\n", name, desc, nativeClass(name), name)
	b.WriteString("\n## args\n```json\n" + indentJSON(d["parameters"]) + "\n```\n")
	return b.String()
}

func indentJSON(v any) string {
	out, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return "{}"
	}
	return string(out)
}
