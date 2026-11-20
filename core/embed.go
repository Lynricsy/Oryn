// Package core 内嵌 Oryn Core（omp）的发布钉 source.json，供控制面校验宿主上的 omp 是否与之一致（M9 协调宿主），
// 也供 Runner 的 orbd 按钉下载 Core（M21）。
package core

import (
	_ "embed"
	"encoding/json"
	"maps"
)

//go:embed source.json
var sourceJSON []byte

// Release 是 source.json 里下载 Core 所需的来源：GitHub 仓库、Release tag 与各资产的 sha256。
// SHA256 的平台键为 "<goos>-<arch>"（arch 取 "x64" / "arm64"），对应资产名 "omp-<键>"。
type Release struct {
	Repo   string
	Tag    string
	SHA256 map[string]string
}

// src 是 source.json 解析结果，包初始化时解析一次；格式不对说明构建产物损坏，直接 panic。
var src = func() Release {
	var s struct {
		Repo   string            `json:"repo"`
		Tag    string            `json:"tag"`
		SHA256 map[string]string `json:"sha256"`
	}
	if err := json.Unmarshal(sourceJSON, &s); err != nil {
		panic("core: source.json 无法解析: " + err.Error())
	}
	if len(s.SHA256) == 0 {
		panic("core: source.json 缺少 sha256 表")
	}
	if s.Repo == "" || s.Tag == "" {
		panic("core: source.json 缺少 repo 或 tag")
	}
	return Release{Repo: s.Repo, Tag: s.Tag, SHA256: s.SHA256}
}()

// Pin 返回 source.json 的 sha256 表（键如 "linux-x64"、"linux-arm64"、"omp-rpc-types.d.ts"，值为小写十六进制）。
// 每次返回新副本，调用方可随意修改。
func Pin() map[string]string { return maps.Clone(src.SHA256) }

// Source 返回 source.json 的仓库、tag 与 sha256 表；SHA256 每次是新副本，调用方可随意修改。
func Source() Release {
	r := src
	r.SHA256 = maps.Clone(src.SHA256)
	return r
}
