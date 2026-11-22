package coord

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/schema"
)

// apiParam 是一个 path 或 query 参数。
type apiParam struct {
	Name, Description string
	Required          bool
	Schema            map[string]any // 已解引用
}

// apiRoute 是规格里的一个 REST 操作（oryn 工具按它构造进程内请求、生成技能文档）。
type apiRoute struct {
	Op, Method, Path        string // Path 是模板，如 /api/v1/projects/{id}
	Summary, Description    string
	Tag                     string     // 第一个 tag
	PathParams, QueryParams []apiParam // 路径级与操作级 parameters 合并；header / cookie 参数丢弃
	BodyType                string     // 请求体的第一个 content type；无请求体为空串
	BodySchema              map[string]any
	BodyRequired            bool
	RespTypes               []string // 2xx 响应的 content type，排序去重
}

type apiSpec struct {
	routes map[string]apiRoute
	ops    []string
}

var loadAPISpec = sync.OnceValue(func() apiSpec {
	s, err := parseAPISpec(schema.OpenAPI)
	if err != nil {
		panic("coord: 内嵌 OpenAPI 规格解析失败: " + err.Error())
	}
	return s
})

// apiRoutes 返回 operationId → 路由（内嵌规格解析失败 panic，由单测保证不会发生）。
func apiRoutes() map[string]apiRoute { return loadAPISpec().routes }

// apiOps 返回规格顺序的 operationId 列表。
func apiOps() []string { return loadAPISpec().ops }

var apiMethods = map[string]bool{"get": true, "put": true, "post": true, "patch": true, "delete": true}

func parseAPISpec(data []byte) (apiSpec, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return apiSpec{}, err
	}
	if len(root.Content) == 0 {
		return apiSpec{}, fmt.Errorf("空文档")
	}
	doc := root.Content[0]
	var full map[string]any
	if err := doc.Decode(&full); err != nil {
		return apiSpec{}, err
	}
	comps, _ := normYAML(full["components"]).(map[string]any)
	r := &specResolver{comps: comps, active: map[string]bool{}}

	paths := nodeGet(doc, "paths")
	if paths == nil {
		return apiSpec{}, fmt.Errorf("缺少 paths")
	}
	out := apiSpec{routes: map[string]apiRoute{}}
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, item := paths.Content[i].Value, paths.Content[i+1]
		var pathParams []any
		if pn := nodeGet(item, "parameters"); pn != nil {
			var v []any
			if err := pn.Decode(&v); err != nil {
				return apiSpec{}, err
			}
			pathParams = v
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			method := item.Content[j].Value
			if !apiMethods[method] {
				continue
			}
			opNode := item.Content[j+1]
			var opRaw map[string]any
			if err := opNode.Decode(&opRaw); err != nil {
				return apiSpec{}, err
			}
			op, _ := normYAML(opRaw).(map[string]any)
			rt, err := r.route(path, strings.ToUpper(method), op, opNode, pathParams)
			if err != nil {
				return apiSpec{}, fmt.Errorf("%s %s: %w", method, path, err)
			}
			if _, dup := out.routes[rt.Op]; dup {
				return apiSpec{}, fmt.Errorf("operationId 重复: %s", rt.Op)
			}
			out.routes[rt.Op] = rt
			out.ops = append(out.ops, rt.Op)
		}
	}
	return out, nil
}

type specResolver struct {
	comps  map[string]any
	active map[string]bool // 正在展开的引用（防环）
}

