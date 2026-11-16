package orbdrt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/orbsock"
)

const (
	// artifactSourceMaxBytes 是 artifact 单个 source 文件的上限（同控制面 artifacts.MaxFileBytes）。
	artifactSourceMaxBytes = 20 << 20
	// artifactCallTimeout 是 artifact.call 的转发上限：控制面在这次请求里还要经 library.fetch 取回全部 source。
	artifactCallTimeout = 10 * time.Minute
)

// artifactCall 处理 orbd.sock 的 artifact.call（M14）：校验 params.htmlSource 与 params.files[*].source（orb 内的
// 绝对路径、不超过 20 MiB 的普通文件），转发期间把它们登记为可经 library.fetch 取回，再转成 ctl artifact.call。
// 路径改写成 Clean 之后的形式，控制面取回时用的就是登记的那个。
func (r *Runtime) artifactCall(ctx context.Context, params json.RawMessage) (any, error) {
	var p apigen.LinkCtlArtifactCallParams
	if err := decodeSock(params, &p); err != nil {
		return nil, err
	}
	sources, err := artifactSources(p.Params)
	if err != nil {
		return nil, err
	}
	defer r.stageLibraryPaths(sources)()
	return r.ctlForwardFor(ctx, apigen.MethodArtifactCall, p, artifactCallTimeout)
}

// artifactSources 校验并就地规范化 params.htmlSource（扩展把 html 参数写成的临时文件）与 params.files[*].source，
// 返回规范化后的路径（可能有重复）。files 或其元素的形状不对时不在这里报错，交给控制面按操作的参数校验。
func artifactSources(params map[string]any) ([]string, error) {
	var out []string
	if raw, has := params["htmlSource"]; has {
		src, err := artifactSource("htmlSource", raw)
		if err != nil {
			return nil, err
		}
		params["htmlSource"] = src
		out = append(out, src)
	}
	files, _ := params["files"].([]any)
	for i, f := range files {
		file, ok := f.(map[string]any)
		if !ok {
			continue
		}
		raw, has := file["source"]
		if !has {
			continue
		}
		src, err := artifactSource(fmt.Sprintf("files[%d].source", i), raw)
		if err != nil {
			return nil, err
		}
		file["source"] = src
		out = append(out, src)
	}
	return out, nil
}

// artifactSource 校验一个 source 参数（where 是它在参数里的位置）：orb 内的绝对路径、不超过上限的普通文件；
// 返回 Clean 之后的路径。
func artifactSource(where string, raw any) (string, error) {
	src, ok := raw.(string)
	if !ok {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s 必须是字符串：给 orb 里文件的绝对路径。", where)
	}
	if !filepath.IsAbs(src) {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s %q 不是绝对路径：改成 orb 里文件的绝对路径后重试。", where, src)
	}
	src = filepath.Clean(src)
	fi, err := os.Lstat(src)
	if err != nil {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s %s 读不到（%v）：确认文件存在后重试。", where, src, err)
	}
	if !fi.Mode().IsRegular() {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s %s 不是普通文件（目录或符号链接）：逐个给出文件，符号链接改给它指向的真实路径。", where, src)
	}
	if fi.Size() > artifactSourceMaxBytes {
		return "", orbsock.Errorf(orbsock.CodeInvalidParams, "%s %s 有 %d 字节，超过单文件上限 %d 字节：压缩或拆分后再试。", where, src, fi.Size(), artifactSourceMaxBytes)
	}
	return src, nil
}

// stageLibraryPaths 把 paths 登记为可经 library.fetch 取回（与 library.put 共用并发计数），返回撤销函数。
func (r *Runtime) stageLibraryPaths(paths []string) func() {
	if len(paths) == 0 {
		return func() {}
	}
	r.lib.mu.Lock()
	if r.lib.staged == nil {
		r.lib.staged = map[string]int{}
	}
	for _, p := range paths {
		r.lib.staged[p]++
	}
	r.lib.mu.Unlock()
	return func() {
		r.lib.mu.Lock()
		for _, p := range paths {
			if r.lib.staged[p]--; r.lib.staged[p] <= 0 {
				delete(r.lib.staged, p)
			}
		}
		r.lib.mu.Unlock()
	}
}
