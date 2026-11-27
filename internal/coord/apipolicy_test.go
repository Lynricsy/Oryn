package coord

import (
	"slices"
	"strings"
	"testing"
)

// 规格里的每个操作都必须先分类：新增接口而没加策略时本测试失败。
func TestAPIPolicyComplete(t *testing.T) {
	routes := apiRoutes()
	var missing, extra []string
	for _, op := range apiOps() {
		if _, ok := apiPolicy[op]; !ok {
			missing = append(missing, op)
		}
	}
	for op := range apiPolicy {
		if _, ok := routes[op]; !ok {
			extra = append(extra, op)
		}
	}
	slices.Sort(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("策略表与规格不一致：缺少 %v；多余 %v", missing, extra)
	}

	counts := map[opClass]int{}
	domains := map[string]bool{}
	for _, d := range apiDomains {
		domains[d.Name] = true
	}
	for op, p := range apiPolicy {
		counts[p.Class]++
		rt := routes[op]
		if p.Class == opDeny {
			if (p.Domain == "") != (p.Deny == "") {
				t.Errorf("%s: 内部接口的 Domain 与 Deny 必须同时为空", op)
			}
			continue
		}
		if !domains[p.Domain] {
			t.Errorf("%s: 未知的域 %q", op, p.Domain)
		}
		if rt.Tag == "websocket" || rt.Tag == "orb-link" {
			t.Errorf("%s: tag %s 不能开放", op, rt.Tag)
		}
		switch rt.BodyType {
		case "", "application/json", "application/octet-stream":
		default:
			t.Errorf("%s: 不支持的请求类型 %s", op, rt.BodyType)
		}
		seen := map[string]bool{}
		for _, pr := range append(slices.Clone(rt.PathParams), rt.QueryParams...) {
			if pr.Name == "body" || seen[pr.Name] {
				t.Errorf("%s: 参数名 %q 冲突", op, pr.Name)
			}
			seen[pr.Name] = true
		}
		if len(rt.RespTypes) > 0 && !slices.ContainsFunc(rt.RespTypes, func(ct string) bool { return !strings.HasPrefix(ct, "image/") }) {
			t.Errorf("%s: 只返回图片", op)
		}
		switch {
		case rt.Method == "GET" && p.Class != opRead:
			t.Errorf("%s: GET 只能是 read 或 deny", op)
		case rt.Method != "GET" && p.Class == opRead && op != "previewSchedule":
			t.Errorf("%s: 非 GET 的 read 只能是 previewSchedule", op)
		case rt.Method == "DELETE" && p.Class == opRead:
			t.Errorf("%s: DELETE 不能是 read", op)
		}
		mutating := p.Class == opWrite || p.Class == opConfirm
		threadPath := strings.HasPrefix(rt.Path, "/api/v1/threads/{id}")
		orbPath := strings.HasPrefix(rt.Path, "/api/v1/orbs/{id}")
		if mutating && threadPath && p.Guard != guardThread {
			t.Errorf("%s: 线程路径的写操作必须 guardThread", op)
		}
		if mutating && orbPath && p.Guard != guardOrb {
			t.Errorf("%s: orb 路径的写操作必须 guardOrb", op)
		}
		if p.Guard == guardThread && !threadPath || p.Guard == guardOrb && !orbPath {
			t.Errorf("%s: 守卫 %d 与路径 %s 不符", op, p.Guard, rt.Path)
		}
		if p.ConfirmWhen != nil && (p.Class != opWrite || p.ConfirmNote == "") {
			t.Errorf("%s: ConfirmWhen 只用于 write 且必须有 ConfirmNote", op)
		}
	}
	want := map[opClass]int{opRead: 139, opWrite: 85, opConfirm: 61, opDeny: 110}
	for c, n := range want {
		if counts[c] != n {
			t.Errorf("%s 数量 = %d, want %d", c, counts[c], n)
		}
	}
}