func (r *specResolver) route(path, method string, op map[string]any, opNode *yaml.Node, pathLevel []any) (apiRoute, error) {
	rt := apiRoute{Method: method, Path: path}
	rt.Op, _ = op["operationId"].(string)
	if rt.Op == "" {
		return rt, fmt.Errorf("缺少 operationId")
	}
	rt.Summary, _ = op["summary"].(string)
	rt.Description, _ = op["description"].(string)
	if tags, _ := op["tags"].([]any); len(tags) > 0 {
		rt.Tag, _ = tags[0].(string)
	}

	// 参数：路径级在前，操作级同名同位置覆盖
	type key struct{ in, name string }
	var order []key
	params := map[key]map[string]any{}
	opParams, _ := op["parameters"].([]any)
	for _, raw := range append(slices.Clone(normYAML(pathLevel).([]any)), opParams...) {
		p, _ := r.deref(raw, false).(map[string]any)
		if p == nil {
			continue
		}
		in, _ := p["in"].(string)
		name, _ := p["name"].(string)
		k := key{in, name}
		if _, seen := params[k]; !seen {
			order = append(order, k)
		}
		params[k] = p
	}
	for _, k := range order {
		p := params[k]
		ap := apiParam{Name: k.name}
		ap.Description, _ = p["description"].(string)
		ap.Required, _ = p["required"].(bool)
		ap.Schema, _ = p["schema"].(map[string]any)
		switch k.in {
		case "path":
			ap.Required = true
			rt.PathParams = append(rt.PathParams, ap)
		case "query":
			rt.QueryParams = append(rt.QueryParams, ap)
		}
	}
	// path 参数按模板里出现的顺序
	slices.SortStableFunc(rt.PathParams, func(a, b apiParam) int {
		return strings.Index(path, "{"+a.Name+"}") - strings.Index(path, "{"+b.Name+"}")
	})

	if rb, ok := r.deref(op["requestBody"], false).(map[string]any); ok {
		rt.BodyRequired, _ = rb["required"].(bool)
		content, _ := rb["content"].(map[string]any)
		// 第一个 content type 按规格书写顺序取
		if cn := nodeGet(nodeGet(opNode, "requestBody"), "content"); cn != nil && len(cn.Content) > 0 {
			rt.BodyType = cn.Content[0].Value
		}
		if mt, _ := content[rt.BodyType].(map[string]any); mt != nil {
			rt.BodySchema, _ = mt["schema"].(map[string]any)
		}
		if rt.BodySchema == nil && rt.BodyType != "" {
			rt.BodySchema = map[string]any{}
		}
	}

	resps, _ := op["responses"].(map[string]any)
	for code, raw := range resps {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		resp, _ := r.deref(raw, false).(map[string]any)
		content, _ := resp["content"].(map[string]any)
		for ct := range content {
			if !slices.Contains(rt.RespTypes, ct) {
				rt.RespTypes = append(rt.RespTypes, ct)
			}
		}
	}
	slices.Sort(rt.RespTypes)
	return rt, nil
}

// deref 递归展开 #/components/<kind>/<name> 引用；环上的引用保留为 {"$ref": "<name>"}。
// 展开后删除 x- 开头的键与 example / examples；inProps 为真时 v 是 properties 映射，键是字段名，不过滤。
func (r *specResolver) deref(v any, inProps bool) any {
	switch t := v.(type) {
	case map[string]any:
		if inProps {
			out := make(map[string]any, len(t))
			for k, val := range t {
				out[k] = r.deref(val, false)
			}
			return out
		}
		if ref, ok := t["$ref"].(string); ok {
			kind, name, ok := splitRef(ref)
			if !ok {
				return map[string]any{"$ref": ref}
			}
			if r.active[ref] {
				return map[string]any{"$ref": name}
			}
			group, _ := r.comps[kind].(map[string]any)
			target, ok := group[name]
			if !ok {
				return map[string]any{"$ref": name}
			}
			r.active[ref] = true
			res := r.deref(target, false)
			delete(r.active, ref)
			// 引用旁的兄弟键（如 description）覆盖到展开结果上
			if m, ok := res.(map[string]any); ok && len(t) > 1 {
				merged := make(map[string]any, len(m)+len(t))
				for k, val := range m {
					merged[k] = val
				}
				for k, val := range t {
					if k == "$ref" || dropSpecKey(k) {
						continue
					}
					merged[k] = r.deref(val, k == "properties")
				}
				return merged
			}
			return res
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			if dropSpecKey(k) {
				continue
			}
			out[k] = r.deref(val, k == "properties")
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = r.deref(val, false)
		}
		return out
	default:
		return v
	}
}

func dropSpecKey(k string) bool {
	return strings.HasPrefix(k, "x-") || k == "example" || k == "examples"
}

func splitRef(ref string) (kind, name string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "#/components/")
	if !ok {
		return "", "", false
	}
	kind, name, ok = strings.Cut(rest, "/")
	return kind, name, ok && name != ""
}

// normYAML 把 yaml 解出的 map[any]any 统一成 map[string]any（非字符串键转成文本）。
func normYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normYAML(val)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normYAML(val)
		}
		return out
	case []any:
		for i, val := range t {
			t[i] = normYAML(val)
		}
		return t
	default:
		return v
	}
}

// nodeGet 取映射节点里 key 对应的值节点；n 为 nil 或不是映射时返回 nil。
func nodeGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
