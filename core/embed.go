// Package core 内嵌 Oryn Core（omp）的发布钉 source.json，供控制面校验宿主上的 omp 是否与之一致（M9 协调宿主）。
package core

import (
	_ "embed"
	"encoding/json"
	"maps"
)

//go:embed source.json
var sourceJSON []byte

// pin 是 source.json 的 sha256 表，包初始化时解析一次；格式不对说明构建产物损坏，直接 panic。
var pin = func() map[string]string {
	var src struct {
		SHA256 map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(sourceJSON, &src); err != nil {
		panic("core: source.json 无法解析: " + err.Error())
	}
	if len(src.SHA256) == 0 {
		panic("core: source.json 缺少 sha256 表")
	}
	return src.SHA256
}()

// Pin 返回 source.json 的 sha256 表（键如 "linux-x64"、"linux-arm64"、"omp-rpc-types.d.ts"，值为小写十六进制）。
// 每次返回新副本，调用方可随意修改。
func Pin() map[string]string { return maps.Clone(pin) }
